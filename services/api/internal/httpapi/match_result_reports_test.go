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
		"/v1/matches/" + testMatchID + "/score-reports/confirmation",
		"/v1/matches/" + testMatchID + "/score-reports/screenshot",
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

func TestScoreReportScopesAreActorBoundAndPerAction(t *testing.T) {
	first, second := "20000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"
	scopes := map[string]bool{}
	for _, user := range []string{first, second} {
		scopes[scoreReportScope(user, testMatchID)] = true
		scopes[resultConfirmationScope(user, testMatchID)] = true
		scopes[resultScreenshotScope(user, testMatchID)] = true
	}
	if len(scopes) != 6 {
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
}

func TestScoreReportHandlersRejectBadRequestsBeforeTheDatabase(t *testing.T) {
	// A cluster without pools proves each rejection happens before any query.
	server := &Server{db: &database.Cluster{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	userID := "20000000-0000-4000-8000-000000000001"
	evidenceID := "60000000-0000-4000-8000-000000000001"
	report, confirmation, screenshot := server.createScoreReport, server.createResultConfirmation, server.createResultScreenshot
	for _, test := range []struct {
		name, matchID, key, suffix, body string
		handler                          http.HandlerFunc
		wantStatus                       int
		wantCode                         string
	}{
		{"missing idempotency key", testMatchID, "", "", `{"homeScore":1,"awayScore":0,"declarationAccepted":true}`, report,
			http.StatusBadRequest, "idempotency_key_required"},
		{"bad match id", "not-a-match", "report-key-1", "", `{"homeScore":1,"awayScore":0,"declarationAccepted":true}`, report,
			http.StatusNotFound, "match_not_found"},
		{"unknown field", testMatchID, "report-key-1", "", `{"homeScore":1,"awayScore":0,"declarationAccepted":true,"evidenceIds":[]}`,
			report, http.StatusBadRequest, "invalid_request"},
		{"declaration not accepted", testMatchID, "report-key-1", "", `{"homeScore":1,"awayScore":0}`, report,
			http.StatusBadRequest, "invalid_score_report"},
		{"answer other than confirm or reject", testMatchID, "report-key-1", "/confirmation", `{"decision":"maybe"}`,
			confirmation, http.StatusBadRequest, "invalid_decision"},
		{"answer missing", testMatchID, "report-key-1", "/confirmation", `{}`, confirmation,
			http.StatusBadRequest, "invalid_decision"},
		{"screenshot without an id", testMatchID, "report-key-1", "/screenshot", `{"evidenceId":""}`, screenshot,
			http.StatusBadRequest, "invalid_evidence"},
		{"screenshot that is not a uuid", testMatchID, "report-key-1", "/screenshot", `{"evidenceId":"shot.png"}`, screenshot,
			http.StatusBadRequest, "invalid_evidence"},
		{"more than one screenshot", testMatchID, "report-key-1", "/screenshot",
			`{"evidenceIds":["` + evidenceID + `","` + evidenceID + `"]}`, screenshot, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := "/v1/matches/" + test.matchID + "/score-reports" + test.suffix
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(test.body))
			request.SetPathValue("matchId", test.matchID)
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			request = request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: userID}))
			recorder := httptest.NewRecorder()
			test.handler(recorder, request)
			if recorder.Code != test.wantStatus || !strings.Contains(recorder.Body.String(), `"`+test.wantCode+`"`) {
				t.Fatalf("got %d %s, want %d %s", recorder.Code, recorder.Body.String(), test.wantStatus, test.wantCode)
			}
		})
	}
}
