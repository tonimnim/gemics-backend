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

// scoreReportInput is one blind score claim. games is optional: without it the
// server derives one aggregate game, so honest players never mismatch on how
// they broke the score down (D6).
type scoreReportInput struct {
	HomeScore           int                 `json:"homeScore"`
	AwayScore           int                 `json:"awayScore"`
	Games               []gameScoreInput    `json:"games,omitempty"`
	Tiebreak            *tiebreakScoreInput `json:"tiebreak"`
	DeclarationAccepted bool                `json:"declarationAccepted"`
}

// scoreClaim is a validated claim. Claims agree on totals and tiebreak only;
// the games breakdown never decides agreement (D6).
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

// agreedGames is the canonical breakdown of an agreement: the shared games when
// both sides sent identical ones, otherwise one aggregate game.
func agreedGames(home, away scoreClaim) []gameScoreInput {
	if len(home.Games) > 0 && slices.Equal(home.Games, away.Games) {
		return slices.Clone(home.Games)
	}
	return []gameScoreInput{{HomeScore: home.HomeScore, AwayScore: home.AwayScore}}
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
	ResponseWindow                                 time.Duration
	Version                                        int
}

// blockedEvidence is an upload by a non-responding entry that may still be
// stuck in Gamics' own screenshot pipeline (T16).
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
	BlockedEvidence []blockedEvidence    // loaded only while awaiting responses
}

// planRejection is a client-facing refusal. Its code and message depend only
// on the caller's own entry and the shared deadlines, never on the other
// entry's claim (R2).
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
	CompletionReason string      // played | report_timeout | response_timeout | no_result_reported | platform_review
	Cause            string      // progressionCause*
	Claim            *scoreClaim // non-nil iff completed
	ClaimAuthorID    *string     // result_submissions.submitted_by
	ConfirmerID      *string     // result_submissions.decided_by
	Origin           string      // agreed_reports | platform_review
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

// reportPlan answers an initial report: "open" starts the report window (T2),
// "finalize" confirms an agreement (T4) and "mismatch" opens the response
// window (T5).
type reportPlan struct {
	Action             planAction
	Resolution         matchResolution
	ResponseDeadlineAt time.Time
}

// responsePlan answers a final report: "finalize" confirms an agreement (T7),
// "wait" keeps the response window open (T8) and "review" queues the match for
// Gamics (T9).
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

// planScoreReport decides an entry's initial blind report. At the exact
// deadline instant the report is rejected, matching the worker, which acts.
func planScoreReport(s verificationState, entryID string, c scoreClaim) (reportPlan, *planRejection) {
	if rejection := s.reportPreconditions(entryID); rejection != nil {
		return reportPlan{}, rejection
	}
	if s.report(entryID, "initial") != nil {
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "report_already_submitted",
			Message: "Your side has already reported a score for this match."}
	}
	var deadline *time.Time
	switch s.Match.State {
	case "in_progress":
		deadline = s.Match.ResultDueAt
	case "awaiting_confirmation":
		deadline = &s.Verification.ReportDeadlineAt
	default:
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "report_not_allowed",
			Message: "This match is not accepting score reports."}
	}
	if deadline != nil && !s.Match.DatabaseNow.Before(*deadline) {
		return reportPlan{}, &planRejection{Status: http.StatusConflict, Code: "report_window_closed",
			Message: "The score report window for this match has closed."}
	}
	if s.Verification == nil {
		return reportPlan{Action: planOpen}, nil
	}
	home, away := currentClaims(s.withReport(s.prospectiveReport(entryID, "initial", c)))
	if home.Claim.equal(away.Claim) {
		return reportPlan{Action: planFinalize, Resolution: s.agreement(*home, *away)}, nil
	}
	return reportPlan{Action: planMismatch, ResponseDeadlineAt: s.Match.DatabaseNow.Add(s.Verification.ResponseWindow)}, nil
}

// planFinalReport decides an entry's one response to a mismatch. The claims
// are re-compared after every response (R6).
func planFinalReport(s verificationState, entryID string, c scoreClaim) (responsePlan, *planRejection) {
	if rejection := s.reportPreconditions(entryID); rejection != nil {
		return responsePlan{}, rejection
	}
	if s.report(entryID, "final") != nil {
		return responsePlan{}, &planRejection{Status: http.StatusConflict, Code: "response_already_submitted",
			Message: "Your side has already submitted a final score for this match."}
	}
	if s.Match.State != "disputed" || s.Verification.Phase != "awaiting_responses" {
		return responsePlan{}, &planRejection{Status: http.StatusConflict, Code: "response_not_allowed",
			Message: "This match is not accepting final scores."}
	}
	if !s.Match.DatabaseNow.Before(*s.Verification.ResponseDeadlineAt) {
		return responsePlan{}, &planRejection{Status: http.StatusConflict, Code: "response_window_closed",
			Message: "The final score window for this match has closed."}
	}
	home, away := currentClaims(s.withReport(s.prospectiveReport(entryID, "final", c)))
	switch {
	case home.Claim.equal(away.Claim):
		return responsePlan{Action: planFinalize, Resolution: s.agreement(*home, *away)}, nil
	case home.Kind == "final" && away.Kind == "final":
		return responsePlan{Action: planReview}, nil
	default:
		return responsePlan{Action: planWait}, nil
	}
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
		if s.Verification.Phase == "awaiting_responses" && !now.Before(*s.Verification.ResponseDeadlineAt) {
			return s.responseDeadlinePlan()
		}
	}
	return none, nil
}

// currentClaims returns each entry's current claim: its final report when it
// has one, otherwise its initial report.
func currentClaims(s verificationState) (home, away *verificationReport) {
	if s.Match.HomeEntryID != nil {
		home = s.currentClaim(*s.Match.HomeEntryID)
	}
	if s.Match.AwayEntryID != nil {
		away = s.currentClaim(*s.Match.AwayEntryID)
	}
	return home, away
}

func (s verificationState) currentClaim(entryID string) *verificationReport {
	if final := s.report(entryID, "final"); final != nil {
		return final
	}
	return s.report(entryID, "initial")
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

// prospectiveReport is the caller's report as it will be stored, so it can be
// compared before it is written.
func (s verificationState) prospectiveReport(entryID, kind string, claim scoreClaim) verificationReport {
	return verificationReport{EntryID: entryID, ReportedBy: s.Match.ActorUserID, Kind: kind, Claim: claim,
		ReportedAt: s.Match.DatabaseNow}
}

func (s verificationState) withReport(report verificationReport) verificationState {
	next := s
	next.Reports = append(slices.Clone(s.Reports), report)
	return next
}

// reportPreconditions are shared by both report planners: the caller's entry
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

// agreement confirms the entries' current claims (T4, T7). The home claim's
// reporter authors the canonical row and the away claim's reporter confirms it.
func (s verificationState) agreement(home, away verificationReport) matchResolution {
	claim := scoreClaim{HomeScore: home.Claim.HomeScore, AwayScore: home.Claim.AwayScore,
		Games: agreedGames(home.Claim, away.Claim)}
	if home.Claim.Tiebreak != nil {
		tiebreak := *home.Claim.Tiebreak
		claim.Tiebreak = &tiebreak
	}
	var winner *string
	switch homeWon, awayWon := resultWinner(claim.HomeScore, claim.AwayScore, claim.Tiebreak); {
	case homeWon:
		winner = cloneOptionalString(s.Match.HomeEntryID)
	case awayWon:
		winner = cloneOptionalString(s.Match.AwayEntryID)
	}
	author, confirmer := home.ReportedBy, away.ReportedBy
	return matchResolution{
		FinalState: "completed", WinnerEntryID: winner, CompletionReason: "played",
		Cause: progressionCausePlayerConfirmation, Claim: &claim, ClaimAuthorID: &author, ConfirmerID: &confirmer,
		Origin: "agreed_reports", RemoveEntryIDs: []string{}, Resolution: "agreed", ApplyRatings: true,
	}
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

// reportDeadlinePlan covers the report window: the reminder (T3) and the
// silent entry's removal when the window ends (T6, R5).
func (s verificationState) reportDeadlinePlan() deadlinePlan {
	v, now := s.Verification, s.Match.DatabaseNow
	reporter := v.FirstReportEntryID
	silent := s.Match.opponentOf(reporter)
	switch {
	case !now.Before(v.ReportDeadlineAt):
		return deadlinePlan{Action: planFinalize, Resolution: s.removal("forfeit", &reporter, "report_timeout", silent)}
	case v.ReminderSentAt == nil && !now.Before(v.ReminderAt):
		return deadlinePlan{Action: planReminder, RemindEntryID: silent}
	default:
		return deadlinePlan{Action: planNone}
	}
}

// responseDeadlinePlan settles an expired response window (R6). A
// non-responder whose screenshot is stuck in Gamics' pipeline is never
// removed; the match goes to the review queue instead (T16).
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
		return deadlinePlan{}, fmt.Errorf("%w: both entries responded while still awaiting responses", errResultVerificationInvariant)
	}
}

// evidenceBlocked reports whether an upload made during the response window is
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
		if v == nil || v.Phase != "awaiting_second_report" || len(s.Reports) != 1 ||
			s.report(v.FirstReportEntryID, "initial") == nil {
			return fmt.Errorf("%w: awaiting a second report without exactly the first report", errResultVerificationInvariant)
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
	if v == nil || (v.Phase != "awaiting_responses" && v.Phase != "in_review") || v.MismatchAt == nil || v.ResponseDeadlineAt == nil {
		return fmt.Errorf("%w: a disputed match has no open mismatch", errResultVerificationInvariant)
	}
	finals := 0
	for _, entryID := range s.Match.entryIDs() {
		if s.report(entryID, "initial") == nil {
			return fmt.Errorf("%w: a disputed match lacks an initial report", errResultVerificationInvariant)
		}
		if s.report(entryID, "final") != nil {
			finals++
		}
	}
	if v.Phase == "awaiting_responses" && finals > 1 {
		return fmt.Errorf("%w: two final reports while awaiting responses", errResultVerificationInvariant)
	}
	return nil
}

// reportsError requires every report to belong to one of the match's entries,
// at most one per entry and kind, and every final to follow an initial.
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
	for _, report := range s.Reports {
		if report.Kind == "final" && !seen[report.EntryID+"/initial"] {
			return fmt.Errorf("%w: a final report without an initial report", errResultVerificationInvariant)
		}
	}
	return nil
}
