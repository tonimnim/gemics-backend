package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// Each player of a match sees the other side's game account and public avatar
// in the room and the list. Nobody else can open the room, and no public
// response carries the eFootball User ID.
func TestIntegrationMatchRoomShowsGameAccountsToParticipantsOnly(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := resultReportsServer(pool)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	ready := readyIntegrationMatches(t, pool, seeded.ID)
	sides := resultReportsStart(t, pool, ready[0])
	suffix := strings.ToLower(rand.Text())[:10]
	awayName, publisherID := "Otieno_"+suffix, "EF-"+suffix
	resultFlowExec(t, pool, `UPDATE game_accounts SET in_game_name=$2,publisher_player_id=$3
		WHERE id=(SELECT game_account_id FROM entry_members WHERE entry_id=$1)`, sides.AwayEntry, awayName, publisherID)
	// Both players have an avatar, but only the away player's profile is public.
	resultFlowExec(t, pool, `UPDATE player_profiles SET avatar_object_key='avatars/'||user_id::text||'/avatar.png',
		discoverable=(user_id=$2) WHERE user_id IN ($1,$2)`, sides.HomeUser, sides.AwayUser)
	var homeName string
	if err := pool.QueryRow(t.Context(), `SELECT account.in_game_name FROM entry_members member
		JOIN game_accounts account ON account.id=member.game_account_id WHERE member.entry_id=$1`,
		sides.HomeEntry).Scan(&homeName); err != nil {
		t.Fatal(err)
	}

	homeView := matchRoomMustGet(t, server, sides.HomeUser, sides.MatchID)
	away := homeView.Away
	if away == nil || away.GameAccount == nil || away.GameAccount.InGameName != awayName ||
		away.GameAccount.Platform != "android" || optionalValue(away.GameAccount.PublisherPlayerID) != publisherID {
		t.Fatalf("the home player cannot see the opponent's game account: %+v", away)
	}
	if optionalValue(away.AvatarURL) != "/v1/players/"+sides.AwayUser+"/avatar" || homeView.Home.AvatarURL != nil {
		t.Fatalf("avatarUrl home=%v away=%v", homeView.Home.AvatarURL, away.AvatarURL)
	}
	awayView := matchRoomMustGet(t, server, sides.AwayUser, sides.MatchID)
	if home := awayView.Home; home == nil || home.GameAccount == nil || home.GameAccount.InGameName != homeName ||
		home.GameAccount.PublisherPlayerID != nil {
		t.Fatalf("the away player cannot see the opponent's game account: %+v", awayView.Home)
	}
	var listed *matchSummaryResponse
	for _, summary := range matchRoomMustList(t, server, sides.HomeUser, "active") {
		if summary.ID == sides.MatchID {
			listed = &summary
		}
	}
	if listed == nil || listed.Away == nil || listed.Away.GameAccount == nil || listed.Away.GameAccount.InGameName != awayName {
		t.Fatalf("the match list does not carry the opponent's game account: %+v", listed)
	}

	otherPlayer := resultReportsStart(t, pool, ready[1]).HomeUser
	for name, userID := range map[string]string{"organizer": seeded.OrganizerID, "another entry's player": otherPlayer} {
		if recorder := resultReportsGet(t, server.getMatch, userID, "matchId", sides.MatchID); recorder.Code != http.StatusNotFound ||
			strings.Contains(recorder.Body.String(), publisherID) {
			t.Fatalf("the %s opened the room: %d %s", name, recorder.Code, recorder.Body.String())
		}
	}
	for name, recorder := range map[string]*httptest.ResponseRecorder{
		"bracket":        resultReportsGet(t, server.getCompetitionBracket, "", "id", seeded.ID),
		"player profile": resultReportsGet(t, server.publicPlayer, "", "id", sides.AwayUser),
	} {
		if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), publisherID) {
			t.Fatalf("the public %s: %d, leaks the User ID: %v", name, recorder.Code,
				strings.Contains(recorder.Body.String(), publisherID))
		}
	}
	if body := resultReportsGet(t, server.getCompetitionBracket, "", "id", seeded.ID).Body.String(); strings.Contains(body, awayName) {
		t.Fatalf("the public bracket carries an in-game name: %s", body)
	}
}
