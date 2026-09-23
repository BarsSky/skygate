// internal/headscale/local_apply_b301_test.go — B301 (2026-09-23).
//
// The live evidence, from `aro` with the B300 fix already running:
//
//	staggeredSync(aggregated): exit-node-vps applied LOCALLY:
//	  local=err=tailscale set (local, sudo): sudo: The "no new privileges" flag is set,
//	  which prevents sudo from running as root. … — no SSH involved
//
// The aggregated path had correctly recognised the relay as LOCAL and walked the
// ladder, but the SUDO rung's refusal was not recognised as a privilege refusal,
// so `ApplyRoutesLocally` returned one rung early: the root-owned helper sat
// installed and idle, `routes-apply.status` was never created, and every prefix
// stayed «нет маршрута». The flag is `NoNewPrivileges=yes`, which the installers
// write into the skygate unit — sudo can never become root from inside it, so the
// refusal MUST fall through.
package headscale

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// b301NoNewPrivileges is the exact two-line refusal sudo prints under a unit with
// NoNewPrivileges=yes (verbatim from the live journal).
const b301NoNewPrivileges = `sudo: The "no new privileges" flag is set, which prevents sudo from running as root.
sudo: If sudo is running in a container, you may need to adjust the container configuration to disable the flag.`

func TestIsPrivilegeRefusal_B301(t *testing.T) {
	refusals := map[string]string{
		"systemd NoNewPrivileges (live aro)": b301NoNewPrivileges,
		"container NoNewPrivileges":          `sudo: The "no new privileges" flag is set`,
		"sudoers command denial":             "sorry, user skygate is not allowed to execute '/usr/bin/tailscale set' as root",
		"sudoers membership":                 "skygate is not in the sudoers file.  This incident will be reported.",
		"password required":                  "sudo: a password is required",
		"requiretty":                         "sudo: no tty present and no askpass program specified",
		"direct socket denial":               "permission denied",
	}
	for name, out := range refusals {
		if !isPrivilegeRefusal(errors.New("exit status 1"), out) {
			t.Errorf("%s: not recognised as a privilege refusal — the ladder would stop instead of reaching the helper:\n%s", name, out)
		}
	}

	// A REAL tailscale failure must keep being surfaced as-is (never masked by a
	// retry on another rung — the same command would fail there too).
	real := map[string]string{
		"unknown flag":     "flag provided but not defined: -advertise-routes",
		"bad CIDR":         "invalid CIDR address: 10.0.0.0/33",
		"daemon down":      "tailscale: failed to connect to local tailscaled",
		"backend stopped":  "Tailscale is stopped.",
		"a version string": "tailscale v1.60.0",
	}
	for name, out := range real {
		if isPrivilegeRefusal(errors.New("exit status 1"), out) {
			t.Errorf("%s: a real failure was classified as a privilege refusal (it would be retried on other rungs):\n%s", name, out)
		}
	}
	if isPrivilegeRefusal(nil, "permission denied") {
		t.Error("a nil error is never a refusal")
	}
}

// The ladder's third rung must be REACHED when the sudo rung is refused by the
// kernel flag — that is the whole of B301.
func TestApplyRoutesLocally_NoNewPrivilegesFallsThroughToHelper_B301(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_ROUTES_REQUEST_PATH", filepath.Join(dir, "routes.request.props"))

	calls := b293Stub(t, map[string]bool{"tailscale": true, "sudo": true},
		func(bin string, args []string) (string, error) {
			if bin == "sudo" {
				return b301NoNewPrivileges, errors.New("exit status 1")
			}
			return "permission denied", errors.New("exit status 1")
		})
	b293ArmedHelper(t)

	// Simulate the root-owned applier consuming the staged request.
	consumed := make(chan struct{})
	go func() {
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "routes.request.props")); err == nil {
				_ = os.WriteFile(filepath.Join(dir, "routes-apply.status"),
					[]byte("RESULT=ok\nTS=2026-09-23T11:30:00Z\nROUTES=0.0.0.0/0\nCOMMAND=tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0\nREASON=\n"), 0o644)
				close(consumed)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	c := New("http://127.0.0.1:1", "stub")
	tr, out, err := c.ApplyRoutesLocally([]string{"0.0.0.0/0"}, 0)
	if err != nil {
		t.Fatalf("the helper rung must be reached when sudo is refused by NoNewPrivileges: %v", err)
	}
	if tr.Name != "helper" {
		t.Fatalf("transport = %q, want helper — a real aro-shaped refusal stopped the ladder one rung early (calls=%v)", tr.Name, *calls)
	}
	if len(*calls) < 2 || gotBin(*calls, 1) != "sudo" {
		t.Errorf("the ladder must have tried direct then sudo before the helper, calls=%v", *calls)
	}
	if !strings.Contains(out, "helper applied") {
		t.Errorf("output = %q, want the applier's command echoed back", out)
	}
	select {
	case <-consumed:
	default:
		t.Error("the request was never staged where the root applier reads it")
	}
}

// gotBin returns the binary of the i-th recorded call ("bin args…").
func gotBin(calls []string, i int) string {
	if i < 0 || i >= len(calls) {
		return ""
	}
	return strings.Fields(calls[i])[0]
}

// The operator-facing hint must say that a NOPASSWD rule cannot help on a systemd
// install, so the next reader does not chase option (c) on a host where the kernel
// flag refuses sudo before sudoers is consulted.
func TestRoutesFallbackHint_NamesTheNoNewPrivilegesTrap_B301(t *testing.T) {
	hint := RoutesFallbackHint()
	for _, want := range []string{"NoNewPrivileges=yes", "install-routes-helper.sh", "--operator=", "sudoers"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint does not mention %q:\n%s", want, hint)
		}
	}
}
