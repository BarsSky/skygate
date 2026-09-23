// internal/headscale/local_apply_b293_test.go — B293 (2026-09-23).
//
// The local route apply walks a PRIVILEGE LADDER (direct → sudo -n → privileged
// helper → named error). The operator asked for this explicitly: skygate must not
// become unconfigurable just because the service user cannot run `tailscale set`.
// These tests stub the two injectable seams (localRunner / localLookPath) so every
// rung and every fall-through is pinned without needing a tailscale daemon, sudo,
// or a POSIX shell.
package headscale

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// b293Stub installs fake runners. Each entry decides what a `bin` does.
func b293Stub(t *testing.T, lookPathOK map[string]bool, run func(bin string, args []string) (string, error)) *[]string {
	t.Helper()
	var calls []string
	origLook, origRun, origArmed := localLookPath, localRunner, routesHelperArmedFn
	localLookPath = func(file string) (string, error) {
		if lookPathOK[file] {
			return "/usr/bin/" + file, nil
		}
		return "", errors.New("not found")
	}
	localRunner = func(bin string, args ...string) (string, error) {
		calls = append(calls, bin+" "+strings.Join(args, " "))
		if run == nil {
			return "", nil
		}
		return run(bin, args)
	}
	// The helper rung is opt-in per test: "armed" must not depend on whether the
	// machine running the tests happens to have systemd and the unit.
	routesHelperArmedFn = func() (bool, bool) { return false, true }
	t.Cleanup(func() { localLookPath, localRunner, routesHelperArmedFn = origLook, origRun, origArmed })
	return &calls
}

// b293ArmedHelper marks the privileged helper as installed for one test.
func b293ArmedHelper(t *testing.T) {
	t.Helper()
	routesHelperArmedFn = func() (bool, bool) { return true, true }
}

func TestApplyRoutesLocally_DirectWins_B293(t *testing.T) {
	calls := b293Stub(t, map[string]bool{"tailscale": true, "sudo": true}, nil)
	c := New("http://127.0.0.1:1", "stub")
	tr, _, err := c.ApplyRoutesLocally([]string{"0.0.0.0/0", "::/0", "10.0.0.0/8"}, -1)
	if err != nil {
		t.Fatalf("direct rung must succeed: %v", err)
	}
	if tr.Name != "direct" {
		t.Errorf("transport = %q, want direct", tr.Name)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0,::/0,10.0.0.0/8 --accept-routes=false") {
		t.Fatalf("calls = %v, want exactly one tailscale set with the base routes and the accept flag", *calls)
	}
	if strings.Contains((*calls)[0], "sudo") {
		t.Error("sudo must not be attempted when the direct rung works")
	}
}

func TestApplyRoutesLocally_PermissionFallsThroughToSudo_B293(t *testing.T) {
	calls := b293Stub(t, map[string]bool{"tailscale": true, "sudo": true}, func(bin string, args []string) (string, error) {
		if bin == "tailscale" {
			return "tailscale: permission denied opening /var/run/tailscale/tailscaled.sock", errors.New("exit status 1")
		}
		return "", nil // sudo -n works
	})
	c := New("http://127.0.0.1:1", "stub")
	tr, _, err := c.ApplyRoutesLocally([]string{"0.0.0.0/0"}, 0)
	if err != nil {
		t.Fatalf("the sudo rung must be used after a permission refusal: %v", err)
	}
	if tr.Name != "sudo" {
		t.Errorf("transport = %q, want sudo", tr.Name)
	}
	if len(*calls) != 2 || !strings.HasPrefix((*calls)[1], "sudo -n tailscale set ") {
		t.Errorf("calls = %v, want a direct attempt then `sudo -n tailscale set`", *calls)
	}
}

func TestApplyRoutesLocally_PermissionFallsThroughToHelper_B293(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_ROUTES_REQUEST_PATH", filepath.Join(dir, "routes.request.props"))
	calls := b293Stub(t, map[string]bool{"tailscale": true, "sudo": true}, func(bin string, args []string) (string, error) {
		return "permission denied", errors.New("exit status 1")
	})
	b293ArmedHelper(t)
	// Simulate the root applier: it consumes the request and writes a verdict.
	//
	// The poll must be DEADLINE-based, not a fixed iteration count. A busy loop of
	// N os.Stat calls can finish before the request has been staged (the staging
	// path does real work: read the old verdict, validate, write a temp file,
	// rename), and then no verdict is ever written — which is exactly how this
	// test turned red on a loaded CI runner (run 35834971383,
	// `output = "queued for the privileged helper (no verdict yet …)"` after the
	// production 8s wait) while passing everywhere else. The request is still
	// REQUIRED to appear: `consumed` below fails the test when it never does.
	consumed := make(chan struct{})
	go func() {
		deadline := time.Now().Add(6 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "routes.request.props")); err == nil {
				_ = os.WriteFile(filepath.Join(dir, "routes-apply.status"),
					[]byte("RESULT=ok\nTS=2026-09-23T05:00:00Z\nROUTES=0.0.0.0/0\nCOMMAND=tailscale set --advertise-exit-node --advertise-routes=0.0.0.0/0\nREASON=\n"), 0o644)
				close(consumed)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	c := New("http://127.0.0.1:1", "stub")
	tr, out, err := c.ApplyRoutesLocally([]string{"0.0.0.0/0"}, 0)
	if err != nil {
		t.Fatalf("the helper rung must be used when both direct and sudo are refused: %v", err)
	}
	if tr.Name != "helper" {
		t.Errorf("transport = %q, want helper (calls=%v)", tr.Name, *calls)
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

func TestApplyRoutesLocally_RealFailureIsNotMasked_B293(t *testing.T) {
	// A genuine tailscale error (unknown flag, bad daemon) must NOT be retried
	// through sudo/helper: the same command would fail there too, and the operator
	// needs the real message.
	calls := b293Stub(t, map[string]bool{"tailscale": true, "sudo": true}, func(bin string, args []string) (string, error) {
		return "flag provided but not defined: -advertise-exit-nodes", errors.New("exit status 1")
	})
	c := New("http://127.0.0.1:1", "stub")
	_, _, err := c.ApplyRoutesLocally([]string{"0.0.0.0/0"}, 0)
	if err == nil {
		t.Fatal("a real tailscale failure must be reported")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Errorf("error = %q, want the tailscale message", err)
	}
	if len(*calls) != 1 {
		t.Errorf("calls = %v, want exactly one attempt (no masking by other rungs)", *calls)
	}
}

func TestApplyRoutesLocally_NoTransportIsNamed_B293(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_ROUTES_REQUEST_PATH", filepath.Join(dir, "routes.request.props"))
	// No tailscale, no sudo, and nobody consumes the helper request.
	b293Stub(t, map[string]bool{}, nil)
	c := New("http://127.0.0.1:1", "stub")
	_, _, err := c.ApplyRoutesLocally([]string{"0.0.0.0/0"}, 0)
	if err == nil {
		t.Fatal("with no transport at all the failure must be explicit")
	}
	msg := err.Error()
	for _, want := range []string{"not in PATH", "install-routes-helper.sh"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error = %q, want it to name %q", msg, want)
		}
	}
}

func TestApplyRoutesLocally_RefusesEmptyRoutes_B293(t *testing.T) {
	b293Stub(t, map[string]bool{"tailscale": true}, nil)
	c := New("http://127.0.0.1:1", "stub")
	if _, _, err := c.ApplyRoutesLocally(nil, 0); err == nil {
		t.Fatal("an empty route list must be refused — it would strip the exit-node bases")
	}
}

func TestRequestRoutesApply_IsDataOnlyAndAtomic_B293(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "routes.request.props")
	t.Setenv("SKYGATE_ROUTES_REQUEST_PATH", req)

	if err := RequestRoutesApply([]string{"0.0.0.0/0", "::/0", "10.0.0.0/8"}, -1); err != nil {
		t.Fatalf("RequestRoutesApply: %v", err)
	}
	raw, err := os.ReadFile(req)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	body := string(raw)
	// Line-based key=value, exactly what the shell applier parses. No JSON, no
	// shell metacharacters, no eval surface.
	for _, want := range []string{"ROUTES=0.0.0.0/0,::/0,10.0.0.0/8\n", "ACCEPT_ROUTES=-1\n", "REQUESTED_BY=skygate\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("request body = %q, want it to contain %q", body, want)
		}
	}
	// No leftover temp file (the handover is a rename).
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file %s was left behind — the handover must be a rename", e.Name())
		}
	}

	// Anything that is not a bare CIDR is refused (a value from the DB must never
	// reach a root-run command).
	for _, bad := range []string{"10.0.0.0/8; rm -rf /", "0.0.0.0/0 --accept-routes=true", "$(id)", "not-a-cidr"} {
		if err := RequestRoutesApply([]string{bad}, 0); err == nil {
			t.Errorf("RequestRoutesApply(%q) must be refused", bad)
		}
	}
	if err := RequestRoutesApply([]string{"0.0.0.0/0"}, 7); err == nil {
		t.Error("an out-of-range accept_routes must be refused")
	}
	if err := RequestRoutesApply(nil, 0); err == nil {
		t.Error("an empty route list must be refused (it would strip the exit-node bases)")
	}
}

func TestReadRoutesApplyStatus_B293(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_ROUTES_STATUS_PATH", filepath.Join(dir, "routes-apply.status"))

	if _, ok := ReadRoutesApplyStatus(); ok {
		t.Error("no status file yet → ok must be false")
	}
	// The shell applier writes UPPERCASE keys; the Go reader is case-insensitive.
	body := "RESULT=failed\nTS=2026-09-23T05:00:00Z\nROUTES=0.0.0.0/0\nCOMMAND=tailscale set --advertise-routes=0.0.0.0/0\nREASON=tailscale set failed (rc=1): unknown flag\n"
	if err := os.WriteFile(filepath.Join(dir, "routes-apply.status"), []byte(body), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	st, ok := ReadRoutesApplyStatus()
	if !ok {
		t.Fatal("a written status must be readable")
	}
	if st.Result != "failed" || st.TS != "2026-09-23T05:00:00Z" || !strings.Contains(st.Reason, "unknown flag") {
		t.Errorf("status = %+v, want the applier's verdict", st)
	}
	if !strings.Contains(st.Command, "tailscale set") {
		t.Errorf("status.Command = %q, want the applied command", st.Command)
	}
}

// TestLocalTransports_NamesEveryRung_B293: the page shows why a rung is missing,
// not just "unavailable".
func TestLocalTransports_NamesEveryRung_B293(t *testing.T) {
	b293Stub(t, map[string]bool{"tailscale": true}, nil)
	trs := LocalTransports()
	byName := map[string]string{}
	for _, tr := range trs {
		byName[tr.Name] = tr.Detail
	}
	for _, want := range []string{"direct", "sudo", "helper"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("LocalTransports() = %v, want a %q rung", trs, want)
		}
	}
	if !strings.Contains(byName["direct"], "operator") {
		t.Errorf("direct detail = %q, want the --operator hint for a service user", byName["direct"])
	}
	if !strings.Contains(byName["sudo"], "sudo not installed") {
		t.Errorf("sudo detail = %q, want it to say sudo is absent on this host", byName["sudo"])
	}
}
