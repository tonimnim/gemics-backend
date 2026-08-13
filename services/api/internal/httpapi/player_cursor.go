package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	publicCursorVersion   = 1
	maxPublicCursorBytes  = 2048
	defaultPublicPageSize = 20
	maxPublicPageSize     = 50
)

var (
	errInvalidPublicCursor = errors.New("invalid public cursor")
	errExpiredPublicCursor = errors.New("expired public cursor")
)

// publicCursor is intentionally internal. Clients receive a signed opaque token
// and must not depend on this payload shape.
type publicCursor struct {
	Version     int    `json:"v"`
	Kind        string `json:"k"`
	ExpiresAt   int64  `json:"exp"`
	SnapshotAt  int64  `json:"at,omitempty"`
	SnapshotID  string `json:"sid,omitempty"`
	GameID      string `json:"g,omitempty"`
	Scope       string `json:"s,omitempty"`
	CountryCode string `json:"c,omitempty"`
	Query       string `json:"q,omitempty"`
	Rank        int    `json:"r,omitempty"`
	Handle      string `json:"h,omitempty"`
	SortTime    int64  `json:"t,omitempty"`
	ID          string `json:"id,omitempty"`
}

func encodePublicCursor(cursor publicCursor, secret string) (string, error) {
	cursor.Version = publicCursorVersion
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	payloadToken := base64.RawURLEncoding.EncodeToString(payload)
	signature := publicCursorSignature(payloadToken, secret)
	return payloadToken + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func decodePublicCursor(raw, expectedKind, secret string, now time.Time) (publicCursor, error) {
	if raw == "" || len(raw) > maxPublicCursorBytes {
		return publicCursor{}, errInvalidPublicCursor
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return publicCursor{}, errInvalidPublicCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, publicCursorSignature(parts[0], secret)) {
		return publicCursor{}, errInvalidPublicCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return publicCursor{}, errInvalidPublicCursor
	}
	var cursor publicCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != publicCursorVersion || cursor.Kind != expectedKind || cursor.ExpiresAt <= 0 {
		return publicCursor{}, errInvalidPublicCursor
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return publicCursor{}, errInvalidPublicCursor
	}
	if now.Unix() >= cursor.ExpiresAt {
		return publicCursor{}, errExpiredPublicCursor
	}
	return cursor, nil
}

func publicCursorSignature(payloadToken, secret string) []byte {
	digest := hmac.New(sha256.New, []byte(secret))
	_, _ = digest.Write([]byte("gamics-public-cursor-v1\x00"))
	_, _ = digest.Write([]byte(payloadToken))
	return digest.Sum(nil)
}

func parsePublicLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return defaultPublicPageSize, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 1 || limit > maxPublicPageSize {
		return 0, errors.New("limit must be between 1 and 50")
	}
	return limit, nil
}

func cursorTime(unixNanos int64) time.Time {
	return time.Unix(0, unixNanos).UTC()
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
