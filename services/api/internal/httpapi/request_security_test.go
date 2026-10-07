package httpapi

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestClientIPTrustsForwardingOnlyFromConfiguredProxy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{TrustedProxyCIDRs: []string{"10.0.0.0/8"}}, logger, "test")

	trusted := httptest.NewRequest("GET", "/", nil)
	trusted.RemoteAddr = "10.0.0.5:443"
	trusted.Header.Set("X-Forwarded-For", "198.51.100.20, 10.1.1.1")
	if actual := server.clientIP(trusted); actual != "198.51.100.20" {
		t.Fatalf("expected forwarded client, got %s", actual)
	}

	untrusted := httptest.NewRequest("GET", "/", nil)
	untrusted.RemoteAddr = "203.0.113.9:443"
	untrusted.Header.Set("X-Forwarded-For", "198.51.100.20")
	if actual := server.clientIP(untrusted); actual != "203.0.113.9" {
		t.Fatalf("untrusted peer spoofed forwarding header: %s", actual)
	}
}

func TestCallbackPathIsRedactedForLogs(t *testing.T) {
	if actual := safeLogPath("/v1/payments/callbacks/stk/super-secret"); actual != "/v1/payments/callbacks/stk/[redacted]" {
		t.Fatalf("callback token was not redacted: %s", actual)
	}
}

func TestPositiveSessionCacheIsShortAndNeverOutlivesToken(t *testing.T) {
	if actual := minDuration(30*time.Second, maxPositiveSessionCacheTTL, time.Minute); actual != 5*time.Second {
		t.Fatalf("expected five-second positive cache cap, got %s", actual)
	}
	if actual := minDuration(30*time.Second, maxPositiveSessionCacheTTL, -time.Second); actual > 0 {
		t.Fatalf("expired token received positive cache TTL %s", actual)
	}
}
