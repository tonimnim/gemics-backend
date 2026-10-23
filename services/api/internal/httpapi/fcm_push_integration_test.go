package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
)

// TestIntegrationPushIsDeliveredThroughFCM sends projected notifications
// through the production claim and delivery to a fake FCM: an accepted push is
// delivered, a dead token is revoked, and an outage is retried.
func TestIntegrationPushIsDeliveredThroughFCM(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := &Server{db: &database.Cluster{Writer: pool, Reader: pool},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: config.Config{NotificationProject: 100, NotificationPushBatch: 100, NotificationPushTries: 8,
			FCMTimeout: 5 * time.Second, FCMConcurrency: 4}}
	var mu sync.Mutex
	sent := map[string]fcmWireMessage{}
	outage := false
	client, _ := fcmTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request fcmRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		device, _, _ := strings.Cut(request.Message.Token, "-")
		sent[device] = request.Message
		down := outage
		mu.Unlock()
		switch {
		case down:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"status":"UNAVAILABLE"}}`))
		case device == "tablet":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","details":[` +
				`{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`))
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "projects/ozone-test/messages/" + device})
		}
	})
	userID := pushSessionIntegrationUser(t, pool)
	phone := pushSessionIntegrationRegister(t, server, userID, pushSessionIntegrationSession(t, pool, userID), "phone", http.StatusOK)
	tablet := pushSessionIntegrationRegister(t, server, userID, pushSessionIntegrationSession(t, pool, userID), "tablet", http.StatusOK)

	pushSessionIntegrationNotify(t, pool, server, userID)
	if count, err := server.deliverNotificationPushBatch(t.Context(), client); err != nil || count != 2 {
		t.Fatalf("delivered %d pushes: %v", count, err)
	}
	var state, messageID string
	if err := pool.QueryRow(t.Context(), `SELECT state,COALESCE(provider_message_id,'') FROM notification_push_deliveries
		WHERE push_token_id=$1`, phone).Scan(&state, &messageID); err != nil || state != "delivered" ||
		messageID != "projects/ozone-test/messages/phone" {
		t.Fatalf("phone delivery = %s %q, %v", state, messageID, err)
	}
	pushed := sent["phone"]
	if pushed.Notification.Title != "Conduct strike recorded" || pushed.Data["kind"] != "player.strike_recorded" ||
		pushed.Data["notificationId"] == "" {
		t.Fatalf("phone push = %+v", pushed)
	}
	var code string
	if err := pool.QueryRow(t.Context(), `SELECT state,last_error_code FROM notification_push_deliveries
		WHERE push_token_id=$1`, tablet).Scan(&state, &code); err != nil || state != "permanent_failure" || code != "fcm_unregistered" {
		t.Fatalf("tablet delivery = %s %s, %v", state, code, err)
	}
	pushSessionIntegrationExpectRevoked(t, pool, tablet, true)
	pushSessionIntegrationExpectRevoked(t, pool, phone, false)

	// An outage leaves the push queued for a later attempt.
	mu.Lock()
	outage = true
	mu.Unlock()
	pushSessionIntegrationNotify(t, pool, server, userID)
	if count, err := server.deliverNotificationPushBatch(t.Context(), client); err != nil || count != 1 {
		t.Fatalf("delivered %d pushes during the outage: %v", count, err)
	}
	var later bool
	if err := pool.QueryRow(t.Context(), `SELECT state,last_error_code,next_attempt_at>now() FROM notification_push_deliveries
		WHERE push_token_id=$1 ORDER BY created_at DESC LIMIT 1`, phone).Scan(&state, &code, &later); err != nil ||
		state != "retry" || code != "fcm_unavailable" || !later {
		t.Fatalf("delivery during the outage = %s %s later=%v, %v", state, code, later, err)
	}
}
