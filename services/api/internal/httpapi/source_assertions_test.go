package httpapi

import (
	"os"
	"strings"
	"testing"
)

// Source-shape assertions pin contracts that a unit test cannot observe
// without a database: lock order, migration fragments and OpenAPI wording.

// readSourceFile returns a file with LF line endings, so fragments pinned
// with "\n" match the same way on a CRLF checkout.
func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.ReplaceAll(string(contents), "\r\n", "\n")
}

func assertFileContains(t *testing.T, path string, fragments ...string) {
	t.Helper()
	contents := readSourceFile(t, path)
	for _, fragment := range fragments {
		if !strings.Contains(contents, fragment) {
			t.Errorf("%s does not contain %q", path, fragment)
		}
	}
}

func assertFileOmits(t *testing.T, path string, fragments ...string) {
	t.Helper()
	contents := readSourceFile(t, path)
	for _, fragment := range fragments {
		if strings.Contains(contents, fragment) {
			t.Errorf("%s still contains %q", path, fragment)
		}
	}
}

// assertFileOrder requires every fragment to appear in the file, each after
// the previous one's first occurrence.
func assertFileOrder(t *testing.T, path string, fragments ...string) {
	t.Helper()
	assertOrder(t, path, readSourceFile(t, path), fragments...)
}

// assertOrder requires every fragment to appear in source, each after the
// previous one's first occurrence.
func assertOrder(t *testing.T, name, source string, fragments ...string) {
	t.Helper()
	offset := 0
	for _, fragment := range fragments {
		index := strings.Index(source[offset:], fragment)
		if index < 0 {
			t.Errorf("%s: %q is missing or out of order after byte %d", name, fragment, offset)
			return
		}
		offset += index + len(fragment)
	}
}
