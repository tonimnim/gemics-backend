package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestExpoPushTokenValidation(t *testing.T) {
	valid := []string{
		"ExpoPushToken[abcdefghijklmnopqrstuvwx]",
		"ExponentPushToken[abcDEF0123456789_-abcdefgh]",
	}
	for _, token := range valid {
		if !expoPushTokenPattern.MatchString(token) {
			t.Fatalf("expected valid Expo token %q", token)
		}
	}
	invalid := []string{
		"", "ExponentPushToken[short]", "not-expo[abcdefghijklmnopqrstuvwx]",
		"ExpoPushToken[abcdefghijklmnopqrstu vwx]",
	}
	for _, token := range invalid {
		if expoPushTokenPattern.MatchString(token) {
			t.Fatalf("accepted invalid Expo token %q", token)
		}
	}
}

func TestNotificationCursorIsSignedAndBoundToUserAndFilter(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	server := &Server{config: config.Config{AccessTokenSecret: "notification-cursor-test-secret"}}
	userID := "550e8400-e29b-41d4-a716-446655440000"
	notificationID := "123e4567-e89b-42d3-a456-426614174000"
	token, err := encodePublicCursor(publicCursor{
		Kind: "my_notifications", ExpiresAt: now.Add(time.Hour).Unix(), SnapshotAt: now.UnixNano(),
		Query: userID, Scope: "unread", SortTime: now.Add(-time.Minute).UnixNano(), ID: notificationID,
	}, server.config.AccessTokenSecret)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/me/notifications?unreadOnly=true&limit=50&cursor="+token, nil)
	options, err := server.parseNotificationPageOptions(request, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	if options.Limit != 50 || !options.UnreadOnly || options.Cursor == nil || options.Cursor.ID != notificationID {
		t.Fatalf("unexpected options: %+v", options)
	}
	if _, err := server.parseNotificationPageOptions(request, "650e8400-e29b-41d4-a716-446655440000", now); err == nil {
		t.Fatal("cursor must not be reusable by another player")
	}
	mismatched := httptest.NewRequest(http.MethodGet, "/v1/me/notifications?unreadOnly=false&cursor="+token, nil)
	if _, err := server.parseNotificationPageOptions(mismatched, userID, now); err == nil {
		t.Fatal("unread cursor must not be reusable for the all-notifications feed")
	}
}

func TestNotificationPaginationValidation(t *testing.T) {
	server := &Server{config: config.Config{AccessTokenSecret: "notification-cursor-test-secret"}}
	for _, target := range []string{
		"/v1/me/notifications?limit=0",
		"/v1/me/notifications?limit=51",
		"/v1/me/notifications?unreadOnly=yes",
		"/v1/me/notifications?cursor=unsigned",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		if _, err := server.parseNotificationPageOptions(request, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC()); err == nil {
			t.Fatalf("expected validation failure for %s", target)
		}
	}
}

func TestIdentityNotificationRoutesAreRegisteredAndProtected(t *testing.T) {
	server := &Server{}
	mux := http.NewServeMux()
	server.registerIdentityNotificationRoutes(mux)

	for method, target := range map[string]string{
		http.MethodPost:   "/v1/me/push-tokens",
		http.MethodGet:    "/v1/me/notifications",
		http.MethodPatch:  "/v1/me/notification-preferences",
		http.MethodDelete: "/v1/me/sessions/550e8400-e29b-41d4-a716-446655440000",
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s returned %d, want 401", method, target, recorder.Code)
		}
	}

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/legal/documents/current", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("public legal route returned %d, want 503 without a database", recorder.Code)
	}
}

func TestAccountDeletionRequiresExactExplicitConfirmation(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440000"
	if !validAccountDeletionConfirmation(id, "DELETE") {
		t.Fatal("valid deletion confirmation rejected")
	}
	for _, confirmation := range []string{"delete", " DELETE", "DELETE ", ""} {
		if validAccountDeletionConfirmation(id, confirmation) {
			t.Fatalf("accepted ambiguous confirmation %q", confirmation)
		}
	}
	if validAccountDeletionConfirmation("not-a-uuid", "DELETE") {
		t.Fatal("accepted invalid deletion request id")
	}
}
