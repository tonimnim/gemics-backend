package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestGameCatalog(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{AllowedOrigins: []string{"http://localhost:3000"}, RequestTimeout: time.Second}, logger, "test")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/games", nil)

	server.http.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if recorder.Header().Get("ETag") == "" || recorder.Header().Get("X-Cache") != "BYPASS" {
		t.Fatalf("missing cache headers: %+v", recorder.Header())
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "efootball-mobile" {
		t.Fatalf("unexpected game catalog: %+v", body.Data)
	}

	conditional := httptest.NewRecorder()
	conditionalRequest := httptest.NewRequest(http.MethodGet, "/v1/games", nil)
	conditionalRequest.Header.Set("If-None-Match", recorder.Header().Get("ETag"))
	server.http.Handler.ServeHTTP(conditional, conditionalRequest)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("expected empty 304, got %d %q", conditional.Code, conditional.Body.String())
	}
}

func TestProfileCORSAllowsPut(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{AllowedOrigins: []string{"https://app.gamics.test"}, RequestTimeout: time.Second}, logger, "test")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/v1/me/profile", nil)
	request.Header.Set("Origin", "https://app.gamics.test")
	server.http.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Methods") != "GET, POST, PUT, PATCH, DELETE, OPTIONS" {
		t.Fatalf("unexpected preflight response: %d %+v", recorder.Code, recorder.Header())
	}
}
