package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

// Registration takes a username, a Konami ID and a password; sign-in takes
// the Konami ID and password. Email and phone come later.

func TestNormalizeKonamiID(t *testing.T) {
	cases := []struct {
		raw, display, key string
		ok                bool
	}{
		{"ABCD-1234-EFGH", "ABCD-1234-EFGH", "ABCD1234EFGH", true},
		{"  abcd 1234 efgh  ", "abcd 1234 efgh", "ABCD1234EFGH", true},
		{"abcd.1234_efgh", "abcd.1234_efgh", "ABCD1234EFGH", true},
		{"123456", "123456", "123456", true},
		{"12345", "", "", false},
		{"-ABCD1234", "", "", false},
		{"ABC-DE", "", "", false},
		{"ABCD/1234", "", "", false},
		{"ÁBCD1234", "", "", false},
		{strings.Repeat("A", 64), strings.Repeat("A", 64), strings.Repeat("A", 64), true},
		{strings.Repeat("A", 65), "", "", false},
	}
	for _, tc := range cases {
		display, key, ok := normalizeKonamiID(tc.raw)
		if display != tc.display || key != tc.key || ok != tc.ok {
			t.Errorf("normalizeKonamiID(%q) = %q, %q, %v; want %q, %q, %v", tc.raw, display, key, ok, tc.display, tc.key, tc.ok)
		}
	}
}

// The sign-in lookup must use exactly the expression the unique index is
// built on, or PostgreSQL cannot use the index and the two could disagree.
func TestKonamiLookupMatchesTheUniqueIndex(t *testing.T) {
	migration := readSourceFile(t, filepath.Join("..", "..", "migrations", "000002_games.up.sql"))
	const index = "CREATE UNIQUE INDEX game_accounts_publisher_id_unique ON game_accounts USING btree (game_id, " +
		"upper(regexp_replace(publisher_player_id, '[^A-Za-z0-9]+'::text, ''::text, 'g'::text))) " +
		"WHERE (publisher_player_id IS NOT NULL);"
	if !strings.Contains(migration, index) {
		t.Fatal("game_accounts_publisher_id_unique is not built on the normalized Konami ID")
	}
	if konamiKeyExpression != `upper(regexp_replace(account.publisher_player_id,'[^A-Za-z0-9]+','','g'))` {
		t.Fatalf("konamiKeyExpression = %s, which the unique index cannot serve", konamiKeyExpression)
	}
	for _, path := range []string{"registration_handlers.go", "account_contact_handlers.go"} {
		assertFileContains(t, path, "AND account.publisher_player_id IS NOT NULL\n\t\tAND `+konamiKeyExpression+`=$2")
	}
}

func TestPasswordRepeatsIdentity(t *testing.T) {
	cases := []struct {
		password string
		want     bool
	}{
		{"Kamau_10", true},
		{"kamau_10", true},
		{" KAMAU_10 ", true},
		{"abcd-1234-efgh", true},
		{"ABCD1234EFGH", true},
		{"Kamau_10 rocks", false},
		{"a long passphrase", false},
	}
	for _, tc := range cases {
		if got := passwordRepeatsIdentity(tc.password, "Kamau_10", "ABCD1234EFGH"); got != tc.want {
			t.Errorf("passwordRepeatsIdentity(%q) = %v, want %v", tc.password, got, tc.want)
		}
	}
}

func TestNormalizeInternationalPhone(t *testing.T) {
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"+254712345678", "+254712345678", true},
		{"+254 712 345 678", "+254712345678", true},
		{"+91 98123-45678", "+919812345678", true},
		{"00919812345678", "+919812345678", true},
		{"+1 (415) 555-0100", "+14155550100", true},
		{"+44 7911 123456", "+447911123456", true},
		{"0712345678", "0712345678", false},
		{"254712345678", "254712345678", false},
		{"+0712345678", "+0712345678", false},
		{"+1234567", "+1234567", false},
		{"+1234567890123456", "+1234567890123456", false},
		{"+2547123abc", "+2547123abc", false},
	}
	for _, tc := range cases {
		got, ok := normalizeInternationalPhone(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Errorf("normalizeInternationalPhone(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRegistrationWritesTheWholeAccountInOneTransaction(t *testing.T) {
	assertFileOrder(t, "registration_handlers.go", "func (s *Server) register(",
		"gamicsauth.HashPassword(input.Password)",
		"tx, err := s.db.Writer.Begin(r.Context())",
		"createRegisteredPlayer(r.Context(), tx,",
		"s.createSession(r.Context(), tx, r, userID, input.DeviceName)",
		"tx.Commit(r.Context())",
		"s.writeSessionResponse(w, r, http.StatusCreated,",
		"func createRegisteredPlayer(",
		"INSERT INTO users(display_name,password_hash,password_changed_at,registration_ip,status)",
		"INSERT INTO player_profiles(user_id,handle)",
		"INSERT INTO game_accounts(user_id,game_id,in_game_name,publisher_player_id)",
		"INSERT INTO legal_acceptances")
}

// A wrong password and an unknown Konami ID must look the same: both verify a
// hash, both count a failure and both answer with one error.
func TestLoginDoesNotRevealWhichKonamiIDsExist(t *testing.T) {
	assertFileOrder(t, "registration_handlers.go", "func (s *Server) login(",
		"s.loginAllowed(r.Context(), key, ip)",
		"s.lookupKonamiAccount(r.Context(), key)",
		"if !gamicsauth.VerifyPassword(passwordHash, input.Password) {",
		"s.recordLoginFailure(r.Context(), key, ip)",
		`writeError(w, http.StatusUnauthorized, "invalid_credentials", "The Konami ID or password is incorrect.")`,
		`if status != "active" {`,
		"DELETE FROM login_failures WHERE konami_key=$1")
	assertFileOrder(t, "account_contact_handlers.go", "func (s *Server) requestPasswordReset(",
		`accepted := map[string]any{"status": "accepted"`,
		`if userID == "" || !s.allowOTPRate(r.Context(), "email", email, s.config.OTPEmailLimit) {`,
		"writeJSON(w, http.StatusAccepted, accepted)",
		"s.issueEmailCode(r.Context(), userID, emailPurposePasswordReset, email, ip)",
		"writeJSON(w, http.StatusAccepted, accepted)")
}

func TestAuthenticationRoutes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")
	// Public routes reach their handler, which reports the missing database.
	for _, path := range []string{"/v1/auth/register", "/v1/auth/login",
		"/v1/auth/password-reset/request", "/v1/auth/password-reset/confirm"} {
		recorder := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s = %d, want 503 without a database", path, recorder.Code)
		}
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/v1/me/password"},
		{http.MethodPost, "/v1/me/email"},
		{http.MethodPost, "/v1/me/email/verify"},
		{http.MethodPut, "/v1/me/phone"},
		{http.MethodDelete, "/v1/me/phone"},
	} {
		recorder := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, strings.NewReader("{}")))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401 without a token", route.method, route.path, recorder.Code)
		}
	}
}

// Registration is all a player needs before entering a competition: free
// entry, paid entry and the eligibility preflight share one gate.
func TestEntryGateNeedsOnlyARegisteredPlayer(t *testing.T) {
	const gate = "(profile.user_id IS NOT NULL\n\t\t AND player.terms_accepted_at IS NOT NULL AND player.privacy_accepted_at IS NOT NULL)"
	for _, path := range []string{"competition_handlers.go", "payment_handlers.go", "competition_eligibility.go"} {
		assertFileContains(t, path, gate)
		assertFileOmits(t, path, "display_name_set_at")
	}
}

func TestCountryFromPhone(t *testing.T) {
	cases := map[string]string{
		"+254712345678":  "KE",
		"+919812345678":  "IN",
		"+447911123456":  "GB",
		"+2348031234567": "NG",
		"+256712345678":  "UG",
		"+27821234567":   "ZA",
		"+971501234567":  "AE",
		"+79161234567":   "RU",
		"+77011234567":   "KZ",
		"+14155550100":   "",
		"+999123456789":  "",
		"254712345678":   "",
	}
	for phone, want := range cases {
		got, ok := countryFromPhone(phone)
		if got != want || ok != (want != "") {
			t.Errorf("countryFromPhone(%q) = %q, %v; want %q", phone, got, ok, want)
		}
	}
	// Calling codes are prefix-free, so no code may extend another.
	for code := range callingCodeCountries {
		for other := range callingCodeCountries {
			if code != other && strings.HasPrefix(other, code) {
				t.Errorf("calling code %s is a prefix of %s", code, other)
			}
		}
		if strings.HasPrefix(code, "1") || strings.HasPrefix(code, "7") {
			t.Errorf("calling code %s overlaps +1 or +7", code)
		}
	}
}
