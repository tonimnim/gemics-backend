package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateScorePolicy(t *testing.T) {
	penalties := &tiebreakScoreInput{Type: "penalties", HomeScore: 5, AwayScore: 4}
	tests := []struct {
		name   string
		claim  scoreClaim
		bestOf int
		format string
		valid  bool
	}{
		{"decisive single game", scoreClaim{HomeScore: 3, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 3, AwayScore: 1}}},
			1, "single_elimination", true},
		{"elimination tie without penalties", scoreClaim{HomeScore: 1, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}},
			1, "single_elimination", false},
		{"elimination tie with penalties", scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: penalties,
			Games: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}}, 1, "double_elimination", true},
		{"level penalties", scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: &tiebreakScoreInput{Type: "penalties", HomeScore: 4, AwayScore: 4},
			Games: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}}, 1, "single_elimination", false},
		{"round-robin draw", scoreClaim{HomeScore: 1, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}},
			1, "round_robin", true},
		{"round-robin draw with penalties", scoreClaim{HomeScore: 1, AwayScore: 1, Tiebreak: penalties,
			Games: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}}, 1, "round_robin", false},
		{"penalties after a decisive score", scoreClaim{HomeScore: 2, AwayScore: 1, Tiebreak: penalties,
			Games: []gameScoreInput{{HomeScore: 2, AwayScore: 1}}}, 1, "single_elimination", false},
		{"games do not sum to the totals", scoreClaim{HomeScore: 4, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 3, AwayScore: 1}}},
			1, "round_robin", false},
		{"more games than best-of", scoreClaim{HomeScore: 2, AwayScore: 0,
			Games: []gameScoreInput{{HomeScore: 1, AwayScore: 0}, {HomeScore: 1, AwayScore: 0}}}, 1, "round_robin", false},
		{"no games", scoreClaim{HomeScore: 0, AwayScore: 0}, 1, "round_robin", false},
		{"game score out of range", scoreClaim{HomeScore: 100, AwayScore: 0, Games: []gameScoreInput{{HomeScore: 100, AwayScore: 0}}},
			1, "round_robin", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if message := validateScorePolicy(test.claim, test.bestOf, test.format); (message == "") != test.valid {
				t.Fatalf("validateScorePolicy(%+v) = %q, want valid=%v", test.claim, message, test.valid)
			}
		})
	}
	if homeWon, awayWon := resultWinner(1, 1, penalties); !homeWon || awayWon {
		t.Fatalf("penalty winner was not resolved: home=%v away=%v", homeWon, awayWon)
	}
}

func TestReadIdempotencyKeyRejectsWhitespace(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/matches/id/score-reports", nil)
	request.Header.Set("Idempotency-Key", "invalid key")
	if _, ok := readIdempotencyKey(recorder, request); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid key accepted: ok=%v status=%d", ok, recorder.Code)
	}
}

func TestRandomUUIDProducesUUIDv4(t *testing.T) {
	value, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidPattern.MatchString(value) || value[14] != '4' {
		t.Fatalf("unexpected UUID: %s", value)
	}
}
