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

func TestCompetitionCursorRoundTrip(t *testing.T) {
	want := competitionCursor{StartsAt: time.Date(2026, 8, 9, 12, 30, 0, 0, time.UTC), ID: "11111111-1111-4111-8111-111111111111"}
	raw := encodeCompetitionCursor(want)
	var got competitionCursor
	if !decodeCompetitionCursor(raw, &got) || !got.StartsAt.Equal(want.StartsAt) || got.ID != want.ID {
		t.Fatalf("cursor round trip failed: %+v", got)
	}
	if decodeCompetitionCursor("not-base64", &got) {
		t.Fatal("invalid cursor was accepted")
	}
}

func TestCompetitionFilterIsBounded(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/v1/competitions?limit=51", nil)
	recorder := httptest.NewRecorder()
	if _, ok := parseCompetitionFilter(recorder, request); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid limit response, got %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/competitions?entryType=stake", nil)
	recorder = httptest.NewRecorder()
	if _, ok := parseCompetitionFilter(recorder, request); ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid entry type response, got %d", recorder.Code)
	}
}

func TestCompetitionRoutesDoNotInventDataWithoutDatabase(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")

	list := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/competitions", nil))
	if list.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable list without database, got %d", list.Code)
	}

	detail := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/v1/competitions/not-a-uuid", nil))
	if detail.Code != http.StatusNotFound {
		t.Fatalf("expected invalid competition id to be hidden as 404, got %d", detail.Code)
	}
}

func TestCompetitionCapacityAndEntryType(t *testing.T) {
	item := competitionSummary{MaxEntries: 32, EntryCount: 35, EntryFeeMinor: 10_000}
	finalizeCompetitionSummary(&item)
	if item.AvailableSlots != 0 || item.EntryType != "paid" {
		t.Fatalf("unexpected finalized summary: %+v", item)
	}
}
