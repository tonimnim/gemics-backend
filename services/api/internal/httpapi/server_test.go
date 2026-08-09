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
}
