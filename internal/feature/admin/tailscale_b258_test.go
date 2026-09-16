// v1.5.8+ (B258): pure-function tests for
// tailscaleAuthKeyDisabled. The helper is the bridge between
// the entrypoint.sh skip check (which decides at container
// start whether to launch tailscaled) and the /admin/tailscale
// UI (which decides at request time whether the Start button
// should be enabled).
//
// Both checks MUST agree on the same path: if entrypoint skipped
// tailscaled because the path is /dev/null, the UI must also
// refuse to start it. Otherwise the operator sees a confusing
// "read auth key: no such file or directory" error on click.

package admin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTailscaleAuthKeyDisabled_DevNull pins the canonical
// "operator wants tailscale off" config: SKYGATE_TS_AUTHKEY_FILE=/dev/null.
// The function must report disabled=true so the UI banner
// renders + Start is hard-disabled + the handler refuses the
// POST.
func TestTailscaleAuthKeyDisabled_DevNull(t *testing.T) {
	s := &Service{TailscaleAuthKeyPath: "/dev/null"}
	if !s.tailscaleAuthKeyDisabled() {
		t.Errorf("/dev/null must report disabled=true")
	}
}

// TestTailscaleAuthKeyDisabled_DevNullSlash covers the typo
// "/dev/null/foo" — defensive (operators typo). Must report
// disabled=true.
func TestTailscaleAuthKeyDisabled_DevNullSlash(t *testing.T) {
	s := &Service{TailscaleAuthKeyPath: "/dev/null/whatever"}
	if !s.tailscaleAuthKeyDisabled() {
		t.Errorf("/dev/null/whatever must report disabled=true")
	}
}

// TestTailscaleAuthKeyDisabled_EmptyPath pins the "no path
// configured at all" case. tailscaleAuthKeyPath() falls back
// to /data/ts/authkey when the field is empty, so the helper
// sees that default. The default file might exist (bind-mounted
// from the host's data/ dir), so the helper does NOT report
// disabled=true — the operator gets the standard "Auth key
// is NOT set" UI badge + the Start button is enabled but the
// click returns "auth key file is empty; paste one first".
func TestTailscaleAuthKeyDisabled_EmptyPath(t *testing.T) {
	s := &Service{TailscaleAuthKeyPath: ""}
	if s.tailscaleAuthKeyDisabled() {
		t.Errorf("empty path falls back to /data/ts/authkey default — should NOT be auto-disabled (UI surfaces the misconfig separately)")
	}
}

// TestTailscaleAuthKeyDisabled_CharacterDevice covers a
// scenario where the operator bound a non-regular-file path
// (e.g. /dev/null or a directory). The helper's Stat()-based
// fallback must catch these.
func TestTailscaleAuthKeyDisabled_CharacterDevice(t *testing.T) {
	// On Linux, /dev/null is a character device (not a
	// regular file). /dev/null on macOS is also a character
	// device. On Windows the test runner would skip — but
	// this test runs in CI on Linux, so we proceed.
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skipf("no /dev/null on this platform: %v", err)
	}
	s := &Service{TailscaleAuthKeyPath: "/dev/null"}
	if !s.tailscaleAuthKeyDisabled() {
		t.Errorf("/dev/null (character device) must report disabled=true")
	}
}

// TestTailscaleAuthKeyDisabled_RegularFileOK pins the happy
// path: a normal file path is NOT disabled (the file might
// or might not exist — that's a separate misconfiguration
// signal that the UI surfaces as "Auth key is NOT set").
func TestTailscaleAuthKeyDisabled_RegularFileOK(t *testing.T) {
	// Create a temp file so the Stat() call sees a regular
	// file. The helper doesn't read the file; it only Stats
	// it. We don't care about content.
	tmp := filepath.Join(t.TempDir(), "authkey")
	if err := os.WriteFile(tmp, []byte("tskey-test"), 0600); err != nil {
		t.Fatalf("tempfile: %v", err)
	}
	s := &Service{TailscaleAuthKeyPath: tmp}
	if s.tailscaleAuthKeyDisabled() {
		t.Errorf("regular-file path with valid contents must NOT be disabled")
	}
}

// TestTailscaleAuthKeyDisabled_MissingFile pins the case
// where the path is set but the file doesn't exist on disk.
// This is NOT disabled — it's a misconfiguration, and the
// UI surfaces it via the "Auth key is NOT set" badge. The
// handler refuses Start with a "auth key file is empty;
// paste one first" error so the operator can recover by
// pasting a key.
func TestTailscaleAuthKeyDisabled_MissingFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "does-not-exist")
	s := &Service{TailscaleAuthKeyPath: tmp}
	if s.tailscaleAuthKeyDisabled() {
		t.Errorf("missing regular-file path must NOT be disabled (UI surfaces the misconfig separately)")
	}
}
