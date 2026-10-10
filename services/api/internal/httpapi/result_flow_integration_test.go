package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/organizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These DB-gated tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it. They play whole draws through
// the score-report handlers, the result verification worker and the Gamics
// review queue, covering R2-R9 for single elimination, double elimination and
// round robin, and then the strike ban, competition cancellation, conflicts of
// interest and the gated withdrawal and payment races.

const resultFlowStrikeBanThreshold = 3

// resultFlowPlay is how the two entries of one match behave.
type resultFlowPlay uint8

const (
	resultFlowAgree            resultFlowPlay = iota // R4: home submits, away confirms
	resultFlowSilentAway                             // R5: away never answers, so home's result stands
	resultFlowNoResponders                           // R6: away rejects and nobody sends a screenshot
	resultFlowOneResponder                           // R6: away rejects and only home sends one
	resultFlowRejecterResponds                       // R6: away rejects and only away sends one
	resultFlowUnreported                             // R7
	resultFlowAcceptHome                             // R8: home's submitted result is accepted
	resultFlowAcceptAway                             // R8: away's submitted result is accepted
	resultFlowCorrected                              // R8
	resultFlowRemoveBoth                             // R8
)

func (play resultFlowPlay) String() string {
	return [...]string{"agree", "silent away", "no responders", "one responder", "rejecter responds",
		"unreported", "accept home", "accept away", "corrected score", "remove both"}[play]
}

// resultFlowRule plays one ready match. Number 0 matches the next ready match
// of the bracket and round, whichever fixture that is.
type resultFlowRule struct {
	Bracket string
	Round   int
	Number  int
	Play    resultFlowPlay
}

func (rule resultFlowRule) matches(match progressionRemovalMatch) bool {
	return rule.Bracket == match.Bracket && rule.Round == match.Round && (rule.Number == 0 || rule.Number == match.Number)
}

type resultFlowHarness struct {
	Server  *Server
	Pool    *pgxpool.Pool
	StaffID string
}

type resultFlowRun struct {
	Competition integrationCompetition
	RemovedBy   map[string]progressionRemovalMatch
	Strikes     map[string]int
}

type resultFlowScenario struct {
	Name    string
	Options integrationSeedOptions
	Rules   []resultFlowRule
	// Choose, when set, is consulted before the rules.
	Choose func(match progressionRemovalMatch, run resultFlowRun) (resultFlowPlay, bool)
	Check  func(t *testing.T, h resultFlowHarness, run resultFlowRun)
}

// resultFlowExpectation is the outcome one play must leave behind.
type resultFlowExpectation struct {
	State, Reason, Resolution, Origin string
	Winner                            *string
	Removed                           []string
	Strikes                           []string
	Score                             [2]int
	Author, Confirmer                 string
	Rated, Reviewed, Mismatch         bool
	Initials, Finals                  int
}

func resultFlowSetup(t *testing.T) resultFlowHarness {
	t.Helper()
	pool := openMigratedIntegrationDatabase(t)
	server := resultReportsServer(pool)
	server.config.StrikeBanThreshold = resultFlowStrikeBanThreshold
	return resultFlowHarness{Server: server, Pool: pool, StaffID: resultFlowInsertStaff(t, pool, "reviewer", "reviewer")}
}

func resultFlowInsertStaff(t *testing.T, pool *pgxpool.Pool, label, role string) string {
	t.Helper()
	userID := resultReportsInsertUser(t, pool, label)
	resultFlowExec(t, pool, `INSERT INTO platform_staff_roles(user_id,role) VALUES ($1,$2)`, userID, role)
	return userID
}

func resultFlowExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func resultFlowRunScenarios(t *testing.T, scenarios []resultFlowScenario) {
	t.Helper()
	h := resultFlowSetup(t)
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			run := resultFlowPlayDraw(t, h, scenario)
			progressionRemovalAssertSettled(t, h.Pool, progressionRemovalRun{Competition: run.Competition, RemovedBy: run.RemovedBy})
			for userID, strikes := range run.Strikes {
				if active := resultReportsCount(t, h.Pool, `SELECT count(*) FROM player_strikes
					WHERE user_id=$1 AND revoked_at IS NULL`, userID); active != strikes {
					t.Fatalf("user %s has %d active strikes, want %d", userID, active, strikes)
				}
			}
			resultReportsAssertNoScoreLeaks(t, h.Pool, run.Competition.ID)
			if scenario.Check != nil {
				scenario.Check(t, h, run)
			}
		})
	}
}

// resultFlowPlayDraw plays ready matches one at a time until the draw is
// finished. Every rule must fire exactly once.
func resultFlowPlayDraw(t *testing.T, h resultFlowHarness, scenario resultFlowScenario) resultFlowRun {
	t.Helper()
	run := resultFlowRun{Competition: seedIntegrationCompetition(t, h.Pool, scenario.Options),
		RemovedBy: map[string]progressionRemovalMatch{}, Strikes: map[string]int{}}
	fired := make([]bool, len(scenario.Rules))
	for step := 0; ; step++ {
		ready := progressionRemovalReadyMatches(t, h.Pool, run.Competition.ID)
		if len(ready) == 0 {
			break
		}
		if step == progressionRemovalMaxSteps {
			t.Fatalf("draw did not finish within %d matches", progressionRemovalMaxSteps)
		}
		match := ready[0]
		play := resultFlowChoose(scenario, run, fired, match)
		expected := resultFlowPlayMatch(t, h, match, play)
		progressionRemovalAssertNoLiveReference(t, h.Pool, run.Competition.ID, match)
		for _, entryID := range expected.Removed {
			run.RemovedBy[entryID] = match
		}
		for _, userID := range expected.Strikes {
			run.Strikes[userID]++
		}
	}
	for index, rule := range scenario.Rules {
		if !fired[index] {
			t.Fatalf("rule %+v (%s) never matched a ready match", rule, rule.Play)
		}
	}
	return run
}

func resultFlowChoose(scenario resultFlowScenario, run resultFlowRun, fired []bool, match progressionRemovalMatch) resultFlowPlay {
	if scenario.Choose != nil {
		if play, ok := scenario.Choose(match, run); ok {
			return play
		}
	}
	for index, rule := range scenario.Rules {
		if !fired[index] && rule.matches(match) {
			fired[index] = true
			return rule.Play
		}
	}
	return resultFlowAgree
}

func resultFlowPlayMatch(t *testing.T, h resultFlowHarness, match progressionRemovalMatch, play resultFlowPlay) resultFlowExpectation {
	t.Helper()
	sides := resultReportsStart(t, h.Pool, match.ID)
	homePlayed, awayPlayed := resultFlowMatchesPlayed(t, h.Pool, sides)
	expected := resultFlowExecute(t, h, sides, play)
	resultFlowAssertOutcome(t, h.Pool, sides, play, expected)
	homeAfter, awayAfter := resultFlowMatchesPlayed(t, h.Pool, sides)
	delta := 0
	if expected.Rated {
		delta = 1
	}
	if homeAfter != homePlayed+delta || awayAfter != awayPlayed+delta {
		t.Fatalf("%s in %s: rated matches went %d->%d and %d->%d, want +%d", play, match, homePlayed, homeAfter,
			awayPlayed, awayAfter, delta)
	}
	return expected
}

func resultFlowMatchesPlayed(t *testing.T, pool *pgxpool.Pool, sides resultReportsSides) (int, int) {
	t.Helper()
	var home, away int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT COALESCE(sum(matches_played),0)::integer FROM player_game_ratings WHERE user_id=$1),
		(SELECT COALESCE(sum(matches_played),0)::integer FROM player_game_ratings WHERE user_id=$2)`,
		sides.HomeUser, sides.AwayUser).Scan(&home, &away); err != nil {
		t.Fatal(err)
	}
	return home, away
}

// resultFlowExecute drives one match through the real handlers and worker.
func resultFlowExecute(t *testing.T, h resultFlowHarness, sides resultReportsSides, play resultFlowPlay) resultFlowExpectation {
	t.Helper()
	home, away := sides.HomeEntry, sides.AwayEntry
	both := []string{home, away}
	slices.Sort(both)
	switch play {
	case resultFlowAgree:
		resultReportsMustPost(t, h.Server, sides.HomeUser, sides.MatchID, "result-1", resultReportsBody(2, 1), "")
		resultReportsMustPost(t, h.Server, sides.AwayUser, sides.MatchID, "confirm-1", resultReportsAnswer("confirm"), "confirmation")
		return resultFlowExpectation{State: "completed", Reason: "played", Resolution: "agreed", Origin: "agreed_reports",
			Winner: &home, Score: [2]int{2, 1}, Author: sides.HomeUser, Confirmer: sides.AwayUser, Rated: true, Initials: 1}
	case resultFlowSilentAway:
		resultReportsMustPost(t, h.Server, sides.HomeUser, sides.MatchID, "result-1", resultReportsBody(2, 1), "")
		shiftVerificationClock(t, h.Pool, sides.MatchID, 10*time.Minute+time.Second)
		resultReportsProcess(t, h.Server, sides)
		return resultFlowExpectation{State: "completed", Reason: "played", Resolution: "confirmation_timeout",
			Origin: "unanswered", Winner: &home, Score: [2]int{2, 1}, Author: sides.HomeUser, Rated: true, Initials: 1}
	case resultFlowNoResponders:
		resultFlowReject(t, h, sides, "home", 2, 1)
		shiftVerificationClock(t, h.Pool, sides.MatchID, 11*time.Minute)
		resultReportsProcess(t, h.Server, sides)
		return resultFlowExpectation{State: "cancelled", Reason: "response_timeout", Resolution: "response_timeout",
			Removed: both, Mismatch: true, Initials: 1}
	case resultFlowOneResponder:
		resultFlowReject(t, h, sides, "home", 2, 1)
		resultFlowRespond(t, h, sides.MatchID, sides.HomeUser)
		shiftVerificationClock(t, h.Pool, sides.MatchID, 11*time.Minute)
		resultReportsProcess(t, h.Server, sides)
		return resultFlowExpectation{State: "forfeit", Reason: "response_timeout", Resolution: "response_timeout",
			Winner: &home, Removed: []string{away}, Mismatch: true, Initials: 1, Finals: 1}
	case resultFlowRejecterResponds:
		resultFlowReject(t, h, sides, "home", 2, 1)
		resultFlowRespond(t, h, sides.MatchID, sides.AwayUser)
		shiftVerificationClock(t, h.Pool, sides.MatchID, 11*time.Minute)
		resultReportsProcess(t, h.Server, sides)
		return resultFlowExpectation{State: "forfeit", Reason: "response_timeout", Resolution: "response_timeout",
			Winner: &away, Removed: []string{home}, Mismatch: true, Initials: 1, Finals: 1}
	case resultFlowUnreported:
		// Later rounds are scheduled hours ahead; two days puts every deadline
		// behind the database clock.
		shiftResultDue(t, h.Pool, sides.MatchID, 48*time.Hour)
		resultReportsProcess(t, h.Server, sides)
		return resultFlowExpectation{State: "cancelled", Reason: "no_result_reported", Removed: both}
	default:
		return resultFlowReview(t, h, sides, play)
	}
}

// resultFlowReject has one entry ("home" or "away") submit a result and the
// other reject it, which opens the screenshot window.
func resultFlowReject(t *testing.T, h resultFlowHarness, sides resultReportsSides, submitter string, home, away int) {
	t.Helper()
	submitterUser, rejecterUser := sides.HomeUser, sides.AwayUser
	if submitter == "away" {
		submitterUser, rejecterUser = sides.AwayUser, sides.HomeUser
	}
	resultReportsMustPost(t, h.Server, submitterUser, sides.MatchID, "result-1", resultReportsBody(home, away), "")
	room := resultReportsMustPost(t, h.Server, rejecterUser, sides.MatchID, "reject-1", resultReportsAnswer("reject"), "confirmation")
	if room.State != "disputed" || room.ResultVerification.Phase != "awaiting_screenshots" {
		t.Fatalf("the rejection did not open the screenshot window: %+v", room)
	}
}

// resultFlowRespond sends one freshly processed screenshot.
func resultFlowRespond(t *testing.T, h resultFlowHarness, matchID, userID string) string {
	t.Helper()
	evidenceID := resultReportsInsertEvidence(t, h.Pool, userID, "completed", time.Now())
	resultReportsMustPost(t, h.Server, userID, matchID, "screenshot-1", resultReportsShot(evidenceID), "screenshot")
	return evidenceID
}

// resultFlowReview sends the match to the Gamics queue (a rejected result and
// both screenshots) and decides it as the unconflicted reviewer. Strikes hit
// the player the decision proves wrong.
func resultFlowReview(t *testing.T, h resultFlowHarness, sides resultReportsSides, play resultFlowPlay) resultFlowExpectation {
	t.Helper()
	submitter, submitted := "home", [2]int{2, 1}
	if play == resultFlowAcceptAway {
		submitter, submitted = "away", [2]int{1, 2}
	}
	resultFlowReject(t, h, sides, submitter, submitted[0], submitted[1])
	resultFlowRespond(t, h, sides.MatchID, sides.HomeUser)
	awayEvidence := resultFlowRespond(t, h, sides.MatchID, sides.AwayUser)
	reviewID := resultFlowReviewID(t, h.Pool, sides.MatchID)
	if queue := resultFlowListReviews(t, h, h.StaffID, "status=queued&competitionId="+sides.CompetitionID); !slices.Contains(queue, reviewID) {
		t.Fatalf("review %s is missing from the queue %v", reviewID, queue)
	}
	detail := resultFlowGetReview(t, h, h.StaffID, reviewID)
	submittedReport := detail.Reports.Home.Initial
	rejecter := sides.AwayUser
	if submitter == "away" {
		submittedReport, rejecter = detail.Reports.Away.Initial, sides.HomeUser
	}
	if detail.Status != "queued" || detail.Reason != "reports_differ" || submittedReport == nil ||
		submittedReport.HomeScore == nil || *submittedReport.HomeScore != submitted[0] ||
		detail.Verification.RejectedBy == nil || *detail.Verification.RejectedBy != rejecter ||
		detail.Reports.Home.Final == nil || detail.Reports.Away.Final == nil || detail.Reports.Away.Final.HomeScore != nil ||
		len(detail.Reports.Away.Final.Evidence) != 1 || detail.Reports.Away.Final.Evidence[0].ID != awayEvidence {
		t.Fatalf("unexpected review detail %+v", detail)
	}
	if code := resultReportsGet(t, h.Server.getEvidenceAccess, h.StaffID, "id", awayEvidence).Code; code != http.StatusOK {
		t.Fatalf("the reviewer cannot open a screenshot: %d", code)
	}
	home, away := sides.HomeEntry, sides.AwayEntry
	body := map[string]any{"expectedVersion": detail.Version, "note": "Screenshots decide this match."}
	expected := resultFlowExpectation{State: "completed", Reason: "platform_review", Resolution: "platform_review",
		Origin: "platform_review", Confirmer: h.StaffID, Rated: true, Reviewed: true, Mismatch: true, Initials: 1, Finals: 2}
	switch play {
	case resultFlowAcceptHome:
		body["decision"], body["strikeUserIds"] = "accept_home", []string{sides.AwayUser}
		expected.Winner, expected.Score, expected.Author, expected.Strikes = &home, [2]int{2, 1}, sides.HomeUser, []string{sides.AwayUser}
	case resultFlowAcceptAway:
		body["decision"], body["strikeUserIds"] = "accept_away", []string{sides.HomeUser}
		expected.Winner, expected.Score, expected.Author, expected.Strikes = &away, [2]int{1, 2}, sides.AwayUser, []string{sides.HomeUser}
	case resultFlowCorrected:
		// 3-0 isn't the submitted 2-1, so the submitter is the one proved wrong.
		body["decision"], body["strikeUserIds"] = "corrected_score", []string{sides.HomeUser}
		body["correctedScore"] = map[string]any{"homeScore": 3, "awayScore": 0}
		expected.Winner, expected.Score, expected.Author, expected.Strikes = &home, [2]int{3, 0}, h.StaffID, []string{sides.HomeUser}
	case resultFlowRemoveBoth:
		body["decision"], body["strikeUserIds"] = "remove_both", []string{sides.HomeUser, sides.AwayUser}
		expected.State, expected.Origin, expected.Confirmer, expected.Rated = "cancelled", "", "", false
		expected.Removed = []string{home, away}
		expected.Strikes = []string{sides.HomeUser, sides.AwayUser}
	default:
		t.Fatalf("%s is not a review play", play)
	}
	slices.Sort(expected.Removed)
	slices.Sort(expected.Strikes)
	recorder := resultFlowDecide(t, h, h.StaffID, reviewID, "review-decision-1", body)
	decided := resultFlowDecodeReview(t, recorder)
	if decided.Status != "decided" || decided.Decision == nil || decided.Decision.Decision != body["decision"] ||
		decided.Decision.DeciderKind != "staff" || len(decided.Strikes) != len(expected.Strikes) {
		t.Fatalf("unexpected decided review %+v", decided)
	}
	replay := resultFlowDecide(t, h, h.StaffID, reviewID, "review-decision-1", body)
	if replay.Code != recorder.Code || replay.Header().Get("Idempotency-Replayed") != "true" ||
		!sameJSON(t, recorder.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatalf("the decision replay differs: %d %s / %d %s", recorder.Code, recorder.Body.String(),
			replay.Code, replay.Body.String())
	}
	return expected
}

func resultFlowReviewID(t *testing.T, pool *pgxpool.Pool, matchID string) string {
	t.Helper()
	var reviewID string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM match_result_reviews WHERE match_id=$1`, matchID).
		Scan(&reviewID); err != nil {
		t.Fatalf("review of %s: %v", matchID, err)
	}
	return reviewID
}

func resultFlowStaffRequest(t *testing.T, method, target, actorID, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	return request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: actorID}))
}

func resultFlowListReviews(t *testing.T, h resultFlowHarness, actorID, query string) []string {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.Server.listResultReviews(recorder, resultFlowStaffRequest(t, http.MethodGet, "/v1/admin/result-reviews?"+query, actorID, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("review queue: %d %s", recorder.Code, recorder.Body.String())
	}
	var page struct {
		Data []resultReviewSummary `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(page.Data))
	for _, item := range page.Data {
		ids = append(ids, item.ID)
	}
	return ids
}

func resultFlowGetReview(t *testing.T, h resultFlowHarness, actorID, reviewID string) resultReviewDetail {
	t.Helper()
	return resultFlowDecodeReview(t, resultReportsGet(t, h.Server.getResultReview, actorID, "id", reviewID))
}

func resultFlowDecide(t *testing.T, h resultFlowHarness, actorID, reviewID, key string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := resultFlowStaffRequest(t, http.MethodPost, "/v1/admin/result-reviews/"+reviewID+"/decisions", actorID, string(raw))
	request.SetPathValue("id", reviewID)
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	h.Server.decideResultReview(recorder, request)
	return recorder
}

func resultFlowDecodeReview(t *testing.T, recorder *httptest.ResponseRecorder) resultReviewDetail {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("review: %d %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data resultReviewDetail `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

// resultFlowAssertOutcome checks the match, removals, verification, canonical
// result, audit trail, pushes and strikes one play must leave behind.
func resultFlowAssertOutcome(t *testing.T, pool *pgxpool.Pool, sides resultReportsSides, play resultFlowPlay,
	expected resultFlowExpectation) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("%s in match %s: %s", play, sides.MatchID, fmt.Sprintf(format, args...))
	}
	var state string
	var reason, winner *string
	if err := pool.QueryRow(t.Context(), `SELECT state,completion_reason,winner_entry_id::text FROM matches WHERE id=$1`,
		sides.MatchID).Scan(&state, &reason, &winner); err != nil {
		t.Fatal(err)
	}
	if state != expected.State || valueOrEmpty(reason) != expected.Reason || !sameOptionalString(winner, expected.Winner) {
		fail("match is %s/%s winner %q", state, valueOrEmpty(reason), valueOrEmpty(winner))
	}
	removed := resultFlowStrings(t, pool, `SELECT removal.entry_id::text FROM competition_entry_removals removal
		JOIN competition_entries entry ON entry.id=removal.entry_id
		WHERE removal.match_id=$1 AND removal.reason_code=$2 AND entry.status='disqualified'
		ORDER BY removal.entry_id`, sides.MatchID, expected.Reason)
	if !slices.Equal(removed, expected.Removed) {
		fail("removed %v, want %v", removed, expected.Removed)
	}
	if rows := resultReportsCount(t, pool, `SELECT count(*) FROM competition_entry_removals WHERE match_id=$1`,
		sides.MatchID); rows != len(expected.Removed) {
		fail("%d removal rows, want %d", rows, len(expected.Removed))
	}
	if expected.Resolution == "" {
		if rows := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_verifications WHERE match_id=$1`, sides.MatchID); rows != 0 {
			fail("an unreported match has a verification row")
		}
	} else if resolved := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_verifications
		WHERE match_id=$1 AND phase='resolved' AND resolution=$2`, sides.MatchID, expected.Resolution); resolved != 1 {
		fail("the verification row is not resolved as %s", expected.Resolution)
	}
	resultFlowAssertCanonicalResult(t, pool, sides, expected, fail)
	resultFlowAssertAuditAndPushes(t, pool, sides, expected, fail)
	strikes := resultFlowStrings(t, pool, `SELECT user_id::text FROM player_strikes WHERE match_id=$1 ORDER BY user_id`, sides.MatchID)
	if !slices.Equal(strikes, expected.Strikes) {
		fail("strikes %v, want %v", strikes, expected.Strikes)
	}
	if pushed := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE event_type='player.strike_recorded'
		AND payload->>'matchId'=$1 AND aggregate_type='user' AND aggregate_id=payload->>'userId'`, sides.MatchID); pushed != len(expected.Strikes) {
		fail("%d strike pushes, want %d", pushed, len(expected.Strikes))
	}
}

func resultFlowAssertCanonicalResult(t *testing.T, pool *pgxpool.Pool, sides resultReportsSides, expected resultFlowExpectation,
	fail func(string, ...any)) {
	t.Helper()
	if expected.State != "completed" {
		if rows := resultReportsCount(t, pool, `SELECT count(*) FROM result_submissions WHERE match_id=$1`, sides.MatchID); rows != 0 {
			fail("a match without a played result has %d result rows", rows)
		}
		return
	}
	var author, origin string
	var confirmer *string
	var home, away int
	if err := pool.QueryRow(t.Context(), `SELECT submitted_by::text,decided_by::text,origin,home_score,away_score
		FROM result_submissions WHERE match_id=$1 AND status='confirmed'`, sides.MatchID).
		Scan(&author, &confirmer, &origin, &home, &away); err != nil {
		fail("canonical result: %v", err)
	}
	if author != expected.Author || valueOrEmpty(confirmer) != expected.Confirmer || origin != expected.Origin ||
		home != expected.Score[0] || away != expected.Score[1] {
		fail("canonical result %d-%d by %s/%s (%s)", home, away, author, valueOrEmpty(confirmer), origin)
	}
}

func resultFlowAssertAuditAndPushes(t *testing.T, pool *pgxpool.Pool, sides resultReportsSides, expected resultFlowExpectation,
	fail func(string, ...any)) {
	t.Helper()
	terminal := map[string][2]string{
		"completed": {"result.confirmed", "match.result_confirmed"},
		"forfeit":   {"match.forfeited", "match.forfeited"},
		"cancelled": {"match.cancelled", "match.cancelled"},
	}[expected.State]
	mismatches := 0
	if expected.Mismatch {
		mismatches = 1
	}
	reviews := 0
	if expected.Reviewed {
		reviews = 1
	}
	for action, want := range map[string]int{
		terminal[0]: 1, "result.reported": expected.Initials, "result.screenshot_submitted": expected.Finals,
		"result.rejected": mismatches, "result.review_queued": reviews,
	} {
		if got := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events WHERE subject_type='match'
			AND subject_id=$1 AND action=$2`, sides.MatchID, action); got != want {
			fail("%d %s audit rows, want %d", got, action, want)
		}
	}
	if got := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events WHERE action='competition.entry_removed'
		AND after_state->>'matchId'=$1`, sides.MatchID); got != len(expected.Removed) {
		fail("%d removal audit rows, want %d", got, len(expected.Removed))
	}
	if got := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events review_audit
		JOIN match_result_reviews review ON review.id::text=review_audit.subject_id
		WHERE review_audit.action='result.review_decided' AND review_audit.organization_id IS NULL
		  AND review.match_id=$1`, sides.MatchID); got != reviews {
		fail("%d review decision audit rows, want %d", got, reviews)
	}
	terminalPushes := 1 - reviews
	if got := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE aggregate_type='match'
		AND aggregate_id=$1 AND event_type=$2`, sides.MatchID, terminal[1]); got != terminalPushes {
		fail("%d %s pushes, want %d", got, terminal[1], terminalPushes)
	}
	if got := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE aggregate_type='match'
		AND aggregate_id=$1 AND event_type='result.review_decided' AND NOT payload ? 'reason'`, sides.MatchID); got != reviews {
		fail("%d review decision pushes, want %d", got, reviews)
	}
	if got := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE event_type='competition.entry_removed'
		AND payload->>'matchId'=$1`, sides.MatchID); got != len(expected.Removed) {
		fail("%d removal pushes, want %d", got, len(expected.Removed))
	}
}

func resultFlowStrings(t *testing.T, pool *pgxpool.Pool, query string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestIntegrationResultFlowSingleElimination(t *testing.T) {
	options := integrationSeedOptions{Format: "single_elimination", Entries: 8, ThirdPlace: true}
	resultFlowRunScenarios(t, []resultFlowScenario{
		{
			Name: "silence, one screenshot each way and three review decisions", Options: options,
			Rules: []resultFlowRule{
				{Bracket: "main", Round: 1, Number: 2, Play: resultFlowSilentAway},
				{Bracket: "main", Round: 1, Number: 3, Play: resultFlowOneResponder},
				{Bracket: "main", Round: 1, Number: 4, Play: resultFlowAcceptAway},
				{Bracket: "main", Round: 2, Number: 1, Play: resultFlowRejecterResponds},
				{Bracket: "main", Round: 2, Number: 2, Play: resultFlowCorrected},
				{Bracket: "main", Round: 3, Number: 1, Play: resultFlowRemoveBoth},
			},
			Check: func(t *testing.T, h resultFlowHarness, run resultFlowRun) {
				// Both finalists were removed: nobody is champion or runner-up.
				progressionRemovalExpectNoFinalists(t, progressionRemovalPlacements(t, h.Pool, run.Competition.ID))
			},
		},
		{
			Name: "no responders, no report at all and an accepted home claim", Options: options,
			Rules: []resultFlowRule{
				{Bracket: "main", Round: 1, Number: 1, Play: resultFlowNoResponders},
				{Bracket: "main", Round: 1, Number: 2, Play: resultFlowUnreported},
				{Bracket: "main", Round: 1, Number: 3, Play: resultFlowAcceptHome},
			},
			Check: func(t *testing.T, h resultFlowHarness, run resultFlowRun) {
				id := run.Competition.ID
				progressionRemovalExpect(t, "semifinal 1", progressionRemovalLoad(t, h.Pool, id, "main", 2, 1),
					"cancelled", "double_no_show", nil)
				semifinal := progressionRemovalLoad(t, h.Pool, id, "main", 2, 2)
				progressionRemovalExpect(t, "final", progressionRemovalLoad(t, h.Pool, id, "main", 3, 1),
					"forfeit", "walkover", semifinal.WinnerEntryID)
				progressionRemovalExpect(t, "bronze", progressionRemovalLoad(t, h.Pool, id, "bronze", 3, 1), "forfeit", "walkover",
					losingEntry(semifinal.HomeEntryID, semifinal.AwayEntryID, semifinal.WinnerEntryID))
			},
		},
	})
}

func TestIntegrationResultFlowDoubleElimination(t *testing.T) {
	resultFlowRunScenarios(t, []resultFlowScenario{
		{
			Name: "four entries: a removal, reviews and the rejecter's lone screenshot", Options: integrationSeedOptions{
				Format: "double_elimination", Entries: 4},
			Rules: []resultFlowRule{
				{Bracket: "winners", Round: 1, Number: 1, Play: resultFlowOneResponder},
				{Bracket: "winners", Round: 1, Number: 2, Play: resultFlowAcceptHome},
				{Bracket: "winners", Round: 2, Number: 1, Play: resultFlowCorrected},
				{Bracket: "losers", Round: 2, Number: 1, Play: resultFlowRejecterResponds},
				{Bracket: "grand_final", Round: 1, Number: 1, Play: resultFlowAcceptAway},
			},
			Check: func(t *testing.T, h resultFlowHarness, run resultFlowRun) {
				id := run.Competition.ID
				fed := progressionRemovalLoad(t, h.Pool, id, "winners", 1, 2)
				progressionRemovalExpect(t, "losers round 1", progressionRemovalLoad(t, h.Pool, id, "losers", 1, 1),
					"forfeit", "walkover", losingEntry(fed.HomeEntryID, fed.AwayEntryID, fed.WinnerEntryID))
				// The losers champion won grand final 1 by review, so the reset was played.
				reset := progressionRemovalLoad(t, h.Pool, id, "grand_final", 2, 1)
				if reset.State != "completed" || valueOrEmpty(reset.CompletionReason) != "played" {
					t.Fatalf("grand final reset = %s/%s", reset.State, valueOrEmpty(reset.CompletionReason))
				}
			},
		},
		{
			Name: "eight entries: every removal path in the winners bracket", Options: integrationSeedOptions{
				Format: "double_elimination", Entries: 8},
			Rules: []resultFlowRule{
				{Bracket: "winners", Round: 1, Number: 1, Play: resultFlowOneResponder},
				{Bracket: "winners", Round: 1, Number: 2, Play: resultFlowNoResponders},
				{Bracket: "winners", Round: 1, Number: 3, Play: resultFlowUnreported},
				{Bracket: "winners", Round: 1, Number: 4, Play: resultFlowRemoveBoth},
			},
			Check: func(t *testing.T, h resultFlowHarness, run resultFlowRun) {
				// Seven entries were removed; the survivor wins every later match
				// by walkover and is the only one placed.
				progressionRemovalExpectPlacements(t, progressionRemovalPlacements(t, h.Pool, run.Competition.ID), 1, nil)
			},
		},
	})
}

func TestIntegrationResultFlowRoundRobin(t *testing.T) {
	resultFlowRunScenarios(t, []resultFlowScenario{
		{
			Name: "two groups: removals settle later fixtures as walkovers", Options: integrationSeedOptions{
				Format: "round_robin", Entries: 8, GroupCount: 2},
			Rules: []resultFlowRule{
				{Bracket: "group_a", Round: 1, Play: resultFlowSilentAway},
				{Bracket: "group_a", Round: 1, Play: resultFlowAcceptAway},
				{Bracket: "group_b", Round: 1, Play: resultFlowRemoveBoth},
				{Bracket: "group_b", Round: 1, Play: resultFlowRejecterResponds},
				{Bracket: "group_a", Round: 2, Play: resultFlowCorrected},
				{Bracket: "group_a", Round: 3, Play: resultFlowOneResponder},
			},
		},
		{
			Name: "double round robin: three strikes ban the liar", Options: integrationSeedOptions{
				Format: "round_robin", Entries: 4, DoubleRoundRobin: true},
			// The seed-1 entry lies in each of its first three fixtures and loses
			// every review; the second leg then exercises R6 and R7.
			Choose: func(match progressionRemovalMatch, run resultFlowRun) (resultFlowPlay, bool) {
				liar := run.Competition.Entries[0]
				liarPlays := match.HomeEntryID == liar.ID || match.AwayEntryID == liar.ID
				switch {
				case liarPlays && run.Strikes[liar.UserID] < resultFlowStrikeBanThreshold && match.HomeEntryID == liar.ID:
					return resultFlowAcceptAway, true
				case liarPlays && run.Strikes[liar.UserID] < resultFlowStrikeBanThreshold:
					return resultFlowAcceptHome, true
				case match.Round == 4 && liarPlays:
					return resultFlowNoResponders, true
				case match.Round == 4:
					return resultFlowUnreported, true
				default:
					return resultFlowAgree, false
				}
			},
			Check: resultFlowCheckStrikeBan,
		},
	})
}

// resultFlowCheckStrikeBan checks that the liar's three strikes block new free
// and paid registrations, and that revoking one lifts the ban.
func resultFlowCheckStrikeBan(t *testing.T, h resultFlowHarness, run resultFlowRun) {
	t.Helper()
	liar := run.Competition.Entries[0].UserID
	control := run.Competition.Entries[1].UserID
	if run.Strikes[liar] != resultFlowStrikeBanThreshold || run.Strikes[control] != 0 {
		t.Fatalf("strikes = %v", run.Strikes)
	}
	feed := resultFlowListStrikes(t, h, "userId="+liar)
	if len(feed) != resultFlowStrikeBanThreshold {
		t.Fatalf("strike feed lists %d strikes, want %d", len(feed), resultFlowStrikeBanThreshold)
	}
	free := resultFlowInsertOpenCompetition(t, h.Pool, run.Competition, 0)
	if !resultFlowConductSuspended(t, h.Pool, free, liar) || resultFlowConductSuspended(t, h.Pool, free, control) {
		t.Fatal("the ban does not follow the strike threshold")
	}

	paid := resultFlowInsertOpenCompetition(t, h.Pool, run.Competition, 10_000)
	paymentID := resultFlowInsertPayment(t, h.Pool, liar, paid, "pending")
	if err := h.Server.completePayment(t.Context(), paymentID, resultFlowReceipt(), "20260928120000", []byte(`{}`)); err != nil {
		t.Fatalf("complete the banned player's payment: %v", err)
	}
	if refunded := resultReportsCount(t, h.Pool, `SELECT count(*) FROM payment_intents payment
		JOIN competition_entries entry ON entry.id=payment.entry_id
		JOIN payment_refunds refund ON refund.payment_id=payment.id
		WHERE payment.id=$1 AND payment.status='succeeded' AND entry.status='withdrawal_pending'
		  AND refund.reason_code='operations_adjustment' AND refund.mandatory
		  AND refund.player_note LIKE '%conduct strike limit%'`, paymentID); refunded != 1 {
		t.Fatal("a paid callback for a banned player did not turn into a refund")
	}

	admin := resultFlowInsertStaff(t, h.Pool, "admin", "admin")
	reason := `{"reason":"The first report was honest after a second look."}`
	revoked := resultFlowRevoke(t, h, admin, feed[0].ID, "revoke-strike-1", reason)
	if revoked.Code != http.StatusOK || !strings.Contains(revoked.Body.String(), `"revokedBy":"`+admin+`"`) {
		t.Fatalf("revocation: %d %s", revoked.Code, revoked.Body.String())
	}
	if replay := resultFlowRevoke(t, h, admin, feed[0].ID, "revoke-strike-1", reason); replay.Code != revoked.Code ||
		!sameJSON(t, revoked.Body.Bytes(), replay.Body.Bytes()) || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("revocation replay differs: %s / %s", revoked.Body.String(), replay.Body.String())
	}
	if again := resultFlowRevoke(t, h, admin, feed[0].ID, "revoke-strike-2", reason); again.Code != http.StatusConflict ||
		!strings.Contains(again.Body.String(), "strike_already_revoked") {
		t.Fatalf("a revoked strike was revoked again: %d %s", again.Code, again.Body.String())
	}
	if pushed := resultReportsCount(t, h.Pool, `SELECT count(*) FROM outbox_events WHERE event_type='player.strike_revoked'
		AND aggregate_type='user' AND aggregate_id=$1 AND payload->>'strikeId'=$2`, liar, feed[0].ID); pushed != 1 {
		t.Fatal("the revocation was not pushed once")
	}
	if active := resultFlowListStrikes(t, h, "userId="+liar); len(active) != resultFlowStrikeBanThreshold-1 {
		t.Fatalf("%d active strikes after the revocation", len(active))
	}
	if resultFlowConductSuspended(t, h.Pool, free, liar) {
		t.Fatal("revoking a strike below the threshold did not lift the ban")
	}
}

func resultFlowListStrikes(t *testing.T, h resultFlowHarness, query string) []playerStrikeView {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.Server.listPlayerStrikes(recorder, resultFlowStaffRequest(t, http.MethodGet, "/v1/admin/player-strikes?"+query, h.StaffID, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("strike feed: %d %s", recorder.Code, recorder.Body.String())
	}
	var page struct {
		Data []playerStrikeView `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page.Data
}

func resultFlowRevoke(t *testing.T, h resultFlowHarness, actorID, strikeID, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := resultFlowStaffRequest(t, http.MethodPost, "/v1/admin/player-strikes/"+strikeID+"/revocations", actorID, body)
	request.SetPathValue("id", strikeID)
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	h.Server.revokePlayerStrike(recorder, request)
	return recorder
}

// resultFlowInsertOpenCompetition opens a new competition of the same
// organization for registration; feeMinor > 0 makes it a paid one.
func resultFlowInsertOpenCompetition(t *testing.T, pool *pgxpool.Pool, seeded integrationCompetition, feeMinor int64) string {
	t.Helper()
	feePurpose := "none"
	if feeMinor > 0 {
		feePurpose = "administration"
	}
	suffix := strings.ToLower(rand.Text())[:12]
	var competitionID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO competitions(organization_id,game_id,name,slug,format,status,
		max_entries,entry_fee_minor,fee_purpose,registration_opens_at,registration_closes_at,starts_at,created_by)
		VALUES ($1,$2,$3,$4,'single_elimination','registration_open',16,$5,$6,now()-interval '1 hour',
		 now()+interval '1 hour',now()+interval '2 hours',$7) RETURNING id::text`,
		seeded.OrganizationID, seeded.GameID, "Open cup "+suffix, "open-"+suffix, feeMinor, feePurpose, seeded.OrganizerID).
		Scan(&competitionID); err != nil {
		t.Fatal(err)
	}
	return competitionID
}

func resultFlowConductSuspended(t *testing.T, pool *pgxpool.Pool, competitionID, userID string) bool {
	t.Helper()
	var gameAccountID string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM game_accounts WHERE user_id=$1`, userID).Scan(&gameAccountID); err != nil {
		t.Fatal(err)
	}
	result, err := loadCompetitionEligibility(t.Context(), pool, competitionID, userID, gameAccountID, time.Now().UTC(),
		resultFlowStrikeBanThreshold)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(result.Issues, func(issue eligibilityIssue) bool { return issue.Code == "conduct_suspended" })
}

func resultFlowInsertPayment(t *testing.T, pool *pgxpool.Pool, userID, competitionID, status string) string {
	t.Helper()
	var paymentID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO payment_intents(user_id,competition_id,game_account_id,
		entry_display_name,amount_minor,phone_e164,request_ip,idempotency_key,request_hash,status,
		merchant_request_id,checkout_request_id)
		VALUES ($1,$2,(SELECT id FROM game_accounts WHERE user_id=$1),'Flow player',10000,'254712345678','127.0.0.1',
		 'flow-payment-'||gen_random_uuid()::text,repeat('a',64),$3,'merchant-'||gen_random_uuid()::text,
		 'checkout-'||gen_random_uuid()::text) RETURNING id::text`, userID, competitionID, status).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	return paymentID
}

func resultFlowReceipt() string {
	return "RF" + strings.ToUpper(rand.Text())[:10]
}

// A reviewer who plays in the match, or belongs to the organizing
// organization, neither sees nor decides its review (D27).
func TestIntegrationResultFlowConflictedReviewers(t *testing.T) {
	h := resultFlowSetup(t)
	seeded := seedIntegrationCompetition(t, h.Pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	sides := resultReportsStart(t, h.Pool, readyIntegrationMatches(t, h.Pool, seeded.ID)[0])
	resultFlowReject(t, h, sides, "home", 2, 1)
	resultFlowRespond(t, h, sides.MatchID, sides.HomeUser)
	resultFlowRespond(t, h, sides.MatchID, sides.AwayUser)
	reviewID := resultFlowReviewID(t, h.Pool, sides.MatchID)

	resultFlowExec(t, h.Pool, `INSERT INTO platform_staff_roles(user_id,role) VALUES ($1,'reviewer')`, sides.HomeUser)
	organizerReviewer := resultFlowInsertStaff(t, h.Pool, "organizer-reviewer", "reviewer")
	resultFlowExec(t, h.Pool, `INSERT INTO organization_members(organization_id,user_id,role) VALUES ($1,$2,'analyst')`,
		seeded.OrganizationID, organizerReviewer)
	body := map[string]any{"expectedVersion": 1, "decision": "accept_home", "note": "Screenshots decide this match."}
	for name, reviewerID := range map[string]string{"player": sides.HomeUser, "organizer": organizerReviewer} {
		if queue := resultFlowListReviews(t, h, reviewerID, "status=queued"); slices.Contains(queue, reviewID) {
			t.Fatalf("the %s reviewer sees the review in the queue", name)
		}
		detail := resultReportsGet(t, h.Server.getResultReview, reviewerID, "id", reviewID)
		decision := resultFlowDecide(t, h, reviewerID, reviewID, "conflicted-decision-1", body)
		for route, recorder := range map[string]*httptest.ResponseRecorder{"detail": detail, "decision": decision} {
			if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "result_review_conflict") {
				t.Fatalf("the %s reviewer reached the %s: %d %s", name, route, recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), `"homeScore"`) {
				t.Fatalf("the %s refusal leaks a claim", route)
			}
		}
	}
	if queued := resultReportsCount(t, h.Pool, `SELECT count(*) FROM match_result_reviews
		WHERE id=$1 AND status='queued' AND version=1`, reviewID); queued != 1 {
		t.Fatal("a conflicted reviewer changed the review")
	}
	if queue := resultFlowListReviews(t, h, h.StaffID, "status=queued"); !slices.Contains(queue, reviewID) {
		t.Fatal("an unconflicted reviewer does not see the review")
	}
	decided := resultFlowDecodeReview(t, resultFlowDecide(t, h, h.StaffID, reviewID, "free-decision-1", body))
	if decided.Status != "decided" || decided.Decision == nil || decided.Decision.DecidedBy == nil ||
		*decided.Decision.DecidedBy != h.StaffID {
		t.Fatalf("unexpected decision %+v", decided)
	}
	if queue := resultFlowListReviews(t, h, h.StaffID, "status=decided"); !slices.Contains(queue, reviewID) {
		t.Fatal("the decided review is missing from the decided list")
	}
}

// Cancelling a competition ends every live match at any verification stage,
// with no removal, strike, rating or per-match push (T17).
func TestIntegrationResultFlowCompetitionCancellation(t *testing.T) {
	h := resultFlowSetup(t)
	seeded := seedIntegrationCompetition(t, h.Pool, integrationSeedOptions{Format: "single_elimination", Entries: 8, ThirdPlace: true})
	ready := readyIntegrationMatches(t, h.Pool, seeded.ID)
	if len(ready) != 4 {
		t.Fatalf("%d first-round matches are ready", len(ready))
	}
	started := make([]resultReportsSides, 0, len(ready))
	for _, matchID := range ready {
		started = append(started, resultReportsStart(t, h.Pool, matchID))
	}
	awaiting, responding, reviewing := started[1], started[2], started[3]
	resultReportsMustPost(t, h.Server, awaiting.HomeUser, awaiting.MatchID, "result-1", resultReportsBody(2, 1), "")
	resultFlowReject(t, h, responding, "home", 2, 1)
	resultFlowReject(t, h, reviewing, "home", 2, 1)
	resultFlowRespond(t, h, reviewing.MatchID, reviewing.HomeUser)
	resultFlowRespond(t, h, reviewing.MatchID, reviewing.AwayUser)
	reviewID := resultFlowReviewID(t, h.Pool, reviewing.MatchID)

	request := resultFlowStaffRequest(t, http.MethodPost, "/", seeded.OrganizerID, `{"status":"cancelled","reason":"Venue closed."}`)
	request = request.WithContext(context.WithValue(request.Context(), organizerContextKey{},
		organizerMembership{OrganizationID: seeded.OrganizationID, Role: organizer.RoleOwner}))
	request.SetPathValue("competitionId", seeded.ID)
	recorder := httptest.NewRecorder()
	h.Server.transitionOrganizerCompetition(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cancel competition: %d %s", recorder.Code, recorder.Body.String())
	}

	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT count(*) FROM matches WHERE competition_id=$1
			AND (state<>'cancelled' OR completion_reason<>'competition_cancelled')`, 0},
		{`SELECT count(*) FROM match_result_verifications WHERE competition_id=$1
			AND (phase<>'resolved' OR resolution<>'competition_cancelled' OR resolved_at IS NULL)`, 0},
		{`SELECT count(*) FROM match_result_verifications WHERE competition_id=$1`, 3},
		{`SELECT count(*) FROM match_result_reviews WHERE competition_id=$1
			AND status='closed' AND decision IS NULL AND decided_at IS NOT NULL`, 1},
		{`SELECT count(*) FROM competition_entry_removals WHERE competition_id=$1`, 0},
		{`SELECT count(*) FROM player_strikes strike JOIN matches match ON match.id=strike.match_id
			WHERE match.competition_id=$1`, 0},
		{`SELECT count(*) FROM competition_entries WHERE competition_id=$1 AND status='disqualified'`, 0},
		{`SELECT count(*) FROM audit_events WHERE action='competition.matches_cancelled' AND subject_id=$1::text
			AND jsonb_array_length(after_state->'matchIds')=8`, 1},
	} {
		if got := resultReportsCount(t, h.Pool, check.query, seeded.ID); got != check.want {
			t.Fatalf("%s = %d, want %d", check.query, got, check.want)
		}
	}
	if pushed := resultReportsCount(t, h.Pool, `SELECT count(*) FROM outbox_events WHERE event_type IN
		('match.cancelled','match.forfeited','match.result_confirmed','competition.entry_removed','result.review_decided')`); pushed != 0 {
		t.Fatalf("cancellation sent %d per-match pushes", pushed)
	}
	if rated := resultReportsCount(t, h.Pool, `SELECT count(*) FROM player_game_ratings`); rated != 0 {
		t.Fatal("cancellation touched ratings")
	}
	body := map[string]any{"expectedVersion": 1, "decision": "accept_home", "note": "Screenshots decide this match."}
	if closed := resultFlowDecide(t, h, h.StaffID, reviewID, "closed-decision-1", body); closed.Code != http.StatusConflict ||
		!strings.Contains(closed.Body.String(), "competition_closed") {
		t.Fatalf("a closed review was decided: %d %s", closed.Code, closed.Body.String())
	}
	for _, sides := range started {
		resultReportsProcess(t, h.Server, sides)
	}
	if changed := resultReportsCount(t, h.Pool, `SELECT count(*) FROM matches WHERE competition_id=$1
		AND (state<>'cancelled' OR completion_reason<>'competition_cancelled')`, seeded.ID); changed != 0 {
		t.Fatal("the worker acted on a cancelled competition")
	}
}

// resultFlowRace starts both functions together and fails if they do not both
// finish in time, which is how a lock-order deadlock between them shows.
func resultFlowRace(t *testing.T, first, second func()) {
	t.Helper()
	start, done := make(chan struct{}), make(chan struct{})
	var group sync.WaitGroup
	for _, run := range []func(){first, second} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			run()
		}()
	}
	close(start)
	go func() {
		group.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the racing transactions did not finish")
	}
}

// resultFlowSilentAwayDue has the away entry of the first semifinal reject
// home's result and then send no screenshot, while home sends theirs, and lets
// the screenshot window expire: the worker is due to remove the away entry.
func resultFlowSilentAwayDue(t *testing.T, h resultFlowHarness) (integrationCompetition, resultReportsSides) {
	t.Helper()
	seeded := seedIntegrationCompetition(t, h.Pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	sides := resultReportsStart(t, h.Pool, readyIntegrationMatches(t, h.Pool, seeded.ID)[0])
	resultFlowReject(t, h, sides, "home", 2, 1)
	resultFlowRespond(t, h, sides.MatchID, sides.HomeUser)
	shiftVerificationClock(t, h.Pool, sides.MatchID, 11*time.Minute)
	return seeded, sides
}

func TestIntegrationResultFlowWithdrawalRacesRemoval(t *testing.T) {
	h := resultFlowSetup(t)
	_, sides := resultFlowSilentAwayDue(t, h)
	candidate := matchResultVerificationCandidate{MatchID: sides.MatchID, CompetitionID: sides.CompetitionID}
	var processErr error
	withdrawal := httptest.NewRecorder()
	resultFlowRace(t, func() {
		processErr = h.Server.processResultVerificationCandidate(context.WithoutCancel(t.Context()), candidate)
	}, func() {
		request := resultFlowStaffRequest(t, http.MethodDelete, "/", sides.AwayUser, "")
		request.SetPathValue("id", sides.CompetitionID)
		h.Server.withdrawRegistration(withdrawal, request)
	})
	if processErr != nil || withdrawal.Code >= http.StatusInternalServerError {
		t.Fatalf("race failed: process=%v withdrawal=%d %s", processErr, withdrawal.Code, withdrawal.Body.String())
	}
	if removed := resultReportsCount(t, h.Pool, `SELECT count(*) FROM competition_entries WHERE id=$1 AND status='disqualified'`,
		sides.AwayEntry); removed != 1 {
		t.Fatal("the silent entry was not removed")
	}
}

func TestIntegrationResultFlowPaymentCallbackRacesRemoval(t *testing.T) {
	h := resultFlowSetup(t)
	seeded, sides := resultFlowSilentAwayDue(t, h)
	paymentID := resultFlowInsertPayment(t, h.Pool, sides.AwayUser, seeded.ID, "callback_received")
	receipt := resultFlowReceipt()
	candidate := matchResultVerificationCandidate{MatchID: sides.MatchID, CompetitionID: sides.CompetitionID}
	var processErr, paymentErr error
	resultFlowRace(t, func() {
		processErr = h.Server.processResultVerificationCandidate(context.WithoutCancel(t.Context()), candidate)
	}, func() {
		paymentErr = h.Server.completePayment(context.WithoutCancel(t.Context()), paymentID, receipt, "20260928120000", []byte(`{}`))
	})
	if processErr != nil || paymentErr != nil {
		t.Fatalf("race failed: process=%v payment=%v", processErr, paymentErr)
	}
	// A duplicate callback for the now succeeded payment changes nothing.
	if err := h.Server.completePayment(t.Context(), paymentID, receipt, "20260928120000", []byte(`{}`)); err != nil {
		t.Fatalf("duplicate callback: %v", err)
	}
	if kept := resultReportsCount(t, h.Pool, `SELECT count(*) FROM competition_entries entry
		JOIN payment_intents payment ON payment.entry_id=entry.id
		WHERE entry.id=$1 AND entry.status='disqualified' AND payment.id=$2 AND payment.status='succeeded'`,
		sides.AwayEntry, paymentID); kept != 1 {
		t.Fatal("a paid callback revived or lost the removed entry")
	}
}
