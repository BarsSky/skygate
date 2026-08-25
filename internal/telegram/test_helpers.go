// Helper functions for telegram package tests.
package telegram

import (
	"os"
	"strings"
	"testing"
)

// readSourceFile reads a .go file from the same package
// (internal/telegram/). Used by tests that pin source-level
// contracts (e.g. SQL placeholder syntax, function
// signatures) without bringing up the full runtime.
func readSourceFile(t *testing.T, name string) (string, error) {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// contains is a tiny case-sensitive substring search
// (avoids pulling in strings.Contains for tests that
// already import strings).
func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
