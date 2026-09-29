package httpapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// A writer that meets a preflight condition again words it exactly as the
// preflight does, so the client sees one issue per condition.
func TestEntryRecheckIssuesMatchThePreflight(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	withdrawn, otherGame := "withdrawn", "another-game"
	tests := []struct {
		issue eligibilityIssue
		set   func(*competitionEligibilityFacts)
	}{
		{entryIssueProfileIncomplete, func(facts *competitionEligibilityFacts) { facts.ProfileComplete = false }},
		{entryIssueRegistrationNotReusable, func(facts *competitionEligibilityFacts) { facts.EntryStatus = &withdrawn }},
		{entryIssueCompetitionFull, func(facts *competitionEligibilityFacts) { facts.OccupiedEntries = facts.MaxEntries }},
		{entryIssueGameAccountRequired, func(facts *competitionEligibilityFacts) { facts.GameAccountID = nil }},
		{entryIssueGameAccountMismatch, func(facts *competitionEligibilityFacts) { facts.GameAccountGameID = &otherGame }},
	}
	for _, test := range tests {
		facts := eligibilityStrikesFacts(now, 0, 0)
		test.set(&facts)
		result := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
		if !slices.Contains(result.Issues, test.issue) {
			t.Errorf("the preflight words %s differently: %+v", test.issue.Code, result.Issues)
		}
	}
}

func TestBlockedDecisionCarriesTheWritersIssue(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	facts := eligibilityStrikesFacts(now, 0, 0)
	facts.EntryFeeMinor = 10_000
	eligible := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
	if !eligible.Eligible || eligible.RequiredAction != "start_payment" || len(eligible.Issues) != 1 {
		t.Fatalf("unexpected paid preflight %+v", eligible)
	}
	blocked := eligible.blockedBy(entryIssueCompetitionFull)
	if blocked.Eligible || blocked.CanRegisterNow || blocked.Status != eligibilityStatusIneligible ||
		blocked.RequiredAction != "none" || !slices.Equal(blocked.Issues, []eligibilityIssue{entryIssueCompetitionFull}) {
		t.Fatalf("a blocked decision reads as %+v", blocked)
	}
	if first := blocked.firstBlockingIssue(); first == nil || *first != entryIssueCompetitionFull {
		t.Fatalf("first blocking issue = %+v", first)
	}
	if len(eligible.Issues) != 1 || eligible.Issues[0].Code != "payment_required" {
		t.Fatalf("blocking a decision changed the original: %+v", eligible.Issues)
	}
	// A payment that has yet to settle is still polled once the entry is blocked.
	paymentID, pending := "4d3e5536-bf3c-4dba-a643-575e43f56970", "pending"
	facts.PaymentID, facts.PaymentStatus = &paymentID, &pending
	polling := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
	if blocked = polling.blockedBy(entryIssueCompetitionFull); blocked.RequiredAction != "poll_payment" ||
		!slices.Equal(blocked.Issues, []eligibilityIssue{entryIssueCompetitionFull}) {
		t.Fatalf("a blocked decision with a pending payment reads as %+v", blocked)
	}
}

// Entry reports why registration is unavailable by the competition's phase: a
// competition past registration has closed, one before it has not opened.
func TestEntryIssueFollowsTheRegistrationPhase(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, status string
		opensAt      time.Duration
		closesAt     time.Duration
		want         string
	}{
		{"published, not open yet", "published", -time.Hour, time.Hour, "registration_not_open"},
		{"open before its start time", "registration_open", time.Hour, 2 * time.Hour, "registration_not_open"},
		{"open after its deadline", "registration_open", -2 * time.Hour, -time.Minute, "registration_closed"},
		{"published after its deadline", "published", -2 * time.Hour, -time.Minute, "registration_closed"},
		{"check-in", "check_in", -2 * time.Hour, time.Hour, "registration_closed"},
		{"running", "running", -2 * time.Hour, -time.Hour, "registration_closed"},
		{"completed", "completed", -2 * time.Hour, -time.Hour, "registration_closed"},
		{"cancelled", "cancelled", -time.Hour, time.Hour, "competition_cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := eligibilityStrikesFacts(now, 0, 0)
			facts.Status = test.status
			facts.RegistrationOpensAt, facts.RegistrationClosesAt = now.Add(test.opensAt), now.Add(test.closesAt)
			result := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
			var codes []string
			for _, issue := range result.Issues {
				codes = append(codes, issue.Code)
			}
			if issue := result.firstBlockingIssue(); issue == nil || issue.Code != test.want || len(codes) != 1 {
				t.Fatalf("issues %v, want only %s", codes, test.want)
			}
		})
	}
	open := eligibilityStrikesFacts(now, 0, 0)
	if result := assessCompetitionEligibility(open, competitionEligibilityRules{AllowedCountries: []string{}}, now); !result.Eligible {
		t.Fatalf("an open competition is blocked: %+v", result.Issues)
	}
}

func entryJourneyErrorFields(t *testing.T, recorder *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestEntryJourneyErrorBodies(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	facts := eligibilityStrikesFacts(now, 3, 3)
	suspended := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
	issue := suspended.firstBlockingIssue()
	if issue == nil || issue.Code != "conduct_suspended" {
		t.Fatalf("unexpected decision %+v", suspended)
	}
	ineligible, blocked := httptest.NewRecorder(), httptest.NewRecorder()
	writeCompetitionIneligible(ineligible, suspended, *issue)
	writeBlockedEntry(blocked, assessCompetitionEligibility(eligibilityStrikesFacts(now, 0, 0),
		competitionEligibilityRules{AllowedCountries: []string{}}, now), entryIssueCompetitionFull)
	wantKeys := []string{"eligibility", "error", "issue", "message"}
	for name, recorder := range map[string]*httptest.ResponseRecorder{"ineligible": ineligible, "blocked": blocked} {
		fields := entryJourneyErrorFields(t, recorder)
		if keys := slices.Sorted(maps.Keys(fields)); !slices.Equal(keys, wantKeys) {
			t.Fatalf("%s body keys = %v, want %v", name, keys, wantKeys)
		}
		var body competitionIneligibleError
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Error != "competition_ineligible" || body.Message != body.Issue.Message || body.Eligibility.Eligible ||
			body.Eligibility.Status != eligibilityStatusIneligible || body.Eligibility.firstBlockingIssue() == nil {
			t.Fatalf("%s body %+v", name, body)
		}
	}

	inProgress := httptest.NewRecorder()
	writePaymentInProgress(inProgress, "4d3e5536-bf3c-4dba-a643-575e43f56970", "pending")
	fields := entryJourneyErrorFields(t, inProgress)
	if keys := slices.Sorted(maps.Keys(fields)); !slices.Equal(keys, []string{"error", "message", "paymentId", "paymentStatus"}) ||
		string(fields["error"]) != `"payment_in_progress"` || string(fields["paymentId"]) != `"4d3e5536-bf3c-4dba-a643-575e43f56970"` {
		t.Fatalf("payment_in_progress body %s", inProgress.Body.String())
	}
}

// Free registration and M-Pesa checkout report a blocked entry through the
// one writer, never with the issue code as the error.
func TestFreeAndPaidEntryShareTheIneligibleShape(t *testing.T) {
	for _, handler := range []struct{ file, declaration string }{
		{"competition_handlers.go", "func (s *Server) createFreeRegistration("},
		{"payment_handlers.go", "func (s *Server) initiateMPesa("},
	} {
		source := resultReportsFunctionSource(t, handler.file, handler.declaration)
		assertOrder(t, handler.declaration, source, "loadCompetitionEligibility(", "eligibility.firstBlockingIssue()",
			"writeCompetitionIneligible(w, eligibility, *issue)")
		for _, retired := range []string{"issue.Code, issue.Message", `"onboarding_required"`, `"wrong_game_account"`,
			`"registration_closed_for_player"`, `"registration_closed"`, `"competition_full"`, `"error": "competition_ineligible"`} {
			if strings.Contains(source, retired) {
				t.Errorf("%s still reports %s outside the shared shape", handler.declaration, retired)
			}
		}
		for _, recheck := range []string{"entryIssueGameAccountRequired", "entryIssueProfileIncomplete",
			"entryIssueGameAccountMismatch", "entryIssueRegistrationNotReusable", "entryIssueCompetitionFull"} {
			if !strings.Contains(source, "writeBlockedEntry(w, eligibility, "+recheck+")") {
				t.Errorf("%s does not report %s as a blocked entry", handler.declaration, recheck)
			}
		}
	}
	initiate := resultReportsFunctionSource(t, "payment_handlers.go", "func (s *Server) initiateMPesa(")
	// A blocked entry is reported before the fee is judged, so a cancelled or
	// closed competition reads the same whichever path the app took.
	assertOrder(t, "initiateMPesa", initiate, "FROM competitions WHERE id=$1 FOR UPDATE",
		"WHERE user_id=$1 AND idempotency_key=$2", "writeJSON(w, http.StatusOK, intent)", "loadCompetitionEligibility(",
		"writeCompetitionIneligible(w, eligibility, *issue)", `"payment_not_available"`, `"unsupported_fee"`)
}

// entryJourneyErrorPatterns adds the error field of a JSON body to the
// writeError and Code patterns of the result surfaces.
var entryJourneyErrorPatterns = append(slices.Clone(openAPIErrorCodePatterns),
	regexp.MustCompile(`(?:"error"|Error):\s*"([a-z_]+)"`))

// TestOpenAPIListsEveryEntryJourneyErrorCode derives each entry operation's
// error codes from its handler and the writers it calls, so a new code cannot
// ship undocumented.
func TestOpenAPIListsEveryEntryJourneyErrorCode(t *testing.T) {
	contract := openAPIFile(t, "openapi.yaml")
	auth := []string{"authentication_required", "invalid_access_token", "invalid_session", "session_check_unavailable"}
	ineligible := [2]string{"entry_journey_errors.go", "func writeCompetitionIneligible("}
	operations := []struct {
		fragment, path string
		sources        [][2]string
		extra          []string
	}{
		{"competition-match-policy.paths.yaml", "/v1/competitions/{id}/eligibility",
			[][2]string{{"competition_eligibility.go", "func (s *Server) getCompetitionEligibility("}},
			append([]string{"database_unavailable"}, auth...)},
		{"competition.paths.yaml", "/v1/competitions/{id}/registrations",
			[][2]string{{"competition_handlers.go", "func (s *Server) createFreeRegistration("}, ineligible},
			append([]string{"database_unavailable", "invalid_request"}, auth...)},
		{"competition.paths.yaml", "/v1/competitions/{id}/registrations/me",
			[][2]string{{"competition_handlers.go", "func (s *Server) withdrawRegistration("}},
			append([]string{"database_unavailable"}, auth...)},
		{"", "/v1/payments/mpesa/stk-push",
			[][2]string{{"payment_handlers.go", "func (s *Server) initiateMPesa("}, ineligible,
				{"entry_journey_errors.go", "func writePaymentInProgress("},
				{"payment_handlers.go", "func writePaymentConflict("}},
			append([]string{"database_unavailable", "invalid_request"}, auth...)},
		{"", "/v1/payments/{id}",
			[][2]string{{"payment_handlers.go", "func (s *Server) getPayment("}},
			append([]string{"database_unavailable"}, auth...)},
		{"payment-account-verification.paths.yaml", "/v1/competitions/{id}/registrations/me/withdrawal-requests",
			[][2]string{{"payment_lifecycle_handlers.go", "func (s *Server) requestPaidWithdrawal("},
				{"result_handlers.go", "func readIdempotencyKey("}},
			append([]string{"database_unavailable", "invalid_request"}, auth...)},
	}
	for _, operation := range operations {
		var codes []string
		for _, source := range operation.sources {
			body := resultReportsFunctionSource(t, source[0], source[1])
			for _, pattern := range entryJourneyErrorPatterns {
				for _, match := range pattern.FindAllStringSubmatch(body, -1) {
					codes = append(codes, match[1])
				}
			}
		}
		if len(codes) == 0 {
			t.Fatalf("no error codes found for %s; the extraction patterns no longer match", operation.path)
		}
		codes = append(codes, operation.extra...)
		slices.Sort(codes)
		codes = slices.Compact(codes)
		documents := map[string]string{"openapi.yaml": contract}
		if operation.fragment != "" {
			documents[operation.fragment] = openAPIFile(t, operation.fragment)
		}
		for name, document := range documents {
			block := openAPIBlock(t, document, operation.path, 2)
			for _, code := range codes {
				if !regexp.MustCompile(`\b` + regexp.QuoteMeta(code) + `\b`).MatchString(block) {
					t.Errorf("%s does not document error code %s on %s", name, code, operation.path)
				}
			}
		}
	}
}

// The typed 409 bodies are declared once and referenced by both entry
// operations, so generated clients keep issue, eligibility and paymentId.
func TestOpenAPIDeclaresTheTypedEntryErrors(t *testing.T) {
	contract := openAPIFile(t, "openapi.yaml")
	ineligible := openAPIBlock(t, contract, "CompetitionIneligibleError", 4)
	for _, field := range []string{"- issue\n", "- eligibility\n", "const: competition_ineligible",
		"$ref: '#/components/schemas/CompetitionEligibilityIssue'", "$ref: '#/components/schemas/CompetitionEligibility'"} {
		if !strings.Contains(ineligible, field) {
			t.Errorf("CompetitionIneligibleError lacks %q", field)
		}
	}
	inProgress := openAPIBlock(t, contract, "PaymentInProgressError", 4)
	if got := openAPIEnum(t, openAPIBlock(t, inProgress, "paymentStatus", 8)); !slices.Equal(got,
		[]string{"initiating", "pending", "callback_received", "review", "succeeded"}) {
		t.Errorf("PaymentInProgressError.paymentStatus enum = %v", got)
	}
	if !strings.Contains(inProgress, "- paymentId\n") || !strings.Contains(inProgress, "const: payment_in_progress") {
		t.Error("PaymentInProgressError does not require paymentId")
	}
	if !strings.Contains(resultReportsFunctionSource(t, "payment_handlers.go", "func loadActiveCompetitionPayment("),
		"status IN ('initiating','pending','callback_received','review','succeeded')") {
		t.Error("the active payment statuses drifted from PaymentInProgressError.paymentStatus")
	}
	for _, reference := range []struct{ document, path, schema string }{
		{"openapi.yaml", "/v1/payments/mpesa/stk-push", "CompetitionIneligibleError"},
		{"openapi.yaml", "/v1/payments/mpesa/stk-push", "PaymentInProgressError"},
		{"openapi.yaml", "/v1/competitions/{id}/registrations", "CompetitionIneligibleError"},
		{"openapi.yaml", "/v1/competitions/{id}/registrations", "PaymentRequiredError"},
		{"competition.paths.yaml", "/v1/competitions/{id}/registrations", "CompetitionIneligibleError"},
		{"competition.paths.yaml", "/v1/competitions/{id}/registrations", "PaymentRequiredError"},
	} {
		block := openAPIBlock(t, openAPIFile(t, reference.document), reference.path, 2)
		if !strings.Contains(block, "#/components/schemas/"+reference.schema) {
			t.Errorf("%s %s does not reference %s", reference.document, reference.path, reference.schema)
		}
	}
}
