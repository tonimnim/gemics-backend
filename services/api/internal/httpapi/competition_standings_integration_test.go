package httpapi

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it. They play draws through the
// progression engine and read the tables and placements back through the
// public standings handler.

type competitionStandingsEnvelope struct {
	Data competitionStandings `json:"data"`
}

func competitionStandingsRead(t *testing.T, server *Server, competitionID string, status int) competitionStandings {
	t.Helper()
	return competitionVisibilityDecode[competitionStandingsEnvelope](t, competitionVisibilityCall(t,
		server.getCompetitionStandings, http.MethodGet, "/v1/competitions/"+competitionID+"/standings", "",
		competitionID, ""), status).Data
}

// competitionStandingsStored returns the stored standings rows of a stage by
// entry id.
func competitionStandingsStored(t *testing.T, pool *pgxpool.Pool, stageID string) map[string]competitionStandingRow {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT entry_id::text,played,wins,draws,losses,goals_for,goals_against,
		points,walkovers FROM competition_standings WHERE stage_id=$1`, stageID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (competitionStandingRow, error) {
		var standing competitionStandingRow
		err := row.Scan(&standing.EntryID, &standing.Played, &standing.Wins, &standing.Draws, &standing.Losses,
			&standing.GoalsFor, &standing.GoalsAgainst, &standing.Points, &standing.Walkovers)
		return standing, err
	})
	if err != nil {
		t.Fatal(err)
	}
	byEntry := make(map[string]competitionStandingRow, len(stored))
	for _, row := range stored {
		byEntry[row.EntryID] = row
	}
	return byEntry
}

// competitionStandingsAssertStored requires every table row to carry the
// stored counters, and the rows to cover the stage exactly once.
func competitionStandingsAssertStored(t *testing.T, pool *pgxpool.Pool, stage competitionStandingsStage) {
	t.Helper()
	stored := competitionStandingsStored(t, pool, stage.ID)
	seen := 0
	for _, group := range stage.Groups {
		for _, row := range group.Rows {
			want, exists := stored[row.EntryID]
			if !exists || row.Played != want.Played || row.Wins != want.Wins || row.Draws != want.Draws ||
				row.Losses != want.Losses || row.GoalsFor != want.GoalsFor || row.GoalsAgainst != want.GoalsAgainst ||
				row.Points != want.Points || row.Walkovers != want.Walkovers ||
				row.GoalDifference != want.GoalsFor-want.GoalsAgainst || !strings.HasPrefix(row.DisplayName, "Player ") {
				t.Fatalf("table row %+v does not match the stored row %+v", row, want)
			}
			seen++
		}
	}
	if seen != len(stored) {
		t.Fatalf("the tables show %d rows of %d stored", seen, len(stored))
	}
}

// competitionStandingsAssertPlacements requires the placements list to be the
// stored placements in placement, then entry, order.
func competitionStandingsAssertPlacements(t *testing.T, pool *pgxpool.Pool, competitionID string,
	placements []competitionPlacement) {
	t.Helper()
	stored := progressionRemovalPlacements(t, pool, competitionID)
	if len(placements) != len(stored) {
		t.Fatalf("placements = %+v, stored %v", placements, stored)
	}
	for index, placement := range placements {
		if stored[placement.EntryID] != placement.Placement || !strings.HasPrefix(placement.DisplayName, "Player ") {
			t.Fatalf("placement %+v, stored %d", placement, stored[placement.EntryID])
		}
		if index > 0 {
			previous := placements[index-1]
			if previous.Placement > placement.Placement ||
				previous.Placement == placement.Placement && previous.EntryID >= placement.EntryID {
				t.Fatalf("placements are not in placement then entry order: %+v", placements)
			}
		}
	}
}

// A single round-robin table, played to the end with an entry removed in the
// first round: the live rows rank exactly as the written placements, the
// removed entry is unranked and last, and the counters are the stored ones.
func TestIntegrationCompetitionStandingsRankRoundRobinAsPlacements(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := competitionVisibilityServer(pool)

	// Mid-season: one drawn match, no placement yet.
	running := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "round_robin", Entries: 4})
	first := progressionRemovalReadyMatches(t, pool, running.ID)[0]
	progressionRemovalFinalize(t, pool, running.ID, first, progressionRemovalDraw.final(first))
	midSeason := competitionStandingsRead(t, server, running.ID, http.StatusOK)
	if midSeason.CompetitionID != running.ID || len(midSeason.Stages) != 1 || len(midSeason.Placements) != 0 ||
		len(midSeason.Stages[0].Groups) != 1 || midSeason.Stages[0].Groups[0].Key != "main" {
		t.Fatalf("mid-season standings = %+v", midSeason)
	}
	competitionStandingsAssertStored(t, pool, midSeason.Stages[0])
	// The drawn pair shares first place and the two entries yet to play share
	// third; exact ties list in entry id order.
	table := standingsTestRanks(midSeason.Stages[0].Groups[0])
	drawn := []string{first.HomeEntryID + ":1", first.AwayEntryID + ":1"}
	slices.Sort(drawn)
	if len(table) != 4 || !slices.Equal(table[:2], drawn) || !strings.HasSuffix(table[2], ":3") ||
		!strings.HasSuffix(table[3], ":3") {
		t.Fatalf("mid-season table = %v, want %v first and the rest third", table, drawn)
	}

	// Played out, with one entry removed in round 1.
	run := progressionRemovalPlay(t, pool, progressionRemovalScenario{
		Options: integrationSeedOptions{Format: "round_robin", Entries: 4},
		Rules:   []progressionRemovalRule{{Bracket: "main", Round: 1, Number: 1, Outcome: progressionRemovalRemoveAway}},
	})
	final := competitionStandingsRead(t, server, run.Competition.ID, http.StatusOK)
	if len(final.Stages) != 1 || final.Stages[0].ID != run.Competition.StageID || final.Stages[0].Status != "completed" ||
		len(final.Stages[0].Groups) != 1 {
		t.Fatalf("final standings = %+v", final)
	}
	competitionStandingsAssertStored(t, pool, final.Stages[0])
	competitionStandingsAssertPlacements(t, pool, run.Competition.ID, final.Placements)
	placements := progressionRemovalPlacements(t, pool, run.Competition.ID)
	rows := final.Stages[0].Groups[0].Rows
	for index, row := range rows {
		_, removed := run.RemovedBy[row.EntryID]
		if row.Removed != removed || (row.Rank == nil) != removed {
			t.Fatalf("row %+v removed=%v, want %v", row, row.Removed, removed)
		}
		if removed {
			if index != len(rows)-1 {
				t.Fatalf("the removed entry is not listed last: %+v", rows)
			}
			continue
		}
		if *row.Rank != placements[row.EntryID] {
			t.Fatalf("entry %s ranks %d in the table but is placed %d", row.EntryID, *row.Rank, placements[row.EntryID])
		}
	}
	if len(run.RemovedBy) != 1 || len(final.Placements) != 3 {
		t.Fatalf("removed %v, placements %+v", run.RemovedBy, final.Placements)
	}
}

// Groups are separate tables keyed as the bracket keys their rounds, and a
// knockout has no table but lists its placements.
func TestIntegrationCompetitionStandingsGroupsAndKnockoutPlacements(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := competitionVisibilityServer(pool)

	groups := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "round_robin", Entries: 8, GroupCount: 2})
	standings := competitionStandingsRead(t, server, groups.ID, http.StatusOK)
	if len(standings.Stages) != 1 || len(standings.Placements) != 0 {
		t.Fatalf("group standings = %+v", standings)
	}
	var keys []string
	for _, group := range standings.Stages[0].Groups {
		keys = append(keys, group.Key)
		if got := standingsTestRanks(group); len(got) != 4 || slices.ContainsFunc(got, func(rank string) bool {
			return !strings.HasSuffix(rank, ":1")
		}) {
			t.Fatalf("an unplayed group is not level on first place: %v", got)
		}
	}
	if !slices.Equal(keys, []string{"group_a", "group_b"}) {
		t.Fatalf("group keys = %v", keys)
	}
	competitionStandingsAssertStored(t, pool, standings.Stages[0])
	var bracketKeys []string
	for _, stage := range competitionVisibilityDecode[competitionVisibilityBracket](t, competitionVisibilityCall(t,
		server.getCompetitionBracket, http.MethodGet, "/", "", groups.ID, ""), http.StatusOK).Data.Stages {
		for _, round := range stage.Rounds {
			if !slices.Contains(bracketKeys, round.Bracket) {
				bracketKeys = append(bracketKeys, round.Bracket)
			}
		}
	}
	slices.Sort(bracketKeys)
	if !slices.Equal(bracketKeys, keys) {
		t.Fatalf("bracket keys %v differ from the table keys %v", bracketKeys, keys)
	}

	knockout := progressionRemovalPlay(t, pool, progressionRemovalScenario{
		Options: integrationSeedOptions{Format: "single_elimination", Entries: 4},
	})
	standings = competitionStandingsRead(t, server, knockout.Competition.ID, http.StatusOK)
	if len(standings.Stages) != 0 || len(standings.Placements) != 4 || standings.Placements[0].Placement != 1 {
		t.Fatalf("knockout standings = %+v", standings)
	}
	competitionStandingsAssertPlacements(t, pool, knockout.Competition.ID, standings.Placements)
}

// The standings follow the bracket's visibility: a cancelled competition keeps
// its table, where cancelling's refunds remove nobody, while drafts and
// unknown ids stay 404.
func TestIntegrationCompetitionStandingsFollowTheBracketVisibility(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := competitionVisibilityServer(pool)
	cancelled := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "round_robin", Entries: 4})
	first := progressionRemovalReadyMatches(t, pool, cancelled.ID)[0]
	progressionRemovalFinalize(t, pool, cancelled.ID, first, progressionRemovalRemoveAway.final(first))
	competitionVisibilityCancel(t, server, cancelled, cancelled.ID)
	// Cancelling a paid competition moves its entries to withdrawal_pending for
	// their refunds; the seeded entries are free, so one is moved by hand.
	refunding := first.HomeEntryID
	resultFlowExec(t, pool, `UPDATE competition_entries SET status='withdrawal_pending' WHERE id=$1`, refunding)
	standings := competitionStandingsRead(t, server, cancelled.ID, http.StatusOK)
	if len(standings.Stages) != 1 || len(standings.Placements) != 0 {
		t.Fatalf("cancelled standings = %+v", standings)
	}
	for _, row := range standings.Stages[0].Groups[0].Rows {
		removed := row.EntryID == first.AwayEntryID
		if row.Removed != removed || (row.Rank == nil) != removed {
			t.Fatalf("cancelled table row %+v, want removed=%v", row, removed)
		}
		if row.EntryID == refunding && *row.Rank != 1 {
			t.Fatalf("the refunding winner of the only match ranks %d", *row.Rank)
		}
	}

	draft := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "round_robin", Entries: 2})
	resultFlowExec(t, pool, `UPDATE competitions SET status='draft',published_at=NULL WHERE id=$1`, draft.ID)
	for _, competitionID := range []string{draft.ID, "11111111-1111-4111-8111-111111111111"} {
		competitionVisibilityRefused(t, competitionVisibilityCall(t, server.getCompetitionStandings, http.MethodGet,
			"/", "", competitionID, ""), http.StatusNotFound, "competition_not_found")
	}
}
