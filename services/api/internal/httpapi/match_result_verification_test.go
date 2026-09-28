package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"
)

const (
	resultReportsHomeEntry = "10000000-0000-4000-8000-000000000001"
	resultReportsAwayEntry = "10000000-0000-4000-8000-000000000002"
	resultReportsHomeUser  = "20000000-0000-4000-8000-000000000001"
	resultReportsAwayUser  = "20000000-0000-4000-8000-000000000002"
	resultReportsOutsider  = "10000000-0000-4000-8000-000000000009"
)

var resultReportsNow = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func resultReportsClaim(home, away int) scoreClaim {
	return scoreClaim{HomeScore: home, AwayScore: away, Games: []gameScoreInput{{HomeScore: home, AwayScore: away}}}
}

func resultReportsMatch(state, actorUserID, actorEntryID string) lockedResultMatch {
	home, away := resultReportsHomeEntry, resultReportsAwayEntry
	homeCaptain, awayCaptain := resultReportsHomeUser, resultReportsAwayUser
	due := resultReportsNow.Add(time.Hour)
	return lockedResultMatch{
		ID: "30000000-0000-4000-8000-000000000001", CompetitionID: "40000000-0000-4000-8000-000000000001",
		OrganizationID: "50000000-0000-4000-8000-000000000001", GameID: "efootball-mobile", CompetitionStatus: "running",
		StageFormat: "single_elimination", BestOf: 1, State: state, Version: 5, ResultDueAt: &due,
		HomeEntryID: &home, AwayEntryID: &away, HomeCaptainID: &homeCaptain, AwayCaptainID: &awayCaptain,
		ActorUserID: actorUserID, ActorEntryID: actorEntryID, DatabaseNow: resultReportsNow,
	}
}

func resultReportsReport(entryID, userID, kind string, claim scoreClaim) verificationReport {
	return verificationReport{ID: entryID + "/" + kind, EntryID: entryID, ReportedBy: userID, Kind: kind, Claim: claim,
		ReportedAt: resultReportsNow.Add(-time.Minute)}
}

// resultReportsAwaitingSecond is T2's outcome: the home entry reported 2-1 two
// minutes ago and the report window is running.
func resultReportsAwaitingSecond() verificationState {
	first := resultReportsNow.Add(-2 * time.Minute)
	return verificationState{
		Match: resultReportsMatch("awaiting_confirmation", resultReportsAwayUser, resultReportsAwayEntry),
		Verification: &lockedVerification{
			Phase: "awaiting_second_report", FirstReportEntryID: resultReportsHomeEntry,
			ReportDeadlineAt: first.Add(10 * time.Minute), ReminderAt: first.Add(7 * time.Minute),
			ResponseWindow: 10 * time.Minute, Version: 1,
		},
		Reports: []verificationReport{resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "initial", resultReportsClaim(2, 1))},
	}
}

// resultReportsAwaitingResponses is T5's outcome: home claimed 2-1, away
// claimed 1-2, and the response window opened a minute ago.
func resultReportsAwaitingResponses() verificationState {
	mismatch := resultReportsNow.Add(-time.Minute)
	deadline := mismatch.Add(10 * time.Minute)
	state := resultReportsAwaitingSecond()
	state.Match = resultReportsMatch("disputed", resultReportsHomeUser, resultReportsHomeEntry)
	state.Verification.Phase = "awaiting_responses"
	state.Verification.MismatchAt, state.Verification.ResponseDeadlineAt = &mismatch, &deadline
	state.Verification.Version = 2
	state.Reports = []verificationReport{
		resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "initial", resultReportsClaim(2, 1)),
		resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "initial", resultReportsClaim(1, 2)),
	}
	return state
}

func resultReportsAt(state verificationState, now time.Time) verificationState {
	state.Match.DatabaseNow = now
	return state
}

func TestNormalizeScoreClaim(t *testing.T) {
	tests := []struct {
		name      string
		input     scoreReportInput
		bestOf    int
		format    string
		wantGames []gameScoreInput
		wantError bool
	}{
		{name: "games omitted for best-of-one", input: scoreReportInput{HomeScore: 2, AwayScore: 1}, bestOf: 1,
			format: "single_elimination", wantGames: []gameScoreInput{{HomeScore: 2, AwayScore: 1}}},
		{name: "games omitted for best-of-three", input: scoreReportInput{HomeScore: 3, AwayScore: 1}, bestOf: 3,
			format: "single_elimination", wantGames: []gameScoreInput{{HomeScore: 3, AwayScore: 1}}},
		{name: "given games that sum to the totals", bestOf: 3, format: "single_elimination",
			input: scoreReportInput{HomeScore: 3, AwayScore: 1,
				Games: []gameScoreInput{{HomeScore: 1, AwayScore: 0}, {HomeScore: 0, AwayScore: 1}, {HomeScore: 2, AwayScore: 0}}},
			wantGames: []gameScoreInput{{HomeScore: 1, AwayScore: 0}, {HomeScore: 0, AwayScore: 1}, {HomeScore: 2, AwayScore: 0}}},
		{name: "given games that do not sum to the totals", bestOf: 3, format: "single_elimination", wantError: true,
			input: scoreReportInput{HomeScore: 3, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 2, AwayScore: 1}}}},
		{name: "round-robin draw", input: scoreReportInput{HomeScore: 1, AwayScore: 1}, bestOf: 1, format: "round_robin",
			wantGames: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}},
		{name: "elimination tie without penalties", input: scoreReportInput{HomeScore: 1, AwayScore: 1}, bestOf: 1,
			format: "double_elimination", wantError: true},
		{name: "elimination tie with penalties", bestOf: 1, format: "double_elimination",
			input:     scoreReportInput{HomeScore: 1, AwayScore: 1, Tiebreak: &tiebreakScoreInput{Type: " Penalties ", HomeScore: 5, AwayScore: 4}},
			wantGames: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}},
		{name: "total out of range", input: scoreReportInput{HomeScore: 100, AwayScore: 0}, bestOf: 1,
			format: "round_robin", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim, message := normalizeScoreClaim(test.input, test.bestOf, test.format)
			if test.wantError {
				if message == "" {
					t.Fatalf("invalid claim accepted: %+v", claim)
				}
				return
			}
			if message != "" || !slices.Equal(claim.Games, test.wantGames) {
				t.Fatalf("claim = %+v, message %q, want games %+v", claim, message, test.wantGames)
			}
			if claim.Tiebreak != nil && claim.Tiebreak.Type != "penalties" {
				t.Fatalf("tiebreak type was not normalized: %+v", claim.Tiebreak)
			}
		})
	}

	input := scoreReportInput{HomeScore: 1, AwayScore: 0, Games: []gameScoreInput{{HomeScore: 1, AwayScore: 0}}}
	claim, _ := normalizeScoreClaim(input, 1, "round_robin")
	claim.Games[0].HomeScore = 9
	if input.Games[0].HomeScore != 1 {
		t.Fatal("the claim aliases the request's games")
	}
}

func TestScoreClaimsAgreeOnTotalsAndTiebreakOnly(t *testing.T) {
	penalties := func(home, away int) *tiebreakScoreInput {
		return &tiebreakScoreInput{Type: "penalties", HomeScore: home, AwayScore: away}
	}
	breakdown := scoreClaim{HomeScore: 3, AwayScore: 1,
		Games: []gameScoreInput{{HomeScore: 2, AwayScore: 0}, {HomeScore: 1, AwayScore: 1}}}
	reordered := scoreClaim{HomeScore: 3, AwayScore: 1,
		Games: []gameScoreInput{{HomeScore: 1, AwayScore: 1}, {HomeScore: 2, AwayScore: 0}}}
	tests := []struct {
		name        string
		left, right scoreClaim
		equal       bool
	}{
		{"different game order", breakdown, reordered, true},
		{"one aggregate game against a breakdown", breakdown, resultReportsClaim(3, 1), true},
		{"different totals", resultReportsClaim(2, 1), resultReportsClaim(1, 2), false},
		{"different penalty scores", scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: penalties(5, 4)},
			scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: penalties(4, 5)}, false},
		{"penalties against none", scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: penalties(5, 4)},
			scoreClaim{HomeScore: 1, AwayScore: 1}, false},
		{"same penalties", scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: penalties(5, 4)},
			scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: penalties(5, 4)}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.left.equal(test.right); got != test.equal || test.right.equal(test.left) != test.equal {
				t.Fatalf("equal = %v, want %v", got, test.equal)
			}
		})
	}

	if games := agreedGames(breakdown, breakdown); !slices.Equal(games, breakdown.Games) {
		t.Fatalf("identical breakdowns must be kept: %+v", games)
	}
	if games := agreedGames(breakdown, reordered); !slices.Equal(games, []gameScoreInput{{HomeScore: 3, AwayScore: 1}}) {
		t.Fatalf("different breakdowns must collapse to one aggregate game: %+v", games)
	}
}

func TestPlanScoreReport(t *testing.T) {
	awaitingSecond := resultReportsAwaitingSecond()
	closedCompetition := resultReportsAwaitingSecond()
	closedCompetition.Match.CompetitionStatus = "cancelled"
	inconsistent := verificationState{Match: resultReportsMatch("in_progress", resultReportsHomeUser, resultReportsHomeEntry),
		Verification: awaitingSecond.Verification}
	completed := verificationState{Match: resultReportsMatch("completed", resultReportsHomeUser, resultReportsHomeEntry)}
	firstReport := verificationState{Match: resultReportsMatch("in_progress", resultReportsHomeUser, resultReportsHomeEntry)}
	deadline := awaitingSecond.Verification.ReportDeadlineAt
	tests := []struct {
		name       string
		state      verificationState
		entryID    string
		claim      scoreClaim
		wantAction planAction
		wantCode   string
	}{
		{"first report opens the window", firstReport, resultReportsHomeEntry, resultReportsClaim(2, 1), "open", ""},
		{"second report agrees", awaitingSecond, resultReportsAwayEntry, resultReportsClaim(2, 1), "finalize", ""},
		{"second report differs", awaitingSecond, resultReportsAwayEntry, resultReportsClaim(1, 2), "mismatch", ""},
		{"own entry already reported", resultReportsAt(awaitingSecond, resultReportsNow), resultReportsHomeEntry,
			resultReportsClaim(2, 1), "", "report_already_submitted"},
		{"first report at the result deadline", resultReportsAt(firstReport, *firstReport.Match.ResultDueAt),
			resultReportsHomeEntry, resultReportsClaim(2, 1), "", "report_window_closed"},
		{"second report at the report deadline", resultReportsAt(awaitingSecond, deadline), resultReportsAwayEntry,
			resultReportsClaim(2, 1), "", "report_window_closed"},
		{"second report after the report deadline", resultReportsAt(awaitingSecond, deadline.Add(time.Second)),
			resultReportsAwayEntry, resultReportsClaim(2, 1), "", "report_window_closed"},
		{"terminal match", completed, resultReportsHomeEntry, resultReportsClaim(2, 1), "", "report_not_allowed"},
		{"entry outside the match", awaitingSecond, resultReportsOutsider, resultReportsClaim(2, 1), "", "match_not_found"},
		{"closed competition", closedCompetition, resultReportsAwayEntry, resultReportsClaim(2, 1), "", "competition_closed"},
		{"verification row while in progress", inconsistent, resultReportsHomeEntry, resultReportsClaim(2, 1), "", "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, rejection := planScoreReport(test.state, test.entryID, test.claim)
			if test.wantCode != "" {
				if rejection == nil || rejection.Code != test.wantCode {
					t.Fatalf("rejection = %+v, want %s", rejection, test.wantCode)
				}
				if (test.wantCode == "internal_error") != errors.Is(rejection.Cause, errResultVerificationInvariant) {
					t.Fatalf("cause = %v for %s", rejection.Cause, test.wantCode)
				}
				return
			}
			if rejection != nil || plan.Action != test.wantAction {
				t.Fatalf("plan = %+v rejection = %+v, want %s", plan, rejection, test.wantAction)
			}
		})
	}
}

func TestPlanScoreReportAgreementMapsAuthorAndConfirmer(t *testing.T) {
	// The away entry reported first here, so the second reporter is home.
	state := resultReportsAwaitingSecond()
	state.Match.ActorUserID, state.Match.ActorEntryID = resultReportsHomeUser, resultReportsHomeEntry
	state.Verification.FirstReportEntryID = resultReportsAwayEntry
	state.Reports = []verificationReport{resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "initial", resultReportsClaim(0, 3))}
	plan, rejection := planScoreReport(state, resultReportsHomeEntry, resultReportsClaim(0, 3))
	if rejection != nil || plan.Action != "finalize" {
		t.Fatalf("plan = %+v rejection = %+v", plan, rejection)
	}
	resolution := plan.Resolution
	if resolution.FinalState != "completed" || resolution.CompletionReason != "played" || resolution.Origin != "agreed_reports" ||
		resolution.Cause != progressionCausePlayerConfirmation || resolution.Resolution != "agreed" || !resolution.ApplyRatings ||
		resolution.WinnerEntryID == nil || *resolution.WinnerEntryID != resultReportsAwayEntry || len(resolution.RemoveEntryIDs) != 0 {
		t.Fatalf("unexpected agreement: %+v", resolution)
	}
	if *resolution.ClaimAuthorID != resultReportsHomeUser || *resolution.ConfirmerID != resultReportsAwayUser ||
		resolution.Claim.HomeScore != 0 || resolution.Claim.AwayScore != 3 {
		t.Fatalf("canonical authorship must follow home then away: %+v", resolution)
	}
}

func TestPlanScoreReportMismatchUsesTheSnapshottedResponseWindow(t *testing.T) {
	state := resultReportsAwaitingSecond()
	state.Verification.ResponseWindow = 25 * time.Minute
	plan, rejection := planScoreReport(state, resultReportsAwayEntry, resultReportsClaim(1, 2))
	if rejection != nil || plan.Action != "mismatch" || !plan.ResponseDeadlineAt.Equal(resultReportsNow.Add(25*time.Minute)) {
		t.Fatalf("plan = %+v rejection = %+v", plan, rejection)
	}
}

func TestPlanScoreReportDrawInRoundRobinHasNoWinner(t *testing.T) {
	state := resultReportsAwaitingSecond()
	state.Match.StageFormat = "round_robin"
	state.Reports[0].Claim = resultReportsClaim(1, 1)
	plan, rejection := planScoreReport(state, resultReportsAwayEntry, resultReportsClaim(1, 1))
	if rejection != nil || plan.Action != "finalize" || plan.Resolution.WinnerEntryID != nil {
		t.Fatalf("plan = %+v rejection = %+v", plan, rejection)
	}
}

func TestPlanFinalReport(t *testing.T) {
	withHomeFinal := resultReportsAwaitingResponses()
	withHomeFinal.Reports = append(withHomeFinal.Reports,
		resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "final", resultReportsClaim(2, 0)))
	withHomeFinal.Match.ActorUserID, withHomeFinal.Match.ActorEntryID = resultReportsAwayUser, resultReportsAwayEntry
	inReview := resultReportsAwaitingResponses()
	inReview.Verification.Phase = "in_review"
	deadline := *resultReportsAwaitingResponses().Verification.ResponseDeadlineAt
	tests := []struct {
		name       string
		state      verificationState
		entryID    string
		claim      scoreClaim
		wantAction planAction
		wantCode   string
	}{
		{"first response equals the other initial", resultReportsAwaitingResponses(), resultReportsHomeEntry,
			resultReportsClaim(1, 2), "finalize", ""},
		{"first response still differs", resultReportsAwaitingResponses(), resultReportsHomeEntry, resultReportsClaim(3, 1), "wait", ""},
		{"second response still differs", withHomeFinal, resultReportsAwayEntry, resultReportsClaim(1, 2), "review", ""},
		{"second response equals the other final", withHomeFinal, resultReportsAwayEntry, resultReportsClaim(2, 0), "finalize", ""},
		{"already responded", withHomeFinal, resultReportsHomeEntry, resultReportsClaim(2, 0), "", "response_already_submitted"},
		{"response at the deadline", resultReportsAt(resultReportsAwaitingResponses(), deadline), resultReportsHomeEntry,
			resultReportsClaim(1, 2), "", "response_window_closed"},
		{"match in review", inReview, resultReportsHomeEntry, resultReportsClaim(1, 2), "", "response_not_allowed"},
		{"no mismatch yet", resultReportsAwaitingSecond(), resultReportsAwayEntry, resultReportsClaim(1, 2), "", "response_not_allowed"},
		{"entry outside the match", resultReportsAwaitingResponses(), resultReportsOutsider, resultReportsClaim(1, 2), "", "match_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, rejection := planFinalReport(test.state, test.entryID, test.claim)
			if test.wantCode != "" {
				if rejection == nil || rejection.Code != test.wantCode || rejection.Status != http.StatusConflict && rejection.Status != http.StatusNotFound {
					t.Fatalf("rejection = %+v, want %s", rejection, test.wantCode)
				}
				return
			}
			if rejection != nil || plan.Action != test.wantAction {
				t.Fatalf("plan = %+v rejection = %+v, want %s", plan, rejection, test.wantAction)
			}
		})
	}
}

func TestPlanFinalReportAgreementUsesCurrentClaims(t *testing.T) {
	// The home entry's final 2-0 is its current claim; the away entry's
	// response agrees with it, so the away reporter confirms the home final.
	state := resultReportsAwaitingResponses()
	state.Reports = append(state.Reports, resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "final", resultReportsClaim(2, 0)))
	state.Match.ActorUserID, state.Match.ActorEntryID = resultReportsAwayUser, resultReportsAwayEntry
	plan, rejection := planFinalReport(state, resultReportsAwayEntry, resultReportsClaim(2, 0))
	if rejection != nil || plan.Action != "finalize" {
		t.Fatalf("plan = %+v rejection = %+v", plan, rejection)
	}
	resolution := plan.Resolution
	if resolution.Claim.HomeScore != 2 || resolution.Claim.AwayScore != 0 || *resolution.ClaimAuthorID != resultReportsHomeUser ||
		*resolution.ConfirmerID != resultReportsAwayUser || *resolution.WinnerEntryID != resultReportsHomeEntry {
		t.Fatalf("unexpected agreement: %+v", resolution)
	}
	home, away := currentClaims(state)
	if home.Kind != "final" || away.Kind != "initial" {
		t.Fatalf("current claims = %s/%s, want final/initial", home.Kind, away.Kind)
	}
}

func TestPlanVerificationDeadline(t *testing.T) {
	awaitingSecond := resultReportsAwaitingSecond()
	reportDeadline := awaitingSecond.Verification.ReportDeadlineAt
	reminderAt := awaitingSecond.Verification.ReminderAt
	reminded := resultReportsAwaitingSecond()
	sentAt := reminderAt
	reminded.Verification.ReminderSentAt = &sentAt
	responseDeadline := *resultReportsAwaitingResponses().Verification.ResponseDeadlineAt
	oneResponder := resultReportsAwaitingResponses()
	oneResponder.Reports = append(oneResponder.Reports,
		resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "final", resultReportsClaim(1, 2)))
	inProgress := verificationState{Match: resultReportsMatch("in_progress", "", "")}
	resultDue := *inProgress.Match.ResultDueAt
	inReview := resultReportsAwaitingResponses()
	inReview.Verification.Phase = "in_review"
	closed := resultReportsAt(resultReportsAwaitingSecond(), reportDeadline.Add(time.Hour))
	closed.Match.CompetitionStatus = "completed"
	tests := []struct {
		name        string
		state       verificationState
		wantAction  planAction
		wantState   string
		wantWinner  string
		wantRemoved []string
		wantReason  string
	}{
		{name: "before the reminder", state: resultReportsAt(awaitingSecond, reminderAt.Add(-time.Second)), wantAction: "none"},
		{name: "reminder due", state: resultReportsAt(awaitingSecond, reminderAt), wantAction: "reminder"},
		{name: "reminder already sent", state: resultReportsAt(reminded, reminderAt.Add(time.Second)), wantAction: "none"},
		{name: "report window ends", state: resultReportsAt(awaitingSecond, reportDeadline), wantAction: "finalize",
			wantState: "forfeit", wantWinner: resultReportsHomeEntry, wantRemoved: []string{resultReportsAwayEntry}, wantReason: "report_timeout"},
		{name: "response window open", state: resultReportsAt(resultReportsAwaitingResponses(), responseDeadline.Add(-time.Nanosecond)),
			wantAction: "none"},
		{name: "response window ends with one responder", state: resultReportsAt(oneResponder, responseDeadline), wantAction: "finalize",
			wantState: "forfeit", wantWinner: resultReportsAwayEntry, wantRemoved: []string{resultReportsHomeEntry}, wantReason: "response_timeout"},
		{name: "response window ends with no responder", state: resultReportsAt(resultReportsAwaitingResponses(), responseDeadline),
			wantAction: "finalize", wantState: "cancelled",
			wantRemoved: []string{resultReportsHomeEntry, resultReportsAwayEntry}, wantReason: "response_timeout"},
		{name: "result deadline passes without a report (R7)", state: resultReportsAt(inProgress, resultDue), wantAction: "finalize",
			wantState: "cancelled", wantRemoved: []string{resultReportsHomeEntry, resultReportsAwayEntry}, wantReason: "no_result_reported"},
		{name: "result deadline not yet reached", state: resultReportsAt(inProgress, resultDue.Add(-time.Second)), wantAction: "none"},
		{name: "match in review", state: resultReportsAt(inReview, responseDeadline.Add(time.Hour)), wantAction: "none"},
		{name: "closed competition", state: closed, wantAction: "none"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planVerificationDeadline(test.state)
			if err != nil || plan.Action != test.wantAction {
				t.Fatalf("plan = %+v err = %v, want %s", plan, err, test.wantAction)
			}
			if plan.Action == "reminder" && plan.RemindEntryID != resultReportsAwayEntry {
				t.Fatalf("reminder goes to %s, want the silent away entry", plan.RemindEntryID)
			}
			if plan.Action != "finalize" {
				return
			}
			resolution := plan.Resolution
			winner := ""
			if resolution.WinnerEntryID != nil {
				winner = *resolution.WinnerEntryID
			}
			if resolution.FinalState != test.wantState || winner != test.wantWinner ||
				!slices.Equal(resolution.RemoveEntryIDs, test.wantRemoved) || resolution.CompletionReason != test.wantReason ||
				resolution.RemovalReason != test.wantReason || resolution.Cause != progressionCauseTimeoutForfeit ||
				resolution.ApplyRatings || resolution.Claim != nil {
				t.Fatalf("unexpected resolution: %+v", resolution)
			}
			wantResolution := test.wantReason
			if test.state.Verification == nil {
				wantResolution = ""
			}
			if resolution.Resolution != wantResolution || validateMatchResolution(test.state.Match, resolution) != nil {
				t.Fatalf("resolution %+v does not fit the verification row", resolution)
			}
		})
	}
}

func TestPlanVerificationDeadlineEscalatesEvidenceStuckAtGamics(t *testing.T) {
	deadline := *resultReportsAwaitingResponses().Verification.ResponseDeadlineAt
	mismatch := *resultReportsAwaitingResponses().Verification.MismatchAt
	code := func(value string) *string { return &value }
	upload := func(entryID, status string, errorCode *string, createdAt time.Time) blockedEvidence {
		return blockedEvidence{EvidenceID: "60000000-0000-4000-8000-000000000001", EntryID: entryID,
			UploadedBy: resultReportsHomeUser, Status: status, ProcessingErrorCode: errorCode, CreatedAt: createdAt}
	}
	during := mismatch.Add(time.Minute)
	tests := []struct {
		name       string
		evidence   blockedEvidence
		responded  bool
		wantAction planAction
		wantState  string
	}{
		{"still processing", upload(resultReportsHomeEntry, "processing", nil, during), false, "escalate", ""},
		{"verification unavailable", upload(resultReportsHomeEntry, "failed", code("verification_unavailable"), during), false, "escalate", ""},
		{"processing aborted", upload(resultReportsHomeEntry, "failed", code("processing_aborted"), during), false, "escalate", ""},
		{"blocked while the other entry responded", upload(resultReportsHomeEntry, "processing", nil, during), true, "escalate", ""},
		{"rejected image", upload(resultReportsHomeEntry, "rejected", nil, during), false, "finalize", "cancelled"},
		{"never uploaded", upload(resultReportsHomeEntry, "pending", nil, during), false, "finalize", "cancelled"},
		{"expired upload", upload(resultReportsHomeEntry, "expired", nil, during), false, "finalize", "cancelled"},
		{"missing upload", upload(resultReportsHomeEntry, "failed", code("upload_missing"), during), true, "finalize", "forfeit"},
		{"uploaded before the mismatch", upload(resultReportsHomeEntry, "processing", nil, mismatch.Add(-time.Second)), true,
			"finalize", "forfeit"},
		{"uploaded at the deadline", upload(resultReportsHomeEntry, "processing", nil, deadline), true, "finalize", "forfeit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := resultReportsAt(resultReportsAwaitingResponses(), deadline)
			if test.responded {
				state.Reports = append(state.Reports,
					resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "final", resultReportsClaim(1, 2)))
			}
			state.BlockedEvidence = []blockedEvidence{test.evidence}
			plan, err := planVerificationDeadline(state)
			if err != nil || plan.Action != test.wantAction {
				t.Fatalf("plan = %+v err = %v, want %s", plan, err, test.wantAction)
			}
			if plan.Action == "finalize" && plan.Resolution.FinalState != test.wantState {
				t.Fatalf("final state = %s, want %s", plan.Resolution.FinalState, test.wantState)
			}
		})
	}
}

func TestPlanVerificationDeadlineRejectsInconsistentState(t *testing.T) {
	deadline := *resultReportsAwaitingResponses().Verification.ResponseDeadlineAt
	twoFinals := resultReportsAt(resultReportsAwaitingResponses(), deadline)
	twoFinals.Reports = append(twoFinals.Reports,
		resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "final", resultReportsClaim(2, 1)),
		resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "final", resultReportsClaim(1, 2)))
	rowInProgress := verificationState{Match: resultReportsMatch("in_progress", "", ""),
		Verification: resultReportsAwaitingSecond().Verification}
	foreignReport := resultReportsAwaitingSecond()
	foreignReport.Reports[0].EntryID = resultReportsOutsider
	responderEvidence := resultReportsAt(resultReportsAwaitingResponses(), deadline)
	responderEvidence.Reports = append(responderEvidence.Reports,
		resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "final", resultReportsClaim(2, 1)))
	responderEvidence.BlockedEvidence = []blockedEvidence{{EntryID: resultReportsHomeEntry, Status: "processing",
		CreatedAt: deadline.Add(-time.Minute)}}
	finalWithoutInitial := resultReportsAwaitingSecond()
	finalWithoutInitial.Reports = append(finalWithoutInitial.Reports,
		resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "final", resultReportsClaim(1, 2)))
	for name, state := range map[string]verificationState{
		"two final reports while awaiting responses":   twoFinals,
		"a verification row while the match is live":   rowInProgress,
		"a report on another entry":                    foreignReport,
		"blocked evidence for an entry that responded": responderEvidence,
		"a final report without an initial report":     finalWithoutInitial,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planVerificationDeadline(state); !errors.Is(err, errResultVerificationInvariant) {
				t.Fatalf("invariant error = %v", err)
			}
		})
	}
}
