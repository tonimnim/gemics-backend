package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"strings"
)

// A recovery code lets a player who never added an email reset their
// password. It is 20 characters of Crockford's base32 (100 bits), shown once
// in groups of four. Only its SHA-256 is stored: at 100 bits of entropy a
// slow hash adds nothing, as with refresh tokens.
const (
	recoveryAlphabet   = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	recoveryCodeLength = 20
)

// NewRecoveryCode returns a code to show the player once, and the hash to
// store.
func NewRecoveryCode() (string, []byte, error) {
	raw := make([]byte, recoveryCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	var code strings.Builder
	for index, value := range raw {
		if index > 0 && index%4 == 0 {
			code.WriteByte('-')
		}
		// 256 is a multiple of 32, so the low five bits are uniform.
		code.WriteByte(recoveryAlphabet[value&31])
	}
	hash, _ := HashRecoveryCode(code.String())
	return code.String(), hash, nil
}

// HashRecoveryCode hashes a code as the player typed it. Case, spaces and
// dashes are ignored, and the letters people confuse with digits read as
// those digits (O as 0, I and L as 1). It reports false for anything that
// cannot be a recovery code.
func HashRecoveryCode(input string) ([]byte, bool) {
	var code strings.Builder
	for _, char := range strings.ToUpper(input) {
		switch {
		case char == ' ' || char == '-':
			continue
		case char == 'O':
			code.WriteByte('0')
		case char == 'I' || char == 'L':
			code.WriteByte('1')
		case strings.ContainsRune(recoveryAlphabet, char):
			code.WriteRune(char)
		default:
			return nil, false
		}
	}
	if code.Len() != recoveryCodeLength {
		return nil, false
	}
	sum := sha256.Sum256([]byte(code.String()))
	return sum[:], true
}
