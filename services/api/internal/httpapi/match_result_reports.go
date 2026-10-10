package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// After a rejection each entry sends exactly one processed screenshot (R6).
// HEIC and video uploads are never accepted as match evidence.
const (
	resultScreenshotMinItems = 1
	resultScreenshotMaxItems = 1
)

// resultConfirmationInput is the other entry's answer to a submitted result.
type resultConfirmationInput struct {
	Decision string `json:"decision"`
}

// resultScreenshotInput is an entry's one screenshot after a rejection.
type resultScreenshotInput struct {
	EvidenceID string `json:"evidenceId"`
}

// Idempotency scopes are bound to the actor and the match, and differ per
// action, so one key can never replay another player's view.
func scoreReportScope(userID, matchID string) string {
	return "score-report:" + userID + ":" + matchID
}

func resultConfirmationScope(userID, matchID string) string {
	return "score-confirmation:" + userID + ":" + matchID
}

func resultScreenshotScope(userID, matchID string) string {
	return "score-screenshot:" + userID + ":" + matchID
}

// problem rejects what no stage could accept, before any lock is taken.
func (input scoreReportInput) problem() string {
	if !input.DeclarationAccepted {
		return "Confirm that the reported score is accurate before submitting it."
	}
	return scoreRangeProblem(input.HomeScore, input.AwayScore)
}

// createScoreReport records the submitted result (R2-R4). Either entry submits
// it; the other entry is then asked to confirm or reject it.
func (s *Server) createScoreReport(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	matchID := strings.ToLower(strings.TrimSpace(r.PathValue("matchId")))
	if !uuidPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var input scoreReportInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if message := input.problem(); message != "" {
		writeError(w, http.StatusBadRequest, "invalid_score_report", message)
		return
	}
	s.recordScoreReport(w, r, "report", matchID, idempotencyKey, input,
		func(ctx context.Context, tx pgx.Tx, state verificationState, actor resolutionActor) (*planRejection, error) {
			return applyScoreReport(ctx, tx, state, input, actor)
		})
}

// createResultConfirmation records the other entry's answer: confirm or reject
// the submitted result (T4, T5).
func (s *Server) createResultConfirmation(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	matchID := strings.ToLower(strings.TrimSpace(r.PathValue("matchId")))
	if !uuidPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var input resultConfirmationInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Decision != "confirm" && input.Decision != "reject" {
		writeError(w, http.StatusBadRequest, "invalid_decision", "Choose confirm or reject.")
		return
	}
	s.recordScoreReport(w, r, "confirmation", matchID, idempotencyKey, input,
		func(ctx context.Context, tx pgx.Tx, state verificationState, actor resolutionActor) (*planRejection, error) {
			return applyResultConfirmation(ctx, tx, state, input.Decision == "confirm", actor)
		})
}

// createResultScreenshot records an entry's one screenshot after a rejection
// (R6).
func (s *Server) createResultScreenshot(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	matchID := strings.ToLower(strings.TrimSpace(r.PathValue("matchId")))
	if !uuidPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var input resultScreenshotInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.EvidenceID = strings.ToLower(strings.TrimSpace(input.EvidenceID))
	if !uuidPattern.MatchString(input.EvidenceID) {
		writeError(w, http.StatusBadRequest, "invalid_evidence", "Attach one screenshot of the Full Time screen.")
		return
	}
	s.recordScoreReport(w, r, "screenshot", matchID, idempotencyKey, input,
		func(ctx context.Context, tx pgx.Tx, state verificationState, actor resolutionActor) (*planRejection, error) {
			return applyResultScreenshot(ctx, tx, state, input.EvidenceID, actor)
		})
}

// recordScoreReport runs one result write (kind report, confirmation or
// screenshot) in the mandatory lock order: idempotency key, unlocked membership
// probe, competition gate, match, verification row and reports; apply takes
// any later locks. The stored and returned body is the caller's view of the
// match.
func (s *Server) recordScoreReport(w http.ResponseWriter, r *http.Request, kind, matchID, idempotencyKey string,
	input any, apply func(context.Context, pgx.Tx, verificationState, resolutionActor) (*planRejection, error)) {
	ctx := r.Context()
	userID := identityFromContext(ctx).UserID
	scope := scoreReportScope(userID, matchID)
	switch kind {
	case "confirmation":
		scope = resultConfirmationScope(userID, matchID)
	case "screenshot":
		scope = resultScreenshotScope(userID, matchID)
	}
	unavailable := func() {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to report the score.")
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to report the score.")
		return
	}
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		unavailable()
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	replay, err := beginIdempotentRequest(ctx, tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another score report.")
		return
	}
	if err != nil {
		unavailable()
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}

	state, err := lockScoreReportState(ctx, tx, matchID, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	if err != nil {
		unavailable()
		return
	}
	rejection, err := apply(ctx, tx, state, resolutionActor{Kind: "player", UserID: &userID, RequestID: r.Header.Get("X-Request-ID")})
	if err != nil {
		s.writeScoreReportFailure(w, matchID, err)
		return
	}
	if rejection != nil {
		if rejection.Status >= http.StatusInternalServerError {
			s.logger.Error("score report state invariant", "match_id", matchID, "competition_id", state.Match.CompetitionID,
				"cause", rejection.Cause)
		}
		writeError(w, rejection.Status, rejection.Code, rejection.Message)
		return
	}
	body, err := scoreReportResponse(ctx, tx, userID, matchID, state.Match.DatabaseNow)
	if err != nil {
		unavailable()
		return
	}
	if err = finishIdempotentRequest(ctx, tx, scope, idempotencyKey, http.StatusCreated, body); err != nil || tx.Commit(ctx) != nil {
		unavailable()
		return
	}
	s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), state.Match.CompetitionID)
	writeResultRawJSON(w, http.StatusCreated, body)
}

// lockScoreReportState takes the report locks in the mandatory order. A caller
// who does not play the match gets pgx.ErrNoRows, before the gate whenever the
// unlocked probe already rules them out.
func lockScoreReportState(ctx context.Context, tx pgx.Tx, matchID, userID string) (verificationState, error) {
	competitionID, err := lookupMatchCompetition(ctx, tx, matchID, userID)
	if err != nil {
		return verificationState{}, err
	}
	if err = lockCompetitionProgressionGate(ctx, tx, competitionID); err != nil {
		return verificationState{}, err
	}
	var state verificationState
	if state.Match, err = lockResultMatch(ctx, tx, matchID, competitionID, userID); err != nil {
		return verificationState{}, err
	}
	if state.Match.ActorEntryID == "" {
		return verificationState{}, pgx.ErrNoRows
	}
	if state.Verification, err = lockResultVerification(ctx, tx, matchID); err != nil {
		return verificationState{}, err
	}
	if state.Reports, err = lockResultReports(ctx, tx, matchID); err != nil {
		return verificationState{}, err
	}
	return state, nil
}

// writeScoreReportFailure maps a failed write. A changed match can be retried
// after a refresh; an invariant is a bug and is logged by class only.
func (s *Server) writeScoreReportFailure(w http.ResponseWriter, matchID string, err error) {
	class := resultVerificationErrorClass(err)
	switch {
	case errors.Is(err, errMatchResolutionChanged), errors.Is(err, errMatchProgressionConflict):
		writeError(w, http.StatusConflict, "match_changed", "The match changed while the score was being reported. Refresh it and try again.")
	case class == "database":
		s.logger.Warn("record score report", "match_id", matchID, "class", class)
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to report the score.")
	default:
		s.logger.Error("record score report", "match_id", matchID, "class", class)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to report the score.")
	}
}

// scoreReportResponse is the caller's room after the write, read back through
// the same room query.
func scoreReportResponse(ctx context.Context, tx pgx.Tx, userID, matchID string, now time.Time) ([]byte, error) {
	record, err := loadMatchRecord(ctx, tx, userID, matchID, false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"data": map[string]any{"match": record.response(userID, now)}})
}

// applyScoreReport stores the submitted result and opens the confirmation
// window (T2).
func applyScoreReport(ctx context.Context, tx pgx.Tx, state verificationState, input scoreReportInput,
	actor resolutionActor) (*planRejection, error) {
	m := state.Match
	claim, message := normalizeScoreClaim(input, m.BestOf, m.StageFormat)
	if message != "" {
		return &planRejection{Status: http.StatusBadRequest, Code: "invalid_score", Message: message}, nil
	}
	plan, rejection := planScoreReport(state, m.ActorEntryID)
	if rejection != nil {
		return rejection, nil
	}
	if plan.Action != planOpen {
		return nil, unknownPlanAction(plan.Action)
	}
	if err := openResultVerification(ctx, tx, m); err != nil {
		return nil, err
	}
	if _, err := insertScoreReport(ctx, tx, m, actor, "initial", &claim); isUniqueViolation(err) {
		return &planRejection{Status: http.StatusConflict, Code: "result_already_submitted",
			Message: "Your side has already submitted the result for this match."}, nil
	} else if err != nil {
		return nil, err
	}
	return nil, startScoreReportWindow(ctx, tx, m)
}

// applyResultConfirmation applies the other entry's answer: confirming
// finalizes the submitted result (T4) and rejecting opens the screenshot
// window (T5).
func applyResultConfirmation(ctx context.Context, tx pgx.Tx, state verificationState, confirm bool,
	actor resolutionActor) (*planRejection, error) {
	m := state.Match
	plan, rejection := planConfirmation(state, m.ActorEntryID, confirm)
	if rejection != nil {
		return rejection, nil
	}
	switch plan.Action {
	case planFinalize:
		_, err := finalizeMatchResolution(ctx, tx, m, state.Verification, plan.Resolution, actor, true)
		return nil, err
	case planMismatch:
		return nil, openScoreMismatch(ctx, tx, m, state.Verification, plan.ResponseDeadlineAt, actor)
	default:
		return nil, unknownPlanAction(plan.Action)
	}
}

// applyResultScreenshot stores an entry's one screenshot after a rejection: a
// lone screenshot waits for the other entry (T8) and the second sends the
// match to Gamics (T9).
func applyResultScreenshot(ctx context.Context, tx pgx.Tx, state verificationState, evidenceID string,
	actor resolutionActor) (*planRejection, error) {
	m := state.Match
	// Planning reads no rows, so it runs first: a screenshot that is no longer
	// allowed is refused as such rather than as unusable evidence. The
	// evidence lock still follows the report locks.
	plan, rejection := planScreenshot(state, m.ActorEntryID)
	if rejection != nil {
		return rejection, nil
	}
	evidenceIDs := []string{evidenceID}
	err := lockCompletedEvidence(ctx, tx, evidenceIDs, m.ActorUserID, screenshotMediaTypes)
	if errors.Is(err, pgx.ErrNoRows) {
		return &planRejection{Status: http.StatusConflict, Code: "evidence_not_ready",
			Message: "The screenshot must be a processed JPEG or PNG that you uploaded and have not used before."}, nil
	}
	if err != nil {
		return nil, err
	}
	reportID, err := insertScoreReport(ctx, tx, m, actor, "final", nil)
	if isUniqueViolation(err) {
		return &planRejection{Status: http.StatusConflict, Code: "screenshot_already_submitted",
			Message: "Your side has already sent its screenshot for this match."}, nil
	}
	if err != nil {
		return nil, err
	}
	if err = attachFinalReportEvidence(ctx, tx, reportID, evidenceIDs); err != nil {
		return nil, err
	}
	switch plan.Action {
	case planReview:
		err = queueResultReview(ctx, tx, m, state.Verification, "reports_differ", actor)
	case planWait:
		_, err = bumpDisputedMatch(ctx, tx, m)
	default:
		err = unknownPlanAction(plan.Action)
	}
	return nil, err
}

// openResultVerification snapshots the resolved windows when the result is
// submitted (T2). Every deadline is computed from the database clock.
func openResultVerification(ctx context.Context, tx pgx.Tx, m lockedResultMatch) error {
	settings := resolveMatchSettings(m.GameID, m.StageFormat, m.RulesSnapshot, m.StageConfig).Verification
	reportDeadline := m.DatabaseNow.Add(settings.ReportWindow)
	_, err := tx.Exec(ctx, `INSERT INTO match_result_verifications
		(match_id,competition_id,phase,first_report_entry_id,first_reported_at,report_window_seconds,
		 reminder_lead_seconds,response_window_seconds,report_deadline_at,reminder_at)
		VALUES ($1,$2,'awaiting_confirmation',$3,$4,$5,$6,$7,$8,$9)`,
		m.ID, m.CompetitionID, m.ActorEntryID, m.DatabaseNow, int(settings.ReportWindow/time.Second),
		int(settings.ReminderLead/time.Second), int(settings.ResponseWindow/time.Second),
		reportDeadline, reportDeadline.Add(-settings.ReminderLead))
	return err
}

// startScoreReportWindow moves the match to awaiting confirmation and asks the
// other entry to confirm or reject the submitted result.
func startScoreReportWindow(ctx context.Context, tx pgx.Tx, m lockedResultMatch) error {
	matchVersion, err := updateResultVersion(ctx, tx, `UPDATE matches SET state='awaiting_confirmation',
		version=version+1,updated_at=now()
		WHERE id=$1 AND competition_id=$2 AND version=$3 AND state='in_progress' RETURNING version`,
		m.ID, m.CompetitionID, m.Version)
	if err != nil {
		return err
	}
	return insertProgressionOutbox(ctx, tx, "match", m.ID, "result.report_received", map[string]any{
		"matchId": m.ID, "competitionId": m.CompetitionID, "entryId": m.opponentOf(m.ActorEntryID),
		"matchVersion": matchVersion,
	})
}

// openScoreMismatch opens the screenshot window when the other entry rejects
// the submitted result (T5), recording who rejected it.
func openScoreMismatch(ctx context.Context, tx pgx.Tx, m lockedResultMatch, v *lockedVerification,
	responseDeadlineAt time.Time, actor resolutionActor) error {
	if _, err := updateResultVersion(ctx, tx, `UPDATE match_result_verifications SET phase='awaiting_screenshots',
		mismatch_at=$2,response_deadline_at=$3,rejected_by=$5,version=version+1,updated_at=now()
		WHERE match_id=$1 AND phase='awaiting_confirmation' AND version=$4 RETURNING version`,
		m.ID, m.DatabaseNow, responseDeadlineAt, v.Version, m.ActorUserID); err != nil {
		return err
	}
	matchVersion, err := updateResultVersion(ctx, tx, `UPDATE matches SET state='disputed',version=version+1,updated_at=now()
		WHERE id=$1 AND competition_id=$2 AND version=$3 AND state='awaiting_confirmation' RETURNING version`,
		m.ID, m.CompetitionID, m.Version)
	if err != nil {
		return err
	}
	if err = appendAuditActorContext(ctx, tx, actor.RequestID, m.OrganizationID, actor.UserID, "result.rejected", "match", m.ID,
		map[string]any{"state": m.State, "matchVersion": m.Version},
		map[string]any{"state": "disputed", "matchVersion": matchVersion, "responseDeadlineAt": responseDeadlineAt}); err != nil {
		return err
	}
	return insertProgressionOutbox(ctx, tx, "match", m.ID, "result.mismatch", map[string]any{
		"matchId": m.ID, "competitionId": m.CompetitionID, "matchVersion": matchVersion,
	})
}

// insertScoreReport stores the submitted result ("initial", with its claim)
// or a screenshot submission ("final", claim nil) and audits it. The audit row
// never carries the score.
func insertScoreReport(ctx context.Context, tx pgx.Tx, m lockedResultMatch, actor resolutionActor, kind string,
	claim *scoreClaim) (string, error) {
	var homeScore, awayScore, homeTiebreak, awayTiebreak *int
	var tiebreakType *string
	var games any
	if claim != nil {
		encoded, err := json.Marshal(claim.Games)
		if err != nil {
			return "", err
		}
		games, homeScore, awayScore = json.RawMessage(encoded), &claim.HomeScore, &claim.AwayScore
		tiebreakType, homeTiebreak, awayTiebreak = claim.tiebreakColumns()
	}
	var reportID string
	if err := tx.QueryRow(ctx, `INSERT INTO match_result_reports
		(match_id,competition_id,entry_id,reported_by,kind,home_score,away_score,tiebreak_type,
		 home_tiebreak_score,away_tiebreak_score,game_results,match_version,reported_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id::text`,
		m.ID, m.CompetitionID, m.ActorEntryID, m.ActorUserID, kind, homeScore, awayScore,
		tiebreakType, homeTiebreak, awayTiebreak, games, m.Version, m.DatabaseNow).Scan(&reportID); err != nil {
		return "", err
	}
	action := "result.reported"
	if kind == "final" {
		action = "result.screenshot_submitted"
	}
	return reportID, appendAuditActorContext(ctx, tx, actor.RequestID, m.OrganizationID, actor.UserID, action, "match", m.ID,
		nil, map[string]any{"reportId": reportID, "matchId": m.ID, "entryId": m.ActorEntryID, "kind": kind, "matchVersion": m.Version})
}

// attachFinalReportEvidence binds the locked screenshot to the screenshot
// submission. An upload can back only one submission.
func attachFinalReportEvidence(ctx context.Context, tx pgx.Tx, reportID string, evidenceIDs []string) error {
	for position, evidenceID := range evidenceIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO match_result_report_evidence(report_id,evidence_id,position)
			VALUES ($1,$2,$3)`, reportID, evidenceID, position); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE evidence_uploads SET bound_kind='match_result_report',bound_id=$1,
			bound_at=now(),updated_at=now() WHERE id=$2 AND bound_id IS NULL AND status='completed'`, reportID, evidenceID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errMatchResolutionChanged
		}
	}
	return queueScreenshotReadings(ctx, tx, reportID, evidenceIDs)
}
