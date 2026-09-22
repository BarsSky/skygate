// internal/headscale/policy_helper_b283_test.go — B283 (2026-09-22).
//
// Two defects in the privileged-policy handoff took the control plane down on
// the live `aro` host:
//
//  1. RequestPolicyApply rewrote `policy.request.props` IN PLACE while the root
//     applier read the same file line by line, so a second request landing
//     mid-read spliced the head of one document onto the tail of another — the
//     7869-byte file on disk was the size of the saved snapshot, valid in the
//     database, and unparseable for headscale.
//  2. A document that cannot be parsed was handed over at all, so the applier
//     wrote it and headscale crash-looped reading its own policy file.
package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b283RequestPath points the helper at a temp dir and returns the request path
// plus a healthy target file (the applier refuses a target that does not exist).
func b283RequestPath(t *testing.T) (req, target string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SKYGATE_UPDATE_DIR", dir)
	t.Setenv("SKYGATE_POLICY_REQUEST_PATH", filepath.Join(dir, "policy.request.props"))
	target = filepath.Join(dir, "policy.hujson")
	if err := os.WriteFile(target, []byte("{}"), 0o640); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	// PolicyHelperArmed() must be true: point the applier + path unit at files
	// that exist in the temp dir.
	applier := filepath.Join(dir, "skygate-apply-policy.sh")
	if err := os.WriteFile(applier, []byte("#!/bin/bash\n"), 0o755); err != nil {
		t.Fatalf("seed applier: %v", err)
	}
	unit := filepath.Join(dir, "skygate-policy.path")
	if err := os.WriteFile(unit, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatalf("seed unit: %v", err)
	}
	t.Setenv("SKYGATE_POLICY_APPLIER", applier)
	t.Setenv("SKYGATE_POLICY_PATH_UNIT", unit)
	return PolicyRequestPath(), target
}

// TestRequestPolicyApplyRefusesUnparseableB283: a malformed policy must never
// reach the request file — that file is what the applier writes to headscale's
// policy path, and a bad document there kills the daemon at startup.
func TestRequestPolicyApplyRefusesUnparseableB283(t *testing.T) {
	req, target := b283RequestPath(t)

	err := RequestPolicyApply(target, `{"grants": [`)
	if err == nil {
		t.Fatal("RequestPolicyApply handed over a policy that does not parse")
	}
	if !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("error does not name the reason: %v", err)
	}
	if _, statErr := os.Stat(req); !os.IsNotExist(statErr) {
		t.Errorf("the request file %s was created for a malformed policy", req)
	}
}

// TestRequestPolicyApplyIsAtomicB283: the request must appear by rename, never
// by an in-place rewrite. The observable contract: after a successful call the
// file holds exactly one complete request (sentinel-framed body included), and
// no temp file is left behind.
func TestRequestPolicyApplyIsAtomicB283(t *testing.T) {
	req, target := b283RequestPath(t)

	policy := `{"grants":[{"src":["tag:a"],"dst":["*:*"]}],"tagOwners":{"tag:a":["u@d"]}}`
	if err := RequestPolicyApply(target, policy); err != nil {
		t.Fatalf("RequestPolicyApply: %v", err)
	}
	body, err := os.ReadFile(req)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	s := string(body)
	for _, want := range []string{"POLICY_PATH=", "POLICY_BEGIN", "POLICY_END", policy} {
		if !strings.Contains(s, want) {
			t.Errorf("request is missing %q:\n%s", want, s)
		}
	}
	if strings.Count(s, "POLICY_BEGIN") != 1 || strings.Count(s, "POLICY_END") != 1 {
		t.Errorf("request is not exactly one framed body:\n%s", s)
	}
	// No leftover temp file: the handover must be a rename of a single temp
	// file, not a write into the watched path.
	entries, err := os.ReadDir(filepath.Dir(req))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q after a successful handover — the applier may read a partial request", e.Name())
		}
	}

	// A second handover must leave a COMPLETE request (the splice bug produced a
	// mixture of two documents of the same length).
	policy2 := `{"grants":[{"src":["tag:b"],"dst":["*:*"]}],"tagOwners":{"tag:a":["u@d"],"tag:b":["u@d"]}}`
	if err := RequestPolicyApply(target, policy2); err != nil {
		t.Fatalf("RequestPolicyApply #2: %v", err)
	}
	body2, err := os.ReadFile(req)
	if err != nil {
		t.Fatalf("read request #2: %v", err)
	}
	if strings.Contains(string(body2), "tag:a\"]") && !strings.Contains(string(body2), "tag:b\"]") {
		t.Errorf("second request still looks like the first one:\n%s", body2)
	}
	if !strings.Contains(string(body2), policy2) {
		t.Errorf("second request body is not the second policy (splice?):\n%s", body2)
	}
}
