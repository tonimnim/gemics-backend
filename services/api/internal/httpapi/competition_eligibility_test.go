package httpapi

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCompetitionEligibilityRulesAreTypedAndNormalized(t *testing.T) {
	minimumAge, minimumRating, maximumRank := 16, 1400, 100
	want := competitionEligibilityRules{
		MinimumAge: &minimumAge, AllowedCountries: []string{"KE", "UG"}, MinimumRating: &minimumRating,
		MaximumRank: &maximumRank, RankingScope: "country", RequireVerifiedGameAccount: true,
	}
	got, err := parseCompetitionEligibilityRules([]byte(`{
		"eligibility":{"minimumAge":16,"allowedCountries":["ke","UG","KE"],
		"minimumRating":1400,"maximumRank":100,"rankingScope":"COUNTRY",
		"requireVerifiedGameAccount":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if *got.MinimumAge != *want.MinimumAge || *got.MinimumRating != *want.MinimumRating ||
		*got.MaximumRank != *want.MaximumRank || got.RankingScope != want.RankingScope ||
		!got.RequireVerifiedGameAccount || !slices.Equal(got.AllowedCountries, want.AllowedCountries) {
		t.Fatalf("unexpected normalized policy: %+v", got)
	}
	for _, raw := range []string{
		`{"eligibility":{"minimumAge":101}}`,
		`{"eligibility":{"allowedCountries":["KEN"]}}`,
		`{"eligibility":{"rankingScope":"global"}}`,
		`{"eligibility":{"maximumRank":10,"rankingScope":"neighborhood"}}`,
	} {
		if _, err := parseCompetitionEligibilityRules([]byte(raw)); !errors.Is(err, errInvalidEligibilityPolicy) {
			t.Fatalf("policy %s was accepted: %v", raw, err)
		}
	}
}

func TestCompetitionEligibilityReturnsEveryBlockingReason(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	birthDate := time.Date(2012, 8, 25, 0, 0, 0, 0, time.UTC)
	minimumAge, minimumRating, maximumRank := 16, 1500, 10
	rating, rank := 1400, 42
	accountID, accountGame, accountStatus := "account", "another-game", "unverified"
	facts := competitionEligibilityFacts{
		CompetitionID: "competition", GameID: "efootball-mobile", Status: "registration_open",
		MaxEntries: 32, OccupiedEntries: 32, EntryFeeMinor: 0, Currency: "KES",
		RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(time.Hour), StartsAt: now.Add(48 * time.Hour),
		CountryCode: "TZ", BirthDate: &birthDate, ProfileComplete: true, Rating: &rating, CountryRank: &rank,
		GameAccountID: &accountID, GameAccountGameID: &accountGame, GameAccountStatus: &accountStatus,
	}
	policy := competitionEligibilityRules{
		MinimumAge: &minimumAge, AllowedCountries: []string{"KE"}, MinimumRating: &minimumRating,
		MaximumRank: &maximumRank, RankingScope: "country", RequireVerifiedGameAccount: true,
	}
	result := assessCompetitionEligibility(facts, policy, now)
	if result.Eligible || result.CanRegisterNow || result.Status != eligibilityStatusIneligible {
		t.Fatalf("unexpected eligibility result: %+v", result)
	}
	codes := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		codes = append(codes, issue.Code)
	}
	for _, code := range []string{
		"competition_full", "minimum_age_not_met", "country_not_allowed",
		"minimum_rating_not_met", "maximum_rank_not_met", "game_account_game_mismatch",
	} {
		if !slices.Contains(codes, code) {
			t.Fatalf("missing %q in %v", code, codes)
		}
	}
}

func TestCompetitionEligibilitySeparatesPaidActionFromEligibility(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	accountID, gameID, status := "account", "efootball-mobile", "unverified"
	facts := competitionEligibilityFacts{
		CompetitionID: "competition", GameID: gameID, Status: "registration_open",
		MaxEntries: 32, EntryFeeMinor: 10_000, Currency: "KES", ProfileComplete: true, CountryCode: "KE",
		RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(time.Hour), StartsAt: now.Add(48 * time.Hour),
		GameAccountID: &accountID, GameAccountGameID: &gameID, GameAccountStatus: &status,
	}
	result := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
	if !result.Eligible || result.CanRegisterNow || result.RequiredAction != "start_payment" ||
		len(result.Issues) != 1 || result.Issues[0].Code != "payment_required" || result.Issues[0].Severity != "action_required" {
		t.Fatalf("unexpected paid preflight: %+v", result)
	}

	pending := "pending"
	facts.PaymentStatus = &pending
	result = assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
	if result.RequiredAction != "poll_payment" || result.Issues[0].Code != "payment_pending" {
		t.Fatalf("unexpected pending payment preflight: %+v", result)
	}
}

func TestCompetitionEligibilityTreatsPendingRefundAsExistingRegistration(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	entryStatus := "withdrawal_pending"
	result := assessCompetitionEligibility(competitionEligibilityFacts{
		CompetitionID: "competition", GameID: "efootball-mobile", Status: "registration_open",
		MaxEntries: 32, Currency: "KES", ProfileComplete: true, EntryStatus: &entryStatus,
		RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(time.Hour), StartsAt: now.Add(24 * time.Hour),
	}, competitionEligibilityRules{}, now)
	if result.Status != eligibilityStatusRegistered || result.RequiredAction != "view_refund" || result.CanRegisterNow {
		t.Fatalf("pending refund was presented as reusable registration: %+v", result)
	}
}

func TestAgeIsMeasuredAtCompetitionStart(t *testing.T) {
	birthDate := time.Date(2010, 8, 25, 0, 0, 0, 0, time.UTC)
	if got := ageOn(birthDate, time.Date(2026, 8, 24, 23, 59, 0, 0, time.UTC)); got != 15 {
		t.Fatalf("age before birthday = %d", got)
	}
	if got := ageOn(birthDate, time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)); got != 16 {
		t.Fatalf("age on birthday = %d", got)
	}
}

func TestOrganizerRulesRejectUnenforceableEligibilityAndVerificationPolicies(t *testing.T) {
	for _, testCase := range []struct {
		raw  string
		code string
	}{
		{`{"eligibility":{"minimumAge":120}}`, "invalid_eligibility_rules"},
		{`{"matchVerification":{"reportWindowMinutes":4}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"reportWindowMinutes":61}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"responseWindowMinutes":4}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"responseWindowMinutes":61}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"reminderBeforeDeadlineMinutes":0}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"reminderBeforeDeadlineMinutes":10}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"reportWindowMinutes":5,"reminderBeforeDeadlineMinutes":5}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"reportWindowMinutes":"10"}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"reportWindowMinutes":7.5}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":[]}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"reviewWindowMinutes":10}}`, "invalid_match_verification_rules"},
		{`{"matchVerification":{"autoConfirmEnabled":false}}`, "invalid_match_verification_rules"},
	} {
		if _, problem := normalizeRules([]byte(testCase.raw)); problem == nil || problem.Code != testCase.code {
			t.Fatalf("rules %s: got problem %+v, want %s", testCase.raw, problem, testCase.code)
		}
	}
	for _, raw := range []string{
		`{"eligibility":{"minimumAge":16},"matchVerification":{"reportWindowMinutes":5,"reminderBeforeDeadlineMinutes":4,"responseWindowMinutes":60}}`,
		`{"matchVerification":{"reportWindowMinutes":60,"reminderBeforeDeadlineMinutes":59,"responseWindowMinutes":5}}`,
		`{"matchVerification":{"reminderBeforeDeadlineMinutes":9}}`,
		`{"matchVerification":{}}`,
		`{"matchVerification":null}`,
	} {
		if _, problem := normalizeRules([]byte(raw)); problem != nil {
			t.Fatalf("valid rules %s rejected: %+v", raw, problem)
		}
	}
}

func TestMatchVerificationRulesRejectRetiredKeysByName(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"autoConfirmEnabled", "autoConfirmEnabled is no longer supported; use reportWindowMinutes"},
		{"confirmationWindowMinutes", "confirmationWindowMinutes is no longer supported; use reportWindowMinutes"},
		{"evidenceRequirement", "evidenceRequirement is no longer supported; use reportWindowMinutes"},
		{"refereeMonitoring", "refereeMonitoring is no longer supported; use reportWindowMinutes"},
		{"reviewWindowMinutes", "reviewWindowMinutes is not supported; use reportWindowMinutes"},
	}
	for _, test := range tests {
		raw := []byte(`{"matchVerification":{"` + test.key + `":null}}`)
		if err := validateMatchVerificationRules(raw); !errors.Is(err, errInvalidMatchVerificationPolicy) ||
			!strings.Contains(err.Error(), test.want) {
			t.Fatalf("key %s: %v", test.key, err)
		}
		// The organizer sees the key-specific message, not a generic one.
		if _, problem := normalizeRules(raw); problem == nil || !strings.HasPrefix(problem.Message, test.want) {
			t.Fatalf("key %s: got problem %+v", test.key, problem)
		}
	}
}

func TestMatchVerificationRulesAreTolerantOnRead(t *testing.T) {
	defaults := matchVerificationSettings{ReportWindow: 10 * time.Minute, ReminderLead: 3 * time.Minute, ResponseWindow: 10 * time.Minute}
	tests := []struct {
		name         string
		competition  string
		stage        string
		wantSettings matchVerificationSettings
	}{
		{"defaults", `{}`, ``, defaults},
		{"retired keys are ignored",
			`{"matchVerification":{"confirmationWindowMinutes":30,"autoConfirmEnabled":true,"refereeMonitoring":"required"}}`, ``, defaults},
		{"out-of-range values keep the defaults",
			`{"matchVerification":{"reportWindowMinutes":90,"responseWindowMinutes":1,"reminderBeforeDeadlineMinutes":0}}`, ``, defaults},
		{"valid overrides",
			`{"matchVerification":{"reportWindowMinutes":20,"reminderBeforeDeadlineMinutes":5,"responseWindowMinutes":15}}`, ``,
			matchVerificationSettings{ReportWindow: 20 * time.Minute, ReminderLead: 5 * time.Minute, ResponseWindow: 15 * time.Minute}},
		{"a reminder at or after the window is clamped inside it",
			`{"matchVerification":{"reportWindowMinutes":5,"reminderBeforeDeadlineMinutes":30}}`, ``,
			matchVerificationSettings{ReportWindow: 5 * time.Minute, ReminderLead: 4 * time.Minute, ResponseWindow: 10 * time.Minute}},
		{"a shorter stage window clamps the competition reminder",
			`{"matchVerification":{"reportWindowMinutes":30,"reminderBeforeDeadlineMinutes":20}}`,
			`{"matchVerification":{"reportWindowMinutes":10}}`,
			matchVerificationSettings{ReportWindow: 10 * time.Minute, ReminderLead: 9 * time.Minute, ResponseWindow: 10 * time.Minute}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := resolveMatchSettings("efootball-mobile", "single_elimination", []byte(test.competition), []byte(test.stage)).Verification
			if settings != test.wantSettings {
				t.Fatalf("settings = %+v, want %+v", settings, test.wantSettings)
			}
		})
	}
}

// Paid entry is collected through M-Pesa only, so a paid competition is open
// to players in Kenya alone; free competitions stay open to every country.
func TestCompetitionEligibilityLimitsPaidEntryToKenya(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	accountID, gameID, status := "account", "efootball-mobile", "unverified"
	facts := competitionEligibilityFacts{
		CompetitionID: "competition", GameID: gameID, Status: "registration_open",
		MaxEntries: 32, EntryFeeMinor: 10_000, Currency: "KES", ProfileComplete: true, CountryCode: "IN",
		RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(time.Hour), StartsAt: now.Add(48 * time.Hour),
		GameAccountID: &accountID, GameAccountGameID: &gameID, GameAccountStatus: &status,
	}
	open := competitionEligibilityRules{AllowedCountries: []string{}}
	result := assessCompetitionEligibility(facts, open, now)
	if result.Eligible || len(result.Issues) == 0 || result.Issues[0].Code != "country_not_allowed" ||
		!strings.Contains(result.Issues[0].Message, "M-Pesa") {
		t.Fatalf("an Indian player could enter a paid competition: %+v", result)
	}
	// An explicit country list that excludes the player reports only once.
	result = assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{"KE"}}, now)
	countryIssues := 0
	for _, issue := range result.Issues {
		if issue.Code == "country_not_allowed" {
			countryIssues++
		}
	}
	if countryIssues != 1 {
		t.Fatalf("country issues = %d, want 1: %+v", countryIssues, result.Issues)
	}
	facts.CountryCode = "ke"
	if result = assessCompetitionEligibility(facts, open, now); !result.Eligible {
		t.Fatalf("a Kenyan player cannot enter a paid competition: %+v", result)
	}
	facts.CountryCode, facts.EntryFeeMinor = "IN", 0
	if result = assessCompetitionEligibility(facts, open, now); !result.Eligible || !result.CanRegisterNow {
		t.Fatalf("an Indian player cannot enter a free competition: %+v", result)
	}
}
