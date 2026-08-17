package db

import (
	"database/sql"
	"testing"
)

func TestThemeLabel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{ThemeLinear, "Linear"},
		{ThemeVercel, "Vercel"},
		{ThemeSentry, "Sentry"},
		{ThemeNvidia, "NVIDIA"},
		{ThemeMint, "Mint"}, // 2026-08-17 B121
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
	for _, ok := range []string{ThemeLinear, ThemeVercel, ThemeSentry, ThemeNvidia, ThemeMint} {
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

// TestOpenAndMigrate — v1.3.0: skygate is PG-only. The
// "fresh DB has the expected tables" check is now a PG test
// (queries information_schema + pg_tables instead of
// sqlite_master). The test skips when SKYGATE_TEST_PG_DSN
// is unset, so it still passes on a dev machine without PG.
func TestOpenAndMigrate(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-12: PG-only check via information_schema.tables
	// (was sqlite_master pre-v1.3.0).
	want := []string{
		"portal_users",          // v0.25 (bootstrap)
		"personal_api_tokens",   // v0.23
		"device_rules",          // v0.20
		"exit_servers",          // v0.20
		"acl_snapshots",         // v0.20
		"exit_rule_logs",        // v0.20
		"global_settings",       // v0.21
	}
	for _, name := range want {
		var got string
		q := `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1`
		if err := d.QueryRow(q, name).Scan(&got); err != nil {
			t.Errorf("table %q missing after Open: %v", name, err)
		}
	}
}

func TestGetSetUserTheme(t *testing.T) {
	d := openTestDB(t)
	// seed user
	res, err := d.Exec(`INSERT INTO portal_users (username, password_hash, is_admin, theme) VALUES ('utester', 'x', 0, $1)`, ThemeVercel)
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

// openTestDB returns a fresh PG DB with the full schema applied.
// v1.3.0: was SQLite (Open → migrate), now uses OpenTestPG which
// connects to SKYGATE_TEST_PG_DSN and runs MigratePostgres.
// The 100+ tests that call openTestDB(t) all transparently
// switched to PG with no per-test changes.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return OpenTestPG(t)
}
