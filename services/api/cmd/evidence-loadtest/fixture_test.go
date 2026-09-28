package main

import (
	"crypto/sha256"
	"github.com/gamics-io/gamics/services/api/internal/evidence"
	"testing"
)

func TestFixtureExactSizeAndDecodes(t *testing.T) {
	b, e := fixture()
	if e != nil {
		t.Fatal(e)
	}
	if len(b) != 2_000_000 {
		t.Fatal(len(b))
	}
	h := sha256.Sum256(b)
	if e = evidence.Validate(b, "image/png", h[:]); e != nil {
		t.Fatal(e)
	}
}
func TestLatencyNearestRank(t *testing.T) {
	r := summarize([]float64{4, 1, 3, 2})
	if r.Count != 4 || r.P50 != 2 || r.P95 != 4 || r.Max != 4 {
		t.Fatal(r)
	}
	if summarize(nil).Count != 0 {
		t.Fatal("empty")
	}
}
