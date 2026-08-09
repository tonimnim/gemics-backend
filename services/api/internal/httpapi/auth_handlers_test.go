package httpapi

import "testing"

func TestNormalizeEmail(t *testing.T) {
	value, ok := normalizeEmail(" Player@Example.COM ")
	if !ok || value != "player@example.com" {
		t.Fatalf("unexpected normalized email: %q %v", value, ok)
	}
	if _, ok := normalizeEmail("Player <player@example.com>"); ok {
		t.Fatal("display-name email syntax must be rejected")
	}
}
