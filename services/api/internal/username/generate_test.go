package username

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func TestGeneratedHandlesAlwaysSatisfyTheStoredConstraint(t *testing.T) {
	for range 2000 {
		handle, err := Generate(rand.Reader)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if !Valid(handle) {
			t.Fatalf("generated handle %q does not satisfy the profile constraint", handle)
		}
		if len(handle) > MaxLength {
			t.Fatalf("generated handle %q is %d characters, over the %d limit", handle, len(handle), MaxLength)
		}
		if strings.Count(handle, "_") != 2 {
			t.Fatalf("expected an Adjective_Noun_Number shape, got %q", handle)
		}
	}
}

// The longest adjective and noun plus a four digit tail must still fit. If a word
// is ever added that breaks this, it fails here rather than at the database.
func TestWorstCaseWordPairingFits(t *testing.T) {
	longest := func(words []string) string {
		var found string
		for _, word := range words {
			if len(word) > len(found) {
				found = word
			}
		}
		return found
	}
	worst := longest(adjectives) + "_" + longest(nouns) + "_0000"
	if len(worst) > MaxLength {
		t.Fatalf("worst case handle %q is %d characters, over the %d limit", worst, len(worst), MaxLength)
	}
	if !Valid(worst) {
		t.Fatalf("worst case handle %q is not valid", worst)
	}
}

func TestWordListsAreCleanAndDistinct(t *testing.T) {
	for _, list := range [][]string{adjectives, nouns} {
		seen := map[string]struct{}{}
		for _, word := range list {
			lower := strings.ToLower(word)
			if _, duplicate := seen[lower]; duplicate {
				t.Errorf("duplicate word %q wastes entropy", word)
			}
			seen[lower] = struct{}{}
			if word == "" || !Valid(word+"_x_0000") {
				t.Errorf("word %q contains characters the handle pattern rejects", word)
			}
		}
	}
}

func TestFromNameKeepsOnlyTheFirstName(t *testing.T) {
	cases := map[string]string{
		"Brian Otieno Maina": "brian",
		"  brian  ":          "brian",
		"Brian-Otieno":       "brianotieno",
		"MAINA":              "maina",
	}
	for input, wantStem := range cases {
		handle, ok, err := FromName(input, rand.Reader)
		if err != nil || !ok {
			t.Fatalf("FromName(%q) returned ok=%v err=%v", input, ok, err)
		}
		if !strings.HasPrefix(handle, wantStem+"_") {
			t.Errorf("FromName(%q) = %q, expected the stem %q", input, handle, wantStem)
		}
		if !Valid(handle) {
			t.Errorf("FromName(%q) = %q which is not a valid handle", input, handle)
		}
	}
}

// A name with nothing usable must report failure so the caller falls back to a
// generated handle, rather than publishing a mangled version of someone's name.
func TestFromNameRefusesUnusableNames(t *testing.T) {
	for _, input := range []string{"", "   ", "!!!", "李", "A", "-"} {
		if handle, ok, err := FromName(input, rand.Reader); ok || err != nil {
			t.Errorf("FromName(%q) = (%q, %v, %v), expected a clean fallback signal", input, handle, ok, err)
		}
	}
}

func TestFromNameTruncatesLongNames(t *testing.T) {
	handle, ok, err := FromName(strings.Repeat("a", 80), rand.Reader)
	if err != nil || !ok {
		t.Fatalf("unexpected failure: ok=%v err=%v", ok, err)
	}
	if len(handle) > MaxLength || !Valid(handle) {
		t.Fatalf("long name produced %q (%d chars)", handle, len(handle))
	}
}

func TestCandidatesAreDistinct(t *testing.T) {
	handles, err := Candidates(rand.Reader, 200)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	seen := map[string]struct{}{}
	for _, handle := range handles {
		if _, duplicate := seen[handle]; duplicate {
			t.Fatalf("Candidates returned %q twice", handle)
		}
		seen[handle] = struct{}{}
	}
	if len(handles) != 200 {
		t.Fatalf("expected 200 handles, got %d", len(handles))
	}
}

// A broken entropy source must surface as an error rather than as a predictable
// handle. An all-zero reader is the degenerate case.
func TestExhaustedEntropySourceFailsLoudly(t *testing.T) {
	empty := bytes.NewReader(nil)
	if _, err := Generate(empty); err == nil {
		t.Fatal("expected an error when the entropy source is exhausted")
	}
}

// Rejection sampling must not bias the low end of the range. A modulus of a raw
// draw would show up here as a skew across buckets.
func TestNumberIsNotVisiblyBiased(t *testing.T) {
	const ceiling, draws = 10, 100000
	counts := make([]int, ceiling)
	for range draws {
		value, err := number(rand.Reader, ceiling)
		if err != nil {
			t.Fatalf("number: %v", err)
		}
		if value < 0 || value >= ceiling {
			t.Fatalf("number returned %d, outside [0,%d)", value, ceiling)
		}
		counts[value]++
	}
	expected := draws / ceiling
	for value, count := range counts {
		if delta := count - expected; delta > expected/5 || delta < -expected/5 {
			t.Errorf("bucket %d had %d draws, expected roughly %d", value, count, expected)
		}
	}
}
