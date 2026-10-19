package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// TestIdentitySigner signs provider-shaped identity tokens for tests.
type testIdentitySigner struct {
	key   *rsa.PrivateKey
	keyID string
}

func newTestIdentitySigner(t *testing.T) testIdentitySigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return testIdentitySigner{key: key, keyID: "test-key"}
}

func (s testIdentitySigner) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": s.keyID, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestIdentityVerifierChecksEveryClaim(t *testing.T) {
	signer := newTestIdentitySigner(t)
	keys := StaticKeySource{"google": {signer.keyID: &signer.key.PublicKey}, "apple": {signer.keyID: &signer.key.PublicKey}}
	verifier := NewIdentityVerifier(keys, GoogleProvider([]string{"android-client", "ios-client"}),
		AppleProvider([]string{"io.tonits.app"}))
	now := time.Now()
	google := func(edit func(map[string]any)) string {
		claims := map[string]any{"iss": "https://accounts.google.com", "aud": "android-client", "sub": "google-123",
			"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "email": "kamau@example.com", "email_verified": true,
			"given_name": "Kamau"}
		if edit != nil {
			edit(claims)
		}
		return signer.sign(t, claims)
	}
	claims, err := verifier.Verify(t.Context(), "google", google(nil), "")
	if err != nil || claims.Subject != "google-123" || !claims.EmailVerified || claims.GivenName != "Kamau" {
		t.Fatalf("valid Google token = %+v, %v", claims, err)
	}
	for name, token := range map[string]string{
		"wrong audience": google(func(c map[string]any) { c["aud"] = "someone-else" }),
		"wrong issuer":   google(func(c map[string]any) { c["iss"] = "https://evil.example" }),
		"expired":        google(func(c map[string]any) { c["exp"] = now.Add(-2 * time.Minute).Unix() }),
		"issued later":   google(func(c map[string]any) { c["iat"] = now.Add(5 * time.Minute).Unix() }),
		"no subject":     google(func(c map[string]any) { delete(c, "sub") }),
		"tampered":       google(nil)[:40] + "x" + google(nil)[41:],
		"not a jwt":      "abc.def",
	} {
		if _, err := verifier.Verify(t.Context(), "google", token, ""); !errors.Is(err, ErrInvalidIdentityToken) {
			t.Errorf("%s: err = %v, want invalid", name, err)
		}
	}
	// An audience array passes when any entry is ours.
	if _, err := verifier.Verify(t.Context(), "google", google(func(c map[string]any) {
		c["aud"] = []string{"other", "ios-client"}
	}), ""); err != nil {
		t.Errorf("audience array: %v", err)
	}
	// A Google nonce, when sent, must match as given.
	if _, err := verifier.Verify(t.Context(), "google", google(func(c map[string]any) { c["nonce"] = "n1" }), "n2"); err == nil {
		t.Error("mismatched Google nonce was accepted")
	}

	// Apple: the token carries SHA-256(raw nonce), the request the raw nonce,
	// and email_verified is the string "true".
	raw := "raw-nonce-value"
	hashed := sha256.Sum256([]byte(raw))
	apple := signer.sign(t, map[string]any{"iss": "https://appleid.apple.com", "aud": "io.tonits.app", "sub": "apple-456",
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": hex.EncodeToString(hashed[:]),
		"email": "x@privaterelay.appleid.com", "email_verified": "true"})
	claims, err = verifier.Verify(t.Context(), "apple", apple, raw)
	if err != nil || claims.Subject != "apple-456" || !claims.EmailVerified {
		t.Fatalf("valid Apple token = %+v, %v", claims, err)
	}
	if _, err := verifier.Verify(t.Context(), "apple", apple, ""); err == nil {
		t.Error("Apple token without the nonce was accepted")
	}
	if _, err := verifier.Verify(t.Context(), "apple", apple, "other"); err == nil {
		t.Error("Apple token with the wrong nonce was accepted")
	}

	// An unconfigured provider is off, not open.
	off := NewIdentityVerifier(keys, GoogleProvider(nil))
	if off.Enabled("google") || off.Enabled("apple") {
		t.Error("providers without client IDs report enabled")
	}
	if _, err := off.Verify(t.Context(), "google", google(nil), ""); !errors.Is(err, ErrIdentityProviderUnavailable) {
		t.Errorf("unconfigured provider: %v", err)
	}
}

func TestParseKeySetReadsRSAKeys(t *testing.T) {
	signer := newTestIdentitySigner(t)
	modulus := base64.RawURLEncoding.EncodeToString(signer.key.N.Bytes())
	exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(signer.key.E)).Bytes())
	document := `{"keys":[{"kty":"EC","kid":"ec","crv":"P-256"},{"kty":"RSA","kid":"k1","alg":"RS256","n":"` +
		modulus + `","e":"` + exponent + `"}]}`
	keys, err := ParseKeySet(strings.NewReader(document))
	if err != nil || len(keys) != 1 || keys["k1"].N.Cmp(signer.key.N) != 0 || keys["k1"].E != signer.key.E {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	if _, err := ParseKeySet(strings.NewReader(`{"keys":[]}`)); err == nil {
		t.Error("an empty key set parsed")
	}
}
