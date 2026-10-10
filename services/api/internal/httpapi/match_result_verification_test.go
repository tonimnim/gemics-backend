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

// resultReportsScreenshot is a screenshot submission: it carries no score.
func resultReportsScreenshot(entryID, userID string) verificationReport {
	return resultReportsReport(entryID, userID, "final", scoreClaim{})
}

// resultReportsAwaitingSecond is T2's outcome: the home entry submitted 2-1
// two minutes ago and the away entry's confirmation window is running.
func resultReportsAwaitingSecond() verificationState {
	first := resultReportsNow.Add(-2 * time.Minute)
	return verificationState{
		Match: resultReportsMatch("awaiting_confirmation", resultReportsAwayUser, resultReportsAwayEntry),
		Verification: &lockedVerification{
			Phase: "awaiting_confirmation", FirstReportEntryID: resultReportsHomeEntry,
			ReportDeadlineAt: first.Add(10 * time.Minute), ReminderAt: first.Add(7 * time.Minute),
			ResponseWindow: 10 * time.Minute, Version: 1,
		},
		Reports: []verificationReport{resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "initial", resultReportsClaim(2, 1))},
	}
}

// resultReportsAwaitingResponses is T5's outcome: home submitted 2-1, away
// rejected it a minute ago, and the screenshot window is running.
func resultReportsAwaitingResponses() verificationState {
	mismatch := resultReportsNow.Add(-time.Minute)
	deadline := mismatch.Add(10 * time.Minute)
	rejecter := resultReportsAwayUser
	state := resultReportsAwaitingSecond()
	state.Match = resultReportsMatch("disputed", resultReportsHomeUser, resultReportsHomeEntry)
	state.Verification.Phase = "awaiting_screenshots"
	state.Verification.MismatchAt, state.Verification.ResponseDeadlineAt = &mismatch, &deadline
	state.Verification.RejectedBy = &rejecter
	state.Verification.Version = 2
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
}

func TestPlanScoreReport(t *testing.T) {
	awaitingSecond := resultReportsAwaitingSecond()
	closedCompetition := verificationState{Match: resultReportsMatch("in_progress", resultReportsHomeUser, resultReportsHomeEntry)}
	closedCompetition.Match.CompetitionStatus = "cancelled"
	inconsistent := verificationState{Match: resultReportsMatch("in_progress", resultReportsHomeUser, resultReportsHomeEntry),
		Verification: awaitingSecond.Verification}
	completed := verificationState{Match: resultReportsMatch("completed", resultReportsHomeUser, resultReportsHomeEntry)}
	firstReport := verificationState{Match: resultReportsMatch("in_progress", resultReportsHomeUser, resultReportsHomeEntry)}
	tests := []struct {
		name       string
		state      verificationState
		entryID    string
		wantAction planAction
		wantCode   string
	}{
		{"either entry submits the result", firstReport, resultReportsHomeEntry, "open", ""},
		{"the away entry may submit it too", firstReport, resultReportsAwayEntry, "open", ""},
		{"the submitter again", awaitingSecond, resultReportsHomeEntry, "", "result_already_submitted"},
		{"the opponent submits instead of answering", awaitingSecond, resultReportsAwayEntry, "", "result_awaiting_confirmation"},
		{"at the result deadline", resultReportsAt(firstReport, *firstReport.Match.ResultDueAt), resultReportsHomeEntry, "",
			"report_window_closed"},
		{"terminal match", completed, resultReportsHomeEntry, "", "report_not_allowed"},
		{"entry outside the match", firstReport, resultReportsOutsider, "", "match_not_found"},
		{"closed competition", closedCompetition, resultReportsHomeEntry, "", "competition_closed"},
		{"verification row while in progress", inconsistent, resultReportsHomeEntry, "", "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, rejection := planScoreReport(test.state, test.entryID)
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

func TestPlanConfirmation(t *testing.T) {
	awaiting := resultReportsAwaitingSecond()
	deadline := awaiting.Verification.ReportDeadlineAt
	inProgress := verificationState{Match: resultReportsMatch("in_progress", resultReportsAwayUser, resultReportsAwayEntry)}
	tests := []struct {
		name       string
		state      verificationState
		entryID    string
		confirm    bool
		wantAction planAction
		wantCode   string
	}{
		{"the opponent confirms", awaiting, resultReportsAwayEntry, true, "finalize", ""},
		{"the opponent rejects", awaiting, resultReportsAwayEntry, false, "mismatch", ""},
		{"the submitter can't answer their own result", awaiting, resultReportsHomeEntry, true, "", "own_result"},
		{"at the confirmation deadline", resultReportsAt(awaiting, deadline), resultReportsAwayEntry, true, "",
			"confirmation_window_closed"},
		{"nothing submitted yet", inProgress, resultReportsAwayEntry, true, "", "confirmation_not_allowed"},
		{"already rejected", resultReportsAwaitingResponses(), resultReportsAwayEntry, false, "", "confirmation_not_allowed"},
		{"entry outside the match", awaiting, resultReportsOutsider, true, "", "match_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, rejection := planConfirmation(test.state, test.entryID, test.confirm)
			if test.wantCode != "" {
				if rejection == nil || rejection.Code != test.wantCode {
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

func TestPlanConfirmationFinalizesTheSubmittedResult(t *testing.T) {
	// The away entry submitted 0-3 here, so the home player confirms it.
	state := resultReportsAwaitingSecond()
	state.Match.ActorUserID, state.Match.ActorEntryID = resultReportsHomeUser, resultReportsHomeEntry
	state.Verification.FirstReportEntryID = resultReportsAwayEntry
	state.Reports = []verificationReport{resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "initial", resultReportsClaim(0, 3))}
	plan, rejection := planConfirmation(state, resultReportsHomeEntry, true)
	if rejection != nil || plan.Action != "finalize" {
		t.Fatalf("plan = %+v rejection = %+v", plan, rejection)
	}
	resolution := plan.Resolution
	if resolution.FinalState != "completed" || resolution.CompletionReason != "played" || resolution.Origin != "agreed_reports" ||
		resolution.Cause != progressionCausePlayerConfirmation || resolution.Resolution != "agreed" || !resolution.ApplyRatings ||
		resolution.WinnerEntryID == nil || *resolution.WinnerEntryID != resultReportsAwayEntry || len(resolution.RemoveEntryIDs) != 0 {
		t.Fatalf("unexpected confirmation: %+v", resolution)
	}
	if *resolution.ClaimAuthorID != resultReportsAwayUser || *resolution.ConfirmerID != resultReportsHomeUser ||
		resolution.Claim.HomeScore != 0 || resolution.Claim.AwayScore != 3 {
		t.Fatalf("the submitter authors and the opponent confirms: %+v", resolution)
	}
	if validateMatchResolution(state.Match, resolution) != nil {
		t.Fatalf("resolution %+v is not applicable", resolution)
	}
}

func TestPlanConfirmationRejectionUsesTheSnapshottedScreenshotWindow(t *testing.T) {
	state := resultReportsAwaitingSecond()
	state.Verification.ResponseWindow = 25 * time.Minute
	plan, rejection := planConfirmation(state, resultReportsAwayEntry, false)
	if rejection != nil || plan.Action != "mismatch" || !plan.ResponseDeadlineAt.Equal(resultReportsNow.Add(25*time.Minute)) {
		t.Fatalf("plan = %+v rejection = %+v", plan, rejection)
	}
}

func TestPlanConfirmationOfARoundRobinDrawHasNoWinner(t *testing.T) {
	state := resultReportsAwaitingSecond()
	state.Match.StageFormat = "round_robin"
	state.Reports[0].Claim = resultReportsClaim(1, 1)
	plan, rejection := planConfirmation(state, resultReportsAwayEntry, true)
	if rejection != nil || plan.Action != "finalize" || plan.Resolution.WinnerEntryID != nil {
		t.Fatalf("plan = %+v rejection = %+v", plan, rejection)
	}
}

func TestPlanScreenshot(t *testing.T) {
	withHomeScreenshot := resultReportsAwaitingResponses()
	withHomeScreenshot.Reports = append(withHomeScreenshot.Reports, resultReportsScreenshot(resultReportsHomeEntry, resultReportsHomeUser))
	inReview := resultReportsAwaitingResponses()
	inReview.Verification.Phase = "in_review"
	deadline := *resultReportsAwaitingResponses().Verification.ResponseDeadlineAt
	tests := []struct {
		name       string
		state      verificationState
		entryID    string
		wantAction planAction
		wantCode   string
	}{
		{"the submitter's screenshot waits for the rejecter's", resultReportsAwaitingResponses(), resultReportsHomeEntry, "wait", ""},
		{"the rejecter's screenshot waits for the submitter's", resultReportsAwaitingResponses(), resultReportsAwayEntry, "wait", ""},
		{"the second screenshot goes to review", withHomeScreenshot, resultReportsAwayEntry, "review", ""},
		{"already sent", withHomeScreenshot, resultReportsHomeEntry, "", "screenshot_already_submitted"},
		{"at the deadline", resultReportsAt(resultReportsAwaitingResponses(), deadline), resultReportsHomeEntry, "",
			"screenshot_window_closed"},
		{"match in review", inReview, resultReportsHomeEntry, "", "screenshot_not_allowed"},
		{"not rejected yet", resultReportsAwaitingSecond(), resultReportsAwayEntry, "", "screenshot_not_allowed"},
		{"entry outside the match", resultReportsAwaitingResponses(), resultReportsOutsider, "", "match_not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, rejection := planScreenshot(test.state, test.entryID)
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

func TestPlanVerificationDeadlineLetsAnUnansweredResultStand(t *testing.T) {
	state := resultReportsAt(resultReportsAwaitingSecond(), resultReportsAwaitingSecond().Verification.ReportDeadlineAt)
	plan, err := planVerificationDeadline(state)
	if err != nil || plan.Action != "finalize" {
		t.Fatalf("plan = %+v err = %v", plan, err)
	}
	resolution := plan.Resolution
	if resolution.FinalState != "completed" || resolution.CompletionReason != "played" || resolution.Origin != "unanswered" ||
		resolution.Resolution != "confirmation_timeout" || !resolution.ApplyRatings || len(resolution.RemoveEntryIDs) != 0 ||
		resolution.ConfirmerID != nil || *resolution.ClaimAuthorID != resultReportsHomeUser ||
		resolution.WinnerEntryID == nil || *resolution.WinnerEntryID != resultReportsHomeEntry ||
		resolution.Claim.HomeScore != 2 || resolution.Claim.AwayScore != 1 {
		t.Fatalf("the submitted result must stand: %+v", resolution)
	}
	if validateMatchResolution(state.Match, resolution) != nil {
		t.Fatalf("resolution %+v is not applicable", resolution)
	}
}

func TestPlanVerificationDeadline(t *testing.T) {
	awaitingSecond := resultReportsAwaitingSecond()
	reminderAt := awaitingSecond.Verification.ReminderAt
	reminded := resultReportsAwaitingSecond()
	sentAt := reminderAt
	reminded.Verification.ReminderSentAt = &sentAt
	responseDeadline := *resultReportsAwaitingResponses().Verification.ResponseDeadlineAt
	oneResponder := resultReportsAwaitingResponses()
	oneResponder.Reports = append(oneResponder.Reports, resultReportsScreenshot(resultReportsAwayEntry, resultReportsAwayUser))
	inProgress := verificationState{Match: resultReportsMatch("in_progress", "", "")}
	resultDue := *inProgress.Match.ResultDueAt
	inReview := resultReportsAwaitingResponses()
	inReview.Verification.Phase = "in_review"
	closed := resultReportsAt(resultReportsAwaitingSecond(), awaitingSecond.Verification.ReportDeadlineAt.Add(time.Hour))
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
		{name: "screenshot window open", state: resultReportsAt(resultReportsAwaitingResponses(), responseDeadline.Add(-time.Nanosecond)),
			wantAction: "none"},
		{name: "screenshot window ends with one screenshot", state: resultReportsAt(oneResponder, responseDeadline), wantAction: "finalize",
			wantState: "forfeit", wantWinner: resultReportsAwayEntry, wantRemoved: []string{resultReportsHomeEntry}, wantReason: "response_timeout"},
		{name: "screenshot window ends with none", state: resultReportsAt(resultReportsAwaitingResponses(), responseDeadline),
			wantAction: "finalize", wantState: "cancelled",
			wantRemoved: []string{resultReportsHomeEntry, resultReportsAwayEntry}, wantReason: "response_timeout"},
		{name: "result deadline passes without a result (R7)", state: resultReportsAt(inProgress, resultDue), wantAction: "finalize",
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
				t.Fatalf("reminder goes to %s, want the away entry that owes its answer", plan.RemindEntryID)
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
				state.Reports = append(state.Reports, resultReportsScreenshot(resultReportsAwayEntry, resultReportsAwayUser))
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
	twoScreenshots := resultReportsAt(resultReportsAwaitingResponses(), deadline)
	twoScreenshots.Reports = append(twoScreenshots.Reports,
		resultReportsScreenshot(resultReportsHomeEntry, resultReportsHomeUser),
		resultReportsScreenshot(resultReportsAwayEntry, resultReportsAwayUser))
	rowInProgress := verificationState{Match: resultReportsMatch("in_progress", "", ""),
		Verification: resultReportsAwaitingSecond().Verification}
	foreignReport := resultReportsAwaitingSecond()
	foreignReport.Reports[0].EntryID = resultReportsOutsider
	responderEvidence := resultReportsAt(resultReportsAwaitingResponses(), deadline)
	responderEvidence.Reports = append(responderEvidence.Reports, resultReportsScreenshot(resultReportsHomeEntry, resultReportsHomeUser))
	responderEvidence.BlockedEvidence = []blockedEvidence{{EntryID: resultReportsHomeEntry, Status: "processing",
		CreatedAt: deadline.Add(-time.Minute)}}
	twoResults := resultReportsAt(resultReportsAwaitingResponses(), deadline)
	twoResults.Reports = append(twoResults.Reports,
		resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "initial", resultReportsClaim(1, 2)))
	noRejecter := resultReportsAt(resultReportsAwaitingResponses(), deadline)
	noRejecter.Verification.RejectedBy = nil
	for name, state := range map[string]verificationState{
		"two screenshots while awaiting screenshots":   twoScreenshots,
		"a verification row while the match is live":   rowInProgress,
		"a report on another entry":                    foreignReport,
		"blocked evidence for an entry that sent one":  responderEvidence,
		"two submitted results in a dispute":           twoResults,
		"a dispute without the player who rejected it": noRejecter,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planVerificationDeadline(state); !errors.Is(err, errResultVerificationInvariant) {
				t.Fatalf("invariant error = %v", err)
			}
		})
	}
}
