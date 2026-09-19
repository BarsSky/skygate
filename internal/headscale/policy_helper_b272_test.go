// B272.3 / B272.4 (2026-09-19) — privileged policy handoff + checkout resilience.
package headscale

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestB2723_PolicyRequestPath: the request lands next to the self-update request
// by default (same ownership model), and both env overrides win.
func TestB2723_PolicyRequestPath(t *testing.T) {
	t.Setenv("SKYGATE_POLICY_REQUEST_PATH", "/tmp/explicit.props")
	if got := PolicyRequestPath(); got != "/tmp/explicit.props" {
		t.Errorf("explicit override ignored: %s", got)
	}
	t.Setenv("SKYGATE_POLICY_REQUEST_PATH", "")
	t.Setenv("SKYGATE_UPDATE_DIR", "/var/lib/skygate/update")
	if got := PolicyRequestPath(); got != filepath.Join("/var/lib/skygate/update", "policy.request.props") {
		t.Errorf("SKYGATE_UPDATE_DIR ignored: %s", got)
	}
	t.Setenv("SKYGATE_UPDATE_DIR", "")
	t.Setenv("SKYGATE_UPDATE_STATE_PATH", "/srv/skygate/skygate-update-status.json")
	if got := PolicyRequestPath(); got != filepath.Join("/srv/skygate/update", "policy.request.props") {
		t.Errorf("state-path derivation wrong: %s", got)
	}
}

// TestB2723_HelperNotArmedWhenAbsent: without the applier + path unit the handoff
// must report ErrPolicyHelperUnavailable, which the caller turns into the
// "apply it by hand" message (never a silent success).
func TestB2723_HelperNotArmedWhenAbsent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper is a POSIX/systemd artifact")
	}
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.hujson")
	if err := os.WriteFile(policy, []byte(`{"tagOwners":{}}`), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("SKYGATE_POLICY_APPLIER", filepath.Join(dir, "absent-applier.sh"))
	t.Setenv("SKYGATE_POLICY_PATH_UNIT", filepath.Join(dir, "absent.path"))
	t.Setenv("SKYGATE_POLICY_REQUEST_PATH", filepath.Join(dir, "policy.request.props"))

	if PolicyHelperArmed() {
		t.Fatal("PolicyHelperArmed must be false when the applier and unit are missing")
	}
	err := RequestPolicyApply(policy, `{"tagOwners":{"tag:x":["a@b"]}}`)
	if !errors.Is(err, ErrPolicyHelperUnavailable) {
		t.Fatalf("err = %v, want ErrPolicyHelperUnavailable", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "policy.request.props")); !os.IsNotExist(statErr) {
		t.Error("no request file may be written when the helper is not installed")
	}
}

// TestB2723_RequestIsWrittenWhenArmed: with the applier + unit present the
// request file carries the target path and the policy between the markers, so the
// root applier can parse it as data.
func TestB2723_RequestIsWrittenWhenArmed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper is a POSIX/systemd artifact")
	}
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.hujson")
	if err := os.WriteFile(policy, []byte(`{"tagOwners":{}}`), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	applier := filepath.Join(dir, "skygate-apply-policy.sh")
	if err := os.WriteFile(applier, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write applier: %v", err)
	}
	unit := filepath.Join(dir, "skygate-policy.path")
	if err := os.WriteFile(unit, []byte("[Path]\n"), 0o644); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	req := filepath.Join(dir, "policy.request.props")
	t.Setenv("SKYGATE_POLICY_APPLIER", applier)
	t.Setenv("SKYGATE_POLICY_PATH_UNIT", unit)
	t.Setenv("SKYGATE_POLICY_REQUEST_PATH", req)

	body := `{"tagOwners":{"tag:dev-daniil-workpc":["daniil@ts.example.com"]}}`
	if err := RequestPolicyApply(policy, body); err != nil {
		t.Fatalf("RequestPolicyApply: %v", err)
	}
	raw, err := os.ReadFile(req)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `POLICY_PATH="`+policy+`"`) {
		t.Errorf("request does not name the target path:\n%s", got)
	}
	if !strings.Contains(got, "POLICY_BEGIN\n"+body+"\nPOLICY_END") {
		t.Errorf("request does not delimit the policy body:\n%s", got)
	}
}

// TestB2723_RequestValidation: guards against a request the applier must reject
// (relative/absent target, empty body) — the Go side refuses first.
func TestB2723_RequestValidation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_POLICY_APPLIER", filepath.Join(dir, "a.sh"))
	t.Setenv("SKYGATE_POLICY_PATH_UNIT", filepath.Join(dir, "a.path"))
	t.Setenv("SKYGATE_POLICY_REQUEST_PATH", filepath.Join(dir, "req.props"))

	if err := RequestPolicyApply("", "{}"); err == nil {
		t.Error("empty path must be rejected")
	}
	if err := RequestPolicyApply("relative/policy.hujson", "{}"); err == nil {
		t.Error("relative path must be rejected")
	}
	if err := RequestPolicyApply(filepath.Join(dir, "missing.hujson"), "{}"); err == nil {
		t.Error("a non-existent target must be rejected (the applier refuses to create it)")
	}
	existing := filepath.Join(dir, "policy.hujson")
	if err := os.WriteFile(existing, []byte("{}"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := RequestPolicyApply(existing, ""); err == nil {
		t.Error("empty policy body must be rejected")
	}
}
