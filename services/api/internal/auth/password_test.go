package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash = %q, want an Argon2id PHC string", hash)
	}
	if !VerifyPassword(hash, "correct horse battery") {
		t.Fatal("the original password does not verify")
	}
	if VerifyPassword(hash, "correct horse batterY") {
		t.Fatal("a different password verifies")
	}
	again, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if again == hash {
		t.Fatal("two hashes of one password share a salt")
	}
}

func TestVerifyPasswordRejectsMissingAndMalformedHashes(t *testing.T) {
	for _, encoded := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$a2V5",
		"$argon2id$v=18$m=19456,t=2,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=0,t=2,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$not base64$a2V5",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$",
	} {
		if VerifyPassword(encoded, "anything at all") {
			t.Errorf("VerifyPassword(%q) matched", encoded)
		}
	}
}

func TestValidPasswordLength(t *testing.T) {
	cases := map[string]bool{
		"":                                       false,
		"seven77":                                false,
		"eight888":                               true,
		"ñandú-ñandú":                            true,
		strings.Repeat("a", PasswordMaxLength):   true,
		strings.Repeat("a", PasswordMaxLength+1): false,
		strings.Repeat("é", PasswordMaxLength):   true,
		"bad\xffbytes!":                          false,
	}
	for password, want := range cases {
		if got := ValidPasswordLength(password); got != want {
			t.Errorf("ValidPasswordLength(%q) = %v, want %v", password, got, want)
		}
	}
}
