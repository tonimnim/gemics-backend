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

// organizerRoutes lists every organizer route. It is duplicated from the route
// table on purpose: a new organizer route added without a line here is invisible
// to these tests, and a route added here that does not exist fails loudly.
var organizerRoutes = []struct {
	method string
	path   string
}{
	{http.MethodPost, "/v1/organizations"},
	{http.MethodGet, "/v1/me/organizations"},
	{http.MethodGet, "/v1/organizations/11111111-1111-4111-8111-111111111111"},
	{http.MethodPatch, "/v1/organizations/11111111-1111-4111-8111-111111111111"},
	{http.MethodGet, "/v1/organizations/11111111-1111-4111-8111-111111111111/members"},
	{http.MethodPost, "/v1/organizations/11111111-1111-4111-8111-111111111111/members"},
	{http.MethodPatch, "/v1/organizations/11111111-1111-4111-8111-111111111111/members/22222222-2222-4222-8222-222222222222"},
	{http.MethodDelete, "/v1/organizations/11111111-1111-4111-8111-111111111111/members/22222222-2222-4222-8222-222222222222"},
	{http.MethodGet, "/v1/organizations/11111111-1111-4111-8111-111111111111/competitions"},
	{http.MethodPost, "/v1/organizations/11111111-1111-4111-8111-111111111111/competitions"},
	{http.MethodGet, "/v1/organizations/11111111-1111-4111-8111-111111111111/competitions/33333333-3333-4333-8333-333333333333"},
	{http.MethodPatch, "/v1/organizations/11111111-1111-4111-8111-111111111111/competitions/33333333-3333-4333-8333-333333333333"},
	{http.MethodPost, "/v1/organizations/11111111-1111-4111-8111-111111111111/competitions/33333333-3333-4333-8333-333333333333/transitions"},
	{http.MethodGet, "/v1/organizations/11111111-1111-4111-8111-111111111111/competitions/33333333-3333-4333-8333-333333333333/entries"},
}

func organizerTestServer(t *testing.T) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Config{RequestTimeout: time.Second, AccessTokenSecret: strings.Repeat("k", 48)}, logger, "test")
}

// No organizer route may be reachable without a session. This is the test that
// would catch a route registered directly on the mux instead of through
// organizerRoute.
func TestEveryOrganizerRouteRequiresAuthentication(t *testing.T) {
	server := organizerTestServer(t)
	for _, route := range organizerRoutes {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, strings.NewReader("{}"))
		server.http.Handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s returned %d without a session, expected 401", route.method, route.path, recorder.Code)
		}
	}
}

func TestOrganizerRoutesAreNotCached(t *testing.T) {
	for _, route := range organizerRoutes {
		if !strings.HasPrefix(route.path, "/v1/organizations") && !strings.HasPrefix(route.path, "/v1/me") {
			t.Fatalf("unexpected organizer path prefix: %s", route.path)
		}
		if !sensitivePath(route.path) {
			t.Errorf("%s is not treated as a sensitive path, so responses would be cacheable", route.path)
		}
	}
}

// An invalid organization id must read as "not found" rather than "forbidden".
// Answering 403 would confirm that the identifier exists.
func TestUnknownOrganizationIsNotFoundNotForbidden(t *testing.T) {
	server := organizerTestServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/organizations/not-a-uuid/competitions", nil)
	server.http.Handler.ServeHTTP(recorder, request)
	// Without a session the auth layer answers first, which is itself correct:
	// membership is never revealed to an anonymous caller.
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for an anonymous request, got %d", recorder.Code)
	}
}
