package httpapi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
)

const (
	testGoogleClientID = "test-google.apps.googleusercontent.com"
	testAppleClientID  = "io.tonits.app.test"
	testIdentityKeyID  = "test-identity-key"
)

var (
	testIdentityKeyOnce sync.Once
	testIdentityKey     *rsa.PrivateKey
)

func testIdentityKeys(t *testing.T) gamicsauth.StaticKeySource {
	t.Helper()
	testIdentityKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testIdentityKey = key
	})
	public := map[string]*rsa.PublicKey{testIdentityKeyID: &testIdentityKey.PublicKey}
	return gamicsauth.StaticKeySource{"google": public, "apple": public}
}

// signTestIdentityToken signs a provider-shaped identity token.
func signTestIdentityToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	testIdentityKeys(t)
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": testIdentityKeyID, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, testIdentityKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func googleToken(t *testing.T, subject, email, givenName string) string {
	now := time.Now()
	return signTestIdentityToken(t, map[string]any{"iss": "https://accounts.google.com", "aud": testGoogleClientID,
		"sub": subject, "email": email, "email_verified": true, "given_name": givenName,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
}

func appleToken(t *testing.T, subject, email, rawNonce string) string {
	now := time.Now()
	hashed := sha256.Sum256([]byte(rawNonce))
	return signTestIdentityToken(t, map[string]any{"iss": "https://appleid.apple.com", "aud": testAppleClientID,
		"sub": subject, "email": email, "email_verified": "true", "nonce": hex.EncodeToString(hashed[:]),
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
}

type socialPlayer struct {
	ID                  string   `json:"id"`
	Username            string   `json:"username"`
	DisplayName         string   `json:"displayName"`
	KonamiID            *string  `json:"konamiId"`
	Email               *string  `json:"email"`
	EmailVerified       bool     `json:"emailVerified"`
	PhoneNumber         *string  `json:"phoneNumber"`
	PhoneVerified       bool     `json:"phoneVerified"`
	CountryCode         string   `json:"countryCode"`
	OnboardingCompleted bool     `json:"onboardingCompleted"`
	SignInMethods       []string `json:"signInMethods"`
}

func decodeSocial(t *testing.T, h registrationHarness, recorder interface {
	Result() *http.Response
}, body []byte, status int) (string, socialPlayer) {
	t.Helper()
	var session struct {
		AccessToken string       `json:"accessToken"`
		Player      socialPlayer `json:"player"`
	}
	if recorder.Result().StatusCode != status {
		t.Fatalf("status = %d %s, want %d", recorder.Result().StatusCode, body, status)
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatal(err)
	}
	return session.AccessToken, session.Player
}

func TestIntegrationGoogleAndAppleSignInAndOnboarding(t *testing.T) {
	h := newRegistrationHarness(t)
	suffix := strings.ToLower(rand.Text())[:8]
	email := "wanjiru." + suffix + "@example.com"

	google := func(token string) (int, []byte) {
		recorder := h.call(h.server.signInWithGoogle, http.MethodPost, "/v1/auth/google",
			`{"idToken":"`+token+`","deviceName":"Pixel"}`, "")
		return recorder.Code, recorder.Body.Bytes()
	}
	apple := func(token, nonce string) (int, []byte) {
		recorder := h.call(h.server.signInWithApple, http.MethodPost, "/v1/auth/apple",
			`{"identityToken":"`+token+`","nonce":"`+nonce+`"}`, "")
		return recorder.Code, recorder.Body.Bytes()
	}
	player := func(body []byte) socialPlayer {
		t.Helper()
		var session struct {
			AccessToken string       `json:"accessToken"`
			Player      socialPlayer `json:"player"`
		}
		if err := json.Unmarshal(body, &session); err != nil || session.AccessToken == "" {
			t.Fatalf("session body = %s, %v", body, err)
		}
		return session.Player
	}

	// First Google sign-in creates the player with a first-name handle, keeps
	// the verified email, and asks for onboarding.
	status, body := google(googleToken(t, "google-"+suffix, email, "Wanjiru"))
	if status != http.StatusCreated {
		t.Fatalf("first Google sign-in = %d %s", status, body)
	}
	created := player(body)
	if !strings.HasPrefix(strings.ToLower(created.Username), "wanjiru") || created.DisplayName != created.Username ||
		created.Email == nil || *created.Email != email || !created.EmailVerified || created.OnboardingCompleted ||
		created.KonamiID != nil || len(created.SignInMethods) != 1 || created.SignInMethods[0] != "google" {
		t.Fatalf("created player = %+v", created)
	}
	// The real name is never stored as the display name.
	if strings.Contains(created.DisplayName, " ") {
		t.Fatalf("display name %q looks like a real name", created.DisplayName)
	}

	// Signing in again is the same player.
	status, body = google(googleToken(t, "google-"+suffix, email, "Wanjiru"))
	if status != http.StatusOK || player(body).ID != created.ID {
		t.Fatalf("second Google sign-in = %d %s", status, body)
	}
	// Apple with the same verified email links to that player.
	status, body = apple(appleToken(t, "apple-"+suffix, email, "nonce-1"), "nonce-1")
	linked := player(body)
	if status != http.StatusOK || linked.ID != created.ID || len(linked.SignInMethods) != 2 {
		t.Fatalf("Apple sign-in with the same email = %d %+v", status, linked)
	}
	// Apple needs its nonce; a token signed by anyone else fails.
	if status, _ = apple(appleToken(t, "apple-x-"+suffix, "other@example.com", "nonce-2"), ""); status != http.StatusUnauthorized {
		t.Fatalf("Apple without the nonce = %d", status)
	}
	forged := googleToken(t, "google-forged", "f@example.com", "F")
	if status, _ = google(forged[:len(forged)-4] + "AAAA"); status != http.StatusUnauthorized {
		t.Fatalf("forged token = %d", status)
	}

	// Before onboarding the entry gate fails: no terms, no Konami ID.
	var termsAccepted bool
	if err := h.server.db.Writer.QueryRow(t.Context(), `SELECT terms_accepted_at IS NOT NULL FROM users WHERE id=$1`,
		created.ID).Scan(&termsAccepted); err != nil || termsAccepted {
		t.Fatalf("terms accepted before onboarding: %v %v", termsAccepted, err)
	}

	onboard := func(userID, body string) (int, []byte) {
		recorder := h.call(h.server.completeOnboarding, http.MethodPost, "/v1/me/onboarding", body, userID)
		return recorder.Code, recorder.Body.Bytes()
	}
	konamiID := "ONBD-" + strings.ToUpper(suffix)
	for _, invalid := range []string{
		`{"konamiId":"x","phoneNumber":"+254712345678","countryCode":"KE"}`,
		`{"konamiId":"` + konamiID + `","phoneNumber":"0712","countryCode":"KE"}`,
		`{"konamiId":"` + konamiID + `","phoneNumber":"+254712345678","countryCode":"Kenya"}`,
	} {
		if status, body = onboard(created.ID, invalid); status != http.StatusBadRequest {
			t.Fatalf("invalid onboarding %s = %d %s", invalid, status, body)
		}
	}
	status, body = onboard(created.ID, `{"konamiId":"`+konamiID+`","phoneNumber":"+254712345678","countryCode":"KE"}`)
	var onboarded socialPlayer
	if err := json.Unmarshal(body, &onboarded); err != nil || status != http.StatusOK {
		t.Fatalf("onboarding = %d %s", status, body)
	}
	if !onboarded.OnboardingCompleted || onboarded.KonamiID == nil || *onboarded.KonamiID != konamiID ||
		onboarded.PhoneNumber == nil || onboarded.PhoneVerified || onboarded.CountryCode != "KE" {
		t.Fatalf("onboarded player = %+v", onboarded)
	}
	if err := h.server.db.Writer.QueryRow(t.Context(), `SELECT terms_accepted_at IS NOT NULL AND privacy_accepted_at IS NOT NULL
		FROM users WHERE id=$1`, created.ID).Scan(&termsAccepted); err != nil || !termsAccepted {
		t.Fatalf("onboarding did not record the terms: %v %v", termsAccepted, err)
	}
	if status, _ = onboard(created.ID, `{"konamiId":"OTHR-`+suffix+`","phoneNumber":"+254712345678","countryCode":"KE"}`); status != http.StatusConflict {
		t.Fatalf("second onboarding = %d", status)
	}

	// Another player can type the same unproven phone, but not the same Konami ID.
	status, body = google(googleToken(t, "google-b-"+suffix, "otieno."+suffix+"@example.com", ""))
	second := player(body)
	if status != http.StatusCreated || second.Username == "" {
		t.Fatalf("second player = %d %s", status, body)
	}
	if status, body = onboard(second.ID, `{"konamiId":"`+strings.ToLower(konamiID)+`","phoneNumber":"+254712345678","countryCode":"KE"}`); status != http.StatusConflict ||
		!strings.Contains(string(body), "konami_id_taken") {
		t.Fatalf("taken Konami ID = %d %s", status, body)
	}
	if status, body = onboard(second.ID, `{"konamiId":"SCND-`+strings.ToUpper(suffix)+`","phoneNumber":"+254712345678","countryCode":"KE"}`); status != http.StatusOK {
		t.Fatalf("same unproven phone = %d %s", status, body)
	}

	// Password sign-up is off unless configured.
	h.server.config.PasswordRegistration = false
	h.expect(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
		`{"username":"Pw_`+suffix+`","konamiId":"PWPW-`+suffix+`-PWPW","password":"long enough secret"}`, ""),
		http.StatusForbidden, "password_registration_disabled")
}

// A successful payment from the profile number locks it to that player; the
// same number can't then be proven by anyone else.
func TestIntegrationPaymentProvesThePhone(t *testing.T) {
	h := resultFlowSetup(t)
	competition := seedIntegrationCompetition(t, h.Pool, integrationSeedOptions{Format: "single_elimination", Entries: 2})
	first, second := competition.Entries[0].UserID, competition.Entries[1].UserID
	resultFlowExec(t, h.Pool, `UPDATE users SET phone_e164='+254712345678' WHERE id IN ($1,$2)`, first, second)
	prove := func(userID string) {
		t.Helper()
		paymentID := resultFlowInsertPayment(t, h.Pool, userID, competition.ID, "pending")
		tx, err := h.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context()) //nolint:errcheck
		if err = provePaidPhone(t.Context(), tx, paymentID); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	verified := func(userID string) bool {
		var value bool
		if err := h.Pool.QueryRow(t.Context(), `SELECT phone_verified_at IS NOT NULL FROM users WHERE id=$1`, userID).
			Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	prove(first)
	if !verified(first) {
		t.Fatal("a payment from the profile number did not prove it")
	}
	prove(second)
	if verified(second) {
		t.Fatal("a number already proven by another player was proven again")
	}
	assertFileOrder(t, "payment_handlers.go", "func (s *Server) completePayment(",
		"UPDATE payment_intents SET status='succeeded'", "stampPaymentUSD(ctx, tx, paymentID)",
		"provePaidPhone(ctx, tx, paymentID)")
}
