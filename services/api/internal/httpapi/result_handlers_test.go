package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateScorePolicy(t *testing.T) {
	valid := submitResultInput{HomeScore: 3, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 3, AwayScore: 1}}}
	if message := validateScorePolicy(valid, 1, "single_elimination"); message != "" {
		t.Fatalf("valid score rejected: %s", message)
	}
	tied := submitResultInput{HomeScore: 1, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 1, AwayScore: 1}}}
	if message := validateScorePolicy(tied, 1, "single_elimination"); message == "" {
		t.Fatal("elimination tie accepted")
	}
	withPenalties := tied
	withPenalties.Tiebreak = &tiebreakScoreInput{Type: "penalties", HomeScore: 5, AwayScore: 4}
	if message := validateScorePolicy(withPenalties, 1, "single_elimination"); message != "" {
		t.Fatalf("valid penalty tiebreak rejected: %s", message)
	}
	if homeWon, awayWon := resultWinner(1, 1, withPenalties.Tiebreak); !homeWon || awayWon {
		t.Fatalf("penalty winner was not resolved: home=%v away=%v", homeWon, awayWon)
	}
	if message := validateScorePolicy(tied, 1, "round_robin"); message != "" {
		t.Fatalf("round-robin draw rejected: %s", message)
	}
	mismatched := submitResultInput{HomeScore: 4, AwayScore: 1, Games: []gameScoreInput{{HomeScore: 3, AwayScore: 1}}}
	if message := validateScorePolicy(mismatched, 1, "round_robin"); message == "" {
		t.Fatal("aggregate mismatch accepted")
	}
}

func TestValidateDecisionInput(t *testing.T) {
	if message := validateDecisionInput(decideResultInput{Decision: "confirm"}); message != "" {
		t.Fatalf("valid confirmation rejected: %s", message)
	}
	if message := validateDecisionInput(decideResultInput{Decision: "confirm", Note: "unexpected"}); message == "" {
		t.Fatal("confirmation payload smuggling accepted")
	}
	if message := validateDecisionInput(decideResultInput{Decision: "dispute", ReasonCode: "score_mismatch", Note: "The score is wrong."}); message != "" {
		t.Fatalf("valid dispute rejected: %s", message)
	}
	if message := validateDecisionInput(decideResultInput{Decision: "dispute", ReasonCode: "score_mismatch", Note: "short"}); message == "" {
		t.Fatal("short dispute note accepted")
	}
}

func TestReadIdempotencyKeyRejectsWhitespace(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/matches/id/result-submissions", nil)
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
