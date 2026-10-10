package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The R, T and D labels in result verification comments are defined in docs/result-verification.md.

// errResultVerificationInvariant marks locked verification state that no valid
// sequence of writes produces. Callers roll back instead of guessing.
var errResultVerificationInvariant = errors.New("result verification state is inconsistent")

// scoreReportInput is a submitted result. games is optional: without it the
// server derives one aggregate game (D6).
type scoreReportInput struct {
	HomeScore           int                 `json:"homeScore"`
	AwayScore           int                 `json:"awayScore"`
	Games               []gameScoreInput    `json:"games,omitempty"`
	Tiebreak            *tiebreakScoreInput `json:"tiebreak"`
	DeclarationAccepted bool                `json:"declarationAccepted"`
}

// scoreClaim is a validated result. Two results are equal on totals and
// tiebreak only; the games breakdown never decides equality (D6).
type scoreClaim struct {
	HomeScore, AwayScore int
	Tiebreak             *tiebreakScoreInput
	Games                []gameScoreInput
}

// scoreRangeProblem is the one range rule for reported, final and corrected
// scores. The handlers apply it before any lock; normalizeScoreClaim applies
// it again for callers, such as a review correction, that skip them.
func scoreRangeProblem(homeScore, awayScore int) string {
	if homeScore < 0 || homeScore > 99 || awayScore < 0 || awayScore > 99 {
		return "Scores must be between 0 and 99."
	}
	return ""
}

// normalizeScoreClaim validates a claim against the stage's best-of and format
// and derives the aggregate game when games were omitted. The returned message
// is safe to show the caller.
func normalizeScoreClaim(input scoreReportInput, bestOf int, format string) (scoreClaim, string) {
	if message := scoreRangeProblem(input.HomeScore, input.AwayScore); message != "" {
		return scoreClaim{}, message
	}
	claim := scoreClaim{HomeScore: input.HomeScore, AwayScore: input.AwayScore, Games: slices.Clone(input.Games)}
	if input.Tiebreak != nil {
		tiebreak := *input.Tiebreak
		tiebreak.Type = strings.ToLower(strings.TrimSpace(tiebreak.Type))
		claim.Tiebreak = &tiebreak
	}
	if len(claim.Games) == 0 {
		claim.Games = []gameScoreInput{{HomeScore: input.HomeScore, AwayScore: input.AwayScore}}
	}
	if message := validateScorePolicy(claim, bestOf, format); message != "" {
		return scoreClaim{}, message
	}
	return claim, ""
}

func (claim scoreClaim) equal(other scoreClaim) bool {
	if claim.HomeScore != other.HomeScore || claim.AwayScore != other.AwayScore {
		return false
	}
	if claim.Tiebreak == nil || other.Tiebreak == nil {
		return claim.Tiebreak == nil && other.Tiebreak == nil
	}
	return *claim.Tiebreak == *other.Tiebreak
}

// tiebreakColumns maps the tiebreak onto its three nullable columns.
func (claim scoreClaim) tiebreakColumns() (*string, *int, *int) {
	if claim.Tiebreak == nil {
		return nil, nil, nil
	}
	tiebreak := *claim.Tiebreak
	return &tiebreak.Type, &tiebreak.HomeScore, &tiebreak.AwayScore
}

type verificationReport struct {
	ID, EntryID, ReportedBy, Kind string
	Claim                         scoreClaim
	ReportedAt                    time.Time
}

// lockedVerification is the match_result_verifications row. Of the
// snapshotted windows only the response window is still needed: the report
// window's deadline and reminder are already stored as instants.
type lockedVerification struct {
	Phase, FirstReportEntryID                      string
	ReportDeadlineAt, ReminderAt                   time.Time
	ReminderSentAt, MismatchAt, ResponseDeadlineAt *time.Time
	RejectedBy                                     *string
	ResponseWindow                                 time.Duration
	Version                                        int
}

// blockedEvidence is an upload by an entry that sent no screenshot and may
// still be stuck in Gamics' own screenshot pipeline (T16).
type blockedEvidence struct {
	EvidenceID, EntryID, UploadedBy, Status string
	ProcessingErrorCode                     *string
	CreatedAt                               time.Time
}

// verificationState is everything a planner may read, all taken under the
// competition gate and the match lock.
type verificationState struct {
	Match           lockedResultMatch
	Verification    *lockedVerification
	Reports         []verificationReport // sorted by entry_id, kind
	BlockedEvidence []blockedEvidence    // loaded only while awaiting screenshots
}

// planRejection is a client-facing refusal. Its code and message depend only
// on the caller's own entry, the submitted result and the shared deadlines.
type planRejection struct {
	Status        int
	Code, Message string
	// Cause is the broken invariant behind an internal_error. It is logged,
	// never sent; every invariant message is static text without row data.
	Cause error
}

// matchResolution is the terminal outcome finalizeMatchResolution applies.
type matchResolution struct {
	FinalState       string // completed | forfeit | cancelled
	WinnerEntryID    *string
	CompletionReason string      // played | response_timeout | no_result_reported | platform_review
	Cause            string      // progressionCause*
	Claim            *scoreClaim // non-nil iff completed
	ClaimAuthorID    *string     // result_submissions.submitted_by
	ConfirmerID      *string     // result_submissions.decided_by
	Origin           string      // agreed_reports | unanswered | platform_review
	RemoveEntryIDs   []string    // sorted, distinct, subset of {home, away}, never the winner
	RemovalReason    string
	Resolution       string // verification resolution; "" when there is no row (R7)
	ApplyRatings     bool
}

// planAction is a planner's decision. Every applier handles each action it can
// receive by name and treats any other as an invariant, so a new or mistyped
// action can never fall through to a destructive transition.
type planAction string

const (
	planNone     planAction = "none"
	planOpen     planAction = "open"
	planFinalize planAction = "finalize"
	planMismatch planAction = "mismatch"
	planWait     planAction = "wait"
	planReview   planAction = "review"
	planReminder planAction = "reminder"
	planEscalate planAction = "escalate"
)

func unknownPlanAction(action planAction) error {
	return fmt.Errorf("%w: unknown plan action %q", errResultVerificationInvariant, action)
}

// reportPlan answers a submitted result with "open", which starts the
// confirmation window (T2), and the other entry's answer with "finalize", which
// confirms the result (T4), or "mismatch", which opens the screenshot window
// (T5).
type reportPlan struct {
	Action             planAction
	Resolution         matchResolution
	ResponseDeadlineAt time.Time
}

// responsePlan answers a screenshot: "wait" keeps the screenshot window open
// for the other entry (T8) and "review" queues the match for Gamics once both
// sent theirs (T9).
type responsePlan struct {
	Action     planAction
	Resolution matchResolution
}

// deadlinePlan is the worker's decision: "none", "reminder" (T3), "escalate"
// (T16) or "finalize" (T6, T10, T11, T12).
type deadlinePlan struct {
	Action        planAction
	Resolution    matchResolution
	RemindEntryID string
}

// planScoreReport decides a submitted result. Either entry submits it, once,
// while the match is in progress; the other entry then confirms or rejects it.
// At the exact resultDueAt instant it is refused, matching the worker, which
// acts.
func planScoreReport(s verificationState, entryID string) (reportPlan, *planRejection) {
	if rejection := s.reportPreconditions(entryID); rejection != nil {
		return reportPlan{}, rejection
	}
	switch s.Match.State {
	case "in_progress":
	case "awaiting_confirmation":
		if s.Verification.FirstReportEntryID == entryID {
			return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "result_already_submitted",
				Message: "Your side has already submitted the result for this match."}
		}
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "result_awaiting_confirmation",
			Message: "Your opponent has already submitted the result. Confirm or reject it."}
	default:
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "report_not_allowed",
			Message: "This match is not accepting results."}
	}
	if due := s.Match.ResultDueAt; due != nil && !s.Match.DatabaseNow.Before(*due) {
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "report_window_closed",
			Message: "The time to submit a result for this match has passed."}
	}
	return reportPlan{Action: planOpen}, nil
}

// planConfirmation decides the other entry's answer to the submitted result:
// confirming finalizes it (T4) and rejecting opens the screenshot window (T5).
// At the exact deadline instant the answer is refused, matching the worker,
// which then lets the submitted result stand.
func planConfirmation(s verificationState, entryID string, confirm bool) (reportPlan, *planRejection) {
	if rejection := s.reportPreconditions(entryID); rejection != nil {
		return reportPlan{}, rejection
	}
	if s.Match.State != "awaiting_confirmation" {
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "confirmation_not_allowed",
			Message: "There is no submitted result to confirm or reject."}
	}
	v := s.Verification
	if v.FirstReportEntryID == entryID {
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "own_result",
			Message: "Your opponent confirms or rejects the result you submitted."}
	}
	if !s.Match.DatabaseNow.Before(v.ReportDeadlineAt) {
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "confirmation_window_closed",
			Message: "The time to confirm or reject this result has passed."}
	}
	if confirm {
		confirmer := s.Match.ActorUserID
		return reportPlan{Action: planFinalize, Resolution: s.submittedResult(&confirmer)}, nil
	}
	return reportPlan{Action: planMismatch, ResponseDeadlineAt: s.Match.DatabaseNow.Add(v.ResponseWindow)}, nil
}

// planScreenshot decides an entry's one screenshot after a rejection (R6).
// The match goes to Gamics once both entries sent theirs.
func planScreenshot(s verificationState, entryID string) (responsePlan, *planRejection) {
	if rejection := s.reportPreconditions(entryID); rejection != nil {
		return responsePlan{}, rejection
	}
	if s.report(entryID, "final") != nil {
		return responsePlan{}, &planRejection{Status: http.StatusConflict, Code: "screenshot_already_submitted",
			Message: "Your side has already sent its screenshot for this match."}
	}
	if s.Match.State != "disputed" || s.Verification.Phase != "awaiting_screenshots" {
		return responsePlan{}, &planRejection{Status: http.StatusConflict, Code: "screenshot_not_allowed",
			Message: "This match is not accepting screenshots."}
	}
	if !s.Match.DatabaseNow.Before(*s.Verification.ResponseDeadlineAt) {
		return responsePlan{}, &planRejection{Status: http.StatusConflict, Code: "screenshot_window_closed",
			Message: "The screenshot window for this match has closed."}
	}
	if s.report(s.Match.opponentOf(entryID), "final") != nil {
		return responsePlan{Action: planReview}, nil
	}
	return responsePlan{Action: planWait}, nil
}

// planVerificationDeadline decides what the worker does for one locked match.
// It never acts on a cancelled or completed competition, and at the exact
// deadline instant it acts while the handlers reject.
func planVerificationDeadline(s verificationState) (deadlinePlan, error) {
	none := deadlinePlan{Action: planNone}
	if s.Match.competitionClosed() {
		return none, nil
	}
	if err := s.consistencyError(); err != nil {
		return deadlinePlan{}, err
	}
	now := s.Match.DatabaseNow
	switch s.Match.State {
	case "in_progress":
		if due := s.Match.ResultDueAt; due != nil && !now.Before(*due) {
			resolution := s.removal("cancelled", nil, "no_result_reported", s.Match.entryIDs()...)
			return deadlinePlan{Action: planFinalize, Resolution: resolution}, nil
		}
	case "awaiting_confirmation":
		return s.reportDeadlinePlan(), nil
	case "disputed":
		if s.Verification.Phase == "awaiting_screenshots" && !now.Before(*s.Verification.ResponseDeadlineAt) {
			return s.responseDeadlinePlan()
		}
	}
	return none, nil
}

// submittedReport is the submitted result: the match's one initial report.
func (s verificationState) submittedReport() *verificationReport {
	if s.Verification == nil {
		return nil
	}
	return s.report(s.Verification.FirstReportEntryID, "initial")
}

func (s verificationState) report(entryID, kind string) *verificationReport {
	for index := range s.Reports {
		if s.Reports[index].EntryID == entryID && s.Reports[index].Kind == kind {
			report := s.Reports[index]
			return &report
		}
	}
	return nil
}

// reportPreconditions are shared by the result planners: the caller's entry
// must play the match, the competition must be open and the locked state must
// be consistent.
func (s verificationState) reportPreconditions(entryID string) *planRejection {
	switch {
	case !s.Match.participant(entryID):
		return &planRejection{Status: http.StatusNotFound, Code: "match_not_found", Message: "Match not found."}
	case s.Match.competitionClosed():
		return &planRejection{Status: http.StatusConflict, Code: "competition_closed",
			Message: "This competition is closed, so its matches no longer accept scores."}
	}
	if err := s.consistencyError(); err != nil {
		return &planRejection{Status: http.StatusInternalServerError, Code: "internal_error",
			Message: "Unable to process the score report.", Cause: err}
	}
	return nil
}

// submittedResult confirms the submitted result, by the other entry (T4) or,
// when it didn't answer in time, by default (T6). The submitter authors the
// canonical row; the confirmer is recorded when there is one.
func (s verificationState) submittedResult(confirmerID *string) matchResolution {
	submitted := s.submittedReport()
	claim := scoreClaim{HomeScore: submitted.Claim.HomeScore, AwayScore: submitted.Claim.AwayScore,
		Games: slices.Clone(submitted.Claim.Games)}
	if submitted.Claim.Tiebreak != nil {
		tiebreak := *submitted.Claim.Tiebreak
		claim.Tiebreak = &tiebreak
	}
	var winner *string
	switch homeWon, awayWon := resultWinner(claim.HomeScore, claim.AwayScore, claim.Tiebreak); {
	case homeWon:
		winner = cloneOptionalString(s.Match.HomeEntryID)
	case awayWon:
		winner = cloneOptionalString(s.Match.AwayEntryID)
	}
	author := submitted.ReportedBy
	resolution := matchResolution{
		FinalState: "completed", WinnerEntryID: winner, CompletionReason: "played",
		Cause: progressionCausePlayerConfirmation, Claim: &claim, ClaimAuthorID: &author, ConfirmerID: confirmerID,
		Origin: "agreed_reports", RemoveEntryIDs: []string{}, Resolution: "agreed", ApplyRatings: true,
	}
	if confirmerID == nil {
		resolution.Origin, resolution.Resolution = "unanswered", "confirmation_timeout"
	}
	return resolution
}

// removal removes the silent entries from the tournament. Forfeits and
// removals never touch ratings, matching the no-show semantics.
func (s verificationState) removal(finalState string, winnerEntryID *string, reason string, removed ...string) matchResolution {
	removed = slices.Clone(removed)
	slices.Sort(removed)
	resolution := reason
	if s.Verification == nil {
		resolution = ""
	}
	return matchResolution{
		FinalState: finalState, WinnerEntryID: winnerEntryID, CompletionReason: reason,
		Cause: progressionCauseTimeoutForfeit, RemoveEntryIDs: removed, RemovalReason: reason, Resolution: resolution,
	}
}

// reportDeadlinePlan covers the confirmation window: the reminder (T3) and,
// when the other entry never answered, the submitted result standing (T6).
func (s verificationState) reportDeadlinePlan() deadlinePlan {
	v, now := s.Verification, s.Match.DatabaseNow
	switch {
	case !now.Before(v.ReportDeadlineAt):
		return deadlinePlan{Action: planFinalize, Resolution: s.submittedResult(nil)}
	case v.ReminderSentAt == nil && !now.Before(v.ReminderAt):
		return deadlinePlan{Action: planReminder, RemindEntryID: s.Match.opponentOf(v.FirstReportEntryID)}
	default:
		return deadlinePlan{Action: planNone}
	}
}

// responseDeadlinePlan settles an expired screenshot window (R6). An entry
// whose screenshot is stuck in Gamics' pipeline is never removed; the match
// goes to the review queue instead (T16).
func (s verificationState) responseDeadlinePlan() (deadlinePlan, error) {
	var responders, silent []string
	for _, entryID := range s.Match.entryIDs() {
		if s.report(entryID, "final") != nil {
			responders = append(responders, entryID)
		} else {
			silent = append(silent, entryID)
		}
	}
	for _, entryID := range silent {
		if s.evidenceBlocked(entryID) {
			return deadlinePlan{Action: planEscalate}, nil
		}
	}
	switch len(responders) {
	case 1:
		winner := responders[0]
		return deadlinePlan{Action: planFinalize, Resolution: s.removal("forfeit", &winner, "response_timeout", silent...)}, nil
	case 0:
		return deadlinePlan{Action: planFinalize, Resolution: s.removal("cancelled", nil, "response_timeout", silent...)}, nil
	default:
		return deadlinePlan{}, fmt.Errorf("%w: both entries sent screenshots while still awaiting them", errResultVerificationInvariant)
	}
}

// evidenceBlocked reports whether an upload made during the screenshot window is
// still processing or failed for a Gamics-side reason. Unfinished, rejected,
// expired and missing uploads are the player's responsibility and never block.
func (s verificationState) evidenceBlocked(entryID string) bool {
	v := s.Verification
	for _, evidence := range s.BlockedEvidence {
		if evidence.EntryID != entryID || evidence.CreatedAt.Before(*v.MismatchAt) ||
			!evidence.CreatedAt.Before(*v.ResponseDeadlineAt) {
			continue
		}
		if evidence.Status == "processing" {
			return true
		}
		if evidence.Status == "failed" && evidence.ProcessingErrorCode != nil &&
			(*evidence.ProcessingErrorCode == "verification_unavailable" || *evidence.ProcessingErrorCode == "processing_aborted") {
			return true
		}
	}
	return false
}

// consistencyError checks the locked rows against the phase table in
// docs/result-verification.md. A terminal match needs no check: nothing is
// planned for it.
func (s verificationState) consistencyError() error {
	m, v := s.Match, s.Verification
	switch m.State {
	case "in_progress", "awaiting_confirmation", "disputed":
	default:
		return nil
	}
	if m.HomeEntryID == nil || m.AwayEntryID == nil || *m.HomeEntryID == *m.AwayEntryID {
		return fmt.Errorf("%w: a live match needs two distinct entries", errResultVerificationInvariant)
	}
	if err := s.reportsError(); err != nil {
		return err
	}
	switch m.State {
	case "in_progress":
		if v != nil || len(s.Reports) > 0 {
			return fmt.Errorf("%w: a match in progress already has reports", errResultVerificationInvariant)
		}
	case "awaiting_confirmation":
		if v == nil || v.Phase != "awaiting_confirmation" || len(s.Reports) != 1 ||
			s.report(v.FirstReportEntryID, "initial") == nil {
			return fmt.Errorf("%w: awaiting confirmation without exactly the submitted result", errResultVerificationInvariant)
		}
	case "disputed":
		if err := s.disputeError(); err != nil {
			return err
		}
	}
	for _, evidence := range s.BlockedEvidence {
		if !m.participant(evidence.EntryID) || s.report(evidence.EntryID, "final") != nil {
			return fmt.Errorf("%w: blocked evidence belongs to an entry that responded", errResultVerificationInvariant)
		}
	}
	return nil
}

func (s verificationState) disputeError() error {
	v := s.Verification
	if v == nil || (v.Phase != "awaiting_screenshots" && v.Phase != "in_review") || v.MismatchAt == nil ||
		v.ResponseDeadlineAt == nil || v.RejectedBy == nil {
		return fmt.Errorf("%w: a disputed match has no rejected result", errResultVerificationInvariant)
	}
	if s.submittedReport() == nil || s.report(s.Match.opponentOf(v.FirstReportEntryID), "initial") != nil {
		return fmt.Errorf("%w: a disputed match needs exactly the submitted result", errResultVerificationInvariant)
	}
	finals := 0
	for _, entryID := range s.Match.entryIDs() {
		if s.report(entryID, "final") != nil {
			finals++
		}
	}
	if v.Phase == "awaiting_screenshots" && finals > 1 {
		return fmt.Errorf("%w: two screenshots while awaiting screenshots", errResultVerificationInvariant)
	}
	return nil
}

// reportsError requires every report to belong to one of the match's entries,
// with at most one per entry and kind. Only the submitter has an initial
// report; a screenshot ("final") carries no score.
func (s verificationState) reportsError() error {
	seen := make(map[string]bool, len(s.Reports))
	for _, report := range s.Reports {
		if !s.Match.participant(report.EntryID) {
			return fmt.Errorf("%w: a report belongs to another entry", errResultVerificationInvariant)
		}
		if report.Kind != "initial" && report.Kind != "final" {
			return fmt.Errorf("%w: unknown report kind", errResultVerificationInvariant)
		}
		key := report.EntryID + "/" + report.Kind
		if seen[key] {
			return fmt.Errorf("%w: duplicate report", errResultVerificationInvariant)
		}
		seen[key] = true
	}
	return nil
}
