// 2026-08-13: v1.3.9 round 7 (B109) — unit test mirrors
// scripts/check_b109.sh. Defense-in-depth: the Go test catches
// regressions in the `go test ./...` cycle; the shell test catches
// regressions in the `verify-pre` deploy cycle. Same 3 contracts
// pinned in both places.
//
// Operator-reported symptom: the .admin-breadcrumb nav (renders
// "Админ › section › page" path on every admin page) sits too
// close to the 220px sidebar on desktop — only 24px of padding
// between the sidebar edge and the breadcrumb text, which looks
// visually cramped because the breadcrumb has its own bg-card
// background + bottom border that separates it from the .shell
// content. Fix: bump padding-left from 24px to 40px in the main
// CSS (outside @media). The B107 mobile rule (padding-left:60px
// inside @media (max-width:768px)) is preserved.

package handlers

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// readThemesCSS returns the contents of static/css/themes.css.
// Test fails with a clear message if the file is missing —
// protects against the test running from the wrong working dir.
func readThemesCSS(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"../../static/css/themes.css",
		"../../../static/css/themes.css",
		"static/css/themes.css",
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err == nil {
			return string(b)
		}
	}
	t.Fatalf("B109: could not read themes.css from any candidate path")
	return ""
}

// extractMediaBlock returns the contents of the @media (max-width:768px)
// { ... } block (the mobile-only CSS). Empty string if not found.
// We use a brace-counting approach via regex because awk-style
// parsing in Go is awkward — the regex grabs the opening line +
// everything up to the matching closing brace at column 0.
func extractMediaBlock(t *testing.T, css string) string {
	t.Helper()
	// Match `@media (max-width:768px) { ... }` where the closing
	// brace is at the start of a line. The (?s) flag makes `.`
	// match newlines so we can span multiple lines; (?m) makes
	// `^` match the start of any line (not just the start of
	// the string), which is what we need for `^\}` to match
	// a closing brace on its own line.
	re := regexp.MustCompile(`(?sm)@media\s*\(max-width:768px\)\s*\{(.*?)^\}`)
	m := re.FindStringSubmatch(css)
	if m == nil {
		return ""
	}
	return m[1]
}

// TestB109_BreadcrumbDesktopPadding pins contract 1+2: the main
// CSS (outside @media) has .admin-breadcrumb with the 4-value
// shorthand padding:10px 24px 10px 40px — top/right/bottom/left.
// The left padding is bumped from 24px (the pre-B109 value) to
// 40px so the breadcrumb text has visible breathing room from
// the 220px sidebar edge. The original 10px vertical and 24px
// right padding are preserved.
func TestB109_BreadcrumbDesktopPadding(t *testing.T) {
	css := readThemesCSS(t)
	// Look for the rule with the exact 4-value padding.
	pattern := regexp.MustCompile(`\.admin-breadcrumb\s*\{[^}]*padding:\s*10px\s+24px\s+10px\s+40px`)
	if !pattern.MatchString(css) {
		t.Fatal("B109 FAIL: .admin-breadcrumb missing padding:10px 24px 10px 40px (40px left padding for desktop breathing room)")
	}
}

// TestB109_BreadcrumbMobilePaddingPreserved pins contract 3:
// the B107 mobile rule (.admin-breadcrumb{padding-left:60px}
// inside @media (max-width:768px)) is still present. B109
// only changes the DESKTOP padding; the mobile hamburger-
// clearance behavior must stay intact.
func TestB109_BreadcrumbMobilePaddingPreserved(t *testing.T) {
	css := readThemesCSS(t)
	media := extractMediaBlock(t, css)
	if media == "" {
		t.Fatal("B109 FAIL: @media (max-width:768px) block not found in themes.css")
	}
	// Look for padding-left:60px on .admin-breadcrumb inside the
	// @media block. We accept both the inline form
	// `.admin-breadcrumb{padding-left:60px}` and the spaced form
	// `.admin-breadcrumb { padding-left: 60px }`.
	pattern := regexp.MustCompile(`\.admin-breadcrumb\s*\{\s*padding-left:\s*60px\s*\}`)
	if !pattern.MatchString(media) {
		t.Fatal("B109 FAIL: B107 mobile rule broken — @media (max-width:768px) missing .admin-breadcrumb{padding-left:60px}")
	}
}

// TestB109_NoRevertToOldPadding guards against accidental revert:
// the pre-B109 value `padding:10px 24px` (no left override)
// must NOT appear for .admin-breadcrumb. If someone reverts the
// change, this test catches it.
func TestB109_NoRevertToOldPadding(t *testing.T) {
	css := readThemesCSS(t)
	// Look for the old shorthand: padding:10px 24px (only 2 values).
	// We use a regex that requires the .admin-breadcrumb selector
	// AND the old 2-value padding in the same rule block.
	oldPattern := regexp.MustCompile(`(?s)\.admin-breadcrumb\s*\{[^}]*padding:\s*10px\s+24px\s*\}`)
	if oldPattern.MatchString(css) {
		t.Fatal("B109 FAIL: .admin-breadcrumb reverted to old padding:10px 24px (should be 10px 24px 10px 40px with 40px left)")
	}
	// Extra guard: the new rule must contain "40px" somewhere in
	// the .admin-breadcrumb block (not in a comment).
	newPattern := regexp.MustCompile(`(?s)\.admin-breadcrumb\s*\{[^}]*40px[^}]*\}`)
	if !newPattern.MatchString(css) {
		t.Fatal("B109 FAIL: .admin-breadcrumb block does not contain 40px (left padding not applied)")
	}
	// And it must NOT contain the literal old `padding-left: 24px`
	// or `padding-left:24px` (which would be a no-op override of
	// the shorthand).
	if strings.Contains(css, ".admin-breadcrumb{padding-left:24px}") ||
		strings.Contains(css, ".admin-breadcrumb { padding-left: 24px }") {
		t.Fatal("B109 FAIL: .admin-breadcrumb has explicit padding-left:24px (would override the 40px shorthand)")
	}
}
