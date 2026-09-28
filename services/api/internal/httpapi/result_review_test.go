package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

const (
	resultReviewStaffUser   = "70000000-0000-4000-8000-000000000001"
	resultReviewOtherStaff  = "70000000-0000-4000-8000-000000000002"
	resultReviewHomeSecond  = "20000000-0000-4000-8000-000000000003"
	resultReviewID          = "60000000-0000-4000-8000-000000000001"
	resultReviewCompetition = "40000000-0000-4000-8000-000000000001"
)

// resultReviewQueued is T9's outcome: home claimed 2-1 and away 1-2, both
// entries responded with the same claims, and the review is queued. The home
// entry's final report comes from a second member of the home team.
func resultReviewQueued() (verificationState, lockedReview) {
	state := resultReportsAwaitingResponses()
	state.Verification.Phase = "in_review"
	state.Verification.Version = 3
	state.Reports = append(state.Reports,
		resultReportsReport(resultReportsHomeEntry, resultReviewHomeSecond, "final", resultReportsClaim(2, 1)),
		resultReportsReport(resultReportsAwayEntry, resultReportsAwayUser, "final", resultReportsClaim(1, 2)))
	review := lockedReview{ID: resultReviewID, MatchID: state.Match.ID,
		Status: "queued", Reason: "reports_differ", Version: 1}
	return state, review
}

func resultReviewStaff() reviewActor {
	staff := resultReviewStaffUser
	return reviewActor{Kind: "staff", UserID: &staff, RequestID: "request-1"}
}

func resultReviewSystem() reviewActor {
	return reviewActor{Kind: "system", DeciderRef: "result-model-2026-09"}
}

func resultReviewInput(decision string, strikes ...string) reviewDecisionInput {
	return reviewDecisionInput{ExpectedVersion: 1, Decision: decision, StrikeUserIDs: strikes,
		Note: "Screenshots show the away claim is wrong."}
}

func resultReviewCorrected(home, away int, strikes ...string) reviewDecisionInput {
	input := resultReviewInput("corrected_score", strikes...)
	input.CorrectedScore = &reviewScoreInput{HomeScore: home, AwayScore: away}
	return input
}

func TestPlanReviewDecisionOutcomes(t *testing.T) {
	staff, homeEntry, awayEntry := resultReviewStaffUser, resultReportsHomeEntry, resultReportsAwayEntry
	tests := []struct {
		name         string
		actor        reviewActor
		input        reviewDecisionInput
		wantState    string
		wantWinner   *string
		wantScore    [2]int
		wantAuthor   string
		wantDecider  *string
		wantRemoved  []string
		wantRatings  bool
		wantNoResult bool
	}{
		{name: "staff accepts home", actor: resultReviewStaff(), input: resultReviewInput("accept_home"),
			wantState: "completed", wantWinner: &homeEntry, wantScore: [2]int{2, 1}, wantAuthor: resultReviewHomeSecond,
			wantDecider: &staff, wantRemoved: []string{}, wantRatings: true},
		{name: "staff accepts away", actor: resultReviewStaff(), input: resultReviewInput("accept_away"),
			wantState: "completed", wantWinner: &awayEntry, wantScore: [2]int{1, 2}, wantAuthor: resultReportsAwayUser,
			wantDecider: &staff, wantRemoved: []string{}, wantRatings: true},
		{name: "staff corrects the score", actor: resultReviewStaff(), input: resultReviewCorrected(3, 0),
			wantState: "completed", wantWinner: &homeEntry, wantScore: [2]int{3, 0}, wantAuthor: staff,
			wantDecider: &staff, wantRemoved: []string{}, wantRatings: true},
		{name: "staff removes both", actor: resultReviewStaff(), input: resultReviewInput("remove_both"),
			wantState: "cancelled", wantRemoved: []string{homeEntry, awayEntry}, wantNoResult: true},
		{name: "system accepts home", actor: resultReviewSystem(), input: resultReviewInput("accept_home"),
			wantState: "completed", wantWinner: &homeEntry, wantScore: [2]int{2, 1}, wantAuthor: resultReviewHomeSecond,
			wantRemoved: []string{}, wantRatings: true},
		{name: "system accepts away", actor: resultReviewSystem(), input: resultReviewInput("accept_away"),
			wantState: "completed", wantWinner: &awayEntry, wantScore: [2]int{1, 2}, wantAuthor: resultReportsAwayUser,
			wantRemoved: []string{}, wantRatings: true},
		{name: "system removes both", actor: resultReviewSystem(), input: resultReviewInput("remove_both"),
			wantState: "cancelled", wantRemoved: []string{homeEntry, awayEntry}, wantNoResult: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, review := resultReviewQueued()
			resolution, strikes, rejection := planReviewDecision(state, review, test.actor, test.input)
			if rejection != nil {
				t.Fatalf("decision rejected: %+v", rejection)
			}
			if len(strikes) != 0 {
				t.Fatalf("unrequested strikes %v", strikes)
			}
			if resolution.FinalState != test.wantState || !sameOptionalString(resolution.WinnerEntryID, test.wantWinner) ||
				resolution.CompletionReason != "platform_review" || resolution.Cause != progressionCausePlatformReview ||
				resolution.Resolution != "platform_review" || resolution.ApplyRatings != test.wantRatings ||
				!slices.Equal(resolution.RemoveEntryIDs, test.wantRemoved) {
				t.Fatalf("unexpected resolution %+v", resolution)
			}
			if err := validateMatchResolution(state.Match, resolution); err != nil {
				t.Fatalf("the finalizer would refuse the plan: %v", err)
			}
			if test.wantNoResult {
				if resolution.Claim != nil || resolution.RemovalReason != "platform_review" || resolution.ClaimAuthorID != nil {
					t.Fatalf("a removal carries a result: %+v", resolution)
				}
				return
			}
			if resolution.Claim == nil || resolution.Claim.HomeScore != test.wantScore[0] || resolution.Claim.AwayScore != test.wantScore[1] ||
				resolution.Origin != "platform_review" || resolution.ClaimAuthorID == nil || *resolution.ClaimAuthorID != test.wantAuthor ||
				!sameOptionalString(resolution.ConfirmerID, test.wantDecider) {
				t.Fatalf("unexpected canonical result %+v (author %v, confirmer %v)", resolution.Claim,
					resolution.ClaimAuthorID, resolution.ConfirmerID)
			}
		})
	}
}

func TestPlanReviewDecisionRejections(t *testing.T) {
	decided, closed, stale := "decided", "closed", 2
	inconsistent := func(state *verificationState, _ *lockedReview) { state.Verification.Phase = "awaiting_responses" }
	tests := []struct {
		name   string
		actor  reviewActor
		input  reviewDecisionInput
		mutate func(*verificationState, *lockedReview)
		status int
		code   string
	}{
		{"decided review", resultReviewStaff(), resultReviewInput("accept_home"),
			func(_ *verificationState, review *lockedReview) { review.Status = decided }, http.StatusConflict, "review_already_decided"},
		{"closed review", resultReviewStaff(), resultReviewInput("accept_home"),
			func(_ *verificationState, review *lockedReview) { review.Status = closed }, http.StatusConflict, "review_already_decided"},
		{"stale version", resultReviewStaff(), resultReviewInput("accept_home"),
			func(_ *verificationState, review *lockedReview) { review.Version = stale }, http.StatusConflict, "review_version_conflict"},
		{"system cannot correct", resultReviewSystem(), resultReviewCorrected(3, 0), nil,
			http.StatusBadRequest, "invalid_review_decision"},
		{"system cannot strike", resultReviewSystem(), resultReviewInput("accept_home", resultReportsAwayUser), nil,
			http.StatusBadRequest, "invalid_strike_user"},
		{"elimination tie without penalties", resultReviewStaff(), resultReviewCorrected(1, 1), nil,
			http.StatusBadRequest, "invalid_score"},
		{"corrected score above the range", resultReviewStaff(), resultReviewCorrected(100, 0), nil,
			http.StatusBadRequest, "invalid_score"},
		{"review of another match", resultReviewStaff(), resultReviewInput("accept_home"),
			func(_ *verificationState, review *lockedReview) { review.MatchID = resultReportsOutsider },
			http.StatusInternalServerError, "internal_error"},
		{"verification not in review", resultReviewStaff(), resultReviewInput("accept_home"), inconsistent,
			http.StatusInternalServerError, "internal_error"},
		{"staff without a user", reviewActor{Kind: "staff"}, resultReviewInput("accept_home"), nil,
			http.StatusInternalServerError, "internal_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, review := resultReviewQueued()
			if test.mutate != nil {
				test.mutate(&state, &review)
			}
			_, _, rejection := planReviewDecision(state, review, test.actor, test.input)
			if rejection == nil || rejection.Status != test.status || rejection.Code != test.code {
				t.Fatalf("got %+v, want %d %s", rejection, test.status, test.code)
			}
			// An invariant keeps its cause for the log; a refusal has none.
			if invariant := test.status == http.StatusInternalServerError; invariant !=
				errors.Is(rejection.Cause, errResultVerificationInvariant) {
				t.Fatalf("cause = %v for a %d rejection", rejection.Cause, test.status)
			}
		})
	}
}

func TestPlanReviewDecisionStrikesOnlyRejectedCurrentClaims(t *testing.T) {
	homeInitial, homeFinal, away := resultReportsHomeUser, resultReviewHomeSecond, resultReportsAwayUser
	tests := []struct {
		name    string
		input   reviewDecisionInput
		want    []string
		allowed bool
	}{
		{"accepting home strikes the away reporter", resultReviewInput("accept_home", away), []string{away}, true},
		{"the accepted side cannot be struck", resultReviewInput("accept_home", homeFinal), nil, false},
		{"an initial-only reporter of the accepted entry cannot be struck", resultReviewInput("accept_home", homeInitial), nil, false},
		{"accepting away strikes the home final reporter", resultReviewInput("accept_away", homeFinal), []string{homeFinal}, true},
		{"a replaced initial claim is not a current claim", resultReviewInput("accept_away", homeInitial), nil, false},
		{"a correction equal to the home claim strikes only away", resultReviewCorrected(2, 1, away), []string{away}, true},
		{"a correction equal to the home claim spares home", resultReviewCorrected(2, 1, homeFinal), nil, false},
		{"a correction matching neither claim strikes both", resultReviewCorrected(3, 0, homeFinal, away),
			[]string{homeFinal, away}, true},
		{"removing both may strike either reporter", resultReviewInput("remove_both", away), []string{away}, true},
		{"removing both may strike both reporters", resultReviewInput("remove_both", away, homeFinal),
			[]string{homeFinal, away}, true},
		{"a player who never reported cannot be struck", resultReviewInput("remove_both", resultReportsOutsider), nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, review := resultReviewQueued()
			_, strikes, rejection := planReviewDecision(state, review, resultReviewStaff(), test.input)
			if !test.allowed {
				if rejection == nil || rejection.Code != "invalid_strike_user" || rejection.Status != http.StatusBadRequest {
					t.Fatalf("strike on %v was accepted: %+v", test.input.StrikeUserIDs, rejection)
				}
				return
			}
			if rejection != nil {
				t.Fatalf("strike rejected: %+v", rejection)
			}
			want := slices.Clone(test.want)
			slices.Sort(want)
			if !slices.Equal(strikes, want) {
				t.Fatalf("strikes %v, want %v", strikes, want)
			}
		})
	}
}

// T16 queues a match where one entry never responded: its initial report is
// then its current claim and the only one that can be struck.
func TestPlanReviewDecisionUsesInitialClaimOfANonResponder(t *testing.T) {
	state := resultReportsAwaitingResponses()
	state.Verification.Phase = "in_review"
	state.Reports = append(state.Reports,
		resultReportsReport(resultReportsHomeEntry, resultReportsHomeUser, "final", resultReportsClaim(2, 1)))
	review := lockedReview{ID: resultReviewID, MatchID: state.Match.ID, Status: "queued", Reason: "evidence_unavailable", Version: 1}
	resolution, strikes, rejection := planReviewDecision(state, review, resultReviewStaff(),
		resultReviewInput("accept_home", resultReportsAwayUser))
	if rejection != nil || resolution.Claim == nil || resolution.Claim.HomeScore != 2 ||
		!slices.Equal(strikes, []string{resultReportsAwayUser}) {
		t.Fatalf("resolution=%+v strikes=%v rejection=%+v", resolution, strikes, rejection)
	}
}

func TestReviewDecisionInputNormalization(t *testing.T) {
	input := reviewDecisionInput{ExpectedVersion: 2, Decision: " ACCEPT_Home ", Note: "  Screenshot proves home won.  ",
		StrikeUserIDs: []string{" " + strings.ToUpper(resultReportsAwayUser) + " "}}
	if rejection := input.normalize(); rejection != nil {
		t.Fatalf("valid input rejected: %+v", rejection)
	}
	if input.Decision != "accept_home" || input.Note != "Screenshot proves home won." ||
		!slices.Equal(input.StrikeUserIDs, []string{resultReportsAwayUser}) {
		t.Fatalf("input not canonicalized: %+v", input)
	}
	withScore := func(input reviewDecisionInput) reviewDecisionInput {
		input.CorrectedScore = &reviewScoreInput{HomeScore: 1}
		return input
	}
	valid := resultReviewInput("accept_home")
	tests := []struct {
		name  string
		input reviewDecisionInput
		code  string
	}{
		{"unknown decision", resultReviewInput("replay"), "invalid_review_decision"},
		{"corrected score missing", resultReviewInput("corrected_score"), "invalid_review_decision"},
		{"corrected score on an acceptance", withScore(valid), "invalid_review_decision"},
		{"note too short", reviewDecisionInput{ExpectedVersion: 1, Decision: "remove_both", Note: "123456789"}, "invalid_review_decision"},
		{"note too long", reviewDecisionInput{ExpectedVersion: 1, Decision: "remove_both", Note: strings.Repeat("x", 2001)}, "invalid_review_decision"},
		{"no version", reviewDecisionInput{Decision: "remove_both", Note: "Both claims are false."}, "invalid_review_decision"},
		{"three strikes", resultReviewInput("remove_both", resultReportsHomeUser, resultReportsAwayUser, resultReviewHomeSecond), "invalid_strike_user"},
		{"duplicate strike", resultReviewInput("remove_both", resultReportsAwayUser, strings.ToUpper(resultReportsAwayUser)), "invalid_strike_user"},
		{"malformed strike", resultReviewInput("remove_both", "not-a-user"), "invalid_strike_user"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rejection := test.input.normalize()
			if rejection == nil || rejection.Code != test.code || rejection.Status != http.StatusBadRequest {
				t.Fatalf("got %+v, want %s", rejection, test.code)
			}
		})
	}
	multibyte := reviewDecisionInput{ExpectedVersion: 1, Decision: "remove_both", Note: strings.Repeat("é", 10)}
	if rejection := multibyte.normalize(); rejection != nil {
		t.Fatalf("a ten-character note was measured in bytes: %+v", rejection)
	}
}

func TestReviewActorValidationAndSystemIdentity(t *testing.T) {
	staff, empty := resultReviewStaffUser, ""
	for name, actor := range map[string]reviewActor{
		"staff without user":      {Kind: "staff"},
		"staff with empty user":   {Kind: "staff", UserID: &empty},
		"staff with a system ref": {Kind: "staff", UserID: &staff, DeciderRef: "model"},
		"system with a user":      {Kind: "system", UserID: &staff, DeciderRef: "model"},
		"system without ref":      {Kind: "system"},
		"system with long ref":    {Kind: "system", DeciderRef: strings.Repeat("r", 129)},
		"player":                  {Kind: "player", UserID: &staff},
	} {
		if err := actor.validate(); !errors.Is(err, errResultVerificationInvariant) {
			t.Fatalf("%s was accepted: %v", name, err)
		}
	}
	system := resultReviewSystem().resolutionActor()
	if system.Kind != "system" || system.UserID != nil || system.RequestID != "match-review-system:result-model-2026-09" {
		t.Fatalf("unexpected system identity %+v", system)
	}
	if kind, userID, err := system.removalActor(); err != nil || kind != "system" || userID != nil {
		t.Fatalf("a system decision cannot remove entries: %s %v %v", kind, userID, err)
	}
	staffActor := resultReviewStaff().resolutionActor()
	if staffActor.Kind != "staff" || staffActor.UserID == nil || *staffActor.UserID != staff || staffActor.RequestID != "request-1" {
		t.Fatalf("unexpected staff identity %+v", staffActor)
	}
}

func TestResultReviewRoutesRequireAuthentication(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")
	mux := http.NewServeMux()
	server.registerResultReviewRoutes(mux)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/admin/result-reviews"},
		{http.MethodGet, "/v1/admin/result-reviews/" + resultReviewID},
		{http.MethodPost, "/v1/admin/result-reviews/" + resultReviewID + "/decisions"},
		{http.MethodGet, "/v1/admin/player-strikes"},
		{http.MethodPost, "/v1/admin/player-strikes/" + resultReviewID + "/revocations"},
	} {
		for name, handler := range map[string]http.Handler{"registrar": mux, "server": server.http.Handler} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`)))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s %s is not auth protected: %d", name, route.method, route.path, recorder.Code)
			}
		}
	}
}

func TestResultReviewPermissions(t *testing.T) {
	for _, test := range []struct {
		role           string
		review, revoke bool
	}{
		{"admin", true, true},
		{"reviewer", true, false},
		{"support", false, false},
		{"owner", false, false},
	} {
		if got := platformRoleCan(test.role, platformResultReviewManage); got != test.review {
			t.Fatalf("%s result review = %v", test.role, got)
		}
		if got := platformRoleCan(test.role, platformPlayerStrikeRevoke); got != test.revoke {
			t.Fatalf("%s strike revoke = %v", test.role, got)
		}
	}
	routes := readSourceFile(t, "result_review_routes.go")
	if strings.Count(routes, "s.platformRoute(review, ") != 4 ||
		strings.Count(routes, "s.platformRoute(platformPlayerStrikeRevoke, s.revokePlayerStrike)") != 1 ||
		!strings.Contains(routes, "review := platformResultReviewManage") {
		t.Fatal("review routes must need result_review.manage and only revocation player_strike.revoke")
	}
	assertFileContains(t, "server.go", "s.registerResultReviewRoutes(mux)")
}

func resultReviewRequest(actorID, target string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	return request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: actorID}))
}

func resultReviewCursor(t *testing.T, server *Server, kind, actorID, scope string) string {
	t.Helper()
	now := time.Now().UTC()
	token, err := encodePublicCursor(publicCursor{Kind: kind, ExpiresAt: now.Add(time.Hour).Unix(), Query: actorID,
		Scope: scope, SortTime: now.UnixNano(), ID: resultReviewID}, server.config.AccessTokenSecret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestResultReviewCursorIsBoundToOperatorAndFilters(t *testing.T) {
	server := &Server{config: config.Config{AccessTokenSecret: "result-review-cursor-test-secret"}}
	filters := resultReviewFilters{Status: "decided", CompetitionID: resultReviewCompetition}
	token := resultReviewCursor(t, server, resultReviewCursorKind, resultReviewStaffUser, filters.scope())
	query := "/v1/admin/result-reviews?status=decided&competitionId=" + resultReviewCompetition + "&limit=100&cursor=" + token
	recorder := httptest.NewRecorder()
	got, limit, cursor, ok := server.resultReviewPageInput(recorder, resultReviewRequest(resultReviewStaffUser, query))
	if !ok || limit != 100 || cursor == nil || got != filters {
		t.Fatalf("valid cursor rejected: %+v %d %+v %s", got, limit, cursor, recorder.Body.String())
	}
	strikeToken := resultReviewCursor(t, server, playerStrikeCursorKind, resultReviewStaffUser, filters.scope())
	for name, request := range map[string]*http.Request{
		"operator":    resultReviewRequest(resultReviewOtherStaff, query),
		"status":      resultReviewRequest(resultReviewStaffUser, strings.Replace(query, "status=decided", "status=queued", 1)),
		"competition": resultReviewRequest(resultReviewStaffUser, strings.Replace(query, "competitionId="+resultReviewCompetition, "competitionId=", 1)),
		"kind":        resultReviewRequest(resultReviewStaffUser, strings.Replace(query, token, strikeToken, 1)),
	} {
		recorder = httptest.NewRecorder()
		if _, _, _, ok := server.resultReviewPageInput(recorder, request); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("cursor escaped its %s binding", name)
		}
	}
	for _, query := range []string{"status=open", "competitionId=nope", "limit=101", "limit=0"} {
		recorder = httptest.NewRecorder()
		if _, _, _, ok := server.resultReviewPageInput(recorder, resultReviewRequest(resultReviewStaffUser,
			"/v1/admin/result-reviews?"+query)); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid queue filter %q accepted", query)
		}
	}
	recorder = httptest.NewRecorder()
	if got, limit, _, ok := server.resultReviewPageInput(recorder, resultReviewRequest(resultReviewStaffUser,
		"/v1/admin/result-reviews")); !ok || got.Status != "queued" || limit != 50 {
		t.Fatalf("queue defaults = %+v %d %v", got, limit, ok)
	}
}

func TestPlayerStrikeFeedCursorIsBoundToOperatorAndFilters(t *testing.T) {
	server := &Server{config: config.Config{AccessTokenSecret: "player-strike-cursor-test-secret"}}
	filters := playerStrikeFilters{UserID: resultReportsAwayUser, Status: "all"}
	token := resultReviewCursor(t, server, playerStrikeCursorKind, resultReviewStaffUser, filters.scope())
	query := "/v1/admin/player-strikes?status=all&userId=" + resultReportsAwayUser + "&cursor=" + token
	recorder := httptest.NewRecorder()
	got, _, cursor, ok := server.playerStrikePageInput(recorder, resultReviewRequest(resultReviewStaffUser, query))
	if !ok || cursor == nil || got != filters {
		t.Fatalf("valid cursor rejected: %+v %+v %s", got, cursor, recorder.Body.String())
	}
	for name, request := range map[string]*http.Request{
		"operator": resultReviewRequest(resultReviewOtherStaff, query),
		"status":   resultReviewRequest(resultReviewStaffUser, strings.Replace(query, "status=all", "status=revoked", 1)),
		"user":     resultReviewRequest(resultReviewStaffUser, strings.Replace(query, resultReportsAwayUser, resultReportsHomeUser, 1)),
	} {
		recorder = httptest.NewRecorder()
		if _, _, _, ok := server.playerStrikePageInput(recorder, request); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("strike cursor escaped its %s binding", name)
		}
	}
	for _, query := range []string{"status=pending", "userId=nope"} {
		recorder = httptest.NewRecorder()
		if _, _, _, ok := server.playerStrikePageInput(recorder, resultReviewRequest(resultReviewStaffUser,
			"/v1/admin/player-strikes?"+query)); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid strike filter %q accepted", query)
		}
	}
	recorder = httptest.NewRecorder()
	if got, _, _, ok := server.playerStrikePageInput(recorder, resultReviewRequest(resultReviewStaffUser,
		"/v1/admin/player-strikes")); !ok || got.Status != "active" || got.UserID != "" {
		t.Fatalf("strike feed defaults = %+v %v", got, ok)
	}
}

func TestResultReviewScopesAreActorBound(t *testing.T) {
	scopes := map[string]bool{
		resultReviewDecisionScope(resultReviewStaffUser, resultReviewID):   true,
		resultReviewDecisionScope(resultReviewOtherStaff, resultReviewID):  true,
		playerStrikeRevocationScope(resultReviewStaffUser, resultReviewID): true,
	}
	if len(scopes) != 3 {
		t.Fatalf("scopes collide: %v", scopes)
	}
	for scope := range scopes {
		if !strings.Contains(scope, resultReviewID) {
			t.Fatalf("scope %s is not bound to its resource", scope)
		}
	}
}

func TestResultReviewDecisionLockOrder(t *testing.T) {
	locks := resultReportsFunctionSource(t, "result_review_decision.go", "func lockResultReviewState(")
	assertOrder(t, "lockResultReviewState", locks,
		"FROM match_result_reviews WHERE id=$1`", "lockCompetitionProgressionGate(", "lockResultMatch(",
		"competitionClosed()", "lockResultVerification(", "FROM match_result_reviews WHERE id=$1 FOR UPDATE",
		"lockResultReports(")
	decide := resultReportsFunctionSource(t, "result_review_decision.go", "func decideResultReviewInTx(")
	assertOrder(t, "decideResultReviewInTx", decide,
		"actor.validate()", "input.normalize()", "lockResultReviewState(", "resultReviewConflicted(",
		"planReviewDecision(", "finalizeMatchResolution(", "decider, false)", "markResultReviewDecided(",
		"recordPlayerStrikes(", `appendPlatformAuditContext(ctx, tx, decider.RequestID, decider.UserID, "result.review_decided"`,
		`insertProgressionOutbox(ctx, tx, "match", state.Match.ID, "result.review_decided"`)
	assertFileContains(t, "result_review_decision.go",
		"WHERE id=$1 AND status='queued' AND version=$2 RETURNING version",
		"ON CONFLICT (review_id,user_id) DO NOTHING",
		`"user", userID, "player.strike_recorded"`)
	handler := resultReportsFunctionSource(t, "result_review_handlers.go", "func (s *Server) decideResultReview(")
	assertOrder(t, "decideResultReview", handler,
		"readIdempotencyKey(", "uuidPattern.MatchString", "decodeJSON(", "input.normalize()", "hashRequest(input)",
		"beginIdempotentRequest(", "decideResultReviewInTx(", "loadResultReviewDetail(ctx, tx,",
		"finishIdempotentRequest(", "tx.Commit(ctx)",
		"s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), outcome.CompetitionID)")
}

func TestResultReviewQueriesExcludeConflictedStaff(t *testing.T) {
	for _, declaration := range []string{"func queryResultReviewPage(", "func queryResultReviewHead("} {
		source := resultReportsFunctionSource(t, "result_review_handlers.go", declaration)
		if !strings.Contains(source, `resultReviewConflictClause("m", `) {
			t.Fatalf("%s does not apply the conflict clause", declaration)
		}
	}
	page := resultReportsFunctionSource(t, "result_review_handlers.go", "func queryResultReviewPage(")
	if !strings.Contains(page, "AND NOT `+resultReviewConflictClause(") {
		t.Fatal("the queue does not omit conflicted reviews")
	}
	decision := resultReportsFunctionSource(t, "result_review_decision.go", "func resultReviewConflicted(")
	if !strings.Contains(decision, `resultReviewConflictClause("m", "$2")`) {
		t.Fatal("the decision does not re-check the conflict under the locks")
	}
	if !strings.Contains(resultReviewFrom, "JOIN matches m ON m.id=review.match_id") {
		t.Fatal("review queries must join the match as m for the conflict clause")
	}
}

// The strike feed carries the decider's review note, so it applies the same
// conflict-of-interest rule as the queue, and an admin can never revoke a
// strike they are conflicted on (D27).
func TestPlayerStrikeRoutesExcludeConflictedStaff(t *testing.T) {
	feed := resultReportsFunctionSource(t, "player_strike_handlers.go", "func (s *Server) listPlayerStrikes(")
	for _, fragment := range []string{"JOIN matches m ON m.id=strike.match_id", "strike.user_id<>$6::uuid",
		"AND NOT `+resultReviewConflictClause(\"m\", \"$6::uuid\")"} {
		if !strings.Contains(feed, fragment) {
			t.Errorf("the strike feed does not contain %q", fragment)
		}
	}
	revoke := resultReportsFunctionSource(t, "player_strike_handlers.go", "func (s *Server) revokePlayerStrike(")
	assertOrder(t, "revokePlayerStrike", revoke,
		"WHERE strike.id=$1 FOR UPDATE", "current.UserID == actorID", "resultReviewConflicted(ctx, tx, current.MatchID, actorID)",
		`"result_review_conflict"`, "current.RevokedAt != nil", "UPDATE player_strikes strike")
}

func TestResultReviewScanTargetsMatchTheirColumns(t *testing.T) {
	var summary resultReviewSummary
	if columns, targets := len(strings.Split(resultReviewSummaryColumns, ",")), len(resultReviewSummaryTargets(&summary)); columns != targets {
		t.Fatalf("summary selects %d columns into %d targets", columns, targets)
	}
	if columns := len(strings.Split(playerStrikeColumns, ",")); columns != 12 {
		t.Fatalf("strike view selects %d columns", columns)
	}
	if _, err := scanPlayerStrike(countOnlyScanner{t, 12}); !errors.Is(err, errCountOnlyScanner) {
		t.Fatalf("unexpected strike scan error: %v", err)
	}
}

func TestResultReviewDetailDecisionShadowsTheSummaryString(t *testing.T) {
	decision := "accept_home"
	detail := resultReviewDetail{
		resultReviewSummary:   resultReviewSummary{ID: resultReviewID, Status: "decided", Decision: &decision},
		Decision:              &resultReviewDecisionView{Decision: decision, DeciderKind: "staff"},
		ResponseWindowUploads: []resultReviewWindowUploadView{}, Strikes: []playerStrikeView{},
		ActiveStrikeCounts: map[string]int{},
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(decoded["decision"]), `{"decision":"accept_home"`) || string(decoded["status"]) != `"decided"` {
		t.Fatalf("unexpected detail JSON: %s", raw)
	}
}
