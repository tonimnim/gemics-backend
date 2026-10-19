package httpapi

import (
	"fmt"
	"slices"
	"sort"
)

// A series is reported in games won, such as 2-1. A dispute carries a
// screenshot of every game from each player, each numbered by its game. Both
// players' screenshots of game N must agree with each other, as in a best of
// 1; the reader then counts who won each game and checks the tally against
// the reported games won. It settles a series only when the screenshots show
// the complete series (games 1 to N with no gap, stopping at the majority or
// after every game), each game from both players, unflagged, with no game
// decided on penalties; anything else stays with staff.

// seriesSameGameMinStats is how many statistics two screenshots must share,
// all equal, before they count as the same game. A score and team names alone
// repeat between real games; thirteen statistics do not.
const seriesSameGameMinStats = 6

func evaluateSeriesScreenshots(in screenshotEvaluationInput) screenshotEvaluation {
	evaluation := screenshotEvaluation{Differences: []string{}, Reasons: []string{}, Games: []seriesGameEvaluation{}}
	reason := func(text string) { evaluation.Reasons = append(evaluation.Reasons, text) }
	prepared := prepareScreenshotReadings(in)
	if flagSeriesDuplicates(prepared.all) {
		prepared.blocked = true
	}
	switch {
	case prepared.pending:
		evaluation.Verdict = verdictPending
		return evaluation
	case len(prepared.all) == 0:
		evaluation.Verdict = verdictUnreadable
		return evaluation
	}
	groups, relabelled := groupSeriesGames(prepared.all)
	for _, group := range groups {
		evaluation.Games = append(evaluation.Games, evaluateSeriesGame(group.game, group.readings))
	}
	conflicted, unoriented := []int{}, false
	for _, game := range evaluation.Games {
		for _, difference := range game.Differences {
			if !slices.Contains(evaluation.Differences, difference) {
				evaluation.Differences = append(evaluation.Differences, difference)
			}
		}
		if len(game.Differences) > 0 {
			conflicted = append(conflicted, game.Game)
		} else if game.Score == nil {
			unoriented = true
		}
	}
	sort.Strings(evaluation.Differences)
	switch {
	case len(conflicted) > 0:
		evaluation.Verdict = verdictConflict
		for _, game := range conflicted {
			reason(fmt.Sprintf("The screenshots of game %d disagree.", game))
		}
		return evaluation
	case unoriented:
		evaluation.Verdict = verdictUnclear
		reason("The players' team names aren't known yet, so the screenshot's sides can't be matched to them.")
		return evaluation
	}
	for index := range evaluation.Games {
		evaluation.Games[index].Winner = seriesGameWinner(*evaluation.Games[index].Score)
	}
	evaluation.Score = seriesScore(evaluation.Games)
	homeFits := in.HomeClaim != nil && sameGamesWon(in.HomeClaim.Score, *evaluation.Score)
	awayFits := in.AwayClaim != nil && sameGamesWon(in.AwayClaim.Score, *evaluation.Score)
	switch {
	case !homeFits && !awayFits:
		evaluation.Verdict = verdictNeither
		return evaluation
	case homeFits && awayFits:
		evaluation.Verdict = verdictUnclear
		return evaluation
	}
	evaluation.Verdict = verdictSupports
	evaluation.SupportedSide = "away"
	if homeFits {
		evaluation.SupportedSide = "home"
	}
	penalties := slices.ContainsFunc(evaluation.Games, func(game seriesGameEvaluation) bool {
		return game.Score.HomePenalties != nil
	})
	switch {
	case relabelled:
		reason("The players numbered their games differently, so the screenshots were matched by what they show.")
	case !completeSeries(evaluation.Games, in.BestOf):
		reason("The screenshots don't show every game of the series.")
	case penalties:
		// The shoot-out display isn't confirmed on real screenshots yet.
		reason("Penalty shoot-outs are always checked by staff.")
	case prepared.blocked:
		reason("A screenshot was unreadable, flagged or below the confidence threshold.")
	case !everyGameFromBothPlayers(groups):
		reason("Each player must provide a readable screenshot of every game.")
	default:
		evaluation.Decision = "accept_" + evaluation.SupportedSide
	}
	return evaluation
}

type seriesGameGroup struct {
	game     int
	readings []*evaluatedReading
}

// groupSeriesGames groups the read result screens by game number. When the
// numbers make screenshots of a game disagree but every screenshot has
// exactly one same-game partner from the other player, the players only
// numbered their games differently: the screenshots are paired by content
// instead, under the home player's numbers, and relabelled is true.
func groupSeriesGames(all []*evaluatedReading) ([]seriesGameGroup, bool) {
	byGame := map[int][]*evaluatedReading{}
	for _, item := range all {
		byGame[item.Game] = append(byGame[item.Game], item)
	}
	labelled := make([]seriesGameGroup, 0, len(byGame))
	consistent := true
	for game, readings := range byGame {
		labelled = append(labelled, seriesGameGroup{game: game, readings: readings})
		consistent = consistent && len(evaluateSeriesGame(game, readings).Differences) == 0
	}
	sort.Slice(labelled, func(i, j int) bool { return labelled[i].game < labelled[j].game })
	if consistent {
		return labelled, false
	}
	var home, away []*evaluatedReading
	for _, item := range all {
		if item.UploadedBySide == "home" {
			home = append(home, item)
		} else {
			away = append(away, item)
		}
	}
	if len(home) == 0 || len(home) != len(away) {
		return labelled, false
	}
	used := make([]bool, len(away))
	paired := make([]seriesGameGroup, 0, len(home))
	for _, mine := range home {
		partner := -1
		for index, theirs := range away {
			if !used[index] && sameGameScreenshot(mine, theirs) {
				partner = index
				break
			}
		}
		if partner < 0 {
			return labelled, false
		}
		used[partner] = true
		paired = append(paired, seriesGameGroup{game: mine.Game, readings: []*evaluatedReading{mine, away[partner]}})
	}
	sort.Slice(paired, func(i, j int) bool { return paired[i].game < paired[j].game })
	return paired, true
}

// evaluateSeriesGame compares one game's screenshots. Like a best of 1, every
// pair must agree on everything read and map to the same home/away score.
func evaluateSeriesGame(game int, readings []*evaluatedReading) seriesGameEvaluation {
	result := seriesGameEvaluation{Game: game, Sides: []string{}, Differences: []string{}}
	for index, item := range readings {
		if !slices.Contains(result.Sides, item.UploadedBySide) {
			result.Sides = append(result.Sides, item.UploadedBySide)
		}
		for _, other := range readings[:index] {
			for _, difference := range screenshotDifferences(*other, *item) {
				if !slices.Contains(result.Differences, difference) {
					result.Differences = append(result.Differences, difference)
				}
			}
		}
		switch {
		case item.Score == nil:
		case result.Score == nil:
			result.Score = item.Score
		case !result.Score.equals(*item.Score) && !slices.Contains(result.Differences, "orientation"):
			result.Differences = append(result.Differences, "orientation")
		}
	}
	sort.Strings(result.Sides)
	sort.Strings(result.Differences)
	if len(result.Differences) > 0 {
		result.Score = nil
	}
	return result
}

// sameGameScreenshot says two readings show the same game: the same teams,
// score and penalties, and at least seriesSameGameMinStats equal statistics.
func sameGameScreenshot(a, b *evaluatedReading) bool {
	if a.Reading == nil || b.Reading == nil || len(screenshotDifferences(*a, *b)) > 0 {
		return false
	}
	shared := 0
	for _, key := range screenshotStatKeys {
		_, inA := a.Reading.Stats[key]
		_, inB := b.Reading.Stats[key]
		if inA && inB {
			shared++
		}
	}
	return shared >= seriesSameGameMinStats &&
		(a.Score == nil || b.Score == nil || a.Score.equals(*b.Score))
}

// flagSeriesDuplicates flags a player's screenshot sent for two games of the
// series. Reuse checks across matches skip the match itself, because both
// players legitimately send the same game; inside a series one player's two
// games must differ.
func flagSeriesDuplicates(all []*evaluatedReading) bool {
	found := false
	for index, item := range all {
		for _, other := range all[:index] {
			if other.UploadedBySide != item.UploadedBySide || other.Game == item.Game || !sameGameScreenshot(other, item) {
				continue
			}
			for _, flagged := range []*evaluatedReading{other, item} {
				if !slices.Contains(flagged.Flags, "duplicate_in_series") {
					flagged.Flags = append(flagged.Flags, "duplicate_in_series")
				}
			}
			found = true
		}
	}
	return found
}

// everyGameFromBothPlayers says every game has a screenshot from both players
// and that no screenshot of it is flagged.
func everyGameFromBothPlayers(groups []seriesGameGroup) bool {
	for _, group := range groups {
		sides := map[string]bool{}
		for _, item := range group.readings {
			if hasBlockingFlag(item.Flags) {
				return false
			}
			sides[item.UploadedBySide] = true
		}
		if !sides["home"] || !sides["away"] {
			return false
		}
	}
	return true
}

// completeSeries says the games run 1 to N without a gap and the series ends
// at game N: one side reaches the majority there and not before, or N is the
// last game of the best-of.
func completeSeries(games []seriesGameEvaluation, bestOf int) bool {
	need, home, away := gamesToWin(bestOf), 0, 0
	for index, game := range games {
		if game.Game != index+1 || home == need || away == need {
			return false
		}
		switch {
		case game.Winner == nil:
		case *game.Winner == "home":
			home++
		default:
			away++
		}
	}
	return len(games) > 0 && (home == need || away == need || len(games) == bestOf)
}

func sameGamesWon(claimed, shown screenshotScore) bool {
	return claimed.HomeScore == shown.HomeScore && claimed.AwayScore == shown.AwayScore
}

// seriesGameWinner is home or away, or nil for a level game without penalties.
func seriesGameWinner(score screenshotScore) *string {
	var tiebreak *tiebreakScoreInput
	if score.HomePenalties != nil && score.AwayPenalties != nil {
		tiebreak = &tiebreakScoreInput{Type: "penalties", HomeScore: *score.HomePenalties, AwayScore: *score.AwayPenalties}
	}
	switch homeWon, awayWon := resultWinner(score.HomeScore, score.AwayScore, tiebreak); {
	case homeWon:
		side := "home"
		return &side
	case awayWon:
		side := "away"
		return &side
	}
	return nil
}

// seriesScore is the games won that the screenshots show.
func seriesScore(games []seriesGameEvaluation) *screenshotScore {
	score := &screenshotScore{}
	for _, game := range games {
		switch {
		case game.Winner == nil:
		case *game.Winner == "home":
			score.HomeScore++
		default:
			score.AwayScore++
		}
	}
	return score
}
