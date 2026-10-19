package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Players sign in with Google or Apple. The app gets an identity token (a JWT
// signed by the provider) and the API verifies it here: the signature against
// the provider's published keys, then issuer, audience, expiry and nonce.

var (
	ErrIdentityProviderUnavailable = errors.New("identity provider is not configured")
	ErrInvalidIdentityToken        = errors.New("identity token is invalid")
)

// IdentityProvider describes one provider's tokens.
type IdentityProvider struct {
	Name      string
	Issuers   []string
	Audiences []string // the app's client IDs; empty means sign-in is off
	KeysURL   string
	// RequireNonce makes the request's nonce mandatory. Apple puts the
	// SHA-256 of the app's raw nonce in the token; Google puts the nonce as
	// given. Either form matches.
	RequireNonce bool
}

func GoogleProvider(clientIDs []string) IdentityProvider {
	return IdentityProvider{Name: "google", Issuers: []string{"https://accounts.google.com", "accounts.google.com"},
		Audiences: clientIDs, KeysURL: "https://www.googleapis.com/oauth2/v3/certs"}
}

func AppleProvider(clientIDs []string) IdentityProvider {
	return IdentityProvider{Name: "apple", Issuers: []string{"https://appleid.apple.com"},
		Audiences: clientIDs, KeysURL: "https://appleid.apple.com/auth/keys", RequireNonce: true}
}

// IdentityClaims is what a verified token says about the player.
type IdentityClaims struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	GivenName     string
}

// KeySource returns a provider's RSA public key by key ID.
type KeySource interface {
	Key(ctx context.Context, provider IdentityProvider, keyID string) (*rsa.PublicKey, error)
}

type IdentityVerifier struct {
	providers map[string]IdentityProvider
	keys      KeySource
	now       func() time.Time
}

func NewIdentityVerifier(keys KeySource, providers ...IdentityProvider) *IdentityVerifier {
	byName := make(map[string]IdentityProvider, len(providers))
	for _, provider := range providers {
		byName[provider.Name] = provider
	}
	return &IdentityVerifier{providers: byName, keys: keys, now: time.Now}
}

// Enabled reports whether the provider has client IDs configured.
func (v *IdentityVerifier) Enabled(provider string) bool {
	if v == nil {
		return false
	}
	return len(v.providers[provider].Audiences) > 0
}

const identityClockLeeway = time.Minute

type identityHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type identityPayload struct {
	Issuer        string          `json:"iss"`
	Audience      json.RawMessage `json:"aud"`
	Subject       string          `json:"sub"`
	ExpiresAt     int64           `json:"exp"`
	IssuedAt      int64           `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	GivenName     string          `json:"given_name"`
}

// Verify checks a provider's identity token. nonce is the raw nonce the app
// generated for this sign-in, or "" when it used none.
func (v *IdentityVerifier) Verify(ctx context.Context, providerName, token, nonce string) (IdentityClaims, error) {
	provider, ok := v.providers[providerName]
	if !ok || len(provider.Audiences) == 0 {
		return IdentityClaims{}, ErrIdentityProviderUnavailable
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return IdentityClaims{}, ErrInvalidIdentityToken
	}
	var header identityHeader
	if !decodeSegment(parts[0], &header) || header.Algorithm != "RS256" || header.KeyID == "" {
		return IdentityClaims{}, ErrInvalidIdentityToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return IdentityClaims{}, ErrInvalidIdentityToken
	}
	key, err := v.keys.Key(ctx, provider, header.KeyID)
	if err != nil {
		return IdentityClaims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return IdentityClaims{}, ErrInvalidIdentityToken
	}
	var payload identityPayload
	if !decodeSegment(parts[1], &payload) {
		return IdentityClaims{}, ErrInvalidIdentityToken
	}
	now := v.now()
	if !slices.Contains(provider.Issuers, payload.Issuer) || payload.Subject == "" ||
		!audienceMatches(payload.Audience, provider.Audiences) ||
		now.After(time.Unix(payload.ExpiresAt, 0).Add(identityClockLeeway)) ||
		time.Unix(payload.IssuedAt, 0).After(now.Add(identityClockLeeway)) {
		return IdentityClaims{}, ErrInvalidIdentityToken
	}
	if (provider.RequireNonce || nonce != "") && !nonceMatches(payload.Nonce, nonce) {
		return IdentityClaims{}, ErrInvalidIdentityToken
	}
	return IdentityClaims{Provider: provider.Name, Subject: payload.Subject, Email: strings.TrimSpace(payload.Email),
		EmailVerified: claimTrue(payload.EmailVerified), GivenName: strings.TrimSpace(payload.GivenName)}, nil
}

func decodeSegment(segment string, into any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	return err == nil && json.Unmarshal(raw, into) == nil
}

// audienceMatches accepts aud as a string or an array, as the JWT spec allows.
func audienceMatches(raw json.RawMessage, allowed []string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return slices.Contains(allowed, single)
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	return slices.ContainsFunc(many, func(audience string) bool { return slices.Contains(allowed, audience) })
}

func nonceMatches(tokenNonce, raw string) bool {
	if tokenNonce == "" || raw == "" {
		return false
	}
	hashed := sha256.Sum256([]byte(raw))
	return subtle.ConstantTimeCompare([]byte(tokenNonce), []byte(raw)) == 1 ||
		subtle.ConstantTimeCompare([]byte(tokenNonce), []byte(hex.EncodeToString(hashed[:]))) == 1
}

// claimTrue reads email_verified, which Google sends as a boolean and Apple
// as the string "true".
func claimTrue(raw json.RawMessage) bool {
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && text == "true"
}

// HTTPKeySource fetches and caches providers' JSON Web Key Sets. An unknown
// key ID triggers a refetch, at most once a minute per provider, because
// providers rotate keys without notice.
type HTTPKeySource struct {
	client *http.Client
	mu     sync.Mutex
	cache  map[string]*keySet
}

type keySet struct {
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

const (
	keySetMaxAge         = 6 * time.Hour
	keySetRefetchMinimum = time.Minute
)

func NewHTTPKeySource(client *http.Client) *HTTPKeySource {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &HTTPKeySource{client: client, cache: map[string]*keySet{}}
}

func (s *HTTPKeySource) Key(ctx context.Context, provider IdentityProvider, keyID string) (*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.cache[provider.Name]
	stale := set == nil || time.Since(set.fetchedAt) > keySetMaxAge
	if set != nil && !stale {
		if key := set.keys[keyID]; key != nil {
			return key, nil
		}
		if time.Since(set.fetchedAt) < keySetRefetchMinimum {
			return nil, ErrInvalidIdentityToken
		}
	}
	fresh, err := s.fetch(ctx, provider.KeysURL)
	if err != nil {
		if set != nil && set.keys[keyID] != nil {
			return set.keys[keyID], nil
		}
		return nil, err
	}
	s.cache[provider.Name] = fresh
	if key := fresh.keys[keyID]; key != nil {
		return key, nil
	}
	return nil, ErrInvalidIdentityToken
}

func (s *HTTPKeySource) fetch(ctx context.Context, url string) (*keySet, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch identity keys: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch identity keys: status %d", response.StatusCode)
	}
	keys, err := ParseKeySet(response.Body)
	if err != nil {
		return nil, err
	}
	return &keySet{keys: keys, fetchedAt: time.Now()}, nil
}

// ParseKeySet reads the RSA signing keys from a JSON Web Key Set.
func ParseKeySet(body io.Reader) (map[string]*rsa.PublicKey, error) {
	var document struct {
		Keys []struct {
			KeyType string `json:"kty"`
			KeyID   string `json:"kid"`
			Modulus string `json:"n"`
			Expo    string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(body).Decode(&document); err != nil {
		return nil, fmt.Errorf("parse identity keys: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, key := range document.Keys {
		if key.KeyType != "RSA" || key.KeyID == "" {
			continue
		}
		modulus, modulusErr := base64.RawURLEncoding.DecodeString(key.Modulus)
		exponent, exponentErr := base64.RawURLEncoding.DecodeString(key.Expo)
		if modulusErr != nil || exponentErr != nil || len(exponent) == 0 || len(exponent) > 4 {
			continue
		}
		keys[key.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(new(big.Int).SetBytes(exponent).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("parse identity keys: no RSA keys")
	}
	return keys, nil
}

// StaticKeySource serves fixed keys, for tests.
type StaticKeySource map[string]map[string]*rsa.PublicKey

func (s StaticKeySource) Key(_ context.Context, provider IdentityProvider, keyID string) (*rsa.PublicKey, error) {
	if key := s[provider.Name][keyID]; key != nil {
		return key, nil
	}
	return nil, ErrInvalidIdentityToken
}
