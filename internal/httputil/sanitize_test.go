// 2026-07-29: refactor-v0.30 Phase D1 — single
// source of truth for SanitizeFilename. This test
// pins the contract that the 3 pre-Phase-D1 copies
// (handlers.go + feature/admin + feature/my) all
// honoured:
//
//   - Empty / whitespace-only → "user"
//   - Non-allowed runes → '_'
//   - Cap at 32 chars
//   - Allowed: ASCII alphanumerics + '-' + '_' + '.'
//
// If any of these assumptions change, every
// download endpoint that uses the helper needs a
// re-audit. The test makes the contract explicit.

package httputil

import "testing"

// TestSanitizeFilename_Empty — the "no caller name at
// all" case. Browsers used to show "attachment" if
// the filename was empty; "user" gives a stable, sane
// fallback.
func TestSanitizeFilename_Empty(t *testing.T) {
	if got := SanitizeFilename(""); got != "user" {
		t.Errorf("empty input: got %q, want %q", got, "user")
	}
	if got := SanitizeFilename("   "); got != "user" {
		t.Errorf("whitespace input: got %q, want %q", got, "user")
	}
}

// TestSanitizeFilename_AllowedChars — alphanumerics +
// dash + underscore + dot are preserved verbatim.
func TestSanitizeFilename_AllowedChars(t *testing.T) {
	in := "abc-DEF_123.xyz"
	if got := SanitizeFilename(in); got != in {
		t.Errorf("allowed chars: got %q, want %q", got, in)
	}
}

// TestSanitizeFilename_ReplacesPathTraversal — the
// security-critical case. A caller-controlled name
// like "../../etc/passwd" must be flattened to a
// safe token. '/' and '.' are partially allowed (only
// the dot in the allowed set), so we expect:
//
//	"../../etc/passwd" → ".._.._etc_passwd"
//
// The leading ".." is preserved as a string of dots
// and underscores (no path semantics), and '/' becomes
// '_'. A browser saving the file to disk would land
// at <downloads>/.._.._etc_passwd — which is just a
// weird filename, not a path traversal.
func TestSanitizeFilename_ReplacesPathTraversal(t *testing.T) {
	got := SanitizeFilename("../../etc/passwd")
	want := ".._.._etc_passwd"
	if got != want {
		t.Errorf("path traversal: got %q, want %q", got, want)
	}
}

// TestSanitizeFilename_ReplacesSpecialChars — the
// common case: an operator's name with non-ASCII
// characters (e.g. a Cyrillic username) gets folded to
// ASCII underscores. The audit-export filename stays
// ASCII-only so any downstream tool that opens the
// file (Windows-1252 editor, FTP, etc.) doesn't
// choke. "Константин" is 10 Cyrillic runes; every
// one falls into the "default" branch and gets '_'.
func TestSanitizeFilename_ReplacesSpecialChars(t *testing.T) {
	got := SanitizeFilename("Константин")
	if got != "__________" {
		t.Errorf("non-ASCII: got %q, want %q (10 underscores)", got, "__________")
	}
}

// TestSanitizeFilename_CapAt32 — the contract says
// no more than 32 chars. This matches typical OS-level
// filename limits and keeps the attachment name short.
func TestSanitizeFilename_CapAt32(t *testing.T) {
	in := "abcdefghijklmnopqrstuvwxyz0123456789-_" // 39 chars
	got := SanitizeFilename(in)
	if len(got) != 32 {
		t.Errorf("cap: got %d chars, want 32 (input was %d)", len(got), len(in))
	}
}

// TestSanitizeFilename_TrimsLeadingTrailingSpaces —
// spaces at the start/end are dropped before the
// rune-by-rune pass. Prevents filenames like
// " user.tar.gz" that some shells would refuse to
// create on Windows.
func TestSanitizeFilename_TrimsLeadingTrailingSpaces(t *testing.T) {
	if got := SanitizeFilename("  alice  "); got != "alice" {
		t.Errorf("trim: got %q, want %q", got, "alice")
	}
}
