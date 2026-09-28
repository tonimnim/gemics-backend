package httpapi

import (
	"strings"
	"testing"
	"time"
)

// eligibilityStrikesFacts is an eligible free registration for a player with
// the given strike count under the given ban threshold.
func eligibilityStrikesFacts(now time.Time, activeStrikes, threshold int) competitionEligibilityFacts {
	accountID, gameID := "account", "efootball-mobile"
	return competitionEligibilityFacts{
		CompetitionID: "competition", GameID: gameID, Status: "registration_open", MaxEntries: 32, Currency: "KES",
		RegistrationOpensAt: now.Add(-time.Hour), RegistrationClosesAt: now.Add(time.Hour), StartsAt: now.Add(24 * time.Hour),
		ProfileComplete: true, GameAccountID: &accountID, GameAccountGameID: &gameID,
		ActiveStrikes: activeStrikes, StrikeBanThreshold: threshold,
	}
}

func TestCompetitionEligibilityConductStrikeBan(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	registered := "registered"
	tests := []struct {
		name          string
		facts         competitionEligibilityFacts
		wantSuspended bool
		wantStatus    string
	}{
		{"below the threshold", eligibilityStrikesFacts(now, 2, 3), false, eligibilityStatusEligible},
		{"at the threshold", eligibilityStrikesFacts(now, 3, 3), true, eligibilityStatusIneligible},
		{"above the threshold", eligibilityStrikesFacts(now, 7, 3), true, eligibilityStatusIneligible},
		{"threshold zero disables the ban", eligibilityStrikesFacts(now, 20, 0), false, eligibilityStatusEligible},
		{"an existing registration is unaffected", func() competitionEligibilityFacts {
			facts := eligibilityStrikesFacts(now, 5, 3)
			facts.EntryStatus = &registered
			return facts
		}(), false, eligibilityStatusRegistered},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := assessCompetitionEligibility(test.facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
			suspended := false
			for _, issue := range result.Issues {
				if issue.Code == "conduct_suspended" {
					suspended = true
					if issue.Severity != "blocking" || issue.Category != "conduct" || !strings.Contains(issue.Message, "conduct strikes") {
						t.Fatalf("unexpected conduct issue %+v", issue)
					}
				}
			}
			if suspended != test.wantSuspended || result.Status != test.wantStatus {
				t.Fatalf("suspended=%v status=%s, want %v %s: %+v", suspended, result.Status, test.wantSuspended,
					test.wantStatus, result.Issues)
			}
			if test.wantSuspended && (result.Eligible || result.CanRegisterNow) {
				t.Fatalf("a suspended player may register: %+v", result)
			}
			if first := result.firstBlockingIssue(); test.wantSuspended && (first == nil || first.Code != "conduct_suspended") {
				t.Fatalf("registration would not report the ban first: %+v", first)
			}
		})
	}
}

func TestStrikeBanAppliesAtTheThresholdOnly(t *testing.T) {
	for _, test := range []struct {
		strikes, threshold int
		want               bool
	}{
		{0, 3, false}, {2, 3, false}, {3, 3, true}, {4, 3, true}, {1, 1, true}, {20, 0, false}, {0, 0, false},
	} {
		if got := strikeBanApplies(test.strikes, test.threshold); got != test.want {
			t.Fatalf("strikeBanApplies(%d, %d) = %v", test.strikes, test.threshold, got)
		}
	}
}

// Registration preflight, free registration, STK initiation and the verified
// payment callback all count active strikes the same way.
func TestConductStrikesShareOneDefinition(t *testing.T) {
	if !strings.Contains(activePlayerStrikesSQL, "strike.revoked_at IS NULL") ||
		!strings.Contains(activePlayerStrikesSQL, "strike.user_id=player.id") {
		t.Fatalf("active strikes are not counted per player and unrevoked: %s", activePlayerStrikesSQL)
	}
	assertFileContains(t, "competition_eligibility.go", "`+activePlayerStrikesSQL+`", "&facts.OccupiedEntries, &facts.ActiveStrikes,")
	assertFileContains(t, "payment_handlers.go", "`SELECT `+activePlayerStrikesSQL+` FROM users player WHERE player.id=$1`",
		"input.GameAccountID, now.UTC(), s.config.StrikeBanThreshold)")
	assertFileContains(t, "competition_handlers.go", "input.GameAccountID, now, s.config.StrikeBanThreshold,")
	assertFileContains(t, "competition_eligibility.go", "gameAccountID, time.Now().UTC(),\n\t\ts.config.StrikeBanThreshold)")
}
