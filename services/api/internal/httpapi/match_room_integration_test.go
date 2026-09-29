package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These DB-gated tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it. They read the match room and
// the match lists through the real handlers after the result flow has run.

func matchRoomMustGet(t *testing.T, server *Server, userID, matchID string) matchRoomResponse {
	t.Helper()
	recorder := resultReportsGet(t, server.getMatch, userID, "matchId", matchID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("get match %s: %d %s", matchID, recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data matchRoomResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func matchRoomMustList(t *testing.T, server *Server, userID, state string) []matchSummaryResponse {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/me/matches?state="+state, nil)
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: userID}))
	recorder := httptest.NewRecorder()
	server.listMyMatches(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list %s matches: %d %s", state, recorder.Code, recorder.Body.String())
	}
	var page matchPageResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page.Data
}

func matchRoomSummaryIDs(summaries []matchSummaryResponse) []string {
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		ids = append(ids, summary.ID)
	}
	slices.Sort(ids)
	return ids
}

// matchRoomEntryMatch returns the id and state of the entry's only match in a
// round.
func matchRoomEntryMatch(t *testing.T, pool *pgxpool.Pool, competitionID, entryID string, round int) (string, string) {
	t.Helper()
	var matchID, state string
	if err := pool.QueryRow(t.Context(), `SELECT id::text,state FROM matches
		WHERE competition_id=$1 AND round_number=$2 AND $3::uuid IN (home_entry_id,away_entry_id)`,
		competitionID, round, entryID).Scan(&matchID, &state); err != nil {
		t.Fatalf("round %d match of %s: %v", round, entryID, err)
	}
	return matchID, state
}

func matchRoomUserOf(seeded integrationCompetition, entryID string) string {
	for _, entry := range seeded.Entries {
		if entry.ID == entryID {
			return entry.UserID
		}
	}
	return ""
}

// A player who wins round 1 and is removed for silence in round 2 keeps a
// clean round-1 room, sees the removal only on the round-2 room, and no
// longer has the unreleased round-3 fixture in the active list.
func TestIntegrationRemovedFlagIsScopedToTheRemovingMatch(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := resultReportsServer(pool)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "round_robin", Entries: 4})

	roundOne := readyIntegrationMatches(t, pool, seeded.ID)
	if len(roundOne) != 2 {
		t.Fatalf("round 1 has %d ready fixtures, want 2", len(roundOne))
	}
	var removedEntry, removedUser, roundOneID string
	for index, matchID := range roundOne {
		sides := resultReportsStart(t, pool, matchID)
		resultReportsMustPost(t, server, sides.HomeUser, matchID, "round-one-home", resultReportsBody(2, 1), false)
		room := resultReportsMustPost(t, server, sides.AwayUser, matchID, "round-one-away", resultReportsBody(2, 1), false)
		if room.State != "completed" {
			t.Fatalf("round 1 match %s did not complete: %+v", matchID, room)
		}
		if index == 0 {
			removedEntry, removedUser, roundOneID = sides.HomeEntry, sides.HomeUser, matchID
		}
	}

	roundTwoID, state := matchRoomEntryMatch(t, pool, seeded.ID, removedEntry, 2)
	if state != "ready" {
		t.Fatalf("round 2 match is %s, want ready", state)
	}
	sides := resultReportsStart(t, pool, roundTwoID)
	winnerUser, removedSide := sides.HomeUser, "away"
	if sides.HomeEntry == removedEntry {
		winnerUser, removedSide = sides.AwayUser, "home"
	}
	resultReportsMustPost(t, server, winnerUser, roundTwoID, "round-two-report", resultReportsBody(1, 1), false)
	shiftVerificationClock(t, pool, roundTwoID, 10*time.Minute+time.Second)
	resultReportsProcess(t, server, sides)

	roundThreeID, state := matchRoomEntryMatch(t, pool, seeded.ID, removedEntry, 3)
	if state != "pending" {
		t.Fatalf("round 3 fixture is %s, want pending until round 2 finishes", state)
	}

	earlier := matchRoomMustGet(t, server, removedUser, roundOneID)
	if earlier.ResultVerification.EntryRemoved || len(earlier.Removals) != 0 || earlier.Lifecycle != "completed" ||
		earlier.Outcome == nil || *earlier.Outcome != "won" {
		t.Fatalf("the round 1 win shows the later removal: %+v", earlier)
	}
	removing := matchRoomMustGet(t, server, removedUser, roundTwoID)
	if !removing.ResultVerification.EntryRemoved || removing.Lifecycle != "forfeited" ||
		removing.Outcome == nil || *removing.Outcome != "lost" || len(removing.Removals) != 1 {
		t.Fatalf("the removing match does not show the removal: %+v", removing)
	}
	if removal := removing.Removals[0]; removal.Side != removedSide || removal.EntryID != removedEntry ||
		removal.ReasonCode != "report_timeout" || removal.RemovedAt.IsZero() {
		t.Fatalf("unexpected removal: %+v", removal)
	}
	winnerView := matchRoomMustGet(t, server, winnerUser, roundTwoID)
	if winnerView.ResultVerification.EntryRemoved || winnerView.Outcome == nil || *winnerView.Outcome != "won" ||
		!slices.Equal(winnerView.Removals, removing.Removals) {
		t.Fatalf("the winner's room does not show the opponent's removal: %+v", winnerView)
	}
	fixture := matchRoomMustGet(t, server, removedUser, roundThreeID)
	if fixture.Lifecycle != "out_of_competition" || len(fixture.AllowedActions) != 0 || fixture.ResultVerification.EntryRemoved {
		t.Fatalf("the unreleased fixture is offered to the removed player: %+v", fixture)
	}

	if active := matchRoomMustList(t, server, removedUser, "active"); len(active) != 0 {
		t.Fatalf("the removed player still has active matches: %v", matchRoomSummaryIDs(active))
	}
	history := matchRoomMustList(t, server, removedUser, "history")
	want := []string{roundOneID, roundTwoID}
	slices.Sort(want)
	if got := matchRoomSummaryIDs(history); !slices.Equal(got, want) {
		t.Fatalf("history = %v, want %v", got, want)
	}

	// The removed player's round-3 opponent is still live and keeps the fixture
	// until the release settles it as a walkover.
	var fixtureHome, fixtureAway string
	if err := pool.QueryRow(t.Context(), `SELECT home_entry_id::text,away_entry_id::text FROM matches WHERE id=$1`,
		roundThreeID).Scan(&fixtureHome, &fixtureAway); err != nil {
		t.Fatal(err)
	}
	opponentEntry := fixtureHome
	if opponentEntry == removedEntry {
		opponentEntry = fixtureAway
	}
	opponentActive := matchRoomSummaryIDs(matchRoomMustList(t, server, matchRoomUserOf(seeded, opponentEntry), "active"))
	if !slices.Contains(opponentActive, roundThreeID) {
		t.Fatalf("the live opponent lost the round 3 fixture from the active list: %v", opponentActive)
	}
}
