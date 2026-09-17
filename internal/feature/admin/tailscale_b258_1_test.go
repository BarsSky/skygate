// v1.5.8+ (B258.1): pure-function tests for the new
// tailscaleAuthKeyMissingForStart helper + the State
// derivation that surfaces "configured but no key" as a
// third visual state on /admin/tailscale.
//
// B258 only modelled two states (disabled-by-config vs
// enabled) and collapsed "configured regular file path
// but file missing" into the "enabled" branch. The result
// was a confusing UI:
//   - status section says "tailscaled: stopped"
//   - the "Disable Tailscale in container" button is
//     rendered (because we're in the "enabled" branch)
//   - clicking it makes no sense (nothing's running)
//   - clicking Start errors with
//     "read auth key: open /data/ts/authkey: no such file"
// because the file genuinely doesn't exist.
//
// B258.1 fixes by adding AuthKeyMissing = !disabled && !set.
// The template uses a 3-way if/else if/else to render
// distinct UI for each state, and Start is hard-disabled
// in the missing state too. The handler also returns a
// clear actionable error if Start is bypassed (e.g. by a
// direct POST in a script) so the operator sees
// "file missing — paste one" instead of the raw ENOENT.

package admin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTailscaleAuthKeyMissingForStart_PathButNoFile pins
// the new state's primary scenario: DB/env points at a
// regular file path like /data/ts/authkey, the file does
// not exist, helper must report missing=true AND
// disabled=false (because the path is a regular path —
// not /dev/null or any other sentinel).
func TestTailscaleAuthKeyMissingForStart_PathButNoFile(t *testing.T) {
	tmp := t.TempDir()
	missingPath := filepath.Join(tmp, "authkey")
	s := &Service{TailscaleAuthKeyPath: missingPath}

	if s.tailscaleAuthKeyDisabled() {
		t.Fatalf("regular missing file path must NOT report disabled=true")
	}
	if !s.tailscaleAuthKeyMissingForStart() {
		t.Fatalf("regular missing file path MUST report missing=true "+
			"(this is the B258.1 third state — pre-fix it was silently "+
			"fall-through to 'enabled' which gave a confusing UI)")
	}
}

// TestTailscaleAuthKeyMissingForStart_DevNullIsNotMissing
// pins the exclusivity: a /dev/null path is "disabled" and
// must NOT also report "missing" — they're distinct states.
// Pre-B258.1 the missing-state field didn't exist; the
// template's 2-way if/else meant a /dev/null path rendered
// the Enable-in-container banner, which is correct. B258.1
// preserves that — Disabled takes priority over Missing.
func TestTailscaleAuthKeyMissingForStart_DevNullIsNotMissing(t *testing.T) {
	s := &Service{TailscaleAuthKeyPath: "/dev/null"}

	if !s.tailscaleAuthKeyDisabled() {
		t.Fatalf("/dev/null must report disabled=true")
	}
	if s.tailscaleAuthKeyMissingForStart() {
		t.Fatalf("/dev/null is disabled, not missing — these are mutually exclusive states")
	}
}

// TestTailscaleAuthKeyMissingForStart_FileExistsEmpty pins
// the "file exists but is empty" sub-case. The previous-state
// code (B258) treated this as a missing file too (because
// readTailscaleAuthKey returns set=false on empty content).
// B258.1 must agree: an empty file at a regular path reports
// missing=true.
func TestTailscaleAuthKeyMissingForStart_FileExistsEmpty(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "authkey")
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s := &Service{TailscaleAuthKeyPath: path}

	if s.tailscaleAuthKeyDisabled() {
		t.Fatalf("regular file (even empty) must NOT report disabled=true")
	}
	if !s.tailscaleAuthKeyMissingForStart() {
		t.Fatalf("empty file at regular path MUST report missing=true")
	}
}

// TestTailscaleAuthKeyMissingForStart_FileExistsWithContent
// pins the happy path: a non-empty file at a regular path
// reports neither disabled nor missing. The UI shows the
// existing Disable-in-container card + Start button enabled.
func TestTailscaleAuthKeyMissingForStart_FileExistsWithContent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "authkey")
	if err := os.WriteFile(path, []byte("tskey-auth-fake"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	s := &Service{TailscaleAuthKeyPath: path}

	if s.tailscaleAuthKeyDisabled() {
		t.Fatalf("regular file with content must NOT report disabled=true")
	}
	if s.tailscaleAuthKeyMissingForStart() {
		t.Fatalf("regular file with content must NOT report missing=true")
	}
}

// TestAuthKeyMissingMutuallyExclusiveWithDisabled is the
// canonical safety check: the two states are designed to be
// mutually exclusive at the helper level. If a future
// refactor makes them overlap, the UI will render the wrong
// banner (the if/else if/else template picks the first match).
// Pinning the exclusivity here catches that regression
// before it reaches a deployed page.
func TestAuthKeyMissingMutuallyExclusiveWithDisabled(t *testing.T) {
	cases := []struct {
		name string
		path string
		// We don't write the file here — a missing path is
		// the most common scenario in prod (B258.1's exact
		// trigger). The other cases are covered above.
	}{
		{"dev_null", "/dev/null"},
		{"missing_regular", "/nonexistent/path/authkey"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Service{TailscaleAuthKeyPath: c.path}
			disabled := s.tailscaleAuthKeyDisabled()
			missing := s.tailscaleAuthKeyMissingForStart()
			if disabled && missing {
				t.Errorf("disabled=%v missing=%v must not both be true (path=%q)",
					disabled, missing, c.path)
			}
		})
	}
}
