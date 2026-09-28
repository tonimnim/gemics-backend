package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
)

func TestScoreReportRoutesRequireAuthentication(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")
	mux := http.NewServeMux()
	server.registerMatchResultRoutes(mux)
	for _, path := range []string{
		"/v1/matches/" + testMatchID + "/score-reports",
		"/v1/matches/" + testMatchID + "/score-reports/final",
	} {
		for name, handler := range map[string]http.Handler{"registrar": mux, "server": server.http.Handler} {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("%s POST %s is not auth protected: %d", name, path, recorder.Code)
			}
		}
	}
}

func TestScoreReportScopesAreActorBoundAndPerKind(t *testing.T) {
	first, second := "20000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"
	scopes := map[string]bool{
		scoreReportScope(first, testMatchID):       true,
		scoreReportScope(second, testMatchID):      true,
		finalScoreReportScope(first, testMatchID):  true,
		finalScoreReportScope(second, testMatchID): true,
	}
	if len(scopes) != 4 {
		t.Fatalf("scopes collide: %v", scopes)
	}
	if scope := scoreReportScope(first, testMatchID); !strings.Contains(scope, first) || !strings.Contains(scope, testMatchID) {
		t.Fatalf("scope is not bound to the actor and match: %s", scope)
	}
}

func TestScoreReportInputValidation(t *testing.T) {
	for _, test := range []struct {
		name  string
		input scoreReportInput
		valid bool
	}{
		{"accepted declaration", scoreReportInput{HomeScore: 2, AwayScore: 1, DeclarationAccepted: true}, true},
		{"declaration missing", scoreReportInput{HomeScore: 2, AwayScore: 1}, false},
		{"negative score", scoreReportInput{HomeScore: -1, AwayScore: 1, DeclarationAccepted: true}, false},
		{"score above 99", scoreReportInput{HomeScore: 2, AwayScore: 100, DeclarationAccepted: true}, false},
	} {
		if message := test.input.problem(); (message == "") != test.valid {
			t.Fatalf("%s: problem = %q", test.name, message)
		}
	}

	evidence := func(ids ...string) finalScoreReportInput {
		return finalScoreReportInput{EvidenceIDs: ids}
	}
	one, two, three, four := "60000000-0000-4000-8000-000000000001", "60000000-0000-4000-8000-000000000002",
		"60000000-0000-4000-8000-000000000003", "60000000-0000-4000-8000-000000000004"
	for _, test := range []struct {
		name  string
		input finalScoreReportInput
		valid bool
	}{
		{"one screenshot", evidence(one), true},
		{"three screenshots", evidence(one, two, three), true},
		{"none", evidence(), false},
		{"four screenshots", evidence(one, two, three, four), false},
		{"duplicate", evidence(one, one), false},
		{"duplicate in another case", evidence(one, strings.ToUpper(" "+one+" ")), false},
		{"not a uuid", evidence("screenshot.png"), false},
	} {
		input := test.input
		if got := input.validEvidence(); got != test.valid {
			t.Fatalf("%s: validEvidence = %v", test.name, got)
		}
	}
}

func TestScoreReportHandlersRejectBadRequestsBeforeTheDatabase(t *testing.T) {
	// A cluster without pools proves each rejection happens before any query.
	server := &Server{db: &database.Cluster{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	userID := "20000000-0000-4000-8000-000000000001"
	evidenceID := "60000000-0000-4000-8000-000000000001"
	for _, test := range []struct {
		name, matchID, key, body string
		final                    bool
		wantStatus               int
		wantCode                 string
	}{
		{"missing idempotency key", testMatchID, "", `{"homeScore":1,"awayScore":0,"declarationAccepted":true}`, false,
			http.StatusBadRequest, "idempotency_key_required"},
		{"bad match id", "not-a-match", "report-key-1", `{"homeScore":1,"awayScore":0,"declarationAccepted":true}`, false,
			http.StatusNotFound, "match_not_found"},
		{"unknown field", testMatchID, "report-key-1", `{"homeScore":1,"awayScore":0,"declarationAccepted":true,"evidenceIds":[]}`,
			false, http.StatusBadRequest, "invalid_request"},
		{"declaration not accepted", testMatchID, "report-key-1", `{"homeScore":1,"awayScore":0}`, false,
			http.StatusBadRequest, "invalid_score_report"},
		{"final without screenshots", testMatchID, "report-key-1", `{"homeScore":1,"awayScore":0,"declarationAccepted":true,"evidenceIds":[]}`,
			true, http.StatusBadRequest, "invalid_evidence"},
		{"final with a repeated screenshot", testMatchID, "report-key-1",
			`{"homeScore":1,"awayScore":0,"declarationAccepted":true,"evidenceIds":["` + evidenceID + `","` + evidenceID + `"]}`,
			true, http.StatusBadRequest, "invalid_evidence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := "/v1/matches/" + test.matchID + "/score-reports"
			handler := server.createScoreReport
			if test.final {
				path, handler = path+"/final", server.createFinalScoreReport
			}
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(test.body))
			request.SetPathValue("matchId", test.matchID)
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			request = request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: userID}))
			recorder := httptest.NewRecorder()
			handler(recorder, request)
			if recorder.Code != test.wantStatus || !strings.Contains(recorder.Body.String(), `"`+test.wantCode+`"`) {
				t.Fatalf("got %d %s, want %d %s", recorder.Code, recorder.Body.String(), test.wantStatus, test.wantCode)
			}
		})
	}
}
