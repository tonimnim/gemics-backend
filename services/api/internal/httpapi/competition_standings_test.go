package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

// standingsTestRecord is a stored row of the "main" group of one stage.
func standingsTestRecord(entryID, entryStatus string, played, wins, draws, losses, goalsFor, goalsAgainst,
	points int) competitionStandingRecord {
	return competitionStandingRecord{StageID: "stage-1", StageName: "League", StageStatus: "active", GroupKey: "main",
		EntryStatus: entryStatus, Row: competitionStandingRow{EntryID: entryID, DisplayName: "Player " + entryID,
			Played: played, Wins: wins, Draws: draws, Losses: losses, GoalsFor: goalsFor, GoalsAgainst: goalsAgainst,
			Points: points}}
}

// standingsTestRanks renders a table as "entry:rank" in row order, with "-"
// for an unranked row.
func standingsTestRanks(group competitionStandingsGroup) []string {
	ranks := make([]string, len(group.Rows))
	for index, row := range group.Rows {
		rank := "-"
		if row.Rank != nil {
			rank = strconv.Itoa(*row.Rank)
		}
		ranks[index] = row.EntryID + ":" + rank
	}
	return ranks
}

// A group table ranks with the placements' own function and comparator: ties
// on points, goal difference and goals scored share a rank, removed entries
// are unranked and listed after the ranked rows, and the table re-ranks
// around them exactly as the final placements do.
func TestRankCompetitionStandingsGroupFollowsThePlacementRule(t *testing.T) {
	tests := []struct {
		name              string
		competitionStatus string
		records           []competitionStandingRecord
		want              []string
		wantRemoved       []string
	}{
		{name: "exact ties share a rank and the next rank skips", competitionStatus: "running",
			records: []competitionStandingRecord{
				standingsTestRecord("delta", "accepted", 3, 0, 1, 2, 1, 5, 1),
				standingsTestRecord("bravo", "accepted", 3, 3, 0, 0, 7, 2, 9),
				standingsTestRecord("charlie", "accepted", 3, 1, 1, 1, 3, 3, 4),
				standingsTestRecord("alpha", "accepted", 3, 3, 0, 0, 7, 2, 9),
			},
			want: []string{"alpha:1", "bravo:1", "charlie:3", "delta:4"}},
		{name: "goal difference then goals scored break a points tie", competitionStatus: "running",
			records: []competitionStandingRecord{
				standingsTestRecord("golf", "accepted", 2, 2, 0, 0, 6, 5, 6),
				standingsTestRecord("foxtrot", "accepted", 2, 2, 0, 0, 4, 2, 6),
				standingsTestRecord("echo", "accepted", 2, 2, 0, 0, 5, 3, 6),
			},
			want: []string{"echo:1", "foxtrot:2", "golf:3"}},
		{name: "a removed leader is unranked, listed last and the table re-ranks", competitionStatus: "running",
			records: []competitionStandingRecord{
				standingsTestRecord("alpha", "accepted", 2, 1, 0, 1, 4, 2, 3),
				standingsTestRecord("bravo", "accepted", 2, 0, 1, 1, 3, 3, 1),
				standingsTestRecord("delta", "disqualified", 2, 2, 0, 0, 6, 1, 6),
				standingsTestRecord("charlie", "accepted", 2, 0, 1, 1, 1, 4, 1),
			},
			want: []string{"alpha:1", "bravo:2", "charlie:3", "delta:-"}, wantRemoved: []string{"delta"}},
		{name: "removed entries follow in table order", competitionStatus: "completed",
			records: []competitionStandingRecord{
				standingsTestRecord("alpha", "disqualified", 1, 0, 0, 1, 0, 3, 0),
				standingsTestRecord("bravo", "accepted", 2, 2, 0, 0, 3, 0, 6),
				standingsTestRecord("charlie", "withdrawn", 1, 1, 0, 0, 2, 1, 3),
			},
			want: []string{"bravo:1", "charlie:-", "alpha:-"}, wantRemoved: []string{"charlie", "alpha"}},
		{name: "a group with every entry removed has no rank", competitionStatus: "running",
			records: []competitionStandingRecord{
				standingsTestRecord("alpha", "disqualified", 1, 0, 1, 0, 1, 1, 1),
				standingsTestRecord("bravo", "disqualified", 1, 0, 1, 0, 1, 1, 1),
			},
			want: []string{"alpha:-", "bravo:-"}, wantRemoved: []string{"alpha", "bravo"}},
		{name: "cancelling ranks the entries it moved to withdrawal_pending", competitionStatus: "cancelled",
			records: []competitionStandingRecord{
				standingsTestRecord("alpha", "withdrawal_pending", 1, 1, 0, 0, 2, 0, 3),
				standingsTestRecord("bravo", "registered", 1, 0, 0, 1, 0, 2, 0),
				standingsTestRecord("charlie", "disqualified", 0, 0, 0, 0, 0, 0, 0),
			},
			want: []string{"alpha:1", "bravo:2", "charlie:-"}, wantRemoved: []string{"charlie"}},
		{name: "a withdrawing entry of a live competition is removed", competitionStatus: "running",
			records: []competitionStandingRecord{
				standingsTestRecord("alpha", "withdrawal_pending", 1, 1, 0, 0, 2, 0, 3),
				standingsTestRecord("bravo", "checked_in", 1, 0, 0, 1, 0, 2, 0),
			},
			want: []string{"bravo:1", "alpha:-"}, wantRemoved: []string{"alpha"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			group, err := rankCompetitionStandingsGroup("main", test.records, test.competitionStatus)
			if err != nil {
				t.Fatal(err)
			}
			if got := standingsTestRanks(group); group.Key != "main" || !slices.Equal(got, test.want) {
				t.Fatalf("group %q table = %v, want %v", group.Key, got, test.want)
			}
			var removed []string
			entries := make([]progressionPlacementEntry, 0, len(test.records))
			standings := make([]progressionStandingForPlacement, 0, len(test.records))
			ranks := map[string]int{}
			for _, row := range group.Rows {
				if row.Removed {
					removed = append(removed, row.EntryID)
				} else {
					ranks[row.EntryID] = *row.Rank
				}
				if row.GoalDifference != row.GoalsFor-row.GoalsAgainst {
					t.Fatalf("%s goal difference = %d, want %d", row.EntryID, row.GoalDifference, row.GoalsFor-row.GoalsAgainst)
				}
				entries = append(entries, progressionPlacementEntry{EntryID: row.EntryID, Live: !row.Removed})
				standings = append(standings, progressionStandingForPlacement{EntryID: row.EntryID, Points: row.Points,
					GoalsFor: row.GoalsFor, GoalsAgainst: row.GoalsAgainst})
			}
			if !slices.Equal(removed, test.wantRemoved) {
				t.Fatalf("removed rows = %v, want %v", removed, test.wantRemoved)
			}
			// The table is what the progression engine would place from the
			// same field.
			placements, err := rankRoundRobinPlacements(entries, standings)
			if err != nil {
				t.Fatal(err)
			}
			if want := progressionTestPlacements(placements); !maps.Equal(ranks, want) {
				t.Fatalf("table ranks = %v, placements = %v", ranks, want)
			}
		})
	}
}

// A stage with groups is placed as one table across its groups, as
// writeProgressionPlacements ranks every standings row of the stage, so a
// group winner can be placed below the runner-up of a stronger group. The
// contract says so; this pins the behaviour it describes.
func TestGroupedStagePlacementsRankTheWholeField(t *testing.T) {
	groupA := []competitionStandingRecord{
		standingsTestRecord("alpha", "accepted", 2, 2, 0, 0, 6, 1, 6),
		standingsTestRecord("bravo", "accepted", 2, 1, 0, 1, 4, 3, 3),
	}
	groupB := []competitionStandingRecord{
		standingsTestRecord("charlie", "accepted", 2, 0, 2, 0, 2, 2, 2),
		standingsTestRecord("delta", "accepted", 2, 0, 1, 1, 1, 3, 1),
	}
	for key, records := range map[string][]competitionStandingRecord{"group_a": groupA, "group_b": groupB} {
		group, err := rankCompetitionStandingsGroup(key, records, "running")
		if err != nil {
			t.Fatal(err)
		}
		if group.Rows[0].Rank == nil || *group.Rows[0].Rank != 1 {
			t.Fatalf("%s winner is not ranked 1: %v", key, standingsTestRanks(group))
		}
	}
	var entries []progressionPlacementEntry
	var standings []progressionStandingForPlacement
	for _, record := range append(slices.Clone(groupA), groupB...) {
		entries = append(entries, progressionPlacementEntry{EntryID: record.Row.EntryID, Live: true})
		standings = append(standings, progressionStandingForPlacement{EntryID: record.Row.EntryID,
			Points: record.Row.Points, GoalsFor: record.Row.GoalsFor, GoalsAgainst: record.Row.GoalsAgainst})
	}
	placements, err := rankRoundRobinPlacements(entries, standings)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(placements))
	for index, placement := range placements {
		got[index] = placement.EntryID + ":" + strconv.Itoa(placement.Placement)
	}
	if want := []string{"alpha:1", "bravo:2", "charlie:3", "delta:4"}; !slices.Equal(got, want) {
		t.Fatalf("placements across the groups = %v, want %v", got, want)
	}
}

func TestCompetitionStandingRemovedFollowsEntryLiveness(t *testing.T) {
	tests := []struct {
		competitionStatus, entryStatus string
		want                           bool
	}{
		{"running", "registered", false},
		{"running", "checked_in", false},
		{"running", "accepted", false},
		{"running", "disqualified", true},
		{"running", "withdrawn", true},
		{"running", "withdrawal_pending", true},
		{"completed", "disqualified", true},
		{"cancelled", "withdrawal_pending", false},
		{"cancelled", "withdrawn", false},
		{"cancelled", "accepted", false},
		{"cancelled", "disqualified", true},
	}
	for _, test := range tests {
		if got := competitionStandingRemoved(test.competitionStatus, test.entryStatus); got != test.want {
			t.Errorf("removed(%s, %s) = %v, want %v", test.competitionStatus, test.entryStatus, got, test.want)
		}
	}
}

func TestBuildCompetitionStandingsStagesGroupsRowsInQueryOrder(t *testing.T) {
	// Each row played once: the winner scored the only goal.
	record := func(stageID string, position int, groupKey, entryID string, won bool) competitionStandingRecord {
		row := standingsTestRecord(entryID, "accepted", 1, 0, 0, 1, 0, 1, 0)
		if won {
			row = standingsTestRecord(entryID, "accepted", 1, 1, 0, 0, 1, 0, 3)
		}
		row.StageID, row.StageName, row.StagePosition, row.GroupKey = stageID, "Stage "+stageID, position, groupKey
		return row
	}
	stages, err := buildCompetitionStandingsStages([]competitionStandingRecord{
		record("groups", 0, "group_a", "a1", false), record("groups", 0, "group_a", "a2", true),
		record("groups", 0, "group_b", "b1", true), record("groups", 0, "group_b", "b2", false),
		record("league", 1, "main", "m1", true), record("league", 1, "main", "m2", false),
	}, "running")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, stage := range stages {
		for _, group := range stage.Groups {
			got = append(got, stage.ID+"/"+group.Key+"="+strings.Join(standingsTestRanks(group), ","))
		}
	}
	want := []string{"groups/group_a=a2:1,a1:2", "groups/group_b=b1:1,b2:2", "league/main=m1:1,m2:2"}
	if !slices.Equal(got, want) || len(stages) != 2 || stages[1].Position != 1 || stages[1].Name != "Stage league" {
		t.Fatalf("stages = %v (%+v), want %v", got, stages, want)
	}
	empty, err := buildCompetitionStandingsStages(nil, "running")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("no rows built %v, %v; want an empty, non-nil list", empty, err)
	}
}

// A removed row serializes an explicit null rank, and a competition without
// tables or placements serializes empty lists rather than null.
func TestCompetitionStandingsSerializeNullRankAndEmptyLists(t *testing.T) {
	group, err := rankCompetitionStandingsGroup("main", []competitionStandingRecord{
		standingsTestRecord("alpha", "accepted", 1, 1, 0, 0, 1, 0, 3),
		standingsTestRecord("bravo", "disqualified", 1, 0, 0, 1, 0, 1, 0),
	}, "running")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(group)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"rank":1,"entryId":"alpha"`) ||
		!strings.Contains(string(raw), `"rank":null,"entryId":"bravo"`) || !strings.Contains(string(raw), `"removed":true`) {
		t.Fatalf("group JSON = %s", raw)
	}
	stages, err := buildCompetitionStandingsStages(nil, "completed")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(competitionStandings{CompetitionID: "c", Stages: stages, Placements: []competitionPlacement{}})
	if err != nil || string(raw) != `{"competitionId":"c","stages":[],"placements":[]}` {
		t.Fatalf("empty standings JSON = %s, %v", raw, err)
	}
}

func TestCompetitionStandingsRouteIsPublicAndNeedsTheDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")
	for _, test := range []struct {
		path   string
		status int
		code   string
	}{
		{"/v1/competitions/not-a-uuid/standings", http.StatusNotFound, "competition_not_found"},
		// A registered public route reaches its handler without a token.
		{"/v1/competitions/11111111-1111-4111-8111-111111111111/standings", http.StatusServiceUnavailable,
			"database_unavailable"},
	} {
		recorder := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))
		if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), `"`+test.code+`"`) {
			t.Errorf("GET %s = %d %s, want %d %s", test.path, recorder.Code, recorder.Body.String(), test.status, test.code)
		}
	}
}

// The standings follow the bracket's visibility, read one snapshot, rank with
// the progression function and are dropped by the same invalidation that every
// result path already calls.
func TestCompetitionStandingsShareVisibilityRankingAndInvalidation(t *testing.T) {
	assertOrder(t, "queryCompetitionStandings",
		resultReportsFunctionSource(t, "competition_standings.go", "func queryCompetitionStandings("),
		"pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}", "readableCompetitionSQL",
		"queryCompetitionStandingRecords(", "buildCompetitionStandingsStages(", "queryCompetitionPlacements(",
		"tx.Commit(ctx)")
	assertOrder(t, "rankCompetitionStandingsGroup",
		resultReportsFunctionSource(t, "competition_standings.go", "func rankCompetitionStandingsGroup("),
		"competitionStandingRemoved(", "rankRoundRobinPlacements(entries, standings)", "sortRoundRobinStandings(removed)")
	assertOrder(t, "rankRoundRobinPlacements",
		resultReportsFunctionSource(t, "match_progression_placements.go", "func rankRoundRobinPlacements("),
		"sortRoundRobinStandings(ordered)", "sameRoundRobinPlacementScore(")
	assertOrder(t, "getCompetitionStandings",
		resultReportsFunctionSource(t, "competition_standings.go", "func (s *Server) getCompetitionStandings("),
		"s.responses.GetOrLoad(r.Context(), competitionStandingsCacheKey(id)", "readPublicCompetition(ctx, s,",
		"writeCompetitionCacheResponse(")
	if !strings.Contains(resultReportsFunctionSource(t, "organizer_competition_handlers.go",
		"func (s *Server) invalidateCompetitionCachesContext("), "competitionStandingsCacheKey(competitionID)") {
		t.Error("invalidateCompetitionCachesContext does not drop the standings")
	}
	assertFileContains(t, "server.go", `mux.HandleFunc("GET /v1/competitions/{id}/standings", s.getCompetitionStandings)`)
}

// The competition.completed push promises final standings; the endpoint is
// what makes that true, so the wording and the deep link stay pinned.
func TestCompletedPushLeadsToTheStandings(t *testing.T) {
	const competitionID = "11111111-1111-4111-8111-111111111111"
	definition := notificationDefinitionForEvent(notificationOutboxEvent{
		ID: "22222222-2222-4222-8222-222222222222", AggregateID: competitionID, EventType: "competition.completed",
		Payload: json.RawMessage(`{"competitionId":"` + competitionID + `"}`),
	})
	if !strings.Contains(definition.Body, "Final standings") || definition.ActionURL != "/competitions/"+competitionID {
		t.Fatalf("competition.completed push = %q -> %q", definition.Body, definition.ActionURL)
	}
}

// openAPIRequiredFields reads the first required list of a block, in flow or
// block style.
func openAPIRequiredFields(t *testing.T, block string) []string {
	t.Helper()
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "required:") {
			continue
		}
		if flow := strings.TrimSpace(strings.TrimPrefix(trimmed, "required:")); flow != "" {
			values := strings.Split(strings.Trim(flow, "[]"), ",")
			for position := range values {
				values[position] = strings.TrimSpace(values[position])
			}
			return values
		}
		var values []string
		for _, item := range lines[index+1:] {
			value, ok := strings.CutPrefix(strings.TrimSpace(item), "- ")
			if !ok {
				break
			}
			values = append(values, value)
		}
		return values
	}
	t.Fatalf("block has no required list:\n%s", block)
	return nil
}

// jsonFieldNames lists a struct's JSON keys in declaration order.
func jsonFieldNames(value any) []string {
	kind := reflect.TypeOf(value)
	names := make([]string, 0, kind.NumField())
	for index := range kind.NumField() {
		name, _, _ := strings.Cut(kind.Field(index).Tag.Get("json"), ",")
		names = append(names, name)
	}
	return names
}

// The assembled contract and its fragment declare the standings operation and
// require every field the handler always writes.
func TestOpenAPICompetitionStandingsContract(t *testing.T) {
	schemas := []struct {
		name   string
		fields []string
	}{
		{"CompetitionStandings", jsonFieldNames(competitionStandings{})},
		{"StandingsStage", jsonFieldNames(competitionStandingsStage{})},
		{"StandingsGroup", jsonFieldNames(competitionStandingsGroup{})},
		{"StandingRow", jsonFieldNames(competitionStandingRow{})},
		{"CompetitionPlacement", jsonFieldNames(competitionPlacement{})},
	}
	for _, name := range []string{"openapi.yaml", "competition-match-policy.paths.yaml"} {
		contract := openAPIFile(t, name)
		operation := openAPIWords(openAPIBlock(t, contract, "/v1/competitions/{id}/standings", 2))
		for _, want := range []string{"operationId: getCompetitionStandings",
			"#/components/schemas/CompetitionStandingsResponse", "competition_not_found",
			"exactly as its final placement is decided"} {
			if !strings.Contains(operation, want) {
				t.Errorf("%s standings operation does not contain %q", name, want)
			}
		}
		for _, schema := range schemas {
			block := openAPIBlock(t, contract, schema.name, 4)
			got := openAPIRequiredFields(t, block)
			slices.Sort(got)
			want := slices.Sorted(slices.Values(schema.fields))
			if !slices.Equal(got, want) {
				t.Errorf("%s %s requires %v, want %v", name, schema.name, got, want)
			}
			if !strings.Contains(block, "additionalProperties: false") {
				t.Errorf("%s %s does not forbid undeclared fields", name, schema.name)
			}
		}
		placements := openAPIWords(openAPIBlock(t, openAPIBlock(t, contract, "CompetitionStandings", 4), "placements", 8))
		if !strings.Contains(placements, "one table across all its groups") {
			t.Errorf("%s CompetitionStandings.placements does not say a grouped stage is placed across its groups", name)
		}
		stageStatus := openAPIBlock(t, openAPIBlock(t, contract, "StandingsStage", 4), "status", 8)
		if got := openAPIEnum(t, stageStatus); !slices.Equal(got, []string{"pending", "active", "completed"}) {
			t.Errorf("%s StandingsStage.status enum = %v", name, got)
		}
	}
}
