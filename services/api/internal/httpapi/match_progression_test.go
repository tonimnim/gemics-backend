package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/bracket"
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

func TestEliminationPlacementsNeverLiftBronzeOrInventAChampion(t *testing.T) {
	resetReason, doubleNoShow := "reset_not_required", "double_no_show"
	eightField := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	eightEarlyRounds := []progressionMatchForPlacement{
		{ID: "q1", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("h"), WinnerEntryID: progressionTestString("a")},
		{ID: "q2", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("e"), WinnerEntryID: progressionTestString("d")},
		{ID: "q3", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("g"), WinnerEntryID: progressionTestString("b")},
		{ID: "q4", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("c"), AwayEntryID: progressionTestString("f"), WinnerEntryID: progressionTestString("c")},
		{ID: "s1", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
		{ID: "s2", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
	}
	fourSemifinals := []progressionMatchForPlacement{
		{ID: "s1", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
		{ID: "s2", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
	}
	cases := []struct {
		name    string
		format  string
		entries []progressionPlacementEntry
		matches []progressionMatchForPlacement
		want    map[string]int
	}{
		{
			name: "cancelled bronze leaves semifinal losers sharing third", format: "single_elimination",
			entries: progressionTestField(eightField),
			matches: slices.Concat(eightEarlyRounds, []progressionMatchForPlacement{
				{ID: "final", Bracket: "main", RoundNumber: 3, GraphRank: 3, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
				{ID: "bronze", Bracket: "bronze", RoundNumber: 3, GraphRank: 3, State: "cancelled", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), CompletionReason: &doubleNoShow},
			}),
			want: map[string]int{"a": 1, "b": 2, "c": 3, "d": 3, "e": 5, "f": 5, "g": 5, "h": 5},
		},
		{
			name: "cancelled bronze in a four-entry bracket", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}),
			matches: slices.Concat(fourSemifinals, []progressionMatchForPlacement{
				{ID: "final", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
				{ID: "bronze", Bracket: "bronze", RoundNumber: 2, GraphRank: 2, State: "cancelled", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), CompletionReason: &doubleNoShow},
			}),
			want: map[string]int{"a": 1, "b": 2, "c": 3, "d": 3},
		},
		{
			name: "cancelled final between live finalists has no first place", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}),
			matches: slices.Concat(fourSemifinals, []progressionMatchForPlacement{
				{ID: "final", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "cancelled", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), CompletionReason: &doubleNoShow},
			}),
			want: map[string]int{"a": 2, "b": 2, "c": 3, "d": 3},
		},
		{
			name: "cancelled final keeps a played bronze result", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}),
			matches: slices.Concat(fourSemifinals, []progressionMatchForPlacement{
				{ID: "final", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "cancelled", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), CompletionReason: &doubleNoShow},
				{ID: "bronze", Bracket: "bronze", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("c")},
			}),
			want: map[string]int{"a": 2, "b": 2, "c": 3, "d": 4},
		},
		{
			name: "cancelled final and bronze keep the deeper placements", format: "single_elimination",
			entries: progressionTestField(eightField),
			matches: slices.Concat(eightEarlyRounds, []progressionMatchForPlacement{
				{ID: "final", Bracket: "main", RoundNumber: 3, GraphRank: 3, State: "cancelled", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), CompletionReason: &doubleNoShow},
				{ID: "bronze", Bracket: "bronze", RoundNumber: 3, GraphRank: 3, State: "cancelled", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), CompletionReason: &doubleNoShow},
			}),
			want: map[string]int{"a": 2, "b": 2, "c": 3, "d": 3, "e": 5, "f": 5, "g": 5, "h": 5},
		},
		{
			// Byes give s2 a shorter path through the graph than s1, but both
			// are semifinals, so their losers share third.
			name: "byes do not split a round", format: "single_elimination",
			entries: progressionTestField([]string{"p1", "p2", "p3", "p4", "p5"}),
			matches: []progressionMatchForPlacement{
				{ID: "r1", Bracket: "main", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("p4"), AwayEntryID: progressionTestString("p5"), WinnerEntryID: progressionTestString("p4")},
				{ID: "s1", Bracket: "main", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("p1"), AwayEntryID: progressionTestString("p4"), WinnerEntryID: progressionTestString("p1")},
				{ID: "s2", Bracket: "main", RoundNumber: 2, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("p2"), AwayEntryID: progressionTestString("p3"), WinnerEntryID: progressionTestString("p2")},
				{ID: "final", Bracket: "main", RoundNumber: 3, GraphRank: 3, State: "completed", HomeEntryID: progressionTestString("p1"), AwayEntryID: progressionTestString("p2"), WinnerEntryID: progressionTestString("p1")},
				{ID: "bronze", Bracket: "bronze", RoundNumber: 3, GraphRank: 3, State: "cancelled", HomeEntryID: progressionTestString("p4"), AwayEntryID: progressionTestString("p3"), CompletionReason: &doubleNoShow},
			},
			want: map[string]int{"p1": 1, "p2": 2, "p3": 3, "p4": 3, "p5": 5},
		},
		{
			name: "cancelled grand final between live finalists has no first place", format: "double_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}),
			matches: []progressionMatchForPlacement{
				{ID: "w1", Bracket: "winners", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("d"), WinnerEntryID: progressionTestString("a")},
				{ID: "w2", Bracket: "winners", RoundNumber: 1, GraphRank: 1, State: "completed", HomeEntryID: progressionTestString("b"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("b")},
				{ID: "wf", Bracket: "winners", RoundNumber: 2, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("a")},
				{ID: "l1", Bracket: "losers", RoundNumber: 1, GraphRank: 2, State: "completed", HomeEntryID: progressionTestString("d"), AwayEntryID: progressionTestString("c"), WinnerEntryID: progressionTestString("c")},
				{ID: "lf", Bracket: "losers", RoundNumber: 2, GraphRank: 3, State: "completed", HomeEntryID: progressionTestString("c"), AwayEntryID: progressionTestString("b"), WinnerEntryID: progressionTestString("b")},
				{ID: "gf1", Bracket: "grand_final", RoundNumber: 1, GraphRank: 4, State: "cancelled", HomeEntryID: progressionTestString("a"), AwayEntryID: progressionTestString("b"), CompletionReason: &doubleNoShow},
				{ID: "gf2", Bracket: "grand_final", RoundNumber: 2, GraphRank: 5, State: "cancelled", CompletionReason: &resetReason},
			},
			want: map[string]int{"a": 2, "b": 2, "c": 3, "d": 4},
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

type eliminationPlacementCase struct {
	name    string
	format  string
	entries []progressionPlacementEntry
	matches []progressionMatchForPlacement
	want    map[string]int
}

func runEliminationPlacementCases(t *testing.T, cases []eliminationPlacementCase) {
	t.Helper()
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

// progressionTestMatch is a terminal match; winner "" means no winner, and an
// empty home or away is a voided slot.
func progressionTestMatch(id, bracketName string, round, rank int, state, home, away, winner string,
	reason string, voided ...string) progressionMatchForPlacement {
	optional := func(value string) *string {
		if value == "" {
			return nil
		}
		return progressionTestString(value)
	}
	return progressionMatchForPlacement{ID: id, Bracket: bracketName, RoundNumber: round, GraphRank: rank,
		State: state, HomeEntryID: optional(home), AwayEntryID: optional(away), WinnerEntryID: optional(winner),
		CompletionReason: optional(reason), VoidedEntryIDs: voided}
}

// The generator numbers main-bracket rounds over the full power-of-two tree,
// but bye pruning leaves matches of one round at different graph ranks. These
// shapes are what bracket.Emit produces for 5 and 10 entries with a bronze.
func TestSingleEliminationPlacementsRankByRoundInByeBrackets(t *testing.T) {
	fiveField := []string{"p01", "p02", "p03", "p04", "p05"}
	fiveEarlyRounds := []progressionMatchForPlacement{
		progressionTestMatch("r1m2", "main", 1, 1, "completed", "p04", "p05", "p04", ""),
		progressionTestMatch("r2m1", "main", 2, 2, "completed", "p01", "p04", "p04", ""),
		progressionTestMatch("r2m2", "main", 2, 1, "completed", "p02", "p03", "p02", ""),
	}
	tenField := []string{"p01", "p02", "p03", "p04", "p05", "p06", "p07", "p08", "p09", "p10"}
	runEliminationPlacementCases(t, []eliminationPlacementCase{
		{
			name: "five entries, cancelled bronze: semifinal losers share third", format: "single_elimination",
			entries: progressionTestField(fiveField),
			matches: slices.Concat(fiveEarlyRounds, []progressionMatchForPlacement{
				progressionTestMatch("final", "main", 3, 3, "completed", "p04", "p02", "p04", ""),
				progressionTestMatch("bronze", "bronze", 3, 3, "cancelled", "p01", "p03", "", "double_no_show"),
			}),
			want: map[string]int{"p01": 3, "p02": 2, "p03": 3, "p04": 1, "p05": 5},
		},
		{
			name: "five entries, played bronze: the first-round loser stays fifth", format: "single_elimination",
			entries: progressionTestField(fiveField),
			matches: slices.Concat(fiveEarlyRounds, []progressionMatchForPlacement{
				progressionTestMatch("final", "main", 3, 3, "completed", "p04", "p02", "p02", ""),
				progressionTestMatch("bronze", "bronze", 3, 3, "completed", "p01", "p03", "p01", ""),
			}),
			want: map[string]int{"p01": 3, "p02": 1, "p03": 4, "p04": 2, "p05": 5},
		},
		{
			name: "five entries, bronze won on a timeout forfeit", format: "single_elimination",
			entries: progressionTestField(fiveField),
			matches: slices.Concat(fiveEarlyRounds, []progressionMatchForPlacement{
				progressionTestMatch("final", "main", 3, 3, "completed", "p04", "p02", "p04", ""),
				progressionTestMatch("bronze", "bronze", 3, 3, "forfeit", "p01", "p03", "p03", "timeout_forfeit"),
			}),
			want: map[string]int{"p01": 4, "p02": 2, "p03": 3, "p04": 1, "p05": 5},
		},
		{
			name: "five entries without a bronze match", format: "single_elimination",
			entries: progressionTestField(fiveField),
			matches: slices.Concat(fiveEarlyRounds, []progressionMatchForPlacement{
				progressionTestMatch("final", "main", 3, 3, "completed", "p04", "p02", "p04", ""),
			}),
			want: map[string]int{"p01": 3, "p02": 2, "p03": 3, "p04": 1, "p05": 5},
		},
		{
			name: "ten entries, cancelled bronze: quarterfinal losers share fifth", format: "single_elimination",
			entries: progressionTestField(tenField),
			matches: []progressionMatchForPlacement{
				progressionTestMatch("r1m2", "main", 1, 1, "completed", "p08", "p09", "p08", ""),
				progressionTestMatch("r1m6", "main", 1, 1, "completed", "p07", "p10", "p07", ""),
				progressionTestMatch("r2m1", "main", 2, 2, "completed", "p01", "p08", "p01", ""),
				progressionTestMatch("r2m2", "main", 2, 1, "completed", "p04", "p05", "p04", ""),
				progressionTestMatch("r2m3", "main", 2, 2, "completed", "p02", "p07", "p02", ""),
				progressionTestMatch("r2m4", "main", 2, 1, "completed", "p03", "p06", "p03", ""),
				progressionTestMatch("r3m1", "main", 3, 3, "completed", "p01", "p04", "p01", ""),
				progressionTestMatch("r3m2", "main", 3, 3, "completed", "p02", "p03", "p02", ""),
				progressionTestMatch("final", "main", 4, 4, "completed", "p01", "p02", "p01", ""),
				progressionTestMatch("bronze", "bronze", 4, 4, "cancelled", "p04", "p03", "", "double_no_show"),
			},
			want: map[string]int{"p01": 1, "p02": 2, "p03": 3, "p04": 3, "p05": 5, "p06": 5, "p07": 5, "p08": 5,
				"p09": 9, "p10": 9},
		},
	})
}

// progressionTestDropsTo records the round a winners match's loser drops into,
// as the stored graph reports it.
func progressionTestDropsTo(match progressionMatchForPlacement, bracketName string,
	round int) progressionMatchForPlacement {
	match.LoserDrop = &progressionPlacementRound{Bracket: bracketName, Round: round}
	return match
}

// progressionTestWith replaces the matches that share an id with a
// replacement, and appends a replacement no match shares an id with.
func progressionTestWith(matches []progressionMatchForPlacement,
	replacements ...progressionMatchForPlacement) []progressionMatchForPlacement {
	result := slices.Clone(matches)
	for _, replacement := range replacements {
		index := slices.IndexFunc(result, func(match progressionMatchForPlacement) bool {
			return match.ID == replacement.ID
		})
		if index < 0 {
			result = append(result, replacement)
			continue
		}
		result[index] = replacement
	}
	return result
}

// progressionTestDoubleEliminationEight is an eight-entry double elimination
// up to its grand final, wired as bracket.Emit wires it. a wins the winners
// bracket, b the losers bracket; c goes out in the losers final, d in losers
// round 3, e and f in losers round 2, and g and h in losers round 1.
func progressionTestDoubleEliminationEight() []progressionMatchForPlacement {
	return []progressionMatchForPlacement{
		progressionTestDropsTo(progressionTestMatch("w1m1", "winners", 1, 1, "completed", "a", "h", "a", ""), "losers", 1),
		progressionTestDropsTo(progressionTestMatch("w1m2", "winners", 1, 1, "completed", "d", "e", "d", ""), "losers", 1),
		progressionTestDropsTo(progressionTestMatch("w1m3", "winners", 1, 1, "completed", "b", "g", "b", ""), "losers", 1),
		progressionTestDropsTo(progressionTestMatch("w1m4", "winners", 1, 1, "completed", "c", "f", "c", ""), "losers", 1),
		progressionTestDropsTo(progressionTestMatch("w2m1", "winners", 2, 2, "completed", "a", "d", "a", ""), "losers", 2),
		progressionTestDropsTo(progressionTestMatch("w2m2", "winners", 2, 2, "completed", "b", "c", "b", ""), "losers", 2),
		progressionTestDropsTo(progressionTestMatch("w3m1", "winners", 3, 3, "completed", "a", "b", "a", ""), "losers", 4),
		progressionTestMatch("l1m1", "losers", 1, 2, "completed", "h", "e", "e", ""),
		progressionTestMatch("l1m2", "losers", 1, 2, "completed", "g", "f", "f", ""),
		progressionTestMatch("l2m1", "losers", 2, 3, "completed", "e", "c", "c", ""),
		progressionTestMatch("l2m2", "losers", 2, 3, "completed", "f", "d", "d", ""),
		progressionTestMatch("l3m1", "losers", 3, 4, "completed", "c", "d", "c", ""),
		progressionTestMatch("l4m1", "losers", 4, 5, "completed", "c", "b", "b", ""),
	}
}

// Without a champion nobody is first, the finalists share second, and every
// other entry keeps the place it would have had with a champion: the exits
// below the championship are always competition-ranked from third.
func TestEliminationPlacementsWithoutAChampionNeverPlaceFirst(t *testing.T) {
	eightField := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	quarterfinals := []progressionMatchForPlacement{
		progressionTestMatch("q1", "main", 1, 1, "completed", "a", "h", "a", ""),
		progressionTestMatch("q2", "main", 1, 1, "completed", "d", "e", "d", ""),
		progressionTestMatch("q3", "main", 1, 1, "completed", "b", "g", "b", ""),
		progressionTestMatch("q4", "main", 1, 1, "completed", "c", "f", "c", ""),
	}
	fourSemifinalNoShows := []progressionMatchForPlacement{
		progressionTestMatch("s1", "main", 1, 1, "cancelled", "a", "d", "", "double_no_show"),
		progressionTestMatch("s2", "main", 1, 1, "cancelled", "b", "c", "", "double_no_show"),
		progressionTestMatch("final", "main", 2, 2, "cancelled", "", "", "", "double_no_show"),
	}
	doubleEliminationToTheGrandFinal := []progressionMatchForPlacement{
		progressionTestMatch("w1", "winners", 1, 1, "completed", "a", "d", "a", ""),
		progressionTestMatch("w2", "winners", 1, 1, "completed", "b", "c", "b", ""),
		progressionTestMatch("wf", "winners", 2, 2, "completed", "a", "b", "a", ""),
		progressionTestMatch("l1", "losers", 1, 2, "completed", "d", "c", "c", ""),
		progressionTestMatch("lf", "losers", 2, 3, "completed", "c", "b", "b", ""),
	}
	withChampion := map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 5, "g": 7, "h": 7}
	runEliminationPlacementCases(t, []eliminationPlacementCase{
		{
			// Both semifinals are double no-shows, so nobody reached the final.
			// Nobody is first, and the four semifinal exits share second.
			name: "empty final after two cancelled semifinals in a four-entry draw", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}),
			matches: fourSemifinalNoShows,
			want:    map[string]int{"a": 2, "b": 2, "c": 2, "d": 2},
		},
		{
			// Four semifinal exits share second, and the quarterfinal losers
			// keep their competition rank, fifth.
			name: "empty final after two cancelled semifinals", format: "single_elimination",
			entries: progressionTestField(eightField),
			matches: slices.Concat(quarterfinals, []progressionMatchForPlacement{
				progressionTestMatch("s1", "main", 2, 2, "cancelled", "a", "d", "", "double_no_show"),
				progressionTestMatch("s2", "main", 2, 2, "cancelled", "b", "c", "", "double_no_show"),
				progressionTestMatch("final", "main", 3, 3, "cancelled", "", "", "", "double_no_show"),
			}),
			want: map[string]int{"a": 2, "b": 2, "c": 2, "d": 2, "e": 5, "f": 5, "g": 5, "h": 5},
		},
		{
			// b won its semifinal and was removed while waiting; the other
			// semifinal was a double no-show. Without a champion nobody is
			// first, so b and the three semifinal exits all share second.
			// Holding a place apart for b would cost a placement beyond the
			// field in smaller draws, which competition ranks never allow.
			name: "a lone removed finalist without a champion shares second", format: "single_elimination",
			entries: progressionTestField(eightField, "b"),
			matches: slices.Concat(quarterfinals, []progressionMatchForPlacement{
				progressionTestMatch("s1", "main", 2, 2, "cancelled", "a", "d", "", "double_no_show"),
				progressionTestMatch("s2", "main", 2, 2, "completed", "b", "c", "b", ""),
				progressionTestMatch("final", "main", 3, 3, "cancelled", "", "", "", "double_no_show", "b"),
			}),
			want: map[string]int{"a": 2, "c": 2, "d": 2, "e": 5, "f": 5, "g": 5, "h": 5},
		},
		{
			name: "grand finalists removed while waiting keep the top places", format: "double_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}, "a", "b"),
			matches: slices.Concat(doubleEliminationToTheGrandFinal, []progressionMatchForPlacement{
				progressionTestMatch("gf1", "grand_final", 1, 4, "cancelled", "", "", "", "double_no_show", "a", "b"),
				progressionTestMatch("gf2", "grand_final", 2, 5, "cancelled", "", "", "", "reset_not_required"),
			}),
			want: map[string]int{"c": 3, "d": 4},
		},
		{
			name: "eight-entry double elimination with a champion", format: "double_elimination",
			entries: progressionTestField(eightField),
			matches: slices.Concat(progressionTestDoubleEliminationEight(), []progressionMatchForPlacement{
				progressionTestMatch("gf1", "grand_final", 1, 6, "completed", "a", "b", "a", ""),
				progressionTestMatch("gf2", "grand_final", 2, 7, "cancelled", "", "", "", "reset_not_required"),
			}),
			want: withChampion,
		},
		{
			// a is removed before the grand final, whose walkover to b
			// activates the reset; b is removed before the reset. Each
			// finalist reached a grand final, so both hold second, and
			// everyone else keeps the place the champion draw gives them.
			name: "grand finalists removed one at a time keep everyone else's places", format: "double_elimination",
			entries: progressionTestField(eightField, "a", "b"),
			matches: slices.Concat(progressionTestDoubleEliminationEight(), []progressionMatchForPlacement{
				progressionTestMatch("gf1", "grand_final", 1, 6, "forfeit", "", "b", "b", "walkover", "a"),
				progressionTestMatch("gf2", "grand_final", 2, 7, "cancelled", "", "", "", "double_no_show", "b"),
			}),
			want: map[string]int{"c": 3, "d": 4, "e": 5, "f": 5, "g": 7, "h": 7},
		},
		{
			name: "grand finalists removed together keep everyone else's places", format: "double_elimination",
			entries: progressionTestField(eightField, "a", "b"),
			matches: slices.Concat(progressionTestDoubleEliminationEight(), []progressionMatchForPlacement{
				progressionTestMatch("gf1", "grand_final", 1, 6, "cancelled", "", "", "", "double_no_show", "a", "b"),
				progressionTestMatch("gf2", "grand_final", 2, 7, "cancelled", "", "", "", "reset_not_required"),
			}),
			want: map[string]int{"c": 3, "d": 4, "e": 5, "f": 5, "g": 7, "h": 7},
		},
		{
			name: "live grand finalists of a cancelled reset share second", format: "double_elimination",
			entries: progressionTestField(eightField),
			matches: slices.Concat(progressionTestDoubleEliminationEight(), []progressionMatchForPlacement{
				progressionTestMatch("gf1", "grand_final", 1, 6, "completed", "a", "b", "b", ""),
				progressionTestMatch("gf2", "grand_final", 2, 7, "cancelled", "a", "b", "", "double_no_show"),
			}),
			want: map[string]int{"a": 2, "b": 2, "c": 3, "d": 4, "e": 5, "f": 5, "g": 7, "h": 7},
		},
	})
}

func TestEliminationPlacementsKeepRemovedEntriesWhereTheyWereDue(t *testing.T) {
	fourField := []string{"a", "b", "c", "d"}
	eightField := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	grandFinalWonByA := []progressionMatchForPlacement{
		progressionTestMatch("gf1", "grand_final", 1, 6, "completed", "a", "b", "a", ""),
		progressionTestMatch("gf2", "grand_final", 2, 7, "cancelled", "", "", "", "reset_not_required"),
	}
	doubleEliminationOpening := []progressionMatchForPlacement{
		progressionTestMatch("w1", "winners", 1, 1, "completed", "a", "d", "a", ""),
		progressionTestMatch("w2", "winners", 1, 1, "completed", "b", "c", "b", ""),
		progressionTestMatch("wf", "winners", 2, 2, "completed", "a", "b", "a", ""),
		progressionTestMatch("l1", "losers", 1, 2, "completed", "d", "c", "c", ""),
	}
	runEliminationPlacementCases(t, []eliminationPlacementCase{
		{
			// c beat b in the losers final and was removed before the grand
			// final. c was due in it, so b stays third and d fourth.
			name: "removed losers champion keeps second", format: "double_elimination",
			entries: progressionTestField(fourField, "c"),
			matches: slices.Concat(doubleEliminationOpening, []progressionMatchForPlacement{
				progressionTestMatch("lf", "losers", 2, 3, "completed", "c", "b", "c", ""),
				progressionTestMatch("gf1", "grand_final", 1, 4, "forfeit", "a", "", "a", "walkover", "c"),
				progressionTestMatch("gf2", "grand_final", 2, 5, "cancelled", "", "", "", "reset_not_required"),
			}),
			want: map[string]int{"a": 1, "b": 3, "d": 4},
		},
		{
			// a won the winners final and was removed before the grand final. The
			// walkover to the losers champion activates the reset, which has no
			// one in its other slot; a still holds second.
			name: "removed winners champion keeps second through the reset", format: "double_elimination",
			entries: progressionTestField(fourField, "a"),
			matches: slices.Concat(doubleEliminationOpening, []progressionMatchForPlacement{
				progressionTestMatch("lf", "losers", 2, 3, "completed", "c", "b", "b", ""),
				progressionTestMatch("gf1", "grand_final", 1, 4, "forfeit", "", "b", "b", "walkover", "a"),
				progressionTestMatch("gf2", "grand_final", 2, 5, "forfeit", "", "b", "b", "walkover"),
			}),
			want: map[string]int{"b": 1, "c": 3, "d": 4},
		},
		{
			// b won its semifinal and was removed before the final, which e won by
			// walkover. The bronze still decides third and fourth, and d, out in
			// the first round, stays fifth.
			name: "removed semifinal winner keeps second", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d", "e"}, "b"),
			matches: []progressionMatchForPlacement{
				progressionTestMatch("r1m2", "main", 1, 1, "completed", "d", "e", "e", ""),
				progressionTestMatch("r2m1", "main", 2, 2, "completed", "a", "e", "e", ""),
				progressionTestMatch("r2m2", "main", 2, 1, "completed", "b", "c", "b", ""),
				progressionTestMatch("final", "main", 3, 3, "forfeit", "e", "", "e", "walkover", "b"),
				progressionTestMatch("bronze", "bronze", 3, 3, "completed", "a", "c", "a", ""),
			},
			want: map[string]int{"a": 3, "c": 4, "d": 5, "e": 1},
		},
		{
			// p02 had a first-round bye and was removed before its semifinal, so
			// it never played. It still holds a semifinal place.
			name: "removed entry that never played keeps its first match's place", format: "single_elimination",
			entries: progressionTestField([]string{"p01", "p02", "p03", "p04", "p05"}, "p02"),
			matches: []progressionMatchForPlacement{
				progressionTestMatch("r1m2", "main", 1, 1, "completed", "p04", "p05", "p04", ""),
				progressionTestMatch("r2m1", "main", 2, 2, "completed", "p01", "p04", "p01", ""),
				progressionTestMatch("r2m2", "main", 2, 1, "forfeit", "", "p03", "p03", "walkover", "p02"),
				progressionTestMatch("final", "main", 3, 3, "completed", "p01", "p03", "p01", ""),
			},
			want: map[string]int{"p01": 1, "p03": 2, "p04": 3, "p05": 5},
		},
		{
			// d won its first winners match and was removed before the next.
			// Losing that match would have dropped d into losers round 2, so d
			// holds a losers-round-2 place and shares fifth with e, who went
			// out there; the losers-round-1 exits stay seventh.
			name: "removed unbeaten winners entry holds the losers round it would drop into", format: "double_elimination",
			entries: progressionTestField(eightField, "d"),
			matches: progressionTestWith(progressionTestDoubleEliminationEight(), slices.Concat(
				[]progressionMatchForPlacement{
					progressionTestDropsTo(progressionTestMatch("w2m1", "winners", 2, 2, "forfeit", "a", "", "a", "walkover", "d"),
						"losers", 2),
					progressionTestMatch("l2m2", "losers", 2, 3, "forfeit", "f", "", "f", "walkover"),
					progressionTestMatch("l3m1", "losers", 3, 4, "completed", "c", "f", "c", ""),
				}, grandFinalWonByA)...),
			want: map[string]int{"a": 1, "b": 2, "c": 3, "e": 5, "f": 4, "g": 7, "h": 7},
		},
		{
			// The same removal after d lost that winners match: d is voided
			// from losers round 2 itself, and every place is the same.
			name: "removed winners loser holds the losers round it dropped into", format: "double_elimination",
			entries: progressionTestField(eightField, "d"),
			matches: progressionTestWith(progressionTestDoubleEliminationEight(), slices.Concat(
				[]progressionMatchForPlacement{
					progressionTestMatch("l2m2", "losers", 2, 3, "forfeit", "f", "", "f", "walkover", "d"),
					progressionTestMatch("l3m1", "losers", 3, 4, "completed", "c", "f", "c", ""),
				}, grandFinalWonByA)...),
			want: map[string]int{"a": 1, "b": 2, "c": 3, "e": 5, "f": 4, "g": 7, "h": 7},
		},
		{
			// bracket.Emit's five-entry draw: byes prune losers round 1 and the
			// second losers round 2 match, so the loser of winners round 2
			// match 1 drops straight into losers round 3. e04, removed before
			// that match, holds the losers-round-3 place the stored graph
			// names, not the losers-round-2 place of the unpruned mapping, so
			// e03, out in losers round 2, stays fifth.
			name: "removed winners entry follows a drop that byes moved", format: "double_elimination",
			entries: progressionTestField([]string{"e01", "e02", "e03", "e04", "e05"}, "e04"),
			matches: []progressionMatchForPlacement{
				progressionTestDropsTo(progressionTestMatch("w1m2", "winners", 1, 1, "completed", "e04", "e05", "e04", ""),
					"losers", 2),
				progressionTestDropsTo(progressionTestMatch("w2m1", "winners", 2, 2, "forfeit", "e01", "", "e01", "walkover",
					"e04"), "losers", 3),
				progressionTestDropsTo(progressionTestMatch("w2m2", "winners", 2, 1, "completed", "e02", "e03", "e02", ""),
					"losers", 2),
				progressionTestDropsTo(progressionTestMatch("w3m1", "winners", 3, 3, "completed", "e01", "e02", "e01", ""),
					"losers", 4),
				progressionTestMatch("l2m1", "losers", 2, 2, "completed", "e05", "e03", "e05", ""),
				progressionTestMatch("l3m1", "losers", 3, 3, "forfeit", "e05", "", "e05", "walkover"),
				progressionTestMatch("l4m1", "losers", 4, 4, "completed", "e05", "e02", "e05", ""),
				progressionTestMatch("gf1", "grand_final", 1, 5, "completed", "e01", "e05", "e01", ""),
				progressionTestMatch("gf2", "grand_final", 2, 6, "cancelled", "", "", "", "reset_not_required"),
			},
			want: map[string]int{"e01": 1, "e02": 3, "e03": 5, "e05": 2},
		},
	})
}

// When nobody reached the championship's other slot, not even a removed
// entry, the deepest remaining exits share second: placements stay competition
// ranks and never exceed the field.
func TestSingleEliminationFinalWithoutRunnerUp(t *testing.T) {
	eightField := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	quarterfinals := []progressionMatchForPlacement{
		progressionTestMatch("q1", "main", 1, 1, "completed", "a", "h", "a", ""),
		progressionTestMatch("q2", "main", 1, 1, "completed", "d", "e", "d", ""),
		progressionTestMatch("q3", "main", 1, 1, "completed", "b", "g", "b", ""),
		progressionTestMatch("q4", "main", 1, 1, "completed", "c", "f", "c", ""),
	}
	runEliminationPlacementCases(t, []eliminationPlacementCase{
		{
			name: "semifinal double no-show: the semifinal exits share second", format: "single_elimination",
			entries: progressionTestField(eightField),
			matches: slices.Concat(quarterfinals, []progressionMatchForPlacement{
				progressionTestMatch("s1", "main", 2, 2, "cancelled", "a", "d", "", "double_no_show"),
				progressionTestMatch("s2", "main", 2, 2, "completed", "b", "c", "b", ""),
				progressionTestMatch("final", "main", 3, 3, "forfeit", "", "b", "b", "walkover"),
			}),
			want: map[string]int{"a": 2, "b": 1, "c": 2, "d": 2, "e": 5, "f": 5, "g": 5, "h": 5},
		},
		{
			name: "removed semifinal winner holds second", format: "single_elimination",
			entries: progressionTestField(eightField, "a"),
			matches: slices.Concat(quarterfinals, []progressionMatchForPlacement{
				progressionTestMatch("s1", "main", 2, 2, "completed", "a", "d", "a", ""),
				progressionTestMatch("s2", "main", 2, 2, "completed", "b", "c", "b", ""),
				progressionTestMatch("final", "main", 3, 3, "forfeit", "", "b", "b", "walkover", "a"),
			}),
			want: map[string]int{"b": 1, "c": 3, "d": 3, "e": 5, "f": 5, "g": 5, "h": 5},
		},
		{
			// e01 had a bye to the final; the other first-round match was a
			// double no-show.
			name: "three entries: first-round no-shows behind a bye", format: "single_elimination",
			entries: progressionTestField([]string{"e01", "e02", "e03"}),
			matches: []progressionMatchForPlacement{
				progressionTestMatch("r1m2", "main", 1, 1, "cancelled", "e02", "e03", "", "double_no_show"),
				progressionTestMatch("final", "main", 2, 2, "forfeit", "e01", "", "e01", "walkover"),
			},
			want: map[string]int{"e01": 1, "e02": 2, "e03": 2},
		},
		{
			// The bronze walkover has no loser, so it overrides nothing, and
			// c shares second with the semifinal no-shows.
			name: "semifinal no-shows and a bronze walkover", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}),
			matches: []progressionMatchForPlacement{
				progressionTestMatch("s1", "main", 1, 1, "cancelled", "a", "d", "", "double_no_show"),
				progressionTestMatch("s2", "main", 1, 1, "completed", "b", "c", "b", ""),
				progressionTestMatch("final", "main", 2, 2, "forfeit", "", "b", "b", "walkover"),
				progressionTestMatch("bronze", "bronze", 2, 2, "forfeit", "", "c", "c", "walkover"),
			},
			want: map[string]int{"a": 2, "b": 1, "c": 2, "d": 2},
		},
		{
			// A five-entry draw with a bye: nobody is placed beyond fifth.
			name: "five entries: a semifinal double no-show never places anyone sixth", format: "single_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d", "e"}),
			matches: []progressionMatchForPlacement{
				progressionTestMatch("r1m2", "main", 1, 1, "completed", "d", "e", "d", ""),
				progressionTestMatch("s1", "main", 2, 2, "cancelled", "a", "d", "", "double_no_show"),
				progressionTestMatch("s2", "main", 2, 1, "completed", "b", "c", "c", ""),
				progressionTestMatch("final", "main", 3, 3, "forfeit", "", "c", "c", "walkover"),
			},
			want: map[string]int{"a": 2, "b": 2, "c": 1, "d": 2, "e": 5},
		},
		{
			// The losers final is a double no-show, so the grand final is a
			// walkover; c, out in losers round one, is fourth of four.
			name: "four-entry double elimination never places anyone fifth", format: "double_elimination",
			entries: progressionTestField([]string{"a", "b", "c", "d"}),
			matches: []progressionMatchForPlacement{
				progressionTestDropsTo(progressionTestMatch("w1", "winners", 1, 1, "completed", "a", "d", "a", ""), "losers", 1),
				progressionTestDropsTo(progressionTestMatch("w2", "winners", 1, 1, "completed", "b", "c", "b", ""), "losers", 1),
				progressionTestMatch("l1", "losers", 1, 2, "completed", "d", "c", "d", ""),
				progressionTestDropsTo(progressionTestMatch("wf", "winners", 2, 2, "completed", "a", "b", "a", ""), "losers", 2),
				progressionTestMatch("lf", "losers", 2, 3, "cancelled", "d", "b", "", "double_no_show"),
				progressionTestMatch("gf1", "grand_final", 1, 4, "forfeit", "a", "", "a", "walkover"),
				progressionTestMatch("gf2", "grand_final", 2, 5, "cancelled", "", "", "", "reset_not_required"),
			},
			want: map[string]int{"a": 1, "b": 2, "c": 4, "d": 2},
		},
	})
}

// progressionTestPlayHomeWins plays a bracket.Emit draw in which every home
// side wins, stored as progression stores it: each slot resolves from its
// source, the reset is cancelled as not required, and every match records the
// round its loser drops into.
func progressionTestPlayHomeWins(t *testing.T, format bracket.Format,
	entryCount int) ([]string, []progressionMatchForPlacement) {
	t.Helper()
	draw := make([]bracket.DrawEntry, entryCount)
	entryIDs := make([]string, entryCount)
	for index := range draw {
		entryIDs[index] = fmt.Sprintf("e%02d", index+1)
		draw[index] = bracket.DrawEntry{EntryID: entryIDs[index], SeedKey: index + 1}
	}
	graph, err := bracket.Emit(bracket.DrawInput{Format: format, Entries: draw})
	if err != nil {
		t.Fatal(err)
	}
	drops := make(map[bracket.LocalRef]*progressionPlacementRound)
	for _, node := range graph.Nodes {
		for _, slot := range []bracket.Slot{node.Home, node.Away} {
			if slot.Kind == bracket.SourceLoserOf {
				drops[slot.Src] = &progressionPlacementRound{Bracket: node.Ref.Bracket, Round: node.Ref.Round}
			}
		}
	}
	nodes := slices.Clone(graph.Nodes)
	slices.SortStableFunc(nodes, func(left, right bracket.Node) int { return left.Rank - right.Rank })
	winners, losers := map[bracket.LocalRef]string{}, map[bracket.LocalRef]string{}
	resolve := func(slot bracket.Slot) string {
		switch slot.Kind {
		case bracket.SourceWinnerOf:
			return winners[slot.Src]
		case bracket.SourceLoserOf:
			return losers[slot.Src]
		}
		return slot.EntryID
	}
	matches := make([]progressionMatchForPlacement, 0, len(nodes))
	for _, node := range nodes {
		id, name, round := node.Ref.String(), node.Ref.Bracket, node.Ref.Round
		match := progressionTestMatch(id, name, round, node.Rank, "cancelled", "", "", "", "reset_not_required")
		if node.Activation == bracket.ActivationUnconditional {
			home, away := resolve(node.Home), resolve(node.Away)
			match = progressionTestMatch(id, name, round, node.Rank, "completed", home, away, home, "")
			winners[node.Ref], losers[node.Ref] = home, away
		}
		match.LoserDrop = drops[node.Ref]
		matches = append(matches, match)
	}
	return entryIDs, matches
}

// Byes leave the matches of one round at different graph ranks, in the losers
// bracket as much as in the main one. Everyone knocked out in the same round
// shares a place, a later round always places better, and places are
// competition ranks.
func TestEliminationPlacementsShareAPlacePerKnockoutRound(t *testing.T) {
	const grandFinal = 1 << 20
	for _, format := range []bracket.Format{bracket.SingleElimination, bracket.DoubleElimination} {
		for entryCount := 2; entryCount <= 40; entryCount++ {
			entryIDs, matches := progressionTestPlayHomeWins(t, format, entryCount)
			placements, err := rankEliminationPlacements(progressionTestField(entryIDs), matches, string(format))
			if err != nil {
				t.Fatalf("%s with %d entries: %v", format, entryCount, err)
			}
			// A main, losers or grand final loss knocks its loser out; a
			// winners loss only drops it into the losers bracket.
			knockedOut := make(map[string]int, entryCount)
			for _, match := range matches {
				if match.WinnerEntryID == nil || match.Bracket == "winners" {
					continue
				}
				round := match.RoundNumber
				if match.Bracket == "grand_final" {
					round = grandFinal
				}
				knockedOut[*losingEntry(match.HomeEntryID, match.AwayEntryID, match.WinnerEntryID)] = round
			}
			want := make(map[string]int, entryCount)
			for _, entryID := range entryIDs {
				round, out := knockedOut[entryID]
				want[entryID] = 1
				for _, other := range entryIDs {
					otherRound, otherOut := knockedOut[other]
					if out && (!otherOut || otherRound > round) {
						want[entryID]++
					}
				}
			}
			if got := progressionTestPlacements(placements); !maps.Equal(got, want) {
				t.Fatalf("%s with %d entries: placements = %v, want %v", format, entryCount, got, want)
			}
		}
	}
	// The draw the bug was found in: e05 goes out in losers round 2 match 3,
	// at graph rank 2, and e06 and e08 in matches 1 and 4, at graph rank 3.
	entryIDs, matches := progressionTestPlayHomeWins(t, bracket.DoubleElimination, 11)
	placements, err := rankEliminationPlacements(progressionTestField(entryIDs), matches, "double_elimination")
	if err != nil {
		t.Fatal(err)
	}
	got := progressionTestPlacements(placements)
	for _, entryID := range []string{"e05", "e06", "e08"} {
		if got[entryID] != 9 {
			t.Errorf("%s placement = %d, want 9 (all: %v)", entryID, got[entryID], got)
		}
	}
}

func TestEliminationPlacementRuleDescribesTheRanking(t *testing.T) {
	rule := progressionPlacementRule("single_elimination")
	if other := progressionPlacementRule("double_elimination"); other != rule {
		t.Fatalf("double elimination rule = %q, want %q", other, rule)
	}
	for _, want := range []string{
		"voided from", "main-bracket round", "losers round", "drops into", "bronze excluded",
		"competition_rank", "no champion: nobody first", "bronze result overrides third/fourth",
		"non-live (removed) entries receive no placement",
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("rule %q does not mention %q", rule, want)
		}
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

// TestIntegrationPlacementMatchesRecoverVoidedEntriesAndLoserDrops reads a
// stored double-elimination draw: every winners match knows the round its
// loser drops into, and a voided slot still names the entry it belonged to.
func TestIntegrationPlacementMatchesRecoverVoidedEntriesAndLoserDrops(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "double_elimination", Entries: 5})
	ctx := t.Context()
	load := func() map[string]progressionMatchForPlacement {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		matches, err := loadProgressionPlacementMatches(ctx, tx, seeded.StageID)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]progressionMatchForPlacement{}
		for _, match := range matches {
			byID[match.ID] = match
		}
		return byID
	}
	var played string
	for id, match := range load() {
		if match.Bracket == "winners" && (match.LoserDrop == nil || match.LoserDrop.Round <= 0 ||
			match.LoserDrop.Bracket != "losers" && match.LoserDrop.Bracket != "grand_final") {
			t.Fatalf("winners match %s has no loser drop: %+v", id, match.LoserDrop)
		}
		if match.Bracket == "winners" && match.HomeEntryID != nil && match.AwayEntryID != nil && played == "" {
			played = id
		}
	}
	if played == "" {
		t.Fatal("no ready winners match")
	}
	// Play it, then void both slots it feeds, as removing its two players would.
	var winner, loser string
	if err := pool.QueryRow(ctx, `UPDATE matches SET state='completed',winner_entry_id=home_entry_id,
		completion_reason='played',completed_at=now() WHERE id=$1
		RETURNING home_entry_id::text,away_entry_id::text`, played).Scan(&winner, &loser); err != nil {
		t.Fatal(err)
	}
	var winnerNext, loserNext string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT match_id::text FROM match_slots WHERE source_match_id=$1 AND source_kind='winner_of'),
		(SELECT match_id::text FROM match_slots WHERE source_match_id=$1 AND source_kind='loser_of')`, played).
		Scan(&winnerNext, &loserNext); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE match_slots SET voided_at=now() WHERE source_match_id=$1`, played); err != nil {
		t.Fatal(err)
	}
	matches := load()
	if got := matches[winnerNext].VoidedEntryIDs; !slices.Equal(got, []string{winner}) {
		t.Fatalf("winner's next match voided %v, want [%s]", got, winner)
	}
	if got := matches[loserNext].VoidedEntryIDs; !slices.Equal(got, []string{loser}) {
		t.Fatalf("loser's next match voided %v, want [%s]", got, loser)
	}
	for id, match := range matches {
		if id != winnerNext && id != loserNext && len(match.VoidedEntryIDs) != 0 {
			t.Fatalf("match %s names voided entries %v", id, match.VoidedEntryIDs)
		}
	}
}
