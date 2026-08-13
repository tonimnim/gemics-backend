package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

const testMatchID = "018f0d5e-7b7a-4f31-a955-37fc0b6fb111"

func TestMatchRoutesRequireAuthentication(t *testing.T) {
	server := newMatchTestServer()
	for _, route := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/me/matches"},
		{http.MethodGet, "/v1/matches/" + testMatchID},
		{http.MethodPost, "/v1/matches/" + testMatchID + "/check-ins"},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, nil)
		server.http.Handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "authentication_required") {
			t.Fatalf("%s %s: expected authenticated route, got %d %q", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestMatchCursorRoundTripAndTamperResistance(t *testing.T) {
	server := newMatchTestServer()
	record := matchRecord{ID: testMatchID, SortAt: time.Date(2026, 8, 9, 12, 30, 0, 123, time.UTC)}
	encoded := server.encodeMatchCursor("active", record)
	decoded, err := server.decodeMatchCursor(encoded, "active")
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != record.ID || decoded.SortAt != record.SortAt.UnixNano() || decoded.State != "active" {
		t.Fatalf("unexpected decoded cursor: %+v", decoded)
	}
	if _, err := server.decodeMatchCursor(encoded, "history"); err == nil {
		t.Fatal("cursor must not be reusable across state buckets")
	}
	replacement := "A"
	if strings.HasSuffix(encoded, replacement) {
		replacement = "B"
	}
	tampered := encoded[:len(encoded)-1] + replacement
	if _, err := server.decodeMatchCursor(tampered, "active"); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
}

func TestMatchPresentationDerivesAllowedActions(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	opens := now.Add(-5 * time.Minute)
	closes := now.Add(5 * time.Minute)
	due := now.Add(time.Hour)
	homeID, awayID := "home-player", "away-player"
	record := matchRecord{
		State: "ready", CurrentSide: "home", HomePlayerID: &homeID, AwayPlayerID: &awayID,
		CheckInOpensAt: &opens, CheckInClosesAt: &closes, ResultDueAt: &due,
	}
	lifecycle, actions := record.presentation(homeID, now, &opens, &closes)
	if lifecycle != "ready_for_check_in" || !slices.Equal(actions, []string{"check_in"}) {
		t.Fatalf("unexpected ready presentation: %q %v", lifecycle, actions)
	}

	checkedIn := now.Add(-time.Minute)
	record.HomeCheckedInAt = &checkedIn
	lifecycle, actions = record.presentation(homeID, now, &opens, &closes)
	if lifecycle != "checked_in" || len(actions) != 0 {
		t.Fatalf("unexpected one-sided check-in presentation: %q %v", lifecycle, actions)
	}

	record.State = "in_progress"
	record.AwayCheckedInAt = &checkedIn
	lifecycle, actions = record.presentation(homeID, now, &opens, &closes)
	if lifecycle != "checked_in" || !slices.Equal(actions, []string{"submit_result"}) {
		t.Fatalf("unexpected playable presentation: %q %v", lifecycle, actions)
	}

	submissionID, submissionState, submittedBy := "submission-1", "pending_confirmation", awayID
	record.State = "awaiting_confirmation"
	record.SubmissionID, record.SubmissionState, record.SubmittedBy = &submissionID, &submissionState, &submittedBy
	lifecycle, actions = record.presentation(homeID, now, &opens, &closes)
	if lifecycle != "opponent_action_required" || !slices.Equal(actions, []string{"confirm_result", "dispute_result"}) {
		t.Fatalf("unexpected opponent decision presentation: %q %v", lifecycle, actions)
	}
	lifecycle, actions = record.presentation(awayID, now, &opens, &closes)
	if lifecycle != "awaiting_opponent" || len(actions) != 0 {
		t.Fatalf("submitter must not confirm their own result: %q %v", lifecycle, actions)
	}
}

func TestCheckInDeadlinesAreServerEnforced(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	homeID, awayID := "home", "away"
	record := matchRecord{State: "ready", HomePlayerID: &homeID, AwayPlayerID: &awayID}
	opens, closes := now.Add(time.Minute), now.Add(10*time.Minute)
	record.CheckInOpensAt, record.CheckInClosesAt = &opens, &closes
	code, _, retry := record.checkInRejection(now)
	if code != "check_in_not_open" || retry != time.Minute {
		t.Fatalf("unexpected early rejection: %q %s", code, retry)
	}
	pastOpen, pastClose := now.Add(-10*time.Minute), now.Add(-time.Second)
	record.CheckInOpensAt, record.CheckInClosesAt = &pastOpen, &pastClose
	code, _, _ = record.checkInRejection(now)
	if code != "check_in_closed" {
		t.Fatalf("unexpected late rejection: %q", code)
	}
	record.State = "in_progress"
	code, _, _ = record.checkInRejection(now)
	if code != "match_not_ready" {
		t.Fatalf("unexpected state rejection: %q", code)
	}
}

func TestMatchSettingsUseValidatedStageOverrides(t *testing.T) {
	competition := []byte(`{"drawAllowed":false,"friendMatchInstructions":[{"title":"Competition rule","detail":"Use this setting."}]}`)
	stage := []byte(`{"drawAllowed":true,"friendMatchInstructions":[{"title":"Final rule","detail":"Use the final setting."}]}`)
	settings := resolveMatchSettings("efootball-mobile", "single_elimination", competition, stage)
	if !settings.DrawAllowed || len(settings.Instructions) != 1 || settings.Instructions[0].Title != "Final rule" {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	invalid := []byte(`{"friendMatchInstructions":[{"title":"","detail":"missing title"}]}`)
	settings = resolveMatchSettings("efootball-mobile", "single_elimination", nil, invalid)
	if len(settings.Instructions) != 4 {
		t.Fatalf("invalid rules must fall back to safe defaults: %+v", settings.Instructions)
	}
}

func TestMatchListRequestValidation(t *testing.T) {
	server := newMatchTestServer()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/me/matches?state=active&limit=50", nil)
	state, limit, cursor, ok := server.parseMatchPageRequest(recorder, request)
	if !ok || state != "active" || limit != 50 || cursor != nil {
		t.Fatalf("unexpected page request: %q %d %+v %v", state, limit, cursor, ok)
	}
	for _, query := range []string{"state=unknown", "state=history&limit=0", "state=active&limit=51", "cursor=not-signed"} {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, "/v1/me/matches?"+query, nil)
		if _, _, _, ok := server.parseMatchPageRequest(recorder, request); ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("expected %q to be rejected, got %d", query, recorder.Code)
		}
	}
}

func TestIdempotencyKeyValidation(t *testing.T) {
	for _, value := range []string{"retry-key-123", "018f0d5e-7b7a-4f31-a955-37fc0b6fb111"} {
		if !validIdempotencyKey(value) {
			t.Fatalf("expected %q to be valid", value)
		}
	}
	for _, value := range []string{"short", "contains space", strings.Repeat("x", 129)} {
		if validIdempotencyKey(value) {
			t.Fatalf("expected %q to be invalid", value)
		}
	}
}

func newMatchTestServer() *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Config{AccessTokenSecret: "match-test-secret", RequestTimeout: time.Second}, logger, "test")
}
