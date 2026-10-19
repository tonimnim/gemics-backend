package httpapi

import (
	"encoding/json"
	"slices"
	"testing"
)

// A best of 3 between Mugz FC (home, always on the left) and SQUAD 0: Mugz
// win game 1 2-1, lose game 2 0-3 and win game 3 3-0, so 2-1 in games.
var (
	seriesGame1 = screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz)
	seriesGame2 = screenshotSample("Mugz FC", 0, "SQUAD 0", 3, sampleMzee)
	seriesGame3 = screenshotSample("Mugz FC", 3, "SQUAD 0", 0, sampleArgentina)
)

func seriesShot(side string, game int, response visionReadResponse) *evaluatedReading {
	item := evaluated(side, side+"-"+string(rune('0'+game)), response)
	item.Game = game
	return item
}

// seriesClaim is a series reported in games won.
func seriesClaim(home, away int) *claimScore {
	return &claimScore{Series: true, Score: screenshotScore{HomeScore: home, AwayScore: away}}
}

func evaluateSeries(readings []*evaluatedReading, claim *claimScore) screenshotEvaluation {
	return evaluateScreenshots(screenshotEvaluationInput{Readings: readings, HomeNames: []string{"Mugz FC"},
		AwayNames: []string{"SQUAD 0"}, HomeClaim: claim, BestOf: 3, MinConfidence: 0.9})
}

func TestEvaluateSeriesScreenshotsGameByGame(t *testing.T) {
	everyGame := func() []*evaluatedReading {
		return []*evaluatedReading{
			seriesShot("home", 1, seriesGame1), seriesShot("home", 2, seriesGame2), seriesShot("home", 3, seriesGame3),
			seriesShot("away", 1, seriesGame1), seriesShot("away", 2, seriesGame2), seriesShot("away", 3, seriesGame3),
		}
	}

	// Six agreeing screenshots that add up to the reported 2-1 settle it.
	agreed := evaluateSeries(everyGame(), seriesClaim(2, 1))
	if agreed.Verdict != verdictSupports || agreed.Decision != "accept_home" || len(agreed.Games) != 3 ||
		agreed.Score == nil || agreed.Score.HomeScore != 2 || agreed.Score.AwayScore != 1 {
		t.Fatalf("six agreeing screenshots = %+v", agreed)
	}
	for index, want := range []string{"home", "away", "home"} {
		game := agreed.Games[index]
		if game.Winner == nil || *game.Winner != want || !slices.Equal(game.Sides, []string{"away", "home"}) {
			t.Fatalf("game %d = %+v", game.Game, game)
		}
	}

	// A game with one player's screenshot still adds up, but only staff can
	// settle it.
	if result := evaluateSeries(everyGame()[:5], seriesClaim(2, 1)); result.Verdict != verdictSupports ||
		result.Decision != "" ||
		!slices.Contains(result.Reasons, "Each player must provide a readable screenshot of every game.") {
		t.Fatalf("a missing screenshot = %+v", result)
	}

	// Screenshots that agree but add up to another result.
	if result := evaluateSeries(everyGame(), seriesClaim(2, 0)); result.Verdict != verdictNeither || result.Decision != "" {
		t.Fatalf("a wrong tally = %+v", result)
	}

	// Two games shown, 1-1: the series isn't complete, so staff decide.
	twoGames := []*evaluatedReading{seriesShot("home", 1, seriesGame1), seriesShot("home", 2, seriesGame2),
		seriesShot("away", 1, seriesGame1), seriesShot("away", 2, seriesGame2)}
	if result := evaluateSeries(twoGames, seriesClaim(1, 1)); result.Verdict != verdictSupports || result.Decision != "" ||
		!slices.Contains(result.Reasons, "The screenshots don't show every game of the series.") {
		t.Fatalf("an incomplete series = %+v", result)
	}

	// The away player numbered games 2 and 3 the other way round: the
	// screenshots are paired by what they show, and staff decide.
	swapped := everyGame()
	swapped[4].Game, swapped[5].Game = 3, 2
	if result := evaluateSeries(swapped, seriesClaim(2, 1)); result.Verdict != verdictSupports || result.Decision != "" ||
		len(result.Differences) != 0 || len(result.Reasons) != 1 {
		t.Fatalf("swapped numbers = %+v", result)
	}

	// The home player sends game 1's screenshot again as game 2.
	reused := everyGame()
	reused[1] = seriesShot("home", 2, seriesGame1)
	result := evaluateSeries(reused, seriesClaim(2, 1))
	if result.Verdict != verdictConflict || result.Decision != "" ||
		!slices.Contains(reused[0].Flags, "duplicate_in_series") || !slices.Contains(reused[1].Flags, "duplicate_in_series") {
		t.Fatalf("a reused screenshot = %+v, flags %v %v", result, reused[0].Flags, reused[1].Flags)
	}

	// A game won on penalties counts for its winner, and is checked by staff.
	shootout := screenshotSample("Mugz FC", 1, "SQUAD 0", 1, sampleMugz)
	pens := json.RawMessage(`{"left":4,"right":3}`)
	shootout.Penalties = &pens
	withPens := everyGame()
	withPens[0], withPens[3] = seriesShot("home", 1, shootout), seriesShot("away", 1, shootout)
	if result := evaluateSeries(withPens, seriesClaim(2, 1)); result.Verdict != verdictSupports || result.Decision != "" ||
		!slices.Contains(result.Reasons, "Penalty shoot-outs are always checked by staff.") {
		t.Fatalf("a shoot-out game = %+v", result)
	}
}

func TestSeriesScreenshotInputIsNumberedByGame(t *testing.T) {
	id := func(n byte) string { return "00000000-0000-4000-8000-00000000000" + string(rune(n)) }
	cases := []struct {
		name  string
		input resultScreenshotInput
		ok    bool
	}{
		{"one evidence id is game 1", resultScreenshotInput{EvidenceID: id('1')}, true},
		{"three games", resultScreenshotInput{Screenshots: []resultScreenshotGame{{3, id('3')}, {1, id('1')}, {2, id('2')}}}, true},
		{"both forms", resultScreenshotInput{EvidenceID: id('1'), Screenshots: []resultScreenshotGame{{2, id('2')}}}, false},
		{"none", resultScreenshotInput{}, false},
		{"a game twice", resultScreenshotInput{Screenshots: []resultScreenshotGame{{1, id('1')}, {1, id('2')}}}, false},
		{"one image twice", resultScreenshotInput{Screenshots: []resultScreenshotGame{{1, id('1')}, {2, id('1')}}}, false},
		{"game 6", resultScreenshotInput{Screenshots: []resultScreenshotGame{{6, id('1')}}}, false},
		{"game 0", resultScreenshotInput{Screenshots: []resultScreenshotGame{{0, id('1')}}}, false},
	}
	for _, test := range cases {
		input := test.input
		if message := input.normalize(); (message == "") != test.ok {
			t.Errorf("%s: %q", test.name, message)
			continue
		}
		if test.ok && !slices.IsSortedFunc(input.Screenshots, func(a, b resultScreenshotGame) int { return a.Game - b.Game }) {
			t.Errorf("%s: screenshots out of game order: %+v", test.name, input.Screenshots)
		}
	}
}

func TestSeriesStandingsScoreOnePointPerGame(t *testing.T) {
	home, away := "entry-a", "entry-b"
	score := func(value int) *int { return &value }
	completed := progressionSource{State: "completed", BestOf: 3, HomeEntryID: &home, AwayEntryID: &away,
		WinnerEntryID: &home, HomeScore: score(2), AwayScore: score(1)}
	deltas, err := roundRobinStandingDeltas(completed)
	if err != nil {
		t.Fatal(err)
	}
	want := []progressionStandingDelta{
		{EntryID: home, Wins: 1, GamesWon: 2, GamesLost: 1, Points: 2},
		{EntryID: away, Losses: 1, GamesWon: 1, GamesLost: 2, Points: 1},
	}
	if !slices.Equal(deltas, want) {
		t.Fatalf("2-1 series = %+v, want %+v", deltas, want)
	}
	level := completed
	level.WinnerEntryID, level.HomeScore, level.AwayScore = nil, score(1), score(1)
	if deltas, err = roundRobinStandingDeltas(level); err != nil || deltas[0].Draws != 1 || deltas[0].Points != 1 {
		t.Fatalf("level series = %+v, %v", deltas, err)
	}
	walkover := progressionSource{State: "forfeit", BestOf: 5, HomeEntryID: &home, AwayEntryID: &away, WinnerEntryID: &away}
	if deltas, err = roundRobinStandingDeltas(walkover); err != nil || deltas[1].Points != 3 || deltas[1].Walkovers != 1 ||
		deltas[0].GamesLost != 3 {
		t.Fatalf("best-of-5 walkover = %+v, %v", deltas, err)
	}

	// Points first, then game difference, then goal difference.
	rows := []progressionStandingForPlacement{
		{EntryID: "c", Points: 4, GamesWon: 4, GamesLost: 3, GoalsFor: 20, GoalsAgainst: 5},
		{EntryID: "a", Points: 4, GamesWon: 4, GamesLost: 1, GoalsFor: 6, GoalsAgainst: 5},
		{EntryID: "b", Points: 5, GamesWon: 5, GamesLost: 4},
	}
	sortRoundRobinStandings(rows)
	if order := []string{rows[0].EntryID, rows[1].EntryID, rows[2].EntryID}; !slices.Equal(order, []string{"b", "a", "c"}) {
		t.Fatalf("table order = %v", order)
	}
}
