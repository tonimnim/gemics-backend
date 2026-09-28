package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	matchNoShowSweepInterval = 10 * time.Second
	matchNoShowSweepBatch    = 25
)

var errMatchNoShowInvalid = errors.New("invalid match no-show state")

type matchNoShowCandidate struct {
	MatchID       string
	CompetitionID string
	Version       int
}

type lockedMatchNoShow struct {
	Version         int
	State           string
	HomeEntryID     *string
	AwayEntryID     *string
	CheckInClosesAt *time.Time
	OrganizationID  string
	DatabaseNow     time.Time
}

type matchNoShowPlan struct {
	Apply            bool
	FinalState       string
	WinnerEntryID    *string
	CompletionReason string
	EventType        string
	CheckedEntryIDs  []string
}

// runMatchNoShowWorker resolves only pre-match attendance deadlines. It never
// touches score reports: in_progress matches whose result window elapsed are
// resolved by the result verification worker (R7).
func (s *Server) runMatchNoShowWorker(ctx context.Context) {
	if s.db == nil {
		return
	}
	s.finalizeExpiredMatchNoShows(ctx)
	ticker := time.NewTicker(matchNoShowSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.finalizeExpiredMatchNoShows(ctx)
		}
	}
}

func (s *Server) finalizeExpiredMatchNoShows(ctx context.Context) {
	candidates, err := s.selectExpiredMatchNoShowCandidates(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("select expired match check-ins", "error", err)
		}
		return
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		applied, err := s.finalizeOneExpiredMatchNoShow(ctx, candidate)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("finalize expired match check-in", "match_id", candidate.MatchID,
					"competition_id", candidate.CompetitionID, "error", err)
			}
			continue
		}
		if applied {
			s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), candidate.CompetitionID)
		}
	}
}

// selectExpiredMatchNoShowCandidates is deliberately lock-free. A worker must
// acquire the competition gate before it takes a match row lock, so this query
// only discovers a bounded amount of work. The transaction below revalidates
// every selected value and performs the actual claim.
func (s *Server) selectExpiredMatchNoShowCandidates(ctx context.Context) ([]matchNoShowCandidate, error) {
	rows, err := s.db.Writer.Query(ctx, `SELECT id::text,competition_id::text,version
		FROM matches
		WHERE state='ready' AND check_in_closes_at IS NOT NULL AND check_in_closes_at<=now()
		ORDER BY check_in_closes_at,id
		LIMIT $1`, matchNoShowSweepBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]matchNoShowCandidate, 0, matchNoShowSweepBatch)
	for rows.Next() {
		var candidate matchNoShowCandidate
		if err = rows.Scan(&candidate.MatchID, &candidate.CompetitionID, &candidate.Version); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (s *Server) finalizeOneExpiredMatchNoShow(ctx context.Context, candidate matchNoShowCandidate) (bool, error) {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// All progression-capable writers use this order: competition gate, source
	// match, then current check-in rows ordered by entry id. Multiple replicas
	// may discover the same candidate, but only one can still satisfy the
	// version/state predicate after obtaining these locks.
	if err = lockCompetitionProgressionGate(ctx, tx, candidate.CompetitionID); err != nil {
		return false, err
	}
	var locked lockedMatchNoShow
	err = tx.QueryRow(ctx, `SELECT match.version,match.state,match.home_entry_id::text,
		match.away_entry_id::text,match.check_in_closes_at,competition.organization_id::text,now()
		FROM matches match
		JOIN competitions competition ON competition.id=match.competition_id
		WHERE match.id=$1 AND match.competition_id=$2
		FOR UPDATE OF match`, candidate.MatchID, candidate.CompetitionID).Scan(
		&locked.Version, &locked.State, &locked.HomeEntryID, &locked.AwayEntryID,
		&locked.CheckInClosesAt, &locked.OrganizationID, &locked.DatabaseNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !matchNoShowNeedsInspection(locked, candidate.Version) {
		return false, nil
	}
	if locked.HomeEntryID == nil || locked.AwayEntryID == nil || *locked.HomeEntryID == *locked.AwayEntryID {
		return false, fmt.Errorf("%w: ready match does not have two distinct participants", errMatchNoShowInvalid)
	}

	checkedEntries, err := lockCurrentMatchCheckIns(ctx, tx, candidate.MatchID,
		*locked.HomeEntryID, *locked.AwayEntryID)
	if err != nil {
		return false, err
	}
	plan, err := planExpiredMatchNoShow(locked, candidate.Version, checkedEntries)
	if err != nil || !plan.Apply {
		return false, err
	}

	var finalizedVersion int
	err = tx.QueryRow(ctx, `UPDATE matches SET state=$1,winner_entry_id=$2,
		completion_reason=$3,completed_at=$4,version=version+1,updated_at=$4
		WHERE id=$5 AND competition_id=$6 AND state='ready' AND version=$7
		  AND check_in_closes_at IS NOT NULL AND check_in_closes_at<=$4
		RETURNING version`, plan.FinalState, plan.WinnerEntryID, plan.CompletionReason,
		locked.DatabaseNow, candidate.MatchID, candidate.CompetitionID, locked.Version).Scan(&finalizedVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	if _, err = applyMatchProgression(ctx, tx, matchProgressionInput{
		MatchID: candidate.MatchID, CompetitionID: candidate.CompetitionID,
		FinalizedVersion: finalizedVersion, FinalState: plan.FinalState,
		WinnerEntryID: plan.WinnerEntryID, Cause: progressionCauseTimeoutForfeit,
	}); err != nil {
		return false, err
	}
	if err = writeMatchNoShowAuditAndOutbox(ctx, tx, locked.OrganizationID, candidate,
		locked, finalizedVersion, plan); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func matchNoShowNeedsInspection(locked lockedMatchNoShow, expectedVersion int) bool {
	return locked.State == "ready" && locked.Version == expectedVersion &&
		locked.CheckInClosesAt != nil && !locked.DatabaseNow.Before(locked.CheckInClosesAt.UTC())
}

func lockCurrentMatchCheckIns(ctx context.Context, tx pgx.Tx, matchID, homeEntryID, awayEntryID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT entry_id::text FROM match_check_ins
		WHERE match_id=$1 AND entry_id IN ($2,$3)
		ORDER BY entry_id FOR UPDATE`, matchID, homeEntryID, awayEntryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checked := make([]string, 0, 2)
	for rows.Next() {
		var entryID string
		if err = rows.Scan(&entryID); err != nil {
			return nil, err
		}
		checked = append(checked, entryID)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return checked, nil
}

func planExpiredMatchNoShow(locked lockedMatchNoShow, expectedVersion int, checkedEntries []string) (matchNoShowPlan, error) {
	if !matchNoShowNeedsInspection(locked, expectedVersion) {
		return matchNoShowPlan{}, nil
	}
	if locked.HomeEntryID == nil || locked.AwayEntryID == nil || *locked.HomeEntryID == *locked.AwayEntryID {
		return matchNoShowPlan{}, fmt.Errorf("%w: ready match does not have two distinct participants", errMatchNoShowInvalid)
	}
	home, away := *locked.HomeEntryID, *locked.AwayEntryID
	seen := make(map[string]struct{}, len(checkedEntries))
	for _, entryID := range checkedEntries {
		if entryID != home && entryID != away {
			return matchNoShowPlan{}, fmt.Errorf("%w: check-in is not a current participant", errMatchNoShowInvalid)
		}
		if _, duplicate := seen[entryID]; duplicate {
			return matchNoShowPlan{}, fmt.Errorf("%w: duplicate participant check-in", errMatchNoShowInvalid)
		}
		seen[entryID] = struct{}{}
	}
	checked := append([]string(nil), checkedEntries...)
	switch len(checked) {
	case 0:
		return matchNoShowPlan{Apply: true, FinalState: "cancelled", CompletionReason: "double_no_show",
			EventType: "match.cancelled", CheckedEntryIDs: checked}, nil
	case 1:
		winner := checked[0]
		return matchNoShowPlan{Apply: true, FinalState: "forfeit", WinnerEntryID: &winner,
			CompletionReason: "timeout_forfeit", EventType: "match.forfeited", CheckedEntryIDs: checked}, nil
	case 2:
		// The second check-in normally advances the match to in_progress in the
		// same transaction. If an old or racing row is observed while ready, the
		// worker still must not invent a result; the normal check-in path owns it.
		return matchNoShowPlan{CheckedEntryIDs: checked}, nil
	default:
		return matchNoShowPlan{}, fmt.Errorf("%w: too many current participant check-ins", errMatchNoShowInvalid)
	}
}

func writeMatchNoShowAuditAndOutbox(ctx context.Context, tx pgx.Tx, organizationID string,
	candidate matchNoShowCandidate, locked lockedMatchNoShow, finalizedVersion int, plan matchNoShowPlan) error {
	before, err := json.Marshal(map[string]any{
		"matchId": candidate.MatchID, "state": locked.State, "matchVersion": locked.Version,
		"checkInClosesAt": locked.CheckInClosesAt, "checkedEntryIds": plan.CheckedEntryIDs,
	})
	if err != nil {
		return err
	}
	after, err := json.Marshal(map[string]any{
		"matchId": candidate.MatchID, "competitionId": candidate.CompetitionID,
		"state": plan.FinalState, "matchVersion": finalizedVersion,
		"winnerEntryId": plan.WinnerEntryID, "completionReason": plan.CompletionReason,
		"checkedEntryIds": plan.CheckedEntryIDs, "completedAt": locked.DatabaseNow,
	})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events
		(organization_id,actor_user_id,action,subject_type,subject_id,request_id,before_state,after_state)
		VALUES ($1,NULL,$2,'match',$3,'match-no-show-worker',$4,$5)`,
		organizationID, plan.EventType, candidate.MatchID, json.RawMessage(before), json.RawMessage(after)); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
		VALUES ('match',$1,$2,$3)`, candidate.MatchID, plan.EventType, json.RawMessage(after))
	return err
}
