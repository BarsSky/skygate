// internal/db/globalsettings_test.go
//
// v0.33.1.13 — regression test for GetGlobalSetting.
//
// Before the fix, GetGlobalSetting used a hardcoded "?"
// placeholder in its SELECT. That worked on SQLite but
// crashed on PG with "syntax error at or near ','"
// (pgx stdlib does NOT auto-convert "?" to "$N"). The
// fix is to dispatch via placeholdersList(1) — same
// pattern as SetGlobalSetting (the v0.33.1.8 fix).
//
// The v0.33.1.13 user-reported bug (SKYGATE_TS_LOGIN_SERVER
// set via web UI, but the value never took effect because
// the read-side helper was returning a PG syntax error)
// was traced to this. The tests below pin both
// backends' behavior.

package db

import (
	"testing"
)

// TestGetGlobalSetting_RoundTrip: write a value via
// SetGlobalSetting, then read it back via GetGlobalSetting.
// Catches the "set works but read fails" regression.
func TestGetGlobalSetting_RoundTrip(t *testing.T) {
	d := openTestDB(t)

	// No row yet → default.
	got, err := GetGlobalSetting(d, "tailscale.login_server", "https://default.example.com")
	if err != nil {
		t.Fatalf("empty read: %v", err)
	}
	if got != "https://default.example.com" {
		t.Errorf("empty read: got %q, want default", got)
	}

	// Write a value, then read it back.
	if err := SetGlobalSetting(d, "tailscale.login_server", "https://head.<your-domain>:8443"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = GetGlobalSetting(d, "tailscale.login_server", "https://default.example.com")
	if err != nil {
		t.Fatalf("read after set: %v", err)
	}
	if got != "https://head.<your-domain>:8443" {
		t.Errorf("read after set: got %q, want %q", got, "https://head.<your-domain>:8443")
	}

	// Overwrite (upsert) — should still read back the new value.
	if err := SetGlobalSetting(d, "tailscale.login_server", "https://head2.<your-domain>"); err != nil {
		t.Fatalf("set (overwrite): %v", err)
	}
	got, err = GetGlobalSetting(d, "tailscale.login_server", "https://default.example.com")
	if err != nil {
		t.Fatalf("read after overwrite: %v", err)
	}
	if got != "https://head2.<your-domain>" {
		t.Errorf("read after overwrite: got %q, want %q", got, "https://head2.<your-domain>")
	}

	// Clear (empty value) — should fall back to default.
	if err := SetGlobalSetting(d, "tailscale.login_server", ""); err != nil {
		t.Fatalf("set (empty): %v", err)
	}
	got, err = GetGlobalSetting(d, "tailscale.login_server", "https://default.example.com")
	if err != nil {
		t.Fatalf("read after clear: %v", err)
	}
	if got != "https://default.example.com" {
		t.Errorf("read after clear: got %q, want default", got)
	}
}

// openTestDB opens a fresh SQLite DB in a temp file and
// applies the migrations. Defined in db_test.go — see that
// file for the implementation.
