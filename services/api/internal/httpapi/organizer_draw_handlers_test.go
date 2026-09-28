package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/bracket"
)

var drawTestClock = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

func validDrawRequest(policy string) organizerDrawRequest {
	return organizerDrawRequest{SeedingPolicy: policy, ExpectedStatus: "check_in", Config: &organizerDrawConfig{}}
}

func TestNormalizeOrganizerDrawRequestUsesCanonicalDefaults(t *testing.T) {
	normalized, problem := normalizeOrganizerDrawRequest(validDrawRequest("rating"))
	if problem != nil {
		t.Fatalf("valid draw request rejected: %+v", problem)
	}
	want := normalizedDrawConfigView{BestOf: 1, GroupCount: 1, CheckInLeadMinutes: 15,
		CheckInGraceMinutes: 10, ResultWindowMinutes: 60, RoundIntervalMinutes: 90}
	if normalized.ExpectedStatus != "check_in" || normalized.SeedingPolicy != "rating" || normalized.Config != want {
		t.Fatalf("unexpected normalized request: %+v", normalized)
	}
}

func TestNormalizeOrganizerDrawRequestRejectsUnsafeConfiguration(t *testing.T) {
	two, ten, sixty, thirty, twentyFour, twentyFive := 2, 10, 60, 30, 24, 25
	cases := []struct {
		name   string
		mutate func(*organizerDrawRequest)
		code   string
	}{
		{"unknown policy", func(input *organizerDrawRequest) { input.SeedingPolicy = "manual" }, "invalid_seeding_policy"},
		{"wrong state", func(input *organizerDrawRequest) { input.ExpectedStatus = "running" }, "invalid_expected_status"},
		{"missing config", func(input *organizerDrawRequest) { input.Config = nil }, "draw_config_required"},
		{"even best of", func(input *organizerDrawRequest) { input.Config.BestOf = &two }, "invalid_best_of"},
		{"zero groups", func(input *organizerDrawRequest) { zero := 0; input.Config.GroupCount = &zero }, "invalid_group_count"},
		{"result before grace", func(input *organizerDrawRequest) {
			input.Config.CheckInGraceMinutes, input.Config.ResultWindowMinutes = &sixty, &thirty
		}, "invalid_result_window"},
		// Missing the result deadline removes both entries (R7), so a late
		// check-in must still leave fifteen minutes to play.
		{"less than fifteen minutes to play after grace", func(input *organizerDrawRequest) {
			input.Config.CheckInGraceMinutes, input.Config.ResultWindowMinutes = &ten, &twentyFour
		}, "invalid_result_window"},
		{"exactly fifteen minutes to play after grace", func(input *organizerDrawRequest) {
			input.Config.CheckInGraceMinutes, input.Config.ResultWindowMinutes = &ten, &twentyFive
		}, ""},
		{"overlapping rounds", func(input *organizerDrawRequest) {
			input.Config.ResultWindowMinutes, input.Config.RoundIntervalMinutes = &sixty, &thirty
		}, "invalid_round_interval"},
		{"lead too short", func(input *organizerDrawRequest) { input.Config.CheckInLeadMinutes = &two }, "invalid_check_in_lead"},
		{"grace accepted boundary control", func(input *organizerDrawRequest) { input.Config.CheckInGraceMinutes = &ten }, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := validDrawRequest("seeded")
			testCase.mutate(&input)
			_, problem := normalizeOrganizerDrawRequest(input)
			if testCase.code == "" {
				if problem != nil {
					t.Fatalf("valid boundary rejected: %+v", problem)
				}
				return
			}
			if problem == nil || problem.Code != testCase.code {
				t.Fatalf("expected %s, got %+v", testCase.code, problem)
			}
		})
	}
}

func TestDecodeOrganizerDrawRequestIsStrict(t *testing.T) {
	valid := `{"seedingPolicy":"seeded","expectedStatus":"check_in","config":{}}`
	for name, body := range map[string]string{
		"unknown root field":   strings.TrimSuffix(valid, "}") + `,"surprise":true}`,
		"unknown config field": `{"seedingPolicy":"seeded","expectedStatus":"check_in","config":{"mystery":1}}`,
		"trailing object":      valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/draws", strings.NewReader(body))
			var decoded organizerDrawRequest
			if decodeOrganizerDrawRequest(recorder, request, &decoded) || recorder.Code != http.StatusBadRequest {
				t.Fatalf("strict decoder accepted %s: status=%d", body, recorder.Code)
			}
		})
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/draws", strings.NewReader(valid))
	var decoded organizerDrawRequest
	if !decodeOrganizerDrawRequest(recorder, request, &decoded) {
		t.Fatalf("valid request rejected: %s", recorder.Body.String())
	}
}

func TestDrawCandidateOrderingIsStableAndPolicySpecific(t *testing.T) {
	seedOne, seedThree := 1, 3
	ratingHigh, ratingLow := 1900, 1200
	candidates := []drawCandidate{
		{EntryID: "d", CreatedAt: drawTestClock.Add(4 * time.Minute), Rating: &ratingLow, MatchesPlayed: 10},
		{EntryID: "b", CreatedAt: drawTestClock.Add(2 * time.Minute), Seed: &seedThree, Rating: &ratingHigh, MatchesPlayed: 2},
		{EntryID: "a", CreatedAt: drawTestClock.Add(time.Minute), Seed: &seedOne, Rating: &ratingHigh, MatchesPlayed: 20},
		{EntryID: "c", CreatedAt: drawTestClock.Add(3 * time.Minute)},
	}
	assertIDs := func(policy string, seed []byte, want []string) {
		t.Helper()
		ordered := orderDrawCandidates(candidates, policy, seed)
		got := make([]string, len(ordered))
		for index := range ordered {
			got[index] = ordered[index].EntryID
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s order=%v, want %v", policy, got, want)
		}
	}
	assertIDs("registration_order", nil, []string{"a", "b", "c", "d"})
	assertIDs("seeded", nil, []string{"a", "b", "c", "d"})
	assertIDs("rating", nil, []string{"a", "b", "c", "d"})

	randomSeed := bytes.Repeat([]byte{0x5a}, 32)
	first := orderDrawCandidates(candidates, "random", randomSeed)
	second := orderDrawCandidates([]drawCandidate{candidates[2], candidates[0], candidates[3], candidates[1]}, "random", randomSeed)
	for index := range first {
		if first[index].EntryID != second[index].EntryID {
			t.Fatalf("random order depends on input/query order: %v vs %v", first, second)
		}
	}
}

func TestRandomDrawSeedMakesPlanReplayable(t *testing.T) {
	input, problem := normalizeOrganizerDrawRequest(validDrawRequest("random"))
	if problem != nil {
		t.Fatal(problem.Message)
	}
	candidates := drawCandidates(8)
	seed := bytes.Repeat([]byte{0x2c}, 32)
	first, problem := buildOrganizerDrawPlan("competition", string(bracket.SingleElimination),
		drawTestClock.Add(time.Hour), drawTestClock, input, candidates, seed)
	if problem != nil {
		t.Fatal(problem.Message)
	}
	second, problem := buildOrganizerDrawPlan("competition", string(bracket.SingleElimination),
		drawTestClock.Add(time.Hour), drawTestClock, input, append([]drawCandidate(nil), candidates...), seed)
	if problem != nil {
		t.Fatal(problem.Message)
	}
	if first.DrawSeed != second.DrawSeed || first.EntryFingerprint != second.EntryFingerprint ||
		first.Graph.Fingerprint != second.Graph.Fingerprint || !reflect.DeepEqual(first.Entries, second.Entries) {
		t.Fatal("the same random seed and frozen vector did not replay the same draw")
	}
	if _, problem = buildOrganizerDrawPlan("competition", string(bracket.SingleElimination),
		drawTestClock.Add(time.Hour), drawTestClock, input, candidates, []byte("short")); problem == nil || problem.Code != "invalid_draw_seed" {
		t.Fatalf("short random seed accepted: %+v", problem)
	}
}

func TestKnockoutOnlySchedulesInitiallyPlayableMatches(t *testing.T) {
	input, _ := normalizeOrganizerDrawRequest(validDrawRequest("registration_order"))
	plan, problem := buildOrganizerDrawPlan("competition", string(bracket.SingleElimination),
		drawTestClock.Add(time.Hour), drawTestClock, input, drawCandidates(4), nil)
	if problem != nil {
		t.Fatal(problem.Message)
	}
	ready, pending := 0, 0
	for _, match := range plan.Matches {
		switch match.State {
		case "ready":
			ready++
			if match.HomeEntryID == nil || match.AwayEntryID == nil || match.ScheduledAt == nil ||
				match.CheckInOpensAt == nil || match.CheckInClosesAt == nil || match.ResultDueAt == nil {
				t.Fatalf("ready match has incomplete participants/window: %+v", match)
			}
			if !match.CheckInOpensAt.Before(*match.CheckInClosesAt) || !match.CheckInClosesAt.Before(*match.ResultDueAt) {
				t.Fatalf("invalid ready window: %+v", match)
			}
		case "pending":
			pending++
			if match.ScheduledAt != nil || match.CheckInOpensAt != nil || match.ResultDueAt != nil {
				t.Fatalf("dependency match was anchored before it became ready: %+v", match)
			}
		}
	}
	if ready != 2 || pending != 1 {
		t.Fatalf("four-player knockout produced ready=%d pending=%d", ready, pending)
	}
}

func TestRoundRobinGatesLaterRoundsButPersistsWindowsAndGroups(t *testing.T) {
	groups := 2
	raw := validDrawRequest("registration_order")
	raw.Config.GroupCount = &groups
	input, problem := normalizeOrganizerDrawRequest(raw)
	if problem != nil {
		t.Fatal(problem.Message)
	}
	plan, problem := buildOrganizerDrawPlan("competition", string(bracket.RoundRobin),
		drawTestClock.Add(time.Hour), drawTestClock, input, drawCandidates(8), nil)
	if problem != nil {
		t.Fatal(problem.Message)
	}
	ready, pending := 0, 0
	for _, match := range plan.Matches {
		if match.ScheduledAt == nil || match.CheckInOpensAt == nil || match.CheckInClosesAt == nil || match.ResultDueAt == nil {
			t.Fatalf("round-robin match lacks a persisted window: %+v", match)
		}
		if match.Node.Ref.Round == 1 && match.State == "ready" {
			ready++
		} else if match.Node.Ref.Round > 1 && match.State == "pending" {
			pending++
		} else {
			t.Fatalf("round gate mismatch: round=%d state=%s", match.Node.Ref.Round, match.State)
		}
	}
	if ready == 0 || pending == 0 {
		t.Fatalf("expected both ready and gated rounds, got ready=%d pending=%d", ready, pending)
	}
	for index, entry := range plan.Entries {
		want := "group_" + drawGroupLabel(index%groups)
		if entry.GroupKey == nil || *entry.GroupKey != want {
			t.Fatalf("position %d group=%v, want %s", index+1, entry.GroupKey, want)
		}
	}
}

func TestFormatSpecificDrawConfigurationIsEnforced(t *testing.T) {
	groups := 2
	raw := validDrawRequest("seeded")
	raw.Config.GroupCount = &groups
	input, _ := normalizeOrganizerDrawRequest(raw)
	if _, problem := buildOrganizerDrawPlan("competition", string(bracket.SingleElimination),
		drawTestClock.Add(time.Hour), drawTestClock, input, drawCandidates(8), nil); problem == nil || problem.Code != "format_config_mismatch" {
		t.Fatalf("knockout accepted round-robin config: %+v", problem)
	}
	raw = validDrawRequest("seeded")
	raw.Config.ThirdPlace = true
	input, _ = normalizeOrganizerDrawRequest(raw)
	if _, problem := buildOrganizerDrawPlan("competition", string(bracket.DoubleElimination),
		drawTestClock.Add(time.Hour), drawTestClock, input, drawCandidates(8), nil); problem == nil || problem.Code != "format_config_mismatch" {
		t.Fatalf("double elimination accepted third-place config: %+v", problem)
	}
}

func TestDrawMatchEstimateRejectsQuadraticRoundRobinBeforeEmission(t *testing.T) {
	config := normalizedDrawConfigView{GroupCount: 1}
	if got := estimatedDrawMatches(string(bracket.RoundRobin), 1024, config); got != 523_776 {
		t.Fatalf("single-table estimate=%d, want 523776", got)
	}
	config.GroupCount = 64
	if got := estimatedDrawMatches(string(bracket.RoundRobin), 1024, config); got != 7_680 {
		t.Fatalf("64-group estimate=%d, want 7680", got)
	}
	input, _ := normalizeOrganizerDrawRequest(validDrawRequest("registration_order"))
	if _, problem := buildOrganizerDrawPlan("competition", string(bracket.RoundRobin),
		drawTestClock.Add(time.Hour), drawTestClock, input, drawCandidates(1024), nil); problem == nil || problem.Code != "draw_too_large" {
		t.Fatalf("quadratic draw was not rejected before emission: %+v", problem)
	}
}

func drawCandidates(count int) []drawCandidate {
	items := make([]drawCandidate, count)
	for index := range items {
		rating := 1600 - index*10
		items[index] = drawCandidate{EntryID: "entry-" + drawGroupLabel(index), Rating: &rating,
			MatchesPlayed: count - index, CreatedAt: drawTestClock.Add(time.Duration(index) * time.Minute)}
	}
	return items
}
