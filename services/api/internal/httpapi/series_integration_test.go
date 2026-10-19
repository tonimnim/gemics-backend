package httpapi

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

// These tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it.

// The draw copies each round's best-of from rules.matchFormat onto the match,
// and a series gets fifteen more minutes for every game after the first.
func TestIntegrationDrawCopiesEachRoundsBestOf(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 8,
		ThirdPlace: true, Rounds: map[string]int{"semifinal": 3, "final": 5, "third_place": 3}})
	rows, err := pool.Query(t.Context(), `SELECT bracket,round_number,best_of,state,
		EXTRACT(EPOCH FROM result_due_at-scheduled_at)::int FROM matches WHERE competition_id=$1`, seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var bracketName, state string
		var round, bestOf int
		var window *int
		if err = rows.Scan(&bracketName, &round, &bestOf, &state, &window); err != nil {
			t.Fatal(err)
		}
		got[fmt.Sprintf("%s-%d", bracketName, round)] = bestOf
		if state == "ready" && (window == nil || *window != int(time.Hour/time.Second)) {
			t.Fatalf("a best-of-1 quarterfinal has a %v second window", window)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"main-1": 1, "main-2": 3, "main-3": 5, "bronze-3": 3}
	for key, bestOf := range want {
		if got[key] != bestOf {
			t.Fatalf("best-of by round = %v, want %v", got, want)
		}
	}
	var stageBestOf int
	if err = pool.QueryRow(t.Context(), `SELECT best_of FROM competition_stages WHERE id=$1`, seeded.StageID).
		Scan(&stageBestOf); err != nil || stageBestOf != 1 {
		t.Fatalf("stage best-of = %d, %v; want the default 1", stageBestOf, err)
	}
}

func seriesReport(home, away int) string {
	return fmt.Sprintf(`{"homeScore":%d,"awayScore":%d,"declarationAccepted":true}`, home, away)
}

// A best-of-3 semifinal is reported in games won: the room says so, a score in
// goals is refused, and the confirmed series is games won in the room and the
// bracket. The final, readied by progression, gets the longer window.
func TestIntegrationSeriesIsScoredInGamesWon(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4, BestOf: 3})
	server := resultReportsServer(pool)
	ready := readyIntegrationMatches(t, pool, seeded.ID)
	sides := resultReportsStart(t, pool, ready[0])

	room := resultReportsRoom(t, server, sides.HomeUser, sides.MatchID)
	evidence := room.VerificationPolicy.ScreenshotEvidence
	if room.BestOf != 3 || room.GamesToWin != 2 || evidence.MaxItems != 3 || !evidence.PerGame ||
		room.VerificationPolicy.ResponseWindowSeconds != 15*60 ||
		!slices.ContainsFunc(room.FriendMatchInstructions, func(step friendMatchInstruction) bool { return step.Title == "Play a best of 3" }) {
		t.Fatalf("series room = bestOf %d, gamesToWin %d, evidence %+v, response %ds, steps %+v", room.BestOf, room.GamesToWin,
			evidence, room.VerificationPolicy.ResponseWindowSeconds, room.FriendMatchInstructions)
	}

	if recorder := resultReportsPost(t, server, sides.HomeUser, sides.MatchID, "series-goals-1", seriesReport(3, 1), ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("a series scored in goals: %d %s", recorder.Code, recorder.Body.String())
	}
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "series-report-1", seriesReport(2, 1), "")
	room = resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "series-confirm-1", resultReportsAnswer("confirm"), "confirmation")
	if room.State != "completed" || room.Result == nil || room.Result.HomeScore != 2 || room.Result.AwayScore != 1 ||
		room.WinnerSide == nil || *room.WinnerSide != "home" {
		t.Fatalf("confirmed series = state %s, result %+v, winner %v", room.State, room.Result, room.WinnerSide)
	}

	stages, err := queryCompetitionBracket(t.Context(), pool, seeded.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, round := range stages[0].Rounds {
		for _, match := range round.Matches {
			if match.ID == sides.MatchID {
				found = match.BestOf == 3 && match.Score != nil && match.Score.HomeScore == 2 && match.Score.AwayScore == 1
			}
		}
	}
	if !found {
		t.Fatalf("the bracket does not show the series: %+v", stages)
	}

	// The other semifinal goes 2-0 and readies the best-of-3 final with fifteen
	// minutes more than a best of 1 for each extra game.
	other := resultReportsStart(t, pool, ready[1])
	resultReportsMustPost(t, server, other.AwayUser, other.MatchID, "series-report-2", seriesReport(0, 2), "")
	resultReportsMustPost(t, server, other.HomeUser, other.MatchID, "series-confirm-2", resultReportsAnswer("confirm"), "confirmation")
	var bestOf, window int
	if err = pool.QueryRow(t.Context(), `SELECT best_of,EXTRACT(EPOCH FROM result_due_at-scheduled_at)::int
		FROM matches WHERE competition_id=$1 AND round_number=2 AND state='ready'`, seeded.ID).Scan(&bestOf, &window); err != nil {
		t.Fatal(err)
	}
	if bestOf != 3 || window != 90*60 {
		t.Fatalf("final = best of %d with a %ds window, want best of 3 and 5400s", bestOf, window)
	}
}

// A disputed series takes a screenshot of every game, numbered by game, and
// sends all of them to the review.
func TestIntegrationSeriesDisputeTakesAScreenshotPerGame(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4, BestOf: 3})
	server := resultReportsServer(pool)
	sides := resultReportsStart(t, pool, readyIntegrationMatches(t, pool, seeded.ID)[0])
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "dispute-report", seriesReport(2, 0), "")
	resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "dispute-reject", resultReportsAnswer("reject"), "confirmation")

	now := time.Now().UTC()
	shot := func(userID string) string { return resultReportsInsertEvidence(t, pool, userID, "completed", now) }
	tooMany := fmt.Sprintf(`{"screenshots":[{"game":1,"evidenceId":"%s"},{"game":4,"evidenceId":"%s"}]}`,
		shot(sides.AwayUser), shot(sides.AwayUser))
	if recorder := resultReportsPost(t, server, sides.AwayUser, sides.MatchID, "dispute-shot-4", tooMany, "screenshot"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("a game 4 screenshot in a best of 3: %d %s", recorder.Code, recorder.Body.String())
	}
	awayShots := fmt.Sprintf(`{"screenshots":[{"game":2,"evidenceId":"%s"},{"game":1,"evidenceId":"%s"},{"game":3,"evidenceId":"%s"}]}`,
		shot(sides.AwayUser), shot(sides.AwayUser), shot(sides.AwayUser))
	room := resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "dispute-shot-away", awayShots, "screenshot")
	mine := room.ResultVerification.MyScreenshots
	if room.Lifecycle != "awaiting_opponent_screenshot" || len(mine) != 3 || mine[0].Game != 1 || mine[2].Game != 3 ||
		room.ResultVerification.MyScreenshot == nil || room.ResultVerification.MyScreenshot.Game != 1 {
		t.Fatalf("away screenshots = %s %+v", room.Lifecycle, mine)
	}
	homeShots := fmt.Sprintf(`{"screenshots":[{"game":1,"evidenceId":"%s"},{"game":2,"evidenceId":"%s"}]}`,
		shot(sides.HomeUser), shot(sides.HomeUser))
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "dispute-shot-home", homeShots, "screenshot")

	if queued := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_reviews
		WHERE match_id=$1 AND status='queued' AND reason='reports_differ'`, sides.MatchID); queued != 1 {
		t.Fatalf("%d reviews queued, want 1", queued)
	}
	if positions := resultReportsCount(t, pool, `SELECT count(*) FROM match_result_report_evidence link
		JOIN match_result_reports report ON report.id=link.report_id WHERE report.match_id=$1`, sides.MatchID); positions != 5 {
		t.Fatalf("%d screenshots bound, want 5", positions)
	}
	screenshots, err := loadScreenshotContext(t.Context(), pool, sides.MatchID)
	if err != nil {
		t.Fatal(err)
	}
	games := map[string][]int{}
	for _, item := range screenshots.Readings {
		games[item.UploadedBySide] = append(games[item.UploadedBySide], item.Game)
	}
	if !slices.Equal(games["home"], []int{1, 2}) || !slices.Equal(games["away"], []int{1, 2, 3}) ||
		screenshots.HomeClaim == nil || !screenshots.HomeClaim.Series || screenshots.HomeClaim.Score.HomeScore != 2 {
		t.Fatalf("reader context = games %v, claim %+v", games, screenshots.HomeClaim)
	}
}

// A round-robin series scores one point per game won and can end level.
func TestIntegrationRoundRobinSeriesStandings(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "round_robin", Entries: 4, BestOf: 3})
	server := resultReportsServer(pool)
	sides := resultReportsStart(t, pool, readyIntegrationMatches(t, pool, seeded.ID)[0])
	resultReportsMustPost(t, server, sides.HomeUser, sides.MatchID, "rr-report", seriesReport(1, 1), "")
	resultReportsMustPost(t, server, sides.AwayUser, sides.MatchID, "rr-confirm", resultReportsAnswer("confirm"), "confirmation")
	for _, entry := range []struct {
		id                                               string
		points, won, lost, draws, goalsFor, goalsAgainst int
	}{{sides.HomeEntry, 1, 1, 1, 1, 0, 0}, {sides.AwayEntry, 1, 1, 1, 1, 0, 0}} {
		var points, won, lost, draws, goalsFor, goalsAgainst int
		if err := pool.QueryRow(t.Context(), `SELECT points,games_won,games_lost,draws,goals_for,goals_against
			FROM competition_standings WHERE entry_id=$1`, entry.id).Scan(&points, &won, &lost, &draws, &goalsFor, &goalsAgainst); err != nil {
			t.Fatal(err)
		}
		if points != entry.points || won != entry.won || lost != entry.lost || draws != entry.draws ||
			goalsFor != entry.goalsFor || goalsAgainst != entry.goalsAgainst {
			t.Fatalf("standing %s = %d pts, %d-%d games, %d draws, %d-%d goals", entry.id, points, won, lost, draws, goalsFor, goalsAgainst)
		}
	}
}
