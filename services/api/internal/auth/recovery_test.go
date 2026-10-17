package auth

import (
	"bytes"
	"regexp"
	"testing"
)

func TestNewRecoveryCodeShapeAndHash(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){4}$`)
	seen := map[string]bool{}
	for range 200 {
		code, hash, err := NewRecoveryCode()
		if err != nil {
			t.Fatal(err)
		}
		if !shape.MatchString(code) {
			t.Fatalf("code %q is not five groups of four Crockford characters", code)
		}
		if seen[code] {
			t.Fatalf("code %q repeated", code)
		}
		seen[code] = true
		again, ok := HashRecoveryCode(code)
		if !ok || !bytes.Equal(again, hash) || len(hash) != 32 {
			t.Fatalf("hash of %q does not round-trip", code)
		}
	}
}

func TestHashRecoveryCodeForgivesHowPlayersType(t *testing.T) {
	want, _ := HashRecoveryCode("7KQ0-1XRP-9TVD-4HNW-C3JA")
	for _, typed := range []string{
		"7KQ01XRP9TVD4HNWC3JA",
		"7kq0 1xrp 9tvd 4hnw c3ja",
		"7KQO-IXRP-9TVD-4HNW-C3JA", // O for 0, I for 1
		"7KQ0-LXRP-9TVD-4HNW-C3JA", // L for 1
	} {
		got, ok := HashRecoveryCode(typed)
		if !ok || !bytes.Equal(got, want) {
			t.Errorf("HashRecoveryCode(%q) did not match the canonical code", typed)
		}
	}
	for _, typed := range []string{"", "7KQ0-1XRP-9TVD-4HNW", "7KQ0-1XRP-9TVD-4HNW-C3JA-X", "7KQ0-1XRP-9TVD-4HNW-C3JU", "7KQ0_1XRP_9TVD_4HNW_C3JA"} {
		if _, ok := HashRecoveryCode(typed); ok {
			t.Errorf("HashRecoveryCode(%q) accepted a malformed code", typed)
		}
	}
}
