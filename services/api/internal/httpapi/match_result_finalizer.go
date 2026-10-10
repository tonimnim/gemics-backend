package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// errMatchResolutionChanged means a guarded write matched no row: the locked
	// state no longer matches the plan. The held locks make it unreachable, so a
	// caller rolls back and never retries blindly.
	errMatchResolutionChanged = errors.New("match result resolution changed")
	errEntryRemovalInvariant  = errors.New("entry removal invariant violated")
	errCompetitionClosed      = errors.New("competition is cancelled or completed")
)

// lockedResultMatch is the match row taken FOR UPDATE by lockResultMatch, with
// everything a result decision needs. DatabaseNow is statement_timestamp()
// read after the competition gate, so a request that waited on the gate
// cannot backdate a deadline decision (D26).
type lockedResultMatch struct {
	ID, CompetitionID, OrganizationID, GameID string
	CompetitionStatus                         string
	StageFormat                               string
	BestOf                                    int
	StageConfig, RulesSnapshot                []byte
	State                                     string
	Version                                   int
	ResultDueAt                               *time.Time
	HomeEntryID, AwayEntryID                  *string
	HomeCaptainID, AwayCaptainID              *string
	ActorUserID, ActorEntryID                 string
	DatabaseNow                               time.Time
}

func (m lockedResultMatch) participant(entryID string) bool {
	return entryID != "" &&
		((m.HomeEntryID != nil && *m.HomeEntryID == entryID) || (m.AwayEntryID != nil && *m.AwayEntryID == entryID))
}

func (m lockedResultMatch) opponentOf(entryID string) string {
	switch {
	case m.HomeEntryID != nil && *m.HomeEntryID == entryID && m.AwayEntryID != nil:
		return *m.AwayEntryID
	case m.AwayEntryID != nil && *m.AwayEntryID == entryID && m.HomeEntryID != nil:
		return *m.HomeEntryID
	default:
		return ""
	}
}

func (m lockedResultMatch) entryIDs() []string {
	entries := make([]string, 0, 2)
	for _, entryID := range []*string{m.HomeEntryID, m.AwayEntryID} {
		if entryID != nil {
			entries = append(entries, *entryID)
		}
	}
	return entries
}

// competitionClosed is true once nothing may change a match's result (D20).
func (m lockedResultMatch) competitionClosed() bool {
	return m.CompetitionStatus == "cancelled" || m.CompetitionStatus == "completed"
}

// resolutionActor names who finalizes a match: a player whose report agreed,
// the worker, Gamics staff, or a future automated "system" decider.
type resolutionActor struct {
	Kind      string // player | worker | staff | system
	UserID    *string
	RequestID string
}

// removalActor maps the actor onto competition_entry_removals. Players never
// remove entries; only a deadline or a Gamics decision does.
func (actor resolutionActor) removalActor() (string, *string, error) {
	switch {
	case (actor.Kind == "worker" || actor.Kind == "system") && actor.UserID == nil:
		return actor.Kind, nil, nil
	case actor.Kind == "staff" && actor.UserID != nil:
		return actor.Kind, actor.UserID, nil
	default:
		return "", nil, fmt.Errorf("%w: a %s actor cannot remove entries", errEntryRemovalInvariant, actor.Kind)
	}
}

type finalizedResult struct {
	MatchVersion    int
	SubmissionID    *string
	RemovedEntryIDs []string
	RatingChanges   []ratingChangeView
	Progression     matchProgressionResult
}

// lookupMatchCompetition is the unlocked membership probe that runs before the
// competition gate. A caller who does not play the match gets pgx.ErrNoRows, so
// spam requests can never queue behind a competition's finalization (D28).
// lockResultMatch repeats the check authoritatively under the lock.
func lookupMatchCompetition(ctx context.Context, tx pgx.Tx, matchID, userID string) (string, error) {
	var competitionID string
	err := tx.QueryRow(ctx, `SELECT match.competition_id::text FROM matches match
		WHERE match.id=$1 AND EXISTS (SELECT 1 FROM entry_members member
			WHERE member.user_id=$2 AND member.roster_role IN ('starter','substitute')
			  AND member.entry_id IN (match.home_entry_id,match.away_entry_id))`, matchID, userID).Scan(&competitionID)
	return competitionID, err
}

// lockResultMatch takes the match row FOR UPDATE after the competition gate.
// actorID may be empty (worker, review decisions); otherwise ActorEntryID is
// the actor's starter or substitute entry in the match, or empty. Coaches
// never report.
func lockResultMatch(ctx context.Context, tx pgx.Tx, matchID, competitionID, actorID string) (lockedResultMatch, error) {
	m := lockedResultMatch{ActorUserID: actorID}
	var actorEntryID *string
	err := tx.QueryRow(ctx, `SELECT match.id::text,match.competition_id::text,competition.organization_id::text,
		competition.game_id,competition.status,competition.rules_snapshot,stage.format,stage.best_of,stage.config,
		match.state,match.version,match.result_due_at,match.home_entry_id::text,match.away_entry_id::text,
		home_entry.captain_user_id::text,away_entry.captain_user_id::text,
		(SELECT member.entry_id::text FROM entry_members member
			WHERE member.user_id=NULLIF($3::text,'')::uuid AND member.roster_role IN ('starter','substitute')
			  AND member.entry_id IN (match.home_entry_id,match.away_entry_id)
			ORDER BY member.entry_id LIMIT 1),
		statement_timestamp()
		FROM matches match
		JOIN competitions competition ON competition.id=match.competition_id
		JOIN competition_stages stage ON stage.id=match.stage_id
		LEFT JOIN competition_entries home_entry ON home_entry.id=match.home_entry_id
		LEFT JOIN competition_entries away_entry ON away_entry.id=match.away_entry_id
		WHERE match.id=$1 AND match.competition_id=$2
		FOR UPDATE OF match`, matchID, competitionID, actorID).Scan(
		&m.ID, &m.CompetitionID, &m.OrganizationID, &m.GameID, &m.CompetitionStatus, &m.RulesSnapshot,
		&m.StageFormat, &m.BestOf, &m.StageConfig, &m.State, &m.Version, &m.ResultDueAt,
		&m.HomeEntryID, &m.AwayEntryID, &m.HomeCaptainID, &m.AwayCaptainID, &actorEntryID, &m.DatabaseNow)
	if err != nil {
		return lockedResultMatch{}, err
	}
	if actorEntryID != nil {
		m.ActorEntryID = *actorEntryID
	}
	m.ResultDueAt = utcTime(m.ResultDueAt)
	m.DatabaseNow = m.DatabaseNow.UTC()
	return m, nil
}

// lockResultVerification returns nil when the match has no verification row:
// nobody has reported yet.
func lockResultVerification(ctx context.Context, tx pgx.Tx, matchID string) (*lockedVerification, error) {
	var v lockedVerification
	var responseWindow int
	err := tx.QueryRow(ctx, `SELECT phase,first_report_entry_id::text,report_deadline_at,reminder_at,
		reminder_sent_at,mismatch_at,response_deadline_at,rejected_by::text,response_window_seconds,version
		FROM match_result_verifications WHERE match_id=$1 FOR UPDATE`, matchID).Scan(
		&v.Phase, &v.FirstReportEntryID, &v.ReportDeadlineAt, &v.ReminderAt, &v.ReminderSentAt, &v.MismatchAt,
		&v.ResponseDeadlineAt, &v.RejectedBy, &responseWindow, &v.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.ReportDeadlineAt, v.ReminderAt = v.ReportDeadlineAt.UTC(), v.ReminderAt.UTC()
	v.ReminderSentAt, v.MismatchAt, v.ResponseDeadlineAt = utcTime(v.ReminderSentAt), utcTime(v.MismatchAt), utcTime(v.ResponseDeadlineAt)
	v.ResponseWindow = time.Duration(responseWindow) * time.Second
	return &v, nil
}

// decodeStoredJSON decodes a JSON column. A stored value that no longer
// decodes is a corrupt row, an invariant rather than an outage, so it is
// logged at Error and answered with 500 instead of 503.
func decodeStoredJSON(raw []byte, target any, column string) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: stored %s are not valid JSON", errResultVerificationInvariant, column)
	}
	return nil
}

// lockResultReports locks the match's reports in (entry_id, kind) order. A
// screenshot ("final") has no score, so its claim stays empty.
func lockResultReports(ctx context.Context, tx pgx.Tx, matchID string) ([]verificationReport, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,entry_id::text,reported_by::text,kind,home_score,away_score,
		tiebreak_type,home_tiebreak_score,away_tiebreak_score,game_results,reported_at
		FROM match_result_reports WHERE match_id=$1 ORDER BY entry_id,kind FOR UPDATE`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := make([]verificationReport, 0, 4)
	for rows.Next() {
		var report verificationReport
		var homeScore, awayScore *int
		var tiebreakType *string
		var homeTiebreak, awayTiebreak *int
		var games []byte
		if err = rows.Scan(&report.ID, &report.EntryID, &report.ReportedBy, &report.Kind,
			&homeScore, &awayScore, &tiebreakType, &homeTiebreak, &awayTiebreak,
			&games, &report.ReportedAt); err != nil {
			return nil, err
		}
		if homeScore != nil && awayScore != nil {
			report.Claim.HomeScore, report.Claim.AwayScore = *homeScore, *awayScore
		}
		if tiebreakType != nil && homeTiebreak != nil && awayTiebreak != nil {
			report.Claim.Tiebreak = &tiebreakScoreInput{Type: *tiebreakType, HomeScore: *homeTiebreak, AwayScore: *awayTiebreak}
		}
		if games != nil {
			if err = decodeStoredJSON(games, &report.Claim.Games, "report games"); err != nil {
				return nil, err
			}
		}
		report.ReportedAt = report.ReportedAt.UTC()
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

// loadBlockedEvidence reads, without locking, the uploads that can stop a
// non-responding entry's removal: made by one of its players during the
// response window and still processing, or failed for a Gamics-side reason.
// The planner re-checks every row.
func loadBlockedEvidence(ctx context.Context, queryer matchQueryer, m lockedResultMatch, v *lockedVerification,
	reports []verificationReport) ([]blockedEvidence, error) {
	return loadResponseWindowUploads(ctx, queryer, m, v, reports, true)
}

// loadResponseWindowUploads reads the unbound image uploads that the
// non-responding entries' players made during the response window. With
// blockingOnly it keeps only the uploads that block a removal; otherwise it
// returns all of them with their current status, which is what a reviewer of
// an evidence_unavailable case needs once processing has caught up (T16).
func loadResponseWindowUploads(ctx context.Context, queryer matchQueryer, m lockedResultMatch, v *lockedVerification,
	reports []verificationReport, blockingOnly bool) ([]blockedEvidence, error) {
	evidence := make([]blockedEvidence, 0)
	if v == nil || v.MismatchAt == nil || v.ResponseDeadlineAt == nil {
		return evidence, nil
	}
	state := verificationState{Match: m, Reports: reports}
	silent := make([]string, 0, 2)
	for _, entryID := range m.entryIDs() {
		if state.report(entryID, "final") == nil {
			silent = append(silent, entryID)
		}
	}
	if len(silent) == 0 {
		return evidence, nil
	}
	query := `SELECT evidence.id::text,member.entry_id::text,evidence.owner_user_id::text,
		evidence.status,evidence.processing_error_code,evidence.created_at
		FROM evidence_uploads evidence
		JOIN entry_members member ON member.user_id=evidence.owner_user_id
		  AND member.entry_id = ANY($1::text[]::uuid[]) AND member.roster_role IN ('starter','substitute')
		WHERE evidence.media_kind='image' AND evidence.bound_id IS NULL
		  AND evidence.created_at >= $2 AND evidence.created_at < $3`
	if blockingOnly {
		query += `
		  AND (evidence.status='processing'
		       OR (evidence.status='failed'
		           AND evidence.processing_error_code IN ('verification_unavailable','processing_aborted')))`
	}
	rows, err := queryer.Query(ctx, query+`
		ORDER BY member.entry_id,evidence.id`, silent, *v.MismatchAt, *v.ResponseDeadlineAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item blockedEvidence
		if err = rows.Scan(&item.EvidenceID, &item.EntryID, &item.UploadedBy, &item.Status,
			&item.ProcessingErrorCode, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.CreatedAt = item.CreatedAt.UTC()
		evidence = append(evidence, item)
	}
	return evidence, rows.Err()
}

// resultReviewConflictClause is true when the user plays in or captains either
// entry of the match, or belongs to the competition's organization. Such staff
// are treated as the player or organizer they are: they can neither see nor
// decide the match's review, nor read its report evidence (D27). Both
// arguments are SQL written by the caller, never request input, and the inner
// aliases are unique so the clause nests in any query.
func resultReviewConflictClause(matchAlias, userParam string) string {
	const clause = `(EXISTS (SELECT 1 FROM entry_members conflict_member
			WHERE conflict_member.entry_id IN ({match}.home_entry_id,{match}.away_entry_id)
			  AND conflict_member.user_id={user})
		OR EXISTS (SELECT 1 FROM competition_entries conflict_entry
			WHERE conflict_entry.id IN ({match}.home_entry_id,{match}.away_entry_id)
			  AND conflict_entry.captain_user_id={user})
		OR EXISTS (SELECT 1 FROM competitions conflict_competition
			JOIN organization_members conflict_org_member
			  ON conflict_org_member.organization_id=conflict_competition.organization_id
			WHERE conflict_competition.id={match}.competition_id AND conflict_org_member.user_id={user}))`
	return strings.NewReplacer("{match}", matchAlias, "{user}", userParam).Replace(clause)
}

// reviewWindowUploadClause is true when the evidence row is an unbound upload a
// non-responding player made during the response window of an
// evidence_unavailable review, and the user has no conflict with that match.
// Those uploads are what the review exists to examine (T16), yet they never
// reached a report, so the bound-evidence rule alone would hide them. The
// window and non-responder rules match loadResponseWindowUploads. Both
// arguments are SQL written by the caller, never request input.
func reviewWindowUploadClause(evidenceAlias, userParam string) string {
	const clause = `({evidence}.bound_id IS NULL AND EXISTS (SELECT 1 FROM match_result_reviews window_review
			JOIN match_result_verifications window_verification
			  ON window_verification.match_id=window_review.match_id
			JOIN matches window_match ON window_match.id=window_review.match_id
			JOIN entry_members window_member ON window_member.user_id={evidence}.owner_user_id
			  AND window_member.entry_id IN (window_match.home_entry_id,window_match.away_entry_id)
			  AND window_member.roster_role IN ('starter','substitute')
			WHERE window_review.reason='evidence_unavailable'
			  AND {evidence}.created_at >= window_verification.mismatch_at
			  AND {evidence}.created_at < window_verification.response_deadline_at
			  AND NOT EXISTS (SELECT 1 FROM match_result_reports window_final
				WHERE window_final.match_id=window_review.match_id
				  AND window_final.entry_id=window_member.entry_id AND window_final.kind='final')
			  AND NOT {conflict}))`
	return strings.NewReplacer("{evidence}", evidenceAlias,
		"{conflict}", resultReviewConflictClause("window_match", userParam)).Replace(clause)
}

// validateMatchResolution is the applier's own guard: the planners and the
// review decision already enforce every rule, so a failure here is a bug and
// nothing is written.
func validateMatchResolution(m lockedResultMatch, r matchResolution) error {
	if m.competitionClosed() {
		return errCompetitionClosed
	}
	invalid := func(reason string) error {
		return fmt.Errorf("%w: %s", errResultVerificationInvariant, reason)
	}
	if m.HomeEntryID == nil || m.AwayEntryID == nil {
		return invalid("the match has no two entries")
	}
	switch r.FinalState {
	case "completed":
		if r.Claim == nil || len(r.Claim.Games) == 0 || r.ClaimAuthorID == nil || r.Origin == "" {
			return invalid("a completed match needs a claim, its author and an origin")
		}
	case "forfeit":
		if r.WinnerEntryID == nil || r.Claim != nil {
			return invalid("a forfeit needs a winner and no score")
		}
	case "cancelled":
		if r.WinnerEntryID != nil || r.Claim != nil {
			return invalid("a cancelled match has no winner and no score")
		}
	default:
		return invalid("unknown final state")
	}
	if r.WinnerEntryID != nil && !m.participant(*r.WinnerEntryID) {
		return invalid("the winner does not play the match")
	}
	if r.CompletionReason == "" || r.Cause == "" || (r.ApplyRatings && r.FinalState != "completed") {
		return invalid("reason, cause or ratings do not fit the final state")
	}
	if len(r.RemoveEntryIDs) > 0 && r.RemovalReason == "" {
		return invalid("a removal needs a reason")
	}
	for index, entryID := range r.RemoveEntryIDs {
		if !m.participant(entryID) || (index > 0 && r.RemoveEntryIDs[index-1] >= entryID) ||
			(r.WinnerEntryID != nil && *r.WinnerEntryID == entryID) {
			return invalid("removed entries must be sorted, distinct losing participants")
		}
	}
	return nil
}

// finalizeMatchResolution applies a terminal outcome in one fixed order,
// pinned by a source-shape test: the version-guarded match update first, then
// removals (so progression re-reads the removed status and voids their slots),
// the canonical row, the verification row, progression, ratings, and finally
// audit and outbox. Any error returns at once; the caller never commits.
func finalizeMatchResolution(ctx context.Context, tx pgx.Tx, m lockedResultMatch, v *lockedVerification,
	r matchResolution, actor resolutionActor, emitTerminalEvent bool) (finalizedResult, error) {
	if err := validateMatchResolution(m, r); err != nil {
		return finalizedResult{}, err
	}
	if (v == nil) != (r.Resolution == "") {
		return finalizedResult{}, fmt.Errorf("%w: the resolution does not match the verification row", errResultVerificationInvariant)
	}
	result := finalizedResult{RatingChanges: []ratingChangeView{}}
	var err error
	result.MatchVersion, err = updateResultVersion(ctx, tx, `UPDATE matches SET state=$1,winner_entry_id=$2,completion_reason=$3,completed_at=$4,
		version=version+1,updated_at=now()
		WHERE id=$5 AND competition_id=$6 AND version=$7 AND state=$8 RETURNING version`,
		r.FinalState, r.WinnerEntryID, r.CompletionReason, m.DatabaseNow, m.ID, m.CompetitionID, m.Version, m.State)
	if err != nil {
		return finalizedResult{}, err
	}
	if result.RemovedEntryIDs, err = removeEntriesFromTournament(ctx, tx, m, r.RemoveEntryIDs, r.RemovalReason, actor, m.DatabaseNow); err != nil {
		return finalizedResult{}, err
	}
	if r.Claim != nil {
		if result.SubmissionID, err = insertCanonicalResult(ctx, tx, m, r); err != nil {
			return finalizedResult{}, err
		}
	}
	if v != nil {
		if _, err = updateResultVersion(ctx, tx, `UPDATE match_result_verifications SET phase='resolved',resolution=$2,
			resolved_at=$3,canonical_submission_id=$4,version=version+1,updated_at=now()
			WHERE match_id=$1 AND version=$5 AND phase=$6 RETURNING version`,
			m.ID, r.Resolution, m.DatabaseNow, result.SubmissionID, v.Version, v.Phase); err != nil {
			return finalizedResult{}, err
		}
	}
	progression := matchProgressionInput{
		MatchID: m.ID, CompetitionID: m.CompetitionID, FinalizedVersion: result.MatchVersion,
		FinalState: r.FinalState, WinnerEntryID: r.WinnerEntryID, Cause: r.Cause,
	}
	if r.Claim != nil {
		progression.HomeScore, progression.AwayScore = &r.Claim.HomeScore, &r.Claim.AwayScore
	}
	if actor.Kind == "staff" {
		progression.ActorUserID = actor.UserID
	}
	if result.Progression, err = applyMatchProgression(ctx, tx, progression); err != nil {
		return finalizedResult{}, err
	}
	if r.ApplyRatings {
		if m.HomeCaptainID == nil || m.AwayCaptainID == nil {
			return finalizedResult{}, fmt.Errorf("%w: rated entries need captains", errResultVerificationInvariant)
		}
		if result.RatingChanges, err = applyConfirmedResultRatings(ctx, tx, resultRatingInput{
			GameID: m.GameID, HomePlayerID: *m.HomeCaptainID, AwayPlayerID: *m.AwayCaptainID,
			HomeScore: r.Claim.HomeScore, AwayScore: r.Claim.AwayScore, Tiebreak: r.Claim.Tiebreak,
		}, m.DatabaseNow); err != nil {
			return finalizedResult{}, err
		}
	}
	if err = writeResolutionAuditAndOutbox(ctx, tx, m, r, actor, result, emitTerminalEvent); err != nil {
		return finalizedResult{}, err
	}
	return result, nil
}

// insertCanonicalResult writes the single confirmed result_submissions row the
// bracket and public history read. match_version is the pre-finalization
// version, following the single-submission rows it replaces.
func insertCanonicalResult(ctx context.Context, tx pgx.Tx, m lockedResultMatch, r matchResolution) (*string, error) {
	games, err := json.Marshal(r.Claim.Games)
	if err != nil {
		return nil, err
	}
	tiebreakType, homeTiebreak, awayTiebreak := r.Claim.tiebreakColumns()
	var submissionID string
	if err = tx.QueryRow(ctx, `INSERT INTO result_submissions
		(match_id,submitted_by,home_score,away_score,tiebreak_type,home_tiebreak_score,away_tiebreak_score,
		 game_results,evidence_objects,status,match_version,submitted_at,decided_at,decided_by,origin)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'[]'::jsonb,'confirmed',$9,$10,$10,$11,$12) RETURNING id::text`,
		m.ID, *r.ClaimAuthorID, r.Claim.HomeScore, r.Claim.AwayScore, tiebreakType, homeTiebreak, awayTiebreak,
		json.RawMessage(games), m.Version, m.DatabaseNow, r.ConfirmerID, r.Origin).Scan(&submissionID); err != nil {
		return nil, err
	}
	return &submissionID, nil
}

// writeResolutionAuditAndOutbox records the terminal transition. Review
// decisions pass emitTerminalEvent=false and push their own event instead.
func writeResolutionAuditAndOutbox(ctx context.Context, tx pgx.Tx, m lockedResultMatch, r matchResolution,
	actor resolutionActor, result finalizedResult, emitTerminalEvent bool) error {
	action := "match.cancelled"
	switch r.FinalState {
	case "completed":
		action = "result.confirmed"
	case "forfeit":
		action = "match.forfeited"
	}
	after := map[string]any{
		"state": r.FinalState, "matchVersion": result.MatchVersion, "winnerEntryId": r.WinnerEntryID,
		"completionReason": r.CompletionReason, "removedEntryIds": result.RemovedEntryIDs,
	}
	if result.SubmissionID != nil {
		after["submissionId"], after["origin"] = *result.SubmissionID, r.Origin
	}
	if err := appendAuditActorContext(ctx, tx, actor.RequestID, m.OrganizationID, actor.UserID, action, "match", m.ID,
		map[string]any{"state": m.State, "matchVersion": m.Version}, after); err != nil {
		return err
	}
	if !emitTerminalEvent {
		return nil
	}
	if r.FinalState == "completed" {
		return insertProgressionOutbox(ctx, tx, "match", m.ID, "match.result_confirmed", map[string]any{
			"matchId": m.ID, "competitionId": m.CompetitionID, "matchVersion": result.MatchVersion,
			"winnerEntryId": r.WinnerEntryID, "origin": r.Origin, "submissionId": result.SubmissionID,
			"ratingChanges": result.RatingChanges,
		})
	}
	return insertProgressionOutbox(ctx, tx, "match", m.ID, action, map[string]any{
		"matchId": m.ID, "competitionId": m.CompetitionID, "matchVersion": result.MatchVersion,
		"winnerEntryId": r.WinnerEntryID, "completionReason": r.CompletionReason,
		"removedEntryIds": result.RemovedEntryIDs,
	})
}

// removeEntriesFromTournament disqualifies the entries (R5-R8) and returns the
// ones it changed. It runs after the match update and before progression, so
// the engine re-reads the removed status and voids their slots. Entries that
// already left (withdrawn or disqualified) are not rewritten and get no removal
// row; they still lose the match.
func removeEntriesFromTournament(ctx context.Context, tx pgx.Tx, m lockedResultMatch, entryIDs []string,
	reason string, actor resolutionActor, removedAt time.Time) ([]string, error) {
	removed := make([]string, 0, len(entryIDs))
	if len(entryIDs) == 0 {
		return removed, nil
	}
	actorKind, actorUserID, err := actor.removalActor()
	if err != nil {
		return nil, err
	}
	statuses, err := lockRemovedEntries(ctx, tx, m.CompetitionID, entryIDs)
	if err != nil {
		return nil, err
	}
	for _, entryID := range entryIDs {
		previous := statuses[entryID]
		switch previous {
		case "registered", "checked_in", "accepted", "withdrawal_pending":
		default:
			continue
		}
		if err = disqualifyEntry(ctx, tx, m, entryID, previous, reason, actorKind, actorUserID, removedAt); err != nil {
			return nil, err
		}
		if err = appendAuditActorContext(ctx, tx, actor.RequestID, m.OrganizationID, actor.UserID,
			"competition.entry_removed", "competition_entry", entryID, map[string]any{"status": previous},
			map[string]any{"status": "disqualified", "reasonCode": reason, "matchId": m.ID}); err != nil {
			return nil, err
		}
		if err = insertProgressionOutbox(ctx, tx, "competition_entry", entryID, "competition.entry_removed", map[string]any{
			"entryId": entryID, "competitionId": m.CompetitionID, "matchId": m.ID, "reasonCode": reason,
		}); err != nil {
			return nil, err
		}
		removed = append(removed, entryID)
	}
	// Knockout entries have one live match at a time and round robin releases
	// one round at a time stage-wide, so the trigger is the only live match of
	// a removed entry. Anything else would strand a match with a removed player.
	var otherMatchID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM matches WHERE competition_id=$1 AND id<>$2
		AND state IN ('ready','in_progress','awaiting_confirmation','disputed')
		AND (home_entry_id = ANY($3::text[]::uuid[]) OR away_entry_id = ANY($3::text[]::uuid[])) LIMIT 1`,
		m.CompetitionID, m.ID, entryIDs).Scan(&otherMatchID)
	if err == nil {
		return nil, fmt.Errorf("%w: a removed entry has another live match", errEntryRemovalInvariant)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return removed, nil
}

// lockRemovedEntries locks the entries in id order and returns their status.
// Every entry must belong to the competition.
func lockRemovedEntries(ctx context.Context, tx pgx.Tx, competitionID string, entryIDs []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,status FROM competition_entries
		WHERE competition_id=$1 AND id = ANY($2::text[]::uuid[]) ORDER BY id FOR UPDATE`, competitionID, entryIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	statuses := make(map[string]string, len(entryIDs))
	for rows.Next() {
		var entryID, status string
		if err = rows.Scan(&entryID, &status); err != nil {
			return nil, err
		}
		statuses[entryID] = status
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, entryID := range entryIDs {
		if _, found := statuses[entryID]; !found {
			return nil, fmt.Errorf("%w: an entry is not in the competition", errEntryRemovalInvariant)
		}
	}
	return statuses, nil
}

// disqualifyEntry writes the status change and its idempotent removal row.
// Both must affect exactly one row under the entry lock.
func disqualifyEntry(ctx context.Context, tx pgx.Tx, m lockedResultMatch, entryID, previous, reason, actorKind string,
	actorUserID *string, removedAt time.Time) error {
	command, err := tx.Exec(ctx, `UPDATE competition_entries SET status='disqualified',updated_at=now()
		WHERE id=$1 AND competition_id=$2
		  AND status IN ('registered','checked_in','accepted','withdrawal_pending')`, entryID, m.CompetitionID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("%w: the entry status changed under its lock", errEntryRemovalInvariant)
	}
	command, err = tx.Exec(ctx, `INSERT INTO competition_entry_removals
		(entry_id,competition_id,match_id,reason_code,previous_status,actor_kind,actor_user_id,removed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (entry_id) DO NOTHING`,
		entryID, m.CompetitionID, m.ID, reason, previous, actorKind, actorUserID, removedAt)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("%w: a live entry already has a removal row", errEntryRemovalInvariant)
	}
	return nil
}

// queueResultReview hands a still-disputed match to the Gamics review queue
// (T9, T16). The match keeps its state and gains a version; the push never
// carries the review reason.
func queueResultReview(ctx context.Context, tx pgx.Tx, m lockedResultMatch, v *lockedVerification, reason string,
	actor resolutionActor) error {
	if _, err := updateResultVersion(ctx, tx, `UPDATE match_result_verifications SET phase='in_review',
		version=version+1,updated_at=now()
		WHERE match_id=$1 AND phase='awaiting_screenshots' AND version=$2 RETURNING version`, m.ID, v.Version); err != nil {
		return err
	}
	var reviewID string
	if err := tx.QueryRow(ctx, `INSERT INTO match_result_reviews (match_id,competition_id,reason,queued_at)
		VALUES ($1,$2,$3,$4) RETURNING id::text`, m.ID, m.CompetitionID, reason, m.DatabaseNow).Scan(&reviewID); err != nil {
		return err
	}
	matchVersion, err := bumpDisputedMatch(ctx, tx, m)
	if err != nil {
		return err
	}
	if err = appendAuditActorContext(ctx, tx, actor.RequestID, m.OrganizationID, actor.UserID, "result.review_queued",
		"match", m.ID, map[string]any{"phase": "awaiting_screenshots"},
		map[string]any{"reviewId": reviewID, "reason": reason, "matchVersion": matchVersion}); err != nil {
		return err
	}
	return insertProgressionOutbox(ctx, tx, "match", m.ID, "result.under_review", map[string]any{
		"matchId": m.ID, "competitionId": m.CompetitionID, "matchVersion": matchVersion,
	})
}

// bumpDisputedMatch records a change inside the dispute (a final report or an
// escalation) so clients holding the old version refresh.
func bumpDisputedMatch(ctx context.Context, tx pgx.Tx, m lockedResultMatch) (int, error) {
	return updateResultVersion(ctx, tx, `UPDATE matches SET version=version+1,updated_at=now()
		WHERE id=$1 AND competition_id=$2 AND version=$3 AND state='disputed' RETURNING version`,
		m.ID, m.CompetitionID, m.Version)
}

// updateResultVersion runs one guarded UPDATE … RETURNING version. No row
// means the locked state no longer matches the plan.
func updateResultVersion(ctx context.Context, tx pgx.Tx, query string, args ...any) (int, error) {
	var version int
	err := tx.QueryRow(ctx, query, args...).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errMatchResolutionChanged
	}
	return version, err
}
