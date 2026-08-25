// Package telegram — lookup_username_test.go pins the
// B187 fix: lookupPortalUsername used `?` placeholder
// (SQLite-era syntax) which silently failed with the PG
// pgx driver. The bot then set env.Username = "" and
// /my_status replied with "чат привязан, но у пользователя
// портала нет username" even when the binding's portal_user
// row had a perfectly good username (the operator's
// 2026-08-25 screenshot).
//
// 2026-08-25 (B187).
package telegram

import "testing"

// TestLookupPortalUsername_PGPlaceholderSyntax — the fix
// is a literal-character-level regression guard: the SQL
// MUST use the `$1` form (pgx positional parameter), not
// the SQLite-era `?`. If a future change restores `?`,
// the pgx driver returns "operator does not exist: ?"
// which env() silently drops — the exact same silent
// regression the operator's screenshot showed.
func TestLookupPortalUsername_PGPlaceholderSyntax(t *testing.T) {
	body, err := readSourceFile(t, "notify.go")
	if err != nil {
		t.Fatalf("read notify.go: %v", err)
	}
	if !contains(body, "SELECT username FROM portal_users WHERE id = $1") {
		t.Errorf("lookupPortalUsername must use $1 placeholder (pgx), got: missing '$1' form")
	}
	if contains(body, "SELECT username FROM portal_users WHERE id = ?") {
		t.Errorf("lookupPortalUsername still uses ? placeholder (SQLite-era) — would silently fail with pgx")
	}
}
