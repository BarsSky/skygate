// Package httputil is a small bag of HTTP / request helpers
// shared across feature packages. The first home for it is
// SanitizeFilename (refactor-v0.30 Phase D1, 2026-07-29),
// which previously had 3 near-identical copies in:
//   - internal/handlers/handlers.go (exported as SanitizeFilename)
//   - internal/feature/admin/user_subnet_download.go (private)
//   - internal/feature/my/audit.go (private)
//
// Kept as a tiny package on purpose — we don't want a "utils"
// grab-bag; if a second helper lands here that isn't a pure
// filename/string utility, it should move to its own
// dedicated package.
package httputil

import "strings"

// SanitizeFilename strips anything that's not safe in a
// Content-Disposition filename — ASCII alphanumerics, dash,
// underscore, dot. Used for audit-export and bundle-download
// attachment filenames so a caller can't trick a browser
// into saving into a parent directory.
//
// The behaviour is the SAME as the 3 pre-Phase-D1 copies:
//   - TrimSpace first
//   - Empty / whitespace-only → "user"
//   - Non-allowed runes → '_' (so "laptop 2" → "laptop_2")
//   - Cap at 32 chars
//
// Phase D1 (2026-07-29): single source of truth. All 3
// call sites now route through this function.
func SanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "user"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "user"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
