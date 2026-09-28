package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

// Organizers never decide results, so the referee case, evidence-request,
// appeal and legacy confirm/dispute routes are gone rather than forbidden. A
// surviving registration would answer 401 before its handler ran.
func TestRemovedRoutesAreNotFound(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")
	const (
		organizationID = "0f8e2f53-4c0b-4d7e-9a3c-6f1d2b8e4a51"
		matchID        = "4d3e5536-bf3c-4dba-a643-575e43f56970"
		caseID         = "b252e94f-a746-42c0-a54b-ff5bf289a64a"
		childID        = "7c9d1a2e-3b4f-4e5a-8d6c-1f2e3a4b5c6d"
	)
	organizationCases := "/v1/organizations/" + organizationID + "/referee-cases"
	removed := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/matches/" + matchID + "/result-submissions"},
		{http.MethodPost, "/v1/result-submissions/" + caseID + "/confirmations"},
		{http.MethodGet, "/v1/matches/" + matchID + "/referee-case"},
		{http.MethodPost, "/v1/referee-cases/" + caseID + "/evidence-requests/" + childID + "/responses"},
		{http.MethodPost, "/v1/referee-cases/" + caseID + "/appeals"},
		{http.MethodGet, organizationCases},
		{http.MethodGet, organizationCases + "/" + caseID},
		{http.MethodPut, organizationCases + "/" + caseID + "/assignment"},
		{http.MethodPost, organizationCases + "/" + caseID + "/evidence-requests"},
		{http.MethodPost, organizationCases + "/" + caseID + "/decision"},
		{http.MethodPost, organizationCases + "/" + caseID + "/appeals/" + childID + "/decision"},
	}
	for _, route := range removed {
		recorder := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s %s is still routed: status=%d", route.method, route.path, recorder.Code)
		}
	}
}
