package httpapi

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	matchResultVerificationSweepInterval  = 10 * time.Second
	matchResultVerificationSweepBatch     = 25
	matchResultVerificationBackoffLimit   = 256
	matchResultVerificationInitialBackoff = 30 * time.Second
	matchResultVerificationMaxBackoff     = 15 * time.Minute
)

type matchResultVerificationCandidate struct {
	MatchID       string
	CompetitionID string
}

// matchResultVerificationBackoff keeps failing candidates out of the next
// sweeps, so one poison match cannot starve the bounded queue behind it. It is
// per process, bounded, and its zero value is ready to use.
type matchResultVerificationBackoff struct {
	retryAt map[string]time.Time
	delay   map[string]time.Duration
}

// excluded lists the candidates still backing off. It is never nil: a NULL
// array would make NOT (id = ANY($2)) filter out every row.
func (backoff *matchResultVerificationBackoff) excluded(now time.Time) []string {
	matchIDs := make([]string, 0, len(backoff.retryAt))
	for matchID, retryAt := range backoff.retryAt {
		if now.Before(retryAt) {
			matchIDs = append(matchIDs, matchID)
		}
	}
	slices.Sort(matchIDs)
	return matchIDs
}

// fail doubles the candidate's delay from 30 seconds up to 15 minutes. At
// capacity it forgets the candidate due soonest, which costs at most one
// early retry.
func (backoff *matchResultVerificationBackoff) fail(matchID string, now time.Time) {
	if backoff.retryAt == nil {
		backoff.retryAt = make(map[string]time.Time, matchResultVerificationBackoffLimit)
		backoff.delay = make(map[string]time.Duration, matchResultVerificationBackoffLimit)
	}
	delay := matchResultVerificationInitialBackoff
	if previous, tracked := backoff.delay[matchID]; tracked {
		delay = min(2*previous, matchResultVerificationMaxBackoff)
	} else if len(backoff.delay) >= matchResultVerificationBackoffLimit {
		backoff.clear(backoff.soonest())
	}
	backoff.delay[matchID] = delay
	backoff.retryAt[matchID] = now.Add(delay)
}

func (backoff *matchResultVerificationBackoff) clear(matchID string) {
	delete(backoff.retryAt, matchID)
	delete(backoff.delay, matchID)
}

func (backoff *matchResultVerificationBackoff) soonest() string {
	soonest := ""
	for matchID, retryAt := range backoff.retryAt {
		if soonest == "" || retryAt.Before(backoff.retryAt[soonest]) {
			soonest = matchID
		}
	}
	return soonest
}

// runMatchResultVerificationWorker enforces the blind-report deadlines: the
// reminder, the report and response windows, and the result deadline (R3, R5,
// R6, R7). Its only state is the backoff of failing candidates.
func (s *Server) runMatchResultVerificationWorker(ctx context.Context) {
	if s.db == nil {
		return
	}
	var backoff matchResultVerificationBackoff
	s.sweepMatchResultVerifications(ctx, &backoff)
	ticker := time.NewTicker(matchResultVerificationSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepMatchResultVerifications(ctx, &backoff)
		}
	}
}

func (s *Server) sweepMatchResultVerifications(ctx context.Context, backoff *matchResultVerificationBackoff) {
	candidates, err := s.selectResultVerificationCandidates(ctx, backoff.excluded(time.Now()))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("select match result verifications", "class", resultVerificationErrorClass(err))
		}
		return
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return
		}
		if err = s.processResultVerificationCandidate(ctx, candidate); err != nil {
			if ctx.Err() != nil {
				return
			}
			backoff.fail(candidate.MatchID, time.Now())
			s.logger.Warn("finalize match result verification", "match_id", candidate.MatchID,
				"competition_id", candidate.CompetitionID, "class", resultVerificationErrorClass(err))
			continue
		}
		backoff.clear(candidate.MatchID)
	}
}

// selectResultVerificationCandidates discovers bounded work without locks: a
// worker must hold the competition gate before it locks a match, and the
// transaction re-plans every candidate under its locks. Every selector skips
// terminal competitions and candidates still backing off.
func (s *Server) selectResultVerificationCandidates(ctx context.Context, excluded []string) ([]matchResultVerificationCandidate, error) {
	selectors := []string{
		`SELECT v.match_id::text,v.competition_id::text FROM match_result_verifications v
		JOIN competitions c ON c.id=v.competition_id AND c.status NOT IN ('cancelled','completed')
		WHERE v.phase='awaiting_second_report' AND v.reminder_sent_at IS NULL
		  AND v.reminder_at<=now() AND v.report_deadline_at>now()
		  AND NOT (v.match_id = ANY($2::text[]::uuid[]))
		ORDER BY v.reminder_at,v.match_id LIMIT $1`,
		`SELECT v.match_id::text,v.competition_id::text FROM match_result_verifications v
		JOIN competitions c ON c.id=v.competition_id AND c.status NOT IN ('cancelled','completed')
		WHERE v.phase='awaiting_second_report' AND v.report_deadline_at<=now()
		  AND NOT (v.match_id = ANY($2::text[]::uuid[]))
		ORDER BY v.report_deadline_at,v.match_id LIMIT $1`,
		`SELECT v.match_id::text,v.competition_id::text FROM match_result_verifications v
		JOIN competitions c ON c.id=v.competition_id AND c.status NOT IN ('cancelled','completed')
		WHERE v.phase='awaiting_responses' AND v.response_deadline_at<=now()
		  AND NOT (v.match_id = ANY($2::text[]::uuid[]))
		ORDER BY v.response_deadline_at,v.match_id LIMIT $1`,
		// R7 is served by matches_deadline_idx.
		`SELECT m.id::text,m.competition_id::text FROM matches m
		JOIN competitions c ON c.id=m.competition_id AND c.status NOT IN ('cancelled','completed')
		WHERE m.state='in_progress' AND m.result_due_at IS NOT NULL AND m.result_due_at<=now()
		  AND NOT (m.id = ANY($2::text[]::uuid[]))
		ORDER BY m.result_due_at,m.id LIMIT $1`,
	}
	seen := make(map[string]bool, len(selectors)*matchResultVerificationSweepBatch)
	candidates := make([]matchResultVerificationCandidate, 0, matchResultVerificationSweepBatch)
	for _, selector := range selectors {
		rows, err := s.db.Writer.Query(ctx, selector, matchResultVerificationSweepBatch, excluded)
		if err != nil {
			return nil, err
		}
		found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[matchResultVerificationCandidate])
		if err != nil {
			return nil, err
		}
		for _, candidate := range found {
			if !seen[candidate.MatchID] {
				seen[candidate.MatchID] = true
				candidates = append(candidates, candidate)
			}
		}
	}
	return candidates, nil
}

// processResultVerificationCandidate decides one candidate in its own
// transaction. Replicas that pick the same match serialize on the gate; the
// later one re-plans, sees the changed phase or version, and writes nothing.
// Any error returns before Commit, so the deferred rollback discards every
// write.
func (s *Server) processResultVerificationCandidate(ctx context.Context, candidate matchResultVerificationCandidate) error {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err = lockCompetitionProgressionGate(ctx, tx, candidate.CompetitionID); err != nil {
		return err
	}
	var state verificationState
	state.Match, err = lockResultMatch(ctx, tx, candidate.MatchID, candidate.CompetitionID, "")
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.Verification, err = lockResultVerification(ctx, tx, candidate.MatchID); err != nil {
		return err
	}
	if state.Reports, err = lockResultReports(ctx, tx, candidate.MatchID); err != nil {
		return err
	}
	if state.Verification != nil && state.Verification.Phase == "awaiting_responses" {
		if state.BlockedEvidence, err = loadBlockedEvidence(ctx, tx, state.Match, state.Verification, state.Reports); err != nil {
			return err
		}
	}
	plan, err := planVerificationDeadline(state)
	if err != nil || plan.Action == planNone {
		return err
	}
	if err = applyVerificationDeadline(ctx, tx, state, plan); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if plan.Action != planReminder {
		s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), candidate.CompetitionID)
	}
	return nil
}

func applyVerificationDeadline(ctx context.Context, tx pgx.Tx, state verificationState, plan deadlinePlan) error {
	actor := resolutionActor{Kind: "worker", RequestID: "match-result-verification-worker"}
	switch plan.Action {
	case planReminder:
		return sendScoreReportReminder(ctx, tx, state.Match, state.Verification, plan.RemindEntryID, actor)
	case planEscalate:
		return queueResultReview(ctx, tx, state.Match, state.Verification, "evidence_unavailable", actor)
	case planFinalize:
		_, err := finalizeMatchResolution(ctx, tx, state.Match, state.Verification, plan.Resolution, actor, true)
		return err
	default:
		return unknownPlanAction(plan.Action)
	}
}

// sendScoreReportReminder warns the silent entry once before its report window
// closes (T3). The match itself does not change.
func sendScoreReportReminder(ctx context.Context, tx pgx.Tx, m lockedResultMatch, v *lockedVerification, entryID string,
	actor resolutionActor) error {
	command, err := tx.Exec(ctx, `UPDATE match_result_verifications SET reminder_sent_at=$2,updated_at=now()
		WHERE match_id=$1 AND phase='awaiting_second_report' AND reminder_sent_at IS NULL
		  AND report_deadline_at>$2`, m.ID, m.DatabaseNow)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errMatchResolutionChanged
	}
	if err = appendAuditActorContext(ctx, tx, actor.RequestID, m.OrganizationID, actor.UserID, "result.report_reminder_sent",
		"match", m.ID, nil, map[string]any{"matchId": m.ID, "entryId": entryID, "reportDeadlineAt": v.ReportDeadlineAt}); err != nil {
		return err
	}
	return insertProgressionOutbox(ctx, tx, "match", m.ID, "result.report_reminder", map[string]any{
		"matchId": m.ID, "competitionId": m.CompetitionID, "entryId": entryID,
	})
}

// resultVerificationErrorClass is the only error detail result verification
// logs: raw database errors can carry row data.
func resultVerificationErrorClass(err error) string {
	switch {
	case errors.Is(err, errMatchProgressionConflict):
		return "progression_conflict"
	case errors.Is(err, errEntryRemovalInvariant):
		return "removal_invariant"
	case errors.Is(err, errMatchResolutionChanged), errors.Is(err, errResultVerificationInvariant),
		errors.Is(err, errCompetitionClosed), errors.Is(err, errMatchProgressionInvalid):
		return "plan_invariant"
	default:
		return "database"
	}
}
