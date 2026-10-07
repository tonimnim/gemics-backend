package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters follow the OWASP password storage baseline: 19 MiB,
// two passes, one lane. They are encoded into every hash, so raising them
// later still verifies hashes written with the old values.
const (
	argonMemoryKiB  = 19 * 1024
	argonIterations = 2
	argonLanes      = 1
	argonSaltBytes  = 16
	argonKeyBytes   = 32

	// PasswordMinLength and PasswordMaxLength are counted in characters. The
	// maximum bounds hashing cost per request; there are no composition rules.
	PasswordMinLength = 8
	PasswordMaxLength = 128
)

var errMalformedPasswordHash = errors.New("malformed password hash")

// dummyPasswordHash is verified against when no account matches, so a miss
// costs the same time as a wrong password.
var dummyPasswordHash = mustHashPassword("gamics-dummy-password-for-timing")

// ValidPasswordLength reports whether a password is within the length bounds.
func ValidPasswordLength(password string) bool {
	length := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && length >= PasswordMinLength && length <= PasswordMaxLength
}

// HashPassword returns a PHC-formatted Argon2id hash with a random salt.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonLanes, argonKeyBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonIterations,
		argonLanes, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword compares a password with a stored hash in constant time. An
// empty hash, as on accounts that never set a password, still spends the full
// hashing cost and never matches.
func VerifyPassword(encoded, password string) bool {
	if encoded == "" {
		_, _ = verifyPassword(dummyPasswordHash, password)
		return false
	}
	ok, err := verifyPassword(encoded, password)
	return err == nil && ok
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errMalformedPasswordHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformedPasswordHash
	}
	var memory, iterations uint32
	var lanes uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &lanes); err != nil ||
		memory == 0 || iterations == 0 || lanes == 0 {
		return false, errMalformedPasswordHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errMalformedPasswordHash
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) == 0 {
		return false, errMalformedPasswordHash
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, lanes, uint32(len(expected)))
	return subtle.ConstantTimeCompare(expected, actual) == 1, nil
}

func mustHashPassword(password string) string {
	hash, err := HashPassword(password)
	if err != nil {
		panic(err)
	}
	return hash
}
