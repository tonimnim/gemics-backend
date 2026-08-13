package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

func TestPublicCursorRoundTripRejectsTamperingAndWrongKind(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	secret := "cursor-test-secret-with-enough-entropy"
	token, err := encodePublicCursor(publicCursor{
		Kind: "rankings", ExpiresAt: now.Add(time.Hour).Unix(), SnapshotID: "snapshot-id",
		GameID: "efootball-mobile", Scope: "country", CountryCode: "KE", Rank: 20,
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := decodePublicCursor(token, "rankings", secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Rank != 20 || cursor.CountryCode != "KE" || cursor.SnapshotID != "snapshot-id" {
		t.Fatalf("unexpected cursor: %+v", cursor)
	}
	if _, err := decodePublicCursor(token, "players", secret, now); !errors.Is(err, errInvalidPublicCursor) {
		t.Fatalf("wrong cursor kind should fail, got %v", err)
	}

	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-2] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(payload) + "." + parts[1]
	if _, err := decodePublicCursor(tampered, "rankings", secret, now); !errors.Is(err, errInvalidPublicCursor) {
		t.Fatalf("tampered cursor should fail, got %v", err)
	}
}

func TestPublicCursorExpires(t *testing.T) {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	token, err := encodePublicCursor(publicCursor{Kind: "players", ExpiresAt: now.Unix()}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePublicCursor(token, "players", "secret", now); !errors.Is(err, errExpiredPublicCursor) {
		t.Fatalf("expected expired cursor, got %v", err)
	}
}

func TestPublicLimitAndLikeEscaping(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want int
		ok   bool
	}{{"", 20, true}, {" 50 ", 50, true}, {"0", 0, false}, {"51", 0, false}, {"all", 0, false}} {
		got, err := parsePublicLimit(test.raw)
		if test.ok && (err != nil || got != test.want) {
			t.Fatalf("parsePublicLimit(%q)=(%d,%v), want %d", test.raw, got, err, test.want)
		}
		if !test.ok && err == nil {
			t.Fatalf("parsePublicLimit(%q) should fail", test.raw)
		}
	}
	if got := escapeLike(`50%_\name`); got != `50\%\_\\name` {
		t.Fatalf("unexpected LIKE escape %q", got)
	}
}

func TestRankingDefaultsAndCursorFilterBinding(t *testing.T) {
	server := &Server{config: config.Config{AccessTokenSecret: "cursor-secret"}}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	request := httptest.NewRequest(http.MethodGet, "/v1/rankings", nil)
	options, err := server.parseRankingsOptions(request, now)
	if err != nil {
		t.Fatal(err)
	}
	if options.GameID != "efootball-mobile" || options.Scope != "country" || options.CountryCode != "KE" || options.Limit != 20 {
		t.Fatalf("unexpected defaults: %+v", options)
	}

	token, err := encodePublicCursor(publicCursor{
		Kind: "rankings", ExpiresAt: now.Add(time.Hour).Unix(), SnapshotID: "snapshot-id",
		GameID: "efootball-mobile", Scope: "country", CountryCode: "KE", Rank: 20,
	}, server.config.AccessTokenSecret)
	if err != nil {
		t.Fatal(err)
	}
	mismatch := httptest.NewRequest(http.MethodGet, "/v1/rankings?scope=country&country=UG&cursor="+token, nil)
	if _, err := server.parseRankingsOptions(mismatch, now); !errors.Is(err, errInvalidPublicCursor) {
		t.Fatalf("cursor must be bound to filters, got %v", err)
	}
}

func TestPlayerSearchValidationAndRouteRegistration(t *testing.T) {
	server := &Server{config: config.Config{AccessTokenSecret: "cursor-secret"}}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	request := httptest.NewRequest(http.MethodGet, "/v1/players?q=a", nil)
	if _, err := server.parsePlayersOptions(request, now); err == nil {
		t.Fatal("one-character public searches must be rejected")
	}

	mux := http.NewServeMux()
	server.registerPlayerDiscoveryRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/rankings", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("registered handler returned %d, want 503 without a database", recorder.Code)
	}
}
