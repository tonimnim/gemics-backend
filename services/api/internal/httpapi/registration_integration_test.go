package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
)

// captureMailer records the last code sent to each address.
type captureMailer struct {
	mu    sync.Mutex
	codes map[string]string
}

func (m *captureMailer) SendOTP(_ context.Context, recipient, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[recipient] = code
	return nil
}

func (m *captureMailer) code(recipient string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codes[recipient]
}

type registrationHarness struct {
	t      *testing.T
	server *Server
	mailer *captureMailer
}

type registrationSession struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	Player       struct {
		ID            string  `json:"id"`
		Username      string  `json:"username"`
		KonamiID      string  `json:"konamiId"`
		DisplayName   string  `json:"displayName"`
		Email         *string `json:"email"`
		EmailVerified bool    `json:"emailVerified"`
		PhoneNumber   *string `json:"phoneNumber"`
	} `json:"player"`
}

func newRegistrationHarness(t *testing.T) registrationHarness {
	pool := openMigratedIntegrationDatabase(t)
	mailer := &captureMailer{codes: map[string]string{}}
	return registrationHarness{t: t, mailer: mailer, server: &Server{
		db:     &database.Cluster{Writer: pool, Reader: pool},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tokens: gamicsauth.NewTokenManager("registration-access-secret", time.Minute),
		mailer: mailer,
		config: config.Config{OTPHashSecret: "registration-otp-secret", OTPMaxAttempts: 5, OTPTTL: 10 * time.Minute,
			OTPRequestWindow: 15 * time.Minute, OTPEmailLimit: 5, OTPIPLimit: 50,
			LoginFailureWindow: 15 * time.Minute, LoginAccountFailures: 3, LoginIPFailures: 100,
			RegistrationWindow: time.Hour, RegistrationIPLimit: 100},
	}}
}

func (h registrationHarness) call(handler http.HandlerFunc, method, target, body, userID string) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if userID != "" {
		request = request.WithContext(context.WithValue(h.t.Context(), identityContextKey{}, identity{UserID: userID}))
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func (h registrationHarness) expect(recorder *httptest.ResponseRecorder, status int, code string) {
	h.t.Helper()
	if recorder.Code != status || (code != "" && !strings.Contains(recorder.Body.String(), `"error":"`+code+`"`)) {
		h.t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body, status, code)
	}
}

func (h registrationHarness) session(recorder *httptest.ResponseRecorder, status int) registrationSession {
	h.t.Helper()
	h.expect(recorder, status, "")
	var session registrationSession
	if err := json.Unmarshal(recorder.Body.Bytes(), &session); err != nil {
		h.t.Fatal(err)
	}
	return session
}

func TestIntegrationRegisterAndSignInWithKonamiID(t *testing.T) {
	h := newRegistrationHarness(t)
	suffix := strings.ToLower(rand.Text())[:6]
	username := "Kamau_" + suffix
	konamiID := "ABCD-" + strings.ToUpper(suffix) + "-EFGH"

	registered := h.session(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
		`{"username":"`+username+`","konamiId":"`+konamiID+`","password":"long enough secret"}`, ""), http.StatusCreated)
	player := registered.Player
	if player.Username != username || player.DisplayName != username || player.KonamiID != konamiID ||
		player.Email != nil || player.EmailVerified || player.PhoneNumber != nil || registered.RefreshToken == "" {
		t.Fatalf("registered player = %+v", player)
	}
	// Registration alone satisfies the entry gate the competition handlers use.
	var entryReady bool
	var accounts int
	if err := h.server.db.Writer.QueryRow(t.Context(), `SELECT
		(profile.user_id IS NOT NULL
		 AND player.terms_accepted_at IS NOT NULL AND player.privacy_accepted_at IS NOT NULL),
		(SELECT count(*) FROM game_accounts WHERE user_id=player.id AND game_id='efootball-mobile')
		FROM users player LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE player.id=$1`, player.ID).Scan(&entryReady, &accounts); err != nil {
		t.Fatal(err)
	}
	if !entryReady || accounts != 1 {
		t.Fatalf("entry ready=%v eFootball accounts=%d", entryReady, accounts)
	}

	// The same username, or the same Konami ID typed differently, is taken.
	h.expect(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
		`{"username":"`+strings.ToUpper(username)+`","konamiId":"ZZZZ-`+suffix+`-9999","password":"long enough secret"}`, ""),
		http.StatusConflict, "username_taken")
	h.expect(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
		`{"username":"Other_`+suffix+`","konamiId":"abcd `+suffix+` efgh","password":"long enough secret"}`, ""),
		http.StatusConflict, "konami_id_taken")
	h.expect(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
		`{"username":"Third_`+suffix+`","konamiId":"QQQQ-`+suffix+`-QQQQ","password":"short"}`, ""),
		http.StatusBadRequest, "invalid_password")

	// Sign-in accepts any spelling of the Konami ID.
	h.session(h.call(h.server.login, http.MethodPost, "/v1/auth/login",
		`{"konamiId":"abcd`+suffix+`efgh","password":"long enough secret"}`, ""), http.StatusOK)
	h.expect(h.call(h.server.login, http.MethodPost, "/v1/auth/login",
		`{"konamiId":"NOPE-`+suffix+`-NOPE","password":"long enough secret"}`, ""), http.StatusUnauthorized, "invalid_credentials")

	// Repeated wrong passwords lock the Konami ID for the window.
	for range 3 {
		h.expect(h.call(h.server.login, http.MethodPost, "/v1/auth/login",
			`{"konamiId":"`+konamiID+`","password":"wrong password"}`, ""), http.StatusUnauthorized, "invalid_credentials")
	}
	h.expect(h.call(h.server.login, http.MethodPost, "/v1/auth/login",
		`{"konamiId":"`+konamiID+`","password":"long enough secret"}`, ""), http.StatusTooManyRequests, "rate_limited")
	if _, err := h.server.db.Writer.Exec(t.Context(), `DELETE FROM login_failures`); err != nil {
		t.Fatal(err)
	}

	// The Konami ID cannot be removed: it is how the player signs in.
	var accountID string
	if err := h.server.db.Writer.QueryRow(t.Context(), `SELECT id::text FROM game_accounts WHERE user_id=$1`, player.ID).
		Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/v1/me/game-accounts/"+accountID, strings.NewReader(`{"publisherPlayerId":""}`))
	request.SetPathValue("id", accountID)
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: player.ID}))
	recorder := httptest.NewRecorder()
	h.server.patchGameAccount(recorder, request)
	h.expect(recorder, http.StatusConflict, "konami_id_required")
}

func TestIntegrationContactDetailsAndPasswordRecovery(t *testing.T) {
	h := newRegistrationHarness(t)
	suffix := strings.ToLower(rand.Text())[:6]
	konamiID := "WXYZ-" + strings.ToUpper(suffix) + "-0001"
	registered := h.session(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
		`{"username":"Achieng_`+suffix+`","konamiId":"`+konamiID+`","password":"first password"}`, ""), http.StatusCreated)
	userID := registered.Player.ID

	// Without a verified email, a reset request answers the same and sends nothing.
	h.expect(h.call(h.server.requestPasswordReset, http.MethodPost, "/v1/auth/password-reset/request",
		`{"konamiId":"`+konamiID+`"}`, ""), http.StatusAccepted, "")
	if len(h.mailer.codes) != 0 {
		t.Fatalf("a code was sent without a verified email: %v", h.mailer.codes)
	}

	// Email is verified with a code before it is stored.
	email := "achieng." + suffix + "@gamics.test"
	h.expect(h.call(h.server.requestEmailVerification, http.MethodPost, "/v1/me/email", `{"email":"`+email+`"}`, userID),
		http.StatusAccepted, "")
	h.expect(h.call(h.server.verifyEmail, http.MethodPost, "/v1/me/email/verify", `{"code":"000000"}`, userID),
		http.StatusUnauthorized, "invalid_code")
	verified := h.call(h.server.verifyEmail, http.MethodPost, "/v1/me/email/verify", `{"code":"`+h.mailer.code(email)+`"}`, userID)
	h.expect(verified, http.StatusOK, "")
	if !strings.Contains(verified.Body.String(), `"emailVerified":true`) {
		t.Fatalf("after verification = %s", verified.Body)
	}

	// Phones are international and belong to one account.
	h.expect(h.call(h.server.putPhone, http.MethodPut, "/v1/me/phone", `{"phoneNumber":"0712345678"}`, userID),
		http.StatusBadRequest, "invalid_phone")
	saved := h.call(h.server.putPhone, http.MethodPut, "/v1/me/phone", `{"phoneNumber":"+91 98123 45678"}`, userID)
	h.expect(saved, http.StatusOK, "")
	if !strings.Contains(saved.Body.String(), `"phoneNumber":"+919812345678"`) {
		t.Fatalf("after saving the phone = %s", saved.Body)
	}
	if !strings.Contains(saved.Body.String(), `"countryCode":"IN"`) {
		t.Fatalf("an Indian phone did not set the country: %s", saved.Body)
	}
	// A country the player chose is never overridden by a later phone.
	h.expect(h.call(h.server.patchMe, http.MethodPatch, "/v1/me", `{"countryCode":"GB"}`, userID), http.StatusOK, "")
	kept := h.call(h.server.putPhone, http.MethodPut, "/v1/me/phone", `{"phoneNumber":"+254 712 000 111"}`, userID)
	h.expect(kept, http.StatusOK, "")
	if !strings.Contains(kept.Body.String(), `"countryCode":"GB"`) {
		t.Fatalf("a later phone replaced the chosen country: %s", kept.Body)
	}
	h.expect(h.call(h.server.putPhone, http.MethodPut, "/v1/me/phone", `{"phoneNumber":"+919812345678"}`, userID),
		http.StatusOK, "")
	other := h.session(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
		`{"username":"Otieno_`+suffix+`","konamiId":"WXYZ-`+strings.ToUpper(suffix)+`-0002","password":"other password"}`, ""), http.StatusCreated)
	h.expect(h.call(h.server.putPhone, http.MethodPut, "/v1/me/phone", `{"phoneNumber":"+919812345678"}`, other.Player.ID),
		http.StatusConflict, "phone_taken")

	// A forgotten password is reset through the verified email, and every
	// existing session is signed out.
	h.expect(h.call(h.server.requestPasswordReset, http.MethodPost, "/v1/auth/password-reset/request",
		`{"konamiId":"`+konamiID+`"}`, ""), http.StatusAccepted, "")
	code := h.mailer.code(email)
	h.expect(h.call(h.server.confirmPasswordReset, http.MethodPost, "/v1/auth/password-reset/confirm",
		`{"konamiId":"`+konamiID+`","code":"`+code+`","newPassword":"`+konamiID+`"}`, ""), http.StatusBadRequest, "weak_password")
	h.expect(h.call(h.server.confirmPasswordReset, http.MethodPost, "/v1/auth/password-reset/confirm",
		`{"konamiId":"`+konamiID+`","code":"`+code+`","newPassword":"second password"}`, ""), http.StatusNoContent, "")
	var live int
	if err := h.server.db.Writer.QueryRow(t.Context(), `SELECT count(*) FROM refresh_sessions
		WHERE user_id=$1 AND revoked_at IS NULL`, userID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d sessions survived a password reset", live)
	}
	h.expect(h.call(h.server.login, http.MethodPost, "/v1/auth/login",
		`{"konamiId":"`+konamiID+`","password":"first password"}`, ""), http.StatusUnauthorized, "invalid_credentials")
	h.session(h.call(h.server.login, http.MethodPost, "/v1/auth/login",
		`{"konamiId":"`+konamiID+`","password":"second password"}`, ""), http.StatusOK)

	// Changing the password needs the current one.
	h.expect(h.call(h.server.changePassword, http.MethodPost, "/v1/me/password",
		`{"currentPassword":"first password","newPassword":"third password"}`, userID), http.StatusUnauthorized, "invalid_credentials")
	h.expect(h.call(h.server.changePassword, http.MethodPost, "/v1/me/password",
		`{"currentPassword":"second password","newPassword":"third password"}`, userID), http.StatusNoContent, "")
	h.session(h.call(h.server.login, http.MethodPost, "/v1/auth/login",
		`{"konamiId":"`+konamiID+`","password":"third password"}`, ""), http.StatusOK)
}
