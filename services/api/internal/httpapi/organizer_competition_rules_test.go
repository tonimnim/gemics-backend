package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

var ruleClock = time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)

var kenyaOnlyRules = json.RawMessage(`{"eligibility":{"allowedCountries":["KE"]}}`)

func validCompetitionInput() organizerCompetitionInput {
	return organizerCompetitionInput{
		Name:                 "Nairobi Sunday Knockout",
		Description:          "Weekly 1v1 knockout.",
		GameID:               "efootball-mobile",
		Format:               "single_elimination",
		MaxEntries:           32,
		RegistrationOpensAt:  ruleClock.Add(time.Hour),
		RegistrationClosesAt: ruleClock.Add(24 * time.Hour),
		StartsAt:             ruleClock.Add(30 * time.Hour),
	}
}

func TestCompetitionInputDerivesSlugFeePurposeAndDefaults(t *testing.T) {
	draft, problem := normalizeCompetitionInput(validCompetitionInput(), ruleClock)
	if problem != nil {
		t.Fatalf("valid input rejected: %+v", problem)
	}
	if draft.Slug != "nairobi-sunday-knockout" {
		t.Errorf("unexpected derived slug %q", draft.Slug)
	}
	// Open to every country, so priced in USD.
	if draft.Currency != "USD" || draft.PrizeFunding != "none" || draft.FeePurpose != "none" {
		t.Errorf("unexpected defaults: %+v", draft)
	}
	if string(draft.Rules) != "{}" {
		t.Errorf("expected an empty rules object, got %q", draft.Rules)
	}

	paid := validCompetitionInput()
	paid.EntryFeeMinor = 20_000
	paid.Rules = kenyaOnlyRules
	draft, problem = normalizeCompetitionInput(paid, ruleClock)
	if problem != nil {
		t.Fatalf("paid input rejected: %+v", problem)
	}
	if draft.FeePurpose != "administration" || draft.Currency != "KES" {
		t.Errorf("a Kenyan fee must derive the administration purpose and KES, got %+v", draft)
	}
}

func TestCompetitionInputRejectsInvalidCombinations(t *testing.T) {
	cases := []struct {
		name  string
		mutta func(*organizerCompetitionInput)
		code  string
	}{
		{"short name", func(in *organizerCompetitionInput) { in.Name = "GG" }, "invalid_name"},
		{"unknown format", func(in *organizerCompetitionInput) { in.Format = "swiss" }, "invalid_format"},
		{"one entrant", func(in *organizerCompetitionInput) { in.MaxEntries = 1 }, "invalid_capacity"},
		{"oversized field", func(in *organizerCompetitionInput) { in.MaxEntries = 4096 }, "invalid_capacity"},
		{"negative fee", func(in *organizerCompetitionInput) { in.EntryFeeMinor = -1 }, "invalid_entry_fee"},
		{"fee with cents", func(in *organizerCompetitionInput) {
			in.EntryFeeMinor = 10_050
			in.Rules = kenyaOnlyRules
		}, "invalid_entry_fee"},
		{"paid entry outside Kenya", func(in *organizerCompetitionInput) { in.EntryFeeMinor = 10_000 }, "paid_entry_kenya_only"},
		{"paid entry in Japan", func(in *organizerCompetitionInput) {
			in.EntryFeeMinor = 1_000
			in.Rules = json.RawMessage(`{"eligibility":{"allowedCountries":["JP"]}}`)
		}, "paid_entry_kenya_only"},
		{"currency the countries do not use", func(in *organizerCompetitionInput) { in.Currency = "KES" }, "currency_mismatch"},
		{"unsupported currency", func(in *organizerCompetitionInput) { in.Currency = "EUR" }, "invalid_currency"},
		{"unfunded prize", func(in *organizerCompetitionInput) { in.PrizeAmountMinor = 50_000 }, "prize_funding_required"},
		{"funder without prize", func(in *organizerCompetitionInput) { in.PrizeFunding = "sponsor" }, "invalid_prize"},
		{"backwards registration", func(in *organizerCompetitionInput) {
			in.RegistrationOpensAt = ruleClock.Add(48 * time.Hour)
		}, "invalid_registration_window"},
		{"start before registration closes", func(in *organizerCompetitionInput) {
			in.StartsAt = ruleClock.Add(2 * time.Hour)
		}, "invalid_start_time"},
		{"start in the past", func(in *organizerCompetitionInput) {
			in.RegistrationOpensAt = ruleClock.Add(-72 * time.Hour)
			in.RegistrationClosesAt = ruleClock.Add(-48 * time.Hour)
			in.StartsAt = ruleClock.Add(-24 * time.Hour)
		}, "start_time_in_past"},
		{"registration already closed", func(in *organizerCompetitionInput) {
			in.RegistrationOpensAt = ruleClock.Add(-48 * time.Hour)
			in.RegistrationClosesAt = ruleClock.Add(-time.Hour)
		}, "registration_window_in_past"},
		{"check-in before registration closes", func(in *organizerCompetitionInput) {
			early := ruleClock.Add(2 * time.Hour)
			in.CheckInOpensAt = &early
		}, "invalid_check_in_window"},
		{"unnameable slug", func(in *organizerCompetitionInput) { in.Name = "!!! ???" }, "invalid_slug"},
		{"rules array", func(in *organizerCompetitionInput) { in.Rules = json.RawMessage(`[1,2]`) }, "invalid_rules"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			input := validCompetitionInput()
			testCase.mutta(&input)
			_, problem := normalizeCompetitionInput(input, ruleClock)
			if problem == nil {
				t.Fatalf("expected %s, got acceptance", testCase.code)
			}
			if problem.Code != testCase.code {
				t.Fatalf("expected %s, got %s (%s)", testCase.code, problem.Code, problem.Message)
			}
			if problem.Status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", problem.Status)
			}
		})
	}
}

func storedDraft() competitionDraft {
	draft, problem := normalizeCompetitionInput(validCompetitionInput(), ruleClock)
	if problem != nil {
		panic(problem.Message)
	}
	return draft
}

func TestPatchFreezesMoneyAndFormatOnceRegistrationOpens(t *testing.T) {
	current := storedDraft()
	frozen := map[string]organizerCompetitionPatch{
		"format":        {Format: pointerTo("round_robin")},
		"gameId":        {GameID: pointerTo("efootball-mobile")},
		"slug":          {Slug: pointerTo("renamed-cup")},
		"entryFeeMinor": {EntryFeeMinor: pointerTo(int64(50_000))},
		// Limiting a USD competition to Kenya would reprice it in KES.
		"currency":            {Rules: pointerTo(kenyaOnlyRules)},
		"prizeAmountMinor":    {PrizeAmountMinor: pointerTo(int64(10_000))},
		"prizeFunding":        {PrizeFunding: pointerTo("sponsor")},
		"registrationOpensAt": {RegistrationOpensAt: pointerTo(ruleClock.Add(2 * time.Hour))},
	}
	for field, patch := range frozen {
		if _, _, problem := applyCompetitionPatch(current, patch, "registration_open", 4, ruleClock); problem == nil {
			t.Errorf("%s was editable while registration was open", field)
		} else if problem.Code != "field_locked" {
			t.Errorf("%s produced %s, expected field_locked", field, problem.Code)
		}
	}

	// The same fields are free to change while the competition is still a draft.
	for field, patch := range frozen {
		if _, _, problem := applyCompetitionPatch(current, patch, "draft", 0, ruleClock); problem != nil &&
			problem.Code == "field_locked" {
			t.Errorf("%s was locked in draft", field)
		}
	}
}

func TestPatchCapacityRules(t *testing.T) {
	current := storedDraft()

	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{MaxEntries: pointerTo(16)},
		"registration_open", 4, ruleClock); problem == nil || problem.Code != "capacity_shrink_forbidden" {
		t.Fatalf("shrinking a live competition should be refused, got %+v", problem)
	}
	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{MaxEntries: pointerTo(64)},
		"registration_open", 4, ruleClock); problem != nil {
		t.Fatalf("growing a live competition should be allowed, got %+v", problem)
	}
	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{MaxEntries: pointerTo(4)},
		"draft", 9, ruleClock); problem == nil || problem.Code != "capacity_below_entries" {
		t.Fatalf("capacity below the entry count should be refused, got %+v", problem)
	}
}

func TestPatchRegistrationWindowOnlyExtends(t *testing.T) {
	current := storedDraft()
	shorter := current.RegistrationClosesAt.Add(-2 * time.Hour)
	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{RegistrationClosesAt: &shorter},
		"registration_open", 2, ruleClock); problem == nil || problem.Code != "registration_shorten_forbidden" {
		t.Fatalf("shortening open registration should be refused, got %+v", problem)
	}
	longer := current.RegistrationClosesAt.Add(2 * time.Hour)
	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{RegistrationClosesAt: &longer},
		"registration_open", 2, ruleClock); problem != nil {
		t.Fatalf("extending open registration should be allowed, got %+v", problem)
	}
}

func TestPatchRefusesLockedStatusesAndEmptyBodies(t *testing.T) {
	current := storedDraft()
	for _, status := range []string{"check_in", "running", "completed", "cancelled"} {
		_, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{Name: pointerTo("New name")},
			status, 8, ruleClock)
		if problem == nil || problem.Code != "competition_locked" {
			t.Errorf("status %s should be locked, got %+v", status, problem)
		}
	}
	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{}, "draft", 0, ruleClock); problem == nil ||
		problem.Code != "empty_patch" {
		t.Fatalf("an empty patch should be refused, got %+v", problem)
	}
}

func TestPatchReportsRulesChangeForVersionBump(t *testing.T) {
	current := storedDraft()
	same := json.RawMessage(`{}`)
	_, changed, problem := applyCompetitionPatch(current, organizerCompetitionPatch{
		Rules: &same, Name: pointerTo("Nairobi Sunday Knockout II")}, "registration_open", 3, ruleClock)
	if problem != nil {
		t.Fatalf("unexpected rejection: %+v", problem)
	}
	if changed {
		t.Fatal("an identical rules document must not report a change")
	}

	different := json.RawMessage(`{"bestOf": 3}`)
	updated, changed, problem := applyCompetitionPatch(current, organizerCompetitionPatch{Rules: &different},
		"registration_open", 3, ruleClock)
	if problem != nil {
		t.Fatalf("unexpected rejection: %+v", problem)
	}
	if !changed {
		t.Fatal("a different rules document must report a change")
	}
	if string(updated.Rules) != `{"bestOf":3}` {
		t.Fatalf("rules were not compacted: %q", updated.Rules)
	}
}

// A live competition whose registration window has already passed must still be
// editable, otherwise an organizer could never extend a window that just closed.
func TestPatchAllowsEditingAfterRegistrationClosed(t *testing.T) {
	current := storedDraft()
	later := ruleClock.Add(48 * time.Hour)
	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{Name: pointerTo("Late edit")},
		"registration_open", 6, later); problem != nil {
		t.Fatalf("editing after registration closed should be allowed, got %+v", problem)
	}
	// The start time is the exception: it can move, but never into the past.
	past := later.Add(-time.Hour)
	if _, _, problem := applyCompetitionPatch(current, organizerCompetitionPatch{StartsAt: &past},
		"registration_open", 6, later); problem == nil || problem.Code != "start_time_in_past" {
		t.Fatalf("a start time in the past should be refused, got %+v", problem)
	}
}

func pointerTo[T any](value T) *T { return &value }

func TestCompetitionCurrencyFollowsItsCountries(t *testing.T) {
	cases := []struct {
		rules string
		want  string
	}{
		{`{}`, "USD"},
		{`{"eligibility":{"allowedCountries":["KE"]}}`, "KES"},
		{`{"eligibility":{"allowedCountries":["jp"]}}`, "JPY"},
		{`{"eligibility":{"allowedCountries":["SG"]}}`, "SGD"},
		{`{"eligibility":{"allowedCountries":["ID"]}}`, "IDR"},
		{`{"eligibility":{"allowedCountries":["BR"]}}`, "BRL"},
		{`{"eligibility":{"allowedCountries":["IN"]}}`, "INR"},
		// Several countries, or one without a supported currency, use USD.
		{`{"eligibility":{"allowedCountries":["KE","UG"]}}`, "USD"},
		{`{"eligibility":{"allowedCountries":["GH"]}}`, "USD"},
	}
	for _, testCase := range cases {
		input := validCompetitionInput()
		input.Rules = json.RawMessage(testCase.rules)
		draft, problem := normalizeCompetitionInput(input, ruleClock)
		if problem != nil {
			t.Fatalf("%s rejected: %+v", testCase.rules, problem)
		}
		if draft.Currency != testCase.want {
			t.Errorf("%s priced in %s, want %s", testCase.rules, draft.Currency, testCase.want)
		}
		// Restating the derived currency is accepted.
		input.Currency = testCase.want
		if _, problem := normalizeCompetitionInput(input, ruleClock); problem != nil {
			t.Errorf("restating %s for %s rejected: %+v", testCase.want, testCase.rules, problem)
		}
	}

	// A draft follows its countries when they change.
	draft := storedDraft()
	updated, _, problem := applyCompetitionPatch(draft, organizerCompetitionPatch{
		Rules: pointerTo(json.RawMessage(`{"eligibility":{"allowedCountries":["BR"]}}`)),
	}, "draft", 0, ruleClock)
	if problem != nil || updated.Currency != "BRL" {
		t.Fatalf("draft limited to Brazil = %+v, %+v", updated.Currency, problem)
	}
	// A prize is never carried into another currency: it must be restated.
	prized := draft
	prized.Rules = kenyaOnlyRules
	prized.Currency = "KES"
	prized.PrizeAmountMinor = 500_000
	prized.PrizeFunding = "organizer"
	japan := pointerTo(json.RawMessage(`{"eligibility":{"allowedCountries":["JP"]}}`))
	for _, status := range []string{"draft", "published"} {
		if _, _, problem := applyCompetitionPatch(prized, organizerCompetitionPatch{Rules: japan}, status, 0,
			ruleClock); problem == nil || problem.Code != "prize_currency_changed" {
			t.Fatalf("%s: a KES prize was carried into JPY: %+v", status, problem)
		}
	}
	restated, _, problem := applyCompetitionPatch(prized, organizerCompetitionPatch{
		Rules: japan, PrizeAmountMinor: pointerTo(int64(150_000)),
	}, "draft", 0, ruleClock)
	if problem != nil || restated.Currency != "JPY" || restated.PrizeAmountMinor != 150_000 {
		t.Fatalf("restated JPY prize = %+v, %+v", restated, problem)
	}
	// Once registration opens, countries that keep the currency can still widen.
	kenyan := draft
	kenyan.Rules = kenyaOnlyRules
	kenyan.Currency = "KES"
	if _, _, problem := applyCompetitionPatch(kenyan, organizerCompetitionPatch{
		Rules: pointerTo(json.RawMessage(`{"eligibility":{"allowedCountries":["KE"],"minimumAge":16}}`)),
	}, "registration_open", 3, ruleClock); problem != nil {
		t.Fatalf("a rule change that keeps KES was refused: %+v", problem)
	}
}

func TestMinorUnitsFollowEachCurrency(t *testing.T) {
	for currency, want := range map[string]int64{"KES": 100, "USD": 100, "IDR": 100, "JPY": 1, "UGX": 1} {
		if got := minorUnitsPerMajor(currency); got != want {
			t.Errorf("minorUnitsPerMajor(%s) = %d, want %d", currency, got, want)
		}
	}
}
