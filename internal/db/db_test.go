package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestThemeLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{ThemeLinear, "Linear"},
		{ThemeVercel, "Vercel"},
		{ThemeSentry, "Sentry"},
		{ThemeNvidia, "NVIDIA"},
		{"dark", "Linear"},       // fallback for unknown
		{"", "Linear"},            // empty → fallback
		{"VerCeL", "Linear"},      // case sensitive: unknown
	}
	for _, c := range cases {
		if got := ThemeLabel(c.in); got != c.want {
			t.Errorf("ThemeLabel(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestIsValidTheme(t *testing.T) {
	for _, ok := range []string{ThemeLinear, ThemeVercel, ThemeSentry, ThemeNvidia} {
		if !IsValidTheme(ok) {
			t.Errorf("IsValidTheme(%q) false, want true", ok)
		}
	}
	for _, bad := range []string{"", "dark", "Light", "theme:foo", "NVIDIA "} {
		if IsValidTheme(bad) {
			t.Errorf("IsValidTheme(%q) true, want false", bad)
		}
	}
}

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "skygate.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	// 2026-07-09: refactor v0.6.0 — Open() now runs migrate() internally.
	// We do not call it again here.
	// All canonical tables should now exist on a fresh DB:
	want := []string{
		"portal_users",   // v0.25 (bootstrap)
		"personal_api_tokens", // v0.23 (was silently failing before)
		"device_rules",   // v0.20
		"exit_servers",   // v0.20
		"acl_snapshots",  // v0.20
		"exit_rule_logs", // v0.20
		"global_settings",// v0.21
	}
	for _, name := range want {
		var got string
		q := "SELECT name FROM sqlite_master WHERE type='table' AND name=?"
		if err := d.QueryRow(q, name).Scan(&got); err != nil {
			t.Errorf("table %q missing after Open: %v", name, err)
		}
	}
}

func TestGetSetUserTheme(t *testing.T) {
	d := openTestDB(t)
	// seed user
	res, err := d.Exec(`INSERT INTO portal_users (username, password_hash, is_admin, theme) VALUES ('utester', 'x', 0, ?)`, ThemeVercel)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id, _ := res.LastInsertId()

	// GetUserTheme returns the seed theme
	if got := GetUserTheme(d, id); got != ThemeVercel {
		t.Errorf("GetUserTheme=%q want %q", got, ThemeVercel)
	}

	// SetUserTheme to a new theme and verify
	if err := SetUserTheme(d, id, ThemeNvidia); err != nil {
		t.Fatalf("SetUserTheme: %v", err)
	}
	if got := GetUserTheme(d, id); got != ThemeNvidia {
		t.Errorf("after set, GetUserTheme=%q want %q", got, ThemeNvidia)
	}

	// Unknown theme in DB falls back to ThemeLinear
	if err := SetUserTheme(d, id, "bogus"); err != nil {
		t.Fatalf("SetUserTheme bogus: %v", err)
	}
	if got := GetUserTheme(d, id); got != ThemeLinear {
		t.Errorf("bogus theme did not fall back: got %q want %q", got, ThemeLinear)
	}

	// Non-existent user → fallback
	if got := GetUserTheme(d, 9999); got != ThemeLinear {
		t.Errorf("unknown user theme did not fall back: got %q", got)
	}
}

// openTestDB returns a fresh sqlite db with the full schema applied.
// As of refactor v0.6.0, Open() calls migrate() so portal_users already exists.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}
