package httpapi

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func progressionTestString(value string) *string { return &value }
func progressionTestInt(value int) *int          { return &value }
func progressionTestTime() *time.Time {
	value := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	return &value
}

func filledProgressionSlot(side, entryID string) progressionSlot {
	return progressionSlot{Side: side, ResolvedEntryID: progressionTestString(entryID), ResolvedAt: progressionTestTime()}
}

func voidProgressionSlot(side string) progressionSlot {
	return progressionSlot{Side: side, VoidedAt: progressionTestTime()}
}

// progressionTestField is a frozen draw field in which the removed entries are
// no longer live.
func progressionTestField(entryIDs []string, removed ...string) []progressionPlacementEntry {
	entries := make([]progressionPlacementEntry, len(entryIDs))
	for index, entryID := range entryIDs {
		entries[index] = progressionPlacementEntry{EntryID: entryID, Live: !slices.Contains(removed, entryID)}
	}
	return entries
}

func progressionTestPlacements(placements []progressionPlacement) map[string]int {
	result := make(map[string]int, len(placements))
	for _, placement := range placements {
		result[placement.EntryID] = placement.Placement
	}
	return result
}

func TestChildSettlementRequiresTwoResolvedSlotsAndActivation(t *testing.T) {
	home := filledProgressionSlot("home", "entry-home")
	away := progressionSlot{Side: "away"}
	plan, err := planChildSettlement(progressionActivationSatisfied, home, away)
	if err != nil || plan.State != "pending" || plan.Terminal {
		t.Fatalf("partial child plan = %+v, %v", plan, err)
	}
	away = filledProgressionSlot("away", "entry-away")
	plan, err = planChildSettlement(progressionActivationPending, home, away)
	if err != nil || plan.State != "pending" {
		t.Fatalf("unactivated child plan = %+v, %v", plan, err)
	}
	plan, err = planChildSettlement(progressionActivationSatisfied, home, away)
	if err != nil || plan.State != "ready" || plan.Terminal || plan.WinnerEntryID != nil {
		t.Fatalf("ready child plan = %+v, %v", plan, err)
	}
}

func TestChildSettlementIsVoidOrderIndependentAndNeverStrands(t *testing.T) {
	orders := []struct {
		first, second progressionSlot
	}{
		{first: voidProgressionSlot("home"), second: filledProgressionSlot("away", "survivor")},
		{first: filledProgressionSlot("away", "survivor"), second: voidProgressionSlot("home")},
	}
	var final childSettlementPlan
	for index, order := range orders {
		partialHome, partialAway := progressionSlot{Side: "home"}, progressionSlot{Side: "away"}
		if order.first.Side == "home" {
			partialHome = order.first
		} else {
			partialAway = order.first
		}
		partial, err := planChildSettlement(progressionActivationSatisfied, partialHome, partialAway)
		if err != nil || partial.State != "pending" {
			t.Fatalf("order %d partial plan = %+v, %v", index, partial, err)
		}
		if order.second.Side == "home" {
			partialHome = order.second
		} else {
			partialAway = order.second
		}
		plan, err := planChildSettlement(progressionActivationSatisfied, partialHome, partialAway)
		if err != nil || plan.State != "forfeit" || !plan.Terminal || plan.WinnerEntryID == nil || *plan.WinnerEntryID != "survivor" {
			t.Fatalf("order %d final plan = %+v, %v", index, plan, err)
		}
		if index == 0 {
			final = plan
		} else if plan.State != final.State || *plan.WinnerEntryID != *final.WinnerEntryID || *plan.CompletionReason != *final.CompletionReason {
			t.Fatalf("settlement depends on arrival order: first=%+v second=%+v", final, plan)
		}
	}

	cancelled, err := planChildSettlement(progressionActivationSatisfied,
		voidProgressionSlot("home"), voidProgressionSlot("away"))
	if err != nil || cancelled.State != "cancelled" || !cancelled.Terminal ||
		cancelled.CompletionReason == nil || *cancelled.CompletionReason != "double_no_show" {
		t.Fatalf("double-void plan = %+v, %v", cancelled, err)
	}
}

func TestConditionalResetActivatesOnlyWhenSourceAwayWins(t *testing.T) {
	home, away := progressionTestString("winners-finalist"), progressionTestString("losers-finalist")
	if got := conditionalProgressionActivation("completed", away, away); got != progressionActivationSatisfied {
		t.Fatalf("away winner activation = %d", got)
	}
	for name, got := range map[string]progressionActivation{
		"home won":   conditionalProgressionActivation("completed", home, away),
		"no contest": conditionalProgressionActivation("cancelled", nil, away),
		"no winner":  conditionalProgressionActivation("completed", nil, away),
	} {
		if got != progressionActivationFailed {
			t.Fatalf("%s activation = %d", name, got)
		}
	}
	if got := conditionalProgressionActivation("in_progress", nil, away); got != progressionActivationPending {
		t.Fatalf("unfinished activation = %d", got)
	}

	plan, err := planChildSettlement(progressionActivationFailed,
		filledProgressionSlot("home", *home), filledProgressionSlot("away", *away))
	if err != nil || plan.State != "cancelled" || !plan.Terminal ||
		plan.CompletionReason == nil || *plan.CompletionReason != "reset_not_required" {
		t.Fatalf("unneeded reset plan = %+v, %v", plan, err)
	}
}

func TestWinnerAndLoserEdgesMirrorTheSourceSides(t *testing.T) {
	home, away := progressionTestString("home-entry"), progressionTestString("away-entry")
	if loser := losingEntry(home, away, home); loser == nil || *loser != *away {
		t.Fatalf("home-win loser = %v", loser)
	}
	if loser := losingEntry(home, away, away); loser == nil || *loser != *home {
		t.Fatalf("away-win loser = %v", loser)
	}
	if loser := losingEntry(home, away, nil); loser != nil {
		t.Fatalf("no-winner loser = %v", *loser)
	}
}

func TestPendingPaidWithdrawalCannotAdvance(t *testing.T) {
	for _, status := range []string{"withdrawal_pending", "withdrawn", "disqualified"} {
		if progressionEntryIsLive(&status) {
			t.Errorf("entry status %s was treated as live", status)
		}
	}
	for _, status := range []string{"registered", "checked_in", "accepted"} {
		if !progressionEntryIsLive(&status) {
			t.Errorf("entry status %s was treated as inactive", status)
		}
	}
}

func TestRoundRobinStandingDeltasCoverWinDrawLossGoalsAndWalkover(t *testing.T) {
	home, away := progressionTestString("000-home"), progressionTestString("999-away")
	homeScore, awayScore := progressionTestInt(3), progressionTestInt(1)
	deltas, err := roundRobinStandingDeltas(progressionSource{
		State: "completed", HomeEntryID: home, AwayEntryID: away, WinnerEntryID: home,
		HomeScore: homeScore, AwayScore: awayScore,
	})
	if err != nil || len(deltas) != 2 {
		t.Fatalf("home win deltas = %+v, %v", deltas, err)
	}
	if deltas[0].Wins != 1 || deltas[0].Points != 3 || deltas[0].GoalsFor != 3 || deltas[0].GoalsAgainst != 1 {
		t.Fatalf("home delta = %+v", deltas[0])
	}
	if deltas[1].Losses != 1 || deltas[1].Points != 0 || deltas[1].GoalsFor != 1 || deltas[1].GoalsAgainst != 3 {
		t.Fatalf("away delta = %+v", deltas[1])
	}

	drawScore := progressionTestInt(2)
	deltas, err = roundRobinStandingDeltas(progressionSource{
		State: "completed", HomeEntryID: home, AwayEntryID: away, HomeScore: drawScore, AwayScore: drawScore,
	})
	if err != nil || deltas[0].Draws != 1 || deltas[1].Draws != 1 || deltas[0].Points != 1 || deltas[1].Points != 1 {
		t.Fatalf("draw deltas = %+v, %v", deltas, err)
	}

	deltas, err = roundRobinStandingDeltas(progressionSource{
		State: "forfeit", HomeEntryID: home, AwayEntryID: away, WinnerEntryID: away,
	})
	if err != nil || deltas[0].Losses != 1 || deltas[1].Wins != 1 || deltas[1].Walkovers != 1 || deltas[1].Points != 3 {
		t.Fatalf("walkover deltas = %+v, %v", deltas, err)
	}
}

func TestRoundRobinPlacementsUseCompetitionRankingAndPreserveExactTies(t *testing.T) {
	entries := progressionTestField([]string{"alpha", "bravo", "charlie", "delta"})
	placements, err := rankRoundRobinPlacements(entries, []progressionStandingForPlacement{
		{EntryID: "delta", Points: 1, GoalsFor: 1, GoalsAgainst: 5},
		{EntryID: "bravo", Points: 9, GoalsFor: 7, GoalsAgainst: 2},
		{EntryID: "charlie", Points: 4, GoalsFor: 3, GoalsAgainst: 3},
		{EntryID: "alpha", Points: 9, GoalsFor: 7, GoalsAgainst: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"alpha": 1, "bravo": 1, "charlie": 3, "delta": 4}
	for _, placement := range placements {
		if placement.Placement != want[placement.EntryID] {
			t.Errorf("%s placement = %d, want %d", placement.EntryID, placement.Placement, want[placement.EntryID])
		}
	}
}

func TestSingleEliminationPlacementsCoverTheFrozenFieldAndBronze(t *testing.T) {
	entries := progressionTestField([]string{"a", "b", "c", "d", "e", "f", "g", "h"})
	matches := []progressionMatchForPlacement{
		{ID: "q1", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("h"), WinnerEntryID: progressionTestString("a")},
		{ID: "q2", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("e"), WinnerEntryID: progressionTestString("d")},
		{ID: "q3", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("g"), WinnerEntryID: progressionTestString("b")},
		{ID: "q4", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("c"), AwayEntryID: progressionTestString("f"), WinnerEntryID: progressionTestString("c")},
		{ID: "s1", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
		{ID: "s2", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
		{ID: "final", Bracket: "main", RoundNumber: 3, GraphRank: 3, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
		{ID: "bronze", Bracket: "bronze", RoundNumber: 3, GraphRank: 3, State: "completed", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("c")},
	}
	placements, err := rankEliminationPlacements(entries, matches, "single_elimination")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 5, "g": 5, "h": 5}
	if len(placements) != len(want) {
		t.Fatalf("placement count = %d, want %d", len(placements), len(want))
	}
	for _, placement := range placements {
		if placement.Placement != want[placement.EntryID] {
			t.Errorf("%s placement = %d, want %d", placement.EntryID, placement.Placement, want[placement.EntryID])
		}
	}
}

func TestDoubleEliminationPlacementsIgnoreUnneededReset(t *testing.T) {
	resetReason := "reset_not_required"
	entries := progressionTestField([]string{"a", "b", "c", "d"})
	matches := []progressionMatchForPlacement{
		{ID: "w1", Bracket: "winners", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
		{ID: "w2", Bracket: "winners", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
		{ID: "wf", Bracket: "winners", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
		{ID: "l1", Bracket: "losers", RoundNumber: 1, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("c")},
		{ID: "lf", Bracket: "losers", RoundNumber: 2, GraphRank: 3, State: "completed", HomeEntryID: progressionTestString("c"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("b")},
		{ID: "gf1", Bracket: "grand_final", RoundNumber: 1, GraphRank: 4, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
		{ID: "gf2", Bracket: "grand_final", RoundNumber: 2, GraphRank: 5, State: "cancelled", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("a"), CompletionReason: &resetReason},
	}
	placements, err := rankEliminationPlacements(entries, matches, "double_elimination")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"a": 1, "b": 2, "c": 3, "d": 4}
	for _, placement := range placements {
		if placement.Placement != want[placement.EntryID] {
			t.Errorf("%s placement = %d, want %d", placement.EntryID, placement.Placement, want[placement.EntryID])
		}
	}
}

func TestPlanRoundFixtureSettlementPlaysOnlyLiveFixtures(t *testing.T) {
	cases := []struct {
		name               string
		homeLive, awayLive bool
		wantState          string
		wantWinner         *string
		wantReason         string
		wantNotLive        []string
	}{
		{name: "both live", homeLive: true, awayLive: true, wantState: "ready"},
		{name: "away removed", homeLive: true, wantState: "forfeit", wantWinner: progressionTestString("home"),
			wantReason: "walkover", wantNotLive: []string{"away"}},
		{name: "home removed", awayLive: true, wantState: "forfeit", wantWinner: progressionTestString("away"),
			wantReason: "walkover", wantNotLive: []string{"home"}},
		{name: "both removed", wantState: "cancelled", wantReason: "double_no_show",
			wantNotLive: []string{"home", "away"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := planRoundFixtureSettlement("home", "away", tc.homeLive, tc.awayLive)
			if got.State != tc.wantState || !sameOptionalString(got.WinnerEntryID, tc.wantWinner) ||
				got.CompletionReason != tc.wantReason || !slices.Equal(got.NotLiveEntryIDs, tc.wantNotLive) {
				t.Fatalf("settlement = %+v, want state=%s winner=%v reason=%q notLive=%v",
					got, tc.wantState, tc.wantWinner, tc.wantReason, tc.wantNotLive)
			}
		})
	}
}

func TestRoundRobinPlacementsRankLiveEntriesOnly(t *testing.T) {
	field := []string{"alpha", "bravo", "charlie", "delta"}
	standings := []progressionStandingForPlacement{
		{EntryID: "alpha", Points: 6, GoalsFor: 4, GoalsAgainst: 2},
		{EntryID: "bravo", Points: 3, GoalsFor: 3, GoalsAgainst: 3},
		{EntryID: "charlie", Points: 0, GoalsFor: 1, GoalsAgainst: 4},
		{EntryID: "delta", Points: 9, GoalsFor: 6, GoalsAgainst: 1},
	}
	cases := []struct {
		name      string
		entries   []progressionPlacementEntry
		standings []progressionStandingForPlacement
		want      map[string]int
		wantErr   error
	}{
		{name: "removed leader is omitted and the table re-ranks",
			entries: progressionTestField(field, "delta"), standings: standings,
			want: map[string]int{"alpha": 1, "bravo": 2, "charlie": 3}},
		{name: "no live entry writes no placement",
			entries: progressionTestField(field, field...), standings: standings, want: map[string]int{}},
		{name: "a removed entry still needs its standings row",
			entries: progressionTestField(field, "delta"), standings: standings[:3], wantErr: errMatchProgressionInvalid},
		{name: "standings outside the field conflict",
			entries: progressionTestField([]string{"alpha", "bravo", "charlie", "echo"}, "echo"), standings: standings,
			wantErr: errMatchProgressionConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			placements, err := rankRoundRobinPlacements(tc.entries, tc.standings)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if got := progressionTestPlacements(placements); tc.wantErr == nil && !maps.Equal(got, tc.want) {
				t.Fatalf("placements = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEliminationPlacementsOmitRemovedEntriesWithoutPromotion(t *testing.T) {
	walkover, doubleNoShow := "walkover", "double_no_show"
	cases := []struct {
		name    string
		format  string
		entries []progressionPlacementEntry
		matches []progressionMatchForPlacement
		want    map[string]int
	}{
		{
			name: "cancelled final between removed finalists has no champion", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}, "a", "b"),
			matches: []progressionMatchForPlacement{
				{ID: "s1", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
				{ID: "s2", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
				{ID: "final", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "cancelled", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), CompletionReason: &doubleNoShow},
			},
			want: map[string]int{"c": 3, "d": 3},
		},
		{
			name: "bronze walkover after a removed semifinal loser", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}, "c"),
			matches: []progressionMatchForPlacement{
				{ID: "s1", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
				{ID: "s2", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "forfeit", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
				{ID: "final", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
				{ID: "bronze", Bracket: "bronze", RoundNumber: 2, GraphRank: 2, State: "forfeit", HomeEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("d"), CompletionReason: &walkover},
			},
			want: map[string]int{"a": 1, "b": 2, "d": 3},
		},
		{
			name: "removed winners finalist leaves the reset to the losers champion", format: "double_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}, "a"),
			matches: []progressionMatchForPlacement{
				{ID: "w1", Bracket: "winners", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
				{ID: "w2", Bracket: "winners", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
				{ID: "wf", Bracket: "winners", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
				{ID: "l1", Bracket: "losers", RoundNumber: 1, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("c")},
				{ID: "lf", Bracket: "losers", RoundNumber: 2, GraphRank: 3, State: "completed", HomeEntryID: progressionTestString("c"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("b")},
				{ID: "gf1", Bracket: "grand_final", RoundNumber: 1, GraphRank: 4, State: "forfeit", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("b")},
				{ID: "gf2", Bracket: "grand_final", RoundNumber: 2, GraphRank: 5, State: "forfeit", AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("b"), CompletionReason: &walkover},
			},
			want: map[string]int{"b": 1, "c": 3, "d": 4},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			placements, err := rankEliminationPlacements(tc.entries, tc.matches, tc.format)
			if err != nil {
				t.Fatal(err)
			}
			if got := progressionTestPlacements(placements); !maps.Equal(got, tc.want) {
				t.Fatalf("placements = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProgressionOutcomeHashIsStableAndDetectsMaterialChanges(t *testing.T) {
	winner := progressionTestString("entry-a")
	home, away := progressionTestInt(2), progressionTestInt(1)
	base := progressionOutcome{MatchID: "match", MatchVersion: 7, State: "completed", WinnerEntryID: winner, HomeScore: home, AwayScore: away}
	rawA, hashA, err := base.canonical()
	if err != nil {
		t.Fatal(err)
	}
	rawB, hashB, err := base.canonical()
	if err != nil || !bytes.Equal(rawA, rawB) || hashA != hashB {
		t.Fatalf("same outcome was not canonical: %q/%x vs %q/%x", rawA, hashA, rawB, hashB)
	}
	changed := base
	changed.AwayScore = progressionTestInt(0)
	_, changedHash, err := changed.canonical()
	if err != nil || changedHash == hashA {
		t.Fatal("different score reused the outcome hash")
	}
	changed = base
	changed.WinnerEntryID = progressionTestString("entry-b")
	_, changedHash, err = changed.canonical()
	if err != nil || changedHash == hashA {
		t.Fatal("different winner reused the outcome hash")
	}
}

func TestValidateMatchProgressionInputRejectsHalfScoresAndNonFinalState(t *testing.T) {
	valid := matchProgressionInput{
		MatchID: "20e3f259-8c3c-4a02-a07a-4aa003689c67", CompetitionID: "13802037-7a27-40cb-850d-d1affcb9c729",
		FinalizedVersion: 4, FinalState: "completed", Cause: progressionCausePlayerConfirmation,
		HomeScore: progressionTestInt(1), AwayScore: progressionTestInt(0),
	}
	if err := validateMatchProgressionInput(valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	invalid := valid
	invalid.AwayScore = nil
	if err := validateMatchProgressionInput(invalid); !errors.Is(err, errMatchProgressionInvalid) {
		t.Fatalf("half score error = %v", err)
	}
	invalid = valid
	invalid.FinalState = "disputed"
	if err := validateMatchProgressionInput(invalid); !errors.Is(err, errMatchProgressionInvalid) {
		t.Fatalf("non-final state error = %v", err)
	}
}

func TestValidateMatchProgressionInputAcceptsOnlyCurrentCauses(t *testing.T) {
	cases := []struct {
		cause string
		valid bool
	}{
		{cause: progressionCausePlayerConfirmation, valid: true},
		{cause: progressionCauseTimeoutForfeit, valid: true},
		{cause: progressionCauseWithdrawal, valid: true},
		{cause: progressionCauseDisqualification, valid: true},
		{cause: progressionCauseAdminCorrection, valid: true},
		{cause: progressionCausePlatformReview, valid: true},
		{cause: "referee"},
		{cause: "draw"},
		{cause: ""},
	}
	for _, tc := range cases {
		input := matchProgressionInput{
			MatchID: "20e3f259-8c3c-4a02-a07a-4aa003689c67", CompetitionID: "13802037-7a27-40cb-850d-d1affcb9c729",
			FinalizedVersion: 4, FinalState: "forfeit", WinnerEntryID: progressionTestString("entry"), Cause: tc.cause,
		}
		err := validateMatchProgressionInput(input)
		if tc.valid && err != nil || !tc.valid && !errors.Is(err, errMatchProgressionInvalid) {
			t.Errorf("cause %q: error = %v, want valid=%v", tc.cause, err, tc.valid)
		}
	}
}

func TestMatchProgressionMigrationHasDurableConflictGuardAndSafeRollback(t *testing.T) {
	upPath := filepath.Join("..", "..", "migrations", "000015_match_progression.up.sql")
	downPath := filepath.Join("..", "..", "migrations", "000015_match_progression.down.sql")
	up, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"PRIMARY KEY (source_match_id, finalized_match_version)",
		"octet_length(outcome_hash) = 32",
		"application_result jsonb NOT NULL",
		"REFERENCES matches (id, competition_id) ON DELETE CASCADE",
		"'standings_updated'",
	} {
		if !strings.Contains(string(up), fragment) {
			t.Errorf("up migration does not contain %q", fragment)
		}
	}
	if !strings.Contains(string(down), "cannot roll back 000015_match_progression") {
		t.Fatal("down migration lacks an explicit standings audit guard")
	}
}

func TestProgressionSourceKeepsGateBeforeSourceAndChildLocks(t *testing.T) {
	path := filepath.Join("match_progression.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	gate := strings.Index(text, "lockCompetitionProgressionGate(ctx, tx, input.CompetitionID)")
	sourceLock := strings.Index(text, "lockProgressionSource(ctx, tx, input.MatchID)")
	if gate < 0 || sourceLock < 0 || gate > sourceLock {
		t.Fatalf("competition gate is not before the source match lock: gate=%d source=%d", gate, sourceLock)
	}
	edges, err := os.ReadFile("match_progression_edges.go")
	if err != nil {
		t.Fatal(err)
	}
	edgeText := string(edges)
	if !strings.Contains(edgeText, "ORDER BY target.graph_rank,target.id") {
		t.Fatal("child matches are not discovered in deterministic graph-rank/id order")
	}
}

func TestProgressionRoundReleaseRunsToAFixpointBeforeStageCompletion(t *testing.T) {
	source, err := os.ReadFile("match_progression.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	claim := strings.Index(text, "claimProgressionApplication(ctx, tx, current, outcomeRaw, outcomeHash)")
	release := strings.Index(text, "releaseProgressionRounds(ctx, tx, roundSources, &result)")
	requeue := strings.Index(text, "queue = append(queue, settled...)")
	stageLock := strings.Index(text, "SELECT status FROM competition_stages")
	if claim < 0 || release < claim || requeue < release || stageLock < requeue {
		t.Fatalf("fixpoint order claim=%d release=%d requeue=%d stageLock=%d", claim, release, requeue, stageLock)
	}
	rounds, err := os.ReadFile("match_progression_rounds.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(rounds), "AND version=$7 AND state='pending'"); got != 2 {
		t.Fatalf("readied and settled fixture updates with a version and state guard = %d, want 2", got)
	}
}
