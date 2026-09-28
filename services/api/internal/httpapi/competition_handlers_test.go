package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestCompetitionCursorRoundTrip(t *testing.T) {
	want := competitionCursor{StartsAt: time.Date(2026, 8, 9, 12, 30, 0, 0, time.UTC), ID: "11111111-1111-4111-8111-111111111111"}
	raw := encodeCompetitionCursor(want)
	var got competitionCursor
	if !decodeCompetitionCursor(raw, &got) || !got.StartsAt.Equal(want.StartsAt) || got.ID != want.ID {
		t.Fatalf("cursor round trip failed: %+v", got)
	}
	if decodeCompetitionCursor("not-base64", &got) {
		t.Fatal("invalid cursor was accepted")
	}
}

func TestCompetitionFilterIsBounded(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/competitions?limit=51", nil)
	recorder := httptest.NewRecorder()
	if _, ok := parseCompetitionFilter(recorder, request); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid limit response, got %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/competitions?entryType=stake", nil)
	recorder = httptest.NewRecorder()
	if _, ok := parseCompetitionFilter(recorder, request); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid entry type response, got %d", recorder.Code)
	}
}

func TestCompetitionRoutesDoNotInventDataWithoutDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")

	list := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/competitions", nil))
	if list.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable list without database, got %d", list.Code)
	}

	detail := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/v1/competitions/not-a-uuid", nil))
	if detail.Code != http.StatusNotFound {
		t.Fatalf("expected invalid competition id to be hidden as 404, got %d", detail.Code)
	}
}

func TestCompetitionEligibilityRouteIsModularAndAuthenticated(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{AccessTokenSecret: "eligibility-test-secret", RequestTimeout: time.Second}, logger, "test")
	mux := http.NewServeMux()
	server.registerCompetitionPolicyRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/v1/competitions/11111111-1111-4111-8111-111111111111/eligibility", nil))
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "authentication_required") {
		t.Fatalf("expected authenticated eligibility route, got %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestCompetitionCapacityAndEntryType(t *testing.T) {
	item := competitionSummary{MaxEntries: 32, EntryCount: 35, EntryFeeMinor: 10_000}
	finalizeCompetitionSummary(&item)
	if item.AvailableSlots != 0 || item.EntryType != "paid" {
		t.Fatalf("unexpected finalized summary: %+v", item)
	}
}

func TestBracketRowsBuildTypedRoundsSlotsScoresAndProgression(t *testing.T) {
	stageID := "stage-1"
	matchID, bracket, state := "match-1", "winners", "completed"
	round, number, graphRank, version := 2, 1, 4, 3
	activation := "unconditional"
	homeID, homeName, awayID, awayName := "home-entry", "Home", "away-entry", "Away"
	winnerID, sourceKind, sourceMatch := homeID, "winner_of", "source-match"
	sourceRank, homeScore, awayScore := 2, 3, 1
	submissionID := "submission-1"
	confirmedAt := time.Date(2026, 8, 24, 12, 30, 0, 0, time.UTC)
	row := bracketQueryRow{
		StageID: stageID, StageName: "Finals", StagePosition: 0, StageFormat: "double_elimination", StageBestOf: 1, StageStatus: "active",
		MatchID: &matchID, Bracket: &bracket, RoundNumber: &round, MatchNumber: &number, MatchState: &state,
		GraphRank: &graphRank, ActivationRule: &activation, HomeEntryID: &homeID, HomeDisplayName: &homeName,
		AwayEntryID: &awayID, AwayDisplayName: &awayName, WinnerEntryID: &winnerID, Version: &version,
		ScoreSubmissionID: &submissionID, HomeScore: &homeScore, AwayScore: &awayScore, ScoreConfirmedAt: &confirmedAt,
		HomeSource: bracketSlotRecord{SourceKind: &sourceKind, SourceMatchID: &sourceMatch, SourceGraphRank: &sourceRank, ResolvedEntryID: &homeID},
	}
	stages := []bracketStage{}
	appendBracketRow(&stages, map[string]int{}, map[string]int{}, row)
	if len(stages) != 1 || len(stages[0].Rounds) != 1 || len(stages[0].Rounds[0].Matches) != 1 {
		t.Fatalf("unexpected typed bracket grouping: %+v", stages)
	}
	match := stages[0].Rounds[0].Matches[0]
	if match.Home.Participant == nil || match.Home.Participant.EntryID != homeID || match.Home.Source == nil ||
		match.Home.Source.Kind != sourceKind || match.Score == nil || match.Score.HomeScore != 3 ||
		match.Progression.GraphRank != graphRank || match.WinnerEntryID == nil || *match.WinnerEntryID != winnerID {
		t.Fatalf("typed bracket lost graph or score data: %+v", match)
	}
}
