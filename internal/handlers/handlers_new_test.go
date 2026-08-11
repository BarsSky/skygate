package handlers

// handlers_new_test.go — regression test for the v0.33.1.31 B83
// "sshKeyPath not assigned to App.SSHKeyPath" bug.
//
// 2026-08-09 operator report: clicking "Set as egress relay" on
// /admin/telegram → "SSH на emilia не удался: no ssh_key_path
// provided; set exit_servers.ssh_key_path or SKYGATE_EXIT_SSH_KEY".
// The env was correctly set (SKYGATE_EXIT_SSH_KEY=/ssh-sync/skygate_sync)
// AND exit_servers.ssh_key_path for emilia was empty (the
// auto-fallback path), so the handler was supposed to fall back
// to the global env-derived value via `s.SSHKeyPath` — but that
// field was always empty because handlers.New() received the
// value as a parameter and never assigned it to the App struct.
//
// The /admin/exit-nodes/sync flow didn't show the bug because
// it uses `s.Cfg.SSHKeyPath` (the config-layer copy, populated
// correctly). The /admin/telegram egress handler is the only
// call site that reads `s.SSHKeyPath` directly — and that's
// exactly where the operator hit the bug.
//
// This test pins the contract: handlers.New() MUST assign the
// sshKeyPath parameter to App.SSHKeyPath so downstream readers
// (s.SSHKeyPath in /admin/telegram egress, the
// /admin/exit-nodes add form's default value, the
// /admin/backup/config SFTP flash message) all see the value.

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"skygate/internal/config"
)

// TestNew_AssignsSSHKeyPath pins the v0.33.1.31 B83 contract:
// handlers.New() MUST assign the sshKeyPath parameter to
// App.SSHKeyPath. The pre-B83 implementation accepted the
// parameter but never used it — App.SSHKeyPath stayed zero-
// valued forever, which broke /admin/telegram egress (the
// only call site that reads s.SSHKeyPath directly, since the
// /admin/exit-nodes/sync flow uses s.Cfg.SSHKeyPath instead).
//
// The fix was a one-line addition in New():
//
//	SSHKeyPath: sshKeyPath,  // was missing in v0.33.1
//
// Without this test, a future refactor that drops the
// assignment silently re-introduces the bug. The operator's
// report would be a vague "telegram egress SSH doesn't work"
// — same shape as the v0.33.1 B43 (no ssh_key_path) error
// from the live VM, but the root cause would be a different
// field.
func TestNew_AssignsSSHKeyPath(t *testing.T) {
	// Minimal in-memory DB. The function doesn't actually
	// query the DB in New() (the DB is just stored on the
	// App struct); an empty in-memory SQLite is enough.
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: db: %v", err)
	}
	defer d.Close()

	// Minimal config — only the fields New() reads.
	cfg := &config.Config{
		DerpBaseURL: "http://derp.example.com:8766",
	}

	const wantKey = "/ssh-sync/skygate_sync"

	a := New(
		d,            // DB
		nil,          // hs (not used in New's body)
		"hskey-stub", // HeadscaleKey
		"jwt-stub",   // JWTSecret
		"https://head.example.com", // ControlURL
		wantKey,      // sshKeyPath — the value under test
		24,           // SessionHours
		cfg,          // Cfg
	)
	if a == nil {
		t.Fatal("New() returned nil")
	}
	if a.SSHKeyPath != wantKey {
		t.Errorf("App.SSHKeyPath = %q, want %q — handlers.New() did NOT "+
			"assign the sshKeyPath parameter to App.SSHKeyPath. "+
			"This breaks /admin/telegram egress (the only call site "+
			"that reads s.SSHKeyPath directly), plus the "+
			"/admin/exit-nodes add-form default and the "+
			"/admin/backup/config SFTP flash message. "+
			"See B83 in scripts/verify_pre_deploy.sh.",
			a.SSHKeyPath, wantKey)
	}

	// Sanity: the rest of the App is wired as expected.
	if a.DB != d {
		t.Errorf("App.DB not wired")
	}
	if a.Cfg != cfg {
		t.Errorf("App.Cfg not wired")
	}
	if a.ControlURL != "https://head.example.com" {
		t.Errorf("App.ControlURL not wired")
	}
}

// TestNew_EmptySSHKeyPath_StaysEmpty is the negative case:
// when the operator hasn't set SKYGATE_EXIT_SSH_KEY, New()
// is called with "" — the App.SSHKeyPath field should also
// be "" (the fallback chain in the call sites then errors
// out with a clear "set ssh_key_path" message). This pins
// the "no silent default substitution" invariant: pre-B83
// the field was always empty regardless of input, so this
// case happened to "work" — but a future refactor that
// silently substitutes a default would mask the empty-input
// contract that SetAdvertisedRoutes relies on (it returns
// "no ssh_key_path provided" exactly so the operator sees
// a clear "set the env or the per-row" message).
func TestNew_EmptySSHKeyPath_StaysEmpty(t *testing.T) {
	d, _ := sql.Open("sqlite3", ":memory:")
	defer d.Close()
	cfg := &config.Config{DerpBaseURL: "http://derp.example.com:8766"}

	a := New(d, nil, "k", "s", "https://head.example.com", "", 24, cfg)
	if a.SSHKeyPath != "" {
		t.Errorf("App.SSHKeyPath = %q, want \"\" — New() must not "+
			"silently substitute a default; the fallback chain "+
			"in /admin/telegram egress + /admin/exit-nodes/sync "+
			"relies on the empty value to surface the "+
			"\"no ssh_key_path provided\" error from headscale.",
			a.SSHKeyPath)
	}
	if !strings.HasPrefix(a.DerpBaseURL, "http") {
		t.Errorf("App.DerpBaseURL not wired: %q", a.DerpBaseURL)
	}
}
