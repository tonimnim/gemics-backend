package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidToken = errors.New("invalid token")

type Claims struct {
	UserID    string `json:"sub"`
	SessionID string `json:"sid"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	TokenID   string `json:"jti"`
}

type TokenManager struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokenManager(secret string, ttl time.Duration) *TokenManager {
	return &TokenManager{secret: []byte(secret), ttl: ttl, now: time.Now}
}

func (m *TokenManager) Issue(userID, sessionID string) (string, time.Time, error) {
	now := m.now().UTC()
	expires := now.Add(m.ttl)
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claims, err := json.Marshal(Claims{UserID: userID, SessionID: sessionID, Issuer: "gamics-api", Audience: "gamics-player", IssuedAt: now.Unix(), ExpiresAt: expires.Unix(), TokenID: RandomID()})
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := encode(header) + "." + encode(claims)
	return unsigned + "." + encode(m.sign(unsigned)), expires, nil
}

func (m *TokenManager) Parse(raw string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, m.sign(unsigned)) {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if json.Unmarshal(payload, &claims) != nil || claims.Issuer != "gamics-api" || claims.Audience != "gamics-player" || claims.UserID == "" || claims.SessionID == "" || m.now().Unix() >= claims.ExpiresAt {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func (m *TokenManager) sign(value string) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func encode(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }

func NewRefreshToken() (string, []byte, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(value)
	hash := sha256.Sum256([]byte(raw))
	return raw, hash[:], nil
}

func HashRefreshToken(raw string) []byte {
	hash := sha256.Sum256([]byte(raw))
	return hash[:]
}

func GenerateOTP() (string, error) {
	var value [4]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(value[:])%1000000), nil
}

func HashOTP(secret, email, code string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strings.ToLower(email)))
	mac.Write([]byte{'|'})
	mac.Write([]byte(code))
	return mac.Sum(nil)
}

func VerifyOTP(expected, actual []byte) bool {
	return len(expected) == len(actual) && subtle.ConstantTimeCompare(expected, actual) == 1
}

func RandomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(value[:])
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
}
