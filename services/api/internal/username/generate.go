// Package username mints public player handles.
//
// Two shapes are supported. A handle derived from the player's own name is the
// friendlier default; a generated Adjective_Noun_Number handle is the fallback
// when the name is unusable, already taken, or the player would rather not put
// their real name on a public leaderboard.
//
// Everything here is pure apart from the randomness source, which is injected so
// tests can pin it. Nothing touches the database: callers own the uniqueness
// check, because only they know which handles are already claimed.
package username

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Pattern mirrors the handle rule already enforced on player_profiles.handle by
// the account handlers. Every handle this package produces satisfies it, and
// Valid is exported so callers can check handles that came from elsewhere.
var Pattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.]{2,23}$`)

// MaxLength is the longest handle the profile column and Pattern accept.
const MaxLength = 24

// suffixCeiling keeps the numeric tail at exactly four digits, matching the
// familiar Reddit-style shape and keeping the handle comfortably short.
const suffixCeiling = 10000

var errNoCandidate = errors.New("username: could not build a valid handle")

// adjectives and nouns are deliberately bland and sport-flavoured. They are kept
// free of anything that could read as an insult, a slur, a body part, a
// nationality or a religious term, because the platform, not the player, is
// accountable for a handle it generated. Lengths are capped so that the longest
// possible pairing plus a four digit tail still fits inside MaxLength.
var adjectives = []string{
	"Able", "Agile", "Alert", "Amber", "Ample", "Arctic", "Bold", "Brave",
	"Brief", "Bright", "Calm", "Chief", "Clean", "Clear", "Clever", "Cosmic",
	"Crisp", "Daily", "Deft", "Eager", "Early", "Easy", "Elite", "Epic",
	"Even", "Fair", "Fast", "Fine", "Firm", "First", "Fleet", "Fresh",
	"Gentle", "Giant", "Glad", "Golden", "Grand", "Great", "Happy", "Ideal",
	"Jolly", "Keen", "Kind", "Level", "Light", "Lively", "Loyal", "Lucky",
	"Magic", "Major", "Mighty", "Modern", "Neat", "Noble", "Prime", "Proud",
	"Quick", "Rapid", "Royal", "Sharp", "Silver", "Smart", "Solid", "Swift",
}

var nouns = []string{
	"Anchor", "Arrow", "Badge", "Ball", "Beacon", "Blade", "Bolt", "Boot",
	"Bridge", "Captain", "Cedar", "Circle", "Comet", "Compass", "Corner", "Crest",
	"Cup", "Dawn", "Delta", "Derby", "Eagle", "Ember", "Falcon", "Field",
	"Finish", "Flame", "Forest", "Fox", "Gate", "Goal", "Harbor", "Hawk",
	"Header", "Heron", "Hill", "Ibis", "Island", "Journey", "Keeper", "Kick",
	"Lantern", "League", "Lion", "Marker", "Meadow", "Motion", "Orbit", "Panther",
	"Pass", "Peak", "Pilot", "Pitch", "Quarter", "Rally", "Ranger", "Ridge",
	"River", "Rocket", "Runner", "Signal", "Spark", "Sprint", "Striker", "Summit",
}

// Generate returns a handle such as "Swift_Falcon_4821".
//
// The two words and the number are drawn independently from a cryptographic
// source, giving 64 * 64 * 10000, a little over 40 million shapes. That is not
// a uniqueness guarantee and is not meant to be one: it only makes collisions
// rare enough that a caller's retry loop terminates quickly.
func Generate(entropy io.Reader) (string, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	adjective, err := pick(entropy, adjectives)
	if err != nil {
		return "", err
	}
	noun, err := pick(entropy, nouns)
	if err != nil {
		return "", err
	}
	suffix, err := number(entropy, suffixCeiling)
	if err != nil {
		return "", err
	}
	handle := fmt.Sprintf("%s_%s_%04d", adjective, noun, suffix)
	if !Valid(handle) {
		return "", errNoCandidate
	}
	return handle, nil
}

// Candidates returns count distinct generated handles, for callers that would
// rather try a batch against the unique index than round-trip once per attempt.
func Candidates(entropy io.Reader, count int) ([]string, error) {
	if count < 1 {
		return nil, errNoCandidate
	}
	seen := make(map[string]struct{}, count)
	handles := make([]string, 0, count)
	// A generous attempt ceiling: the space is far larger than any batch a
	// caller would sensibly ask for, so this only guards against a broken
	// entropy source rather than against genuine exhaustion.
	for attempt := 0; attempt < count*16 && len(handles) < count; attempt++ {
		handle, err := Generate(entropy)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[handle]; duplicate {
			continue
		}
		seen[handle] = struct{}{}
		handles = append(handles, handle)
	}
	if len(handles) < count {
		return nil, errNoCandidate
	}
	return handles, nil
}

// FromName derives a handle from what the player told us their name is, keeping
// the first name only. "Brian Otieno Maina" becomes "brian" plus a short numeric
// tail, so the handle stays recognisably theirs without publishing a full legal
// name on a leaderboard.
//
// It returns ok=false when the name has no usable ASCII letters or digits, which
// is the signal to fall back to Generate rather than to publish something
// mangled.
func FromName(name string, entropy io.Reader) (string, bool, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	first := strings.TrimSpace(name)
	if index := strings.IndexAny(first, " \t"); index > 0 {
		first = first[:index]
	}
	stem := sanitize(first)
	if len(stem) < 2 {
		return "", false, nil
	}
	// Reserve room for the separator and four digits so the result cannot run
	// past MaxLength for a long first name.
	if limit := MaxLength - 5; len(stem) > limit {
		stem = stem[:limit]
	}
	suffix, err := number(entropy, suffixCeiling)
	if err != nil {
		return "", false, err
	}
	handle := fmt.Sprintf("%s_%04d", stem, suffix)
	if !Valid(handle) {
		return "", false, nil
	}
	return handle, true, nil
}

// Valid reports whether a handle satisfies the stored profile constraint.
func Valid(handle string) bool {
	return Pattern.MatchString(handle)
}

// sanitize reduces a name to the ASCII letters and digits Pattern allows and
// lowercases it, so "Brian-Otieno" and "brian otieno" reduce to the same stem.
// Non-ASCII letters are dropped rather than transliterated: a wrong
// transliteration of someone's name is worse than falling back to a generated
// handle.
func sanitize(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z':
			builder.WriteRune(character)
		case character >= 'A' && character <= 'Z':
			builder.WriteRune(character + ('a' - 'A'))
		case character >= '0' && character <= '9' && builder.Len() > 0:
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

func pick(entropy io.Reader, words []string) (string, error) {
	index, err := number(entropy, len(words))
	if err != nil {
		return "", err
	}
	return words[index], nil
}

// number returns a uniform value in [0, ceiling) using rejection sampling.
// Taking a modulus of a raw 32 bit draw would bias the low end of the range,
// which over millions of handles is a visible pattern rather than a nicety.
func number(entropy io.Reader, ceiling int) (int, error) {
	if ceiling < 1 {
		return 0, errNoCandidate
	}
	limit := uint32(ceiling)
	largest := ^uint32(0) - (^uint32(0) % limit) - 1
	var raw [4]byte
	for attempt := 0; attempt < 64; attempt++ {
		if _, err := io.ReadFull(entropy, raw[:]); err != nil {
			return 0, err
		}
		value := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
		if value <= largest {
			return int(value % limit), nil
		}
	}
	return 0, errNoCandidate
}
