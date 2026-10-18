package httpapi

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A player always sees their own record, history and rating line, even while
// their profile is private; nobody else does. Their place on the ladder comes
// from the snapshot, so it needs a public profile.
func TestIntegrationPlayersSeeTheirOwnRecordRatingLineAndPlace(t *testing.T) {
	h := resultFlowSetup(t)
	ctx := t.Context()
	competition := seedIntegrationCompetition(t, h.Pool, integrationSeedOptions{Format: "single_elimination", Entries: 2})
	ready := progressionRemovalReadyMatches(t, h.Pool, competition.ID)
	if len(ready) != 1 {
		t.Fatalf("ready matches = %d, want the final", len(ready))
	}
	played := resultFlowPlayMatch(t, h, ready[0], resultFlowAgree)
	home, away := played.Author, played.Confirmer
	for index, userID := range []string{home, away} {
		resultFlowExec(t, h.Pool, `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)
			ON CONFLICT (user_id) DO UPDATE SET discoverable=false`,
			userID, []string{"own_home_", "own_away_"}[index]+userID[:8])
	}

	// Private by default: only the player themselves gets the profile.
	if _, err := h.Server.queryPublicPlayer(ctx, h.Pool, home, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("anonymous read of a private profile: %v, want no rows", err)
	}
	if _, err := h.Server.queryPublicPlayer(ctx, h.Pool, home, away); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("another player's read of a private profile: %v, want no rows", err)
	}
	own, err := h.Server.queryPublicPlayer(ctx, h.Pool, home, home)
	if err != nil || own.Record.MatchesPlayed != 1 || own.Record.Wins != 1 || len(own.Ratings) != 1 {
		t.Fatalf("own profile = %+v, %v", own, err)
	}
	if err := requireVisiblePlayer(ctx, h.Pool, home, home); err != nil {
		t.Fatalf("own history hidden: %v", err)
	}
	if err := requireVisiblePlayer(ctx, h.Pool, home, away); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("someone else's private history: %v, want no rows", err)
	}

	// The rating line ends at the current rating.
	line, err := queryRatingHistory(ctx, h.Pool, home, registrationGameID, 30)
	if err != nil || len(line) != 1 || line[0].Rating != own.Ratings[0].Rating {
		t.Fatalf("rating line = %+v, %v; current rating %d", line, err, own.Ratings[0].Rating)
	}
	// A change older than the window opens the line at the window's start.
	resultFlowExec(t, h.Pool, `UPDATE player_rating_changes SET changed_at=now()-interval '40 days' WHERE user_id=$1`, home)
	line, err = queryRatingHistory(ctx, h.Pool, home, registrationGameID, 30)
	if err != nil || len(line) != 1 || line[0].Rating != own.Ratings[0].Rating ||
		time.Since(line[0].At) < 29*24*time.Hour || time.Since(line[0].At) > 31*24*time.Hour {
		t.Fatalf("carried rating line = %+v, %v", line, err)
	}
	if empty, err := queryRatingHistory(ctx, h.Pool, h.StaffID, registrationGameID, 30); err != nil || len(empty) != 0 {
		t.Fatalf("unrated player's line = %+v, %v", empty, err)
	}

	// Around me: both public and ranked, the loser sees the winner above them.
	resultFlowExec(t, h.Pool, `UPDATE player_profiles SET discoverable=true WHERE user_id IN ($1,$2)`, home, away)
	resultFlowExec(t, h.Pool, `SELECT publish_leaderboard_snapshot($1,'global')`, registrationGameID)
	around := func(userID string, span int) rankingsResponse {
		t.Helper()
		page, err := h.Server.queryRankings(ctx, h.Pool, rankingsOptions{GameID: registrationGameID, Scope: "global",
			Limit: 20, Around: true, Span: span, AroundUserID: userID}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	slice := around(away, 1)
	if len(slice.Data) != 2 || slice.Data[0].PlayerID != home || slice.Data[1].PlayerID != away ||
		slice.Page.HasMore || slice.Page.NextCursor != nil || slice.SnapshotAt == nil {
		t.Fatalf("around the runner-up = %+v", slice)
	}
	if unranked := around(h.StaffID, 2); len(unranked.Data) != 0 || unranked.SnapshotAt == nil {
		t.Fatalf("around an unranked player = %+v", unranked)
	}
}
