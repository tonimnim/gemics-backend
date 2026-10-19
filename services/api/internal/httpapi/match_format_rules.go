package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/gamics-io/gamics/services/api/internal/bracket"
)

// rules.matchFormat sets how many games every match is: a best of 1, 3 or 5,
// with overrides for named rounds. Rounds are named counting back from the
// final, because the bracket size is only known at the draw: "the final is a
// best of 5" holds whether 5 or 500 players join. The draw copies each match's
// best-of onto matches.best_of, and every result rule reads that column.
//
//	{"matchFormat": {"bestOf": 1, "rounds": {"semifinal": 3, "final": 5}}}
//
// Single elimination names its rounds final, semifinal, quarterfinal and
// round_of_16 up to round_of_1024, plus third_place. Double elimination names
// final (the grand final and its reset), winners_final and losers_final. A
// round robin has one best-of for every match.

var allowedBestOf = []int{1, 3, 5}

// seriesExtraPlayMinutes is the time added to a match's result window, and to
// a round robin's round interval, for every game after the first.
const seriesExtraPlayMinutes = 15

type matchFormatRules struct {
	BestOf int            `json:"bestOf"`
	Rounds map[string]int `json:"rounds,omitempty"`
}

var errInvalidMatchFormat = errors.New("match format is invalid")

type matchFormatRuleError struct{ message string }

func (e *matchFormatRuleError) Error() string {
	return errInvalidMatchFormat.Error() + ": " + e.message
}
func (e *matchFormatRuleError) Unwrap() error { return errInvalidMatchFormat }

func invalidMatchFormat(message string) error { return &matchFormatRuleError{message: message} }

// readMatchFormat is the tolerant read path: a missing or unreadable format is
// a best of 1 everywhere, and an unknown round or value is ignored.
func readMatchFormat(rules []byte) matchFormatRules {
	format := matchFormatRules{BestOf: 1}
	var document struct {
		MatchFormat *matchFormatRules `json:"matchFormat"`
	}
	if len(rules) == 0 || json.Unmarshal(rules, &document) != nil || document.MatchFormat == nil {
		return format
	}
	if slices.Contains(allowedBestOf, document.MatchFormat.BestOf) {
		format.BestOf = document.MatchFormat.BestOf
	}
	for round, bestOf := range document.MatchFormat.Rounds {
		if slices.Contains(allowedBestOf, bestOf) {
			if format.Rounds == nil {
				format.Rounds = map[string]int{}
			}
			format.Rounds[round] = bestOf
		}
	}
	return format
}

// validateMatchFormatRules is the strict write path. Every round must exist in
// the competition's format, so staff learn at once that an override would
// never apply.
func validateMatchFormatRules(rules []byte, competitionFormat string) error {
	var document struct {
		MatchFormat json.RawMessage `json:"matchFormat"`
	}
	if err := json.Unmarshal(rules, &document); err != nil {
		return invalidMatchFormat("matchFormat must be an object.")
	}
	if len(document.MatchFormat) == 0 || bytes.Equal(document.MatchFormat, []byte("null")) {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(document.MatchFormat, &fields); err != nil {
		return invalidMatchFormat("matchFormat must be an object.")
	}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		if key != "bestOf" && key != "rounds" {
			return invalidMatchFormat(key + " is not supported; use bestOf and rounds.")
		}
	}
	var format struct {
		BestOf *int           `json:"bestOf"`
		Rounds map[string]int `json:"rounds"`
	}
	if err := json.Unmarshal(document.MatchFormat, &format); err != nil {
		return invalidMatchFormat("bestOf and every round must be 1, 3 or 5.")
	}
	if format.BestOf == nil || !slices.Contains(allowedBestOf, *format.BestOf) {
		return invalidMatchFormat("bestOf must be 1, 3 or 5.")
	}
	for _, round := range slices.Sorted(maps.Keys(format.Rounds)) {
		if !matchFormatRoundAllowed(competitionFormat, round) {
			return invalidMatchFormat(round + " is not a round of a " + strings.ReplaceAll(competitionFormat, "_", " ") + " competition.")
		}
		if !slices.Contains(allowedBestOf, format.Rounds[round]) {
			return invalidMatchFormat("The " + round + " must be a best of 1, 3 or 5.")
		}
	}
	return nil
}

func matchFormatRoundAllowed(competitionFormat, round string) bool {
	switch bracket.Format(competitionFormat) {
	case bracket.SingleElimination:
		switch round {
		case "final", "semifinal", "quarterfinal", "third_place":
			return true
		}
		size, ok := strings.CutPrefix(round, "round_of_")
		if !ok {
			return false
		}
		players, err := strconv.Atoi(size)
		return err == nil && players >= 16 && players <= bracket.MaxEntries && players&(players-1) == 0 &&
			"round_of_"+strconv.Itoa(players) == round
	case bracket.DoubleElimination:
		return round == "final" || round == "winners_final" || round == "losers_final"
	default:
		return false
	}
}

// matchFormatRoundName names a match's round counting back from the end of its
// bracket. lastRound is the highest round number in each bracket of the graph.
func matchFormatRoundName(competitionFormat string, ref bracket.LocalRef, lastRound map[string]int) string {
	switch bracket.Format(competitionFormat) {
	case bracket.SingleElimination:
		if ref.Bracket == bracket.BracketBronze {
			return "third_place"
		}
		switch fromFinal := lastRound[ref.Bracket] - ref.Round; fromFinal {
		case 0:
			return "final"
		case 1:
			return "semifinal"
		case 2:
			return "quarterfinal"
		default:
			return "round_of_" + strconv.Itoa(1<<(fromFinal+1))
		}
	case bracket.DoubleElimination:
		switch {
		case ref.Bracket == bracket.BracketGrandFinal:
			return "final"
		case ref.Round == lastRound[ref.Bracket] && ref.Bracket == bracket.BracketWinners:
			return "winners_final"
		case ref.Round == lastRound[ref.Bracket] && ref.Bracket == bracket.BracketLosers:
			return "losers_final"
		}
	}
	return ""
}

// bestOfFor is one match's best-of: its round's override, else the default.
func (format matchFormatRules) bestOfFor(competitionFormat string, ref bracket.LocalRef, lastRound map[string]int) int {
	if bestOf, ok := format.Rounds[matchFormatRoundName(competitionFormat, ref, lastRound)]; ok {
		return bestOf
	}
	return format.BestOf
}

// lastRounds is the highest round number in each bracket of a graph.
func lastRounds(graph bracket.Graph) map[string]int {
	last := map[string]int{}
	for _, node := range graph.Nodes {
		last[node.Ref.Bracket] = max(last[node.Ref.Bracket], node.Ref.Round)
	}
	return last
}

// gamesToWin is the majority of a best-of: 2 of 3, 3 of 5.
func gamesToWin(bestOf int) int {
	return bestOf/2 + 1
}

// seriesExtraMinutes is the play time a series adds to a best-of-1 window.
func seriesExtraMinutes(bestOf int) int {
	return seriesExtraPlayMinutes * max(0, bestOf-1)
}
