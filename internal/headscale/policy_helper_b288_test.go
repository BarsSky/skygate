// internal/headscale/policy_helper_b288_test.go — B288.1 (2026-09-22).
//
// The half of the drift story that was missing. skygate hands a policy to the
// privileged applier asynchronously: `RequestPolicyApply` renames a data-only
// request into the directory the root-owned `skygate-policy.path` unit watches,
// and that is ALL skygate used to know. The handoff succeeding is not the write
// succeeding — live on `aro` the ACL was "applied" every five minutes (a fresh
// acl_snapshots row with status OK) while the file headscale served stayed the
// pre-B274 document for days, and /admin/exit-nodes said «политика УСТАРЕЛА»
// with no cause named anywhere.
//
// The applier now records its verdict in `policy-apply.status`; these tests pin
// the reader (including the shapes the applier actually writes).
package headscale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB288_PolicyApplyStatusPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_POLICY_REQUEST_PATH", filepath.Join(dir, "policy.request.props"))
	t.Setenv("SKYGATE_POLICY_STATUS_PATH", "")
	if got, want := PolicyApplyStatusPath(), filepath.Join(dir, "policy-apply.status"); got != want {
		t.Errorf("PolicyApplyStatusPath() = %q, want %q (next to the request the applier consumes)", got, want)
	}
	t.Setenv("SKYGATE_POLICY_STATUS_PATH", "/tmp/explicit.status")
	if got := PolicyApplyStatusPath(); got != "/tmp/explicit.status" {
		t.Errorf("an explicit SKYGATE_POLICY_STATUS_PATH must win, got %q", got)
	}
}

func TestB288_ReadPolicyApplyStatus(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "policy-apply.status")
	t.Setenv("SKYGATE_POLICY_STATUS_PATH", statusPath)

	// Missing file: ok=false, no error — an install that never applied a policy.
	if _, ok := ReadPolicyApplyStatus(); ok {
		t.Error("ReadPolicyApplyStatus on a missing file reported ok=true")
	}

	// The failure shape (what the operator saw: the write never lands).
	if err := os.WriteFile(statusPath, []byte(
		"RESULT=failed\nTS=2026-09-22T21:47:41Z\nPATH=/etc/headscale/policy.hujson\nBYTES=11341\n"+
			"REASON=headscale did not answer on http://127.0.0.1:8081/health within 20s after the write; the previous policy was restored\n",
	), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	st, ok := ReadPolicyApplyStatus()
	if !ok {
		t.Fatal("ReadPolicyApplyStatus did not read the failure status")
	}
	if st.Result != "failed" {
		t.Errorf("Result = %q, want failed", st.Result)
	}
	if st.Bytes != "11341" || st.Path != "/etc/headscale/policy.hujson" {
		t.Errorf("Bytes/Path = %q/%q, want 11341//etc/headscale/policy.hujson", st.Bytes, st.Path)
	}
	sum := st.Summary()
	for _, want := range []string{"failed", "2026-09-22T21:47:41Z", "11341 bytes", "did not answer"} {
		if !strings.Contains(sum, want) {
			t.Errorf("Summary() = %q, want it to mention %q", sum, want)
		}
	}

	// The success shape.
	if err := os.WriteFile(statusPath, []byte(
		"RESULT=ok\nTS=2026-09-22T21:50:00Z\nPATH=/etc/headscale/policy.hujson\nBYTES=5082\n"+
			"REASON=wrote 5082 bytes to /etc/headscale/policy.hujson; headscale restarted and answered on http://127.0.0.1:8081/health\n",
	), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	st, ok = ReadPolicyApplyStatus()
	if !ok || st.Result != "ok" {
		t.Fatalf("ReadPolicyApplyStatus = (%+v, %v), want an ok result", st, ok)
	}
	if st.ModTime.IsZero() {
		t.Error("ModTime is zero — the page cannot say WHEN the applier last ran")
	}

	// An empty/garbage file must read as "unknown", not as a status.
	if err := os.WriteFile(statusPath, []byte("not key=value data at all\n"), 0o644); err != nil {
		t.Fatalf("write status: %v", err)
	}
	if _, ok := ReadPolicyApplyStatus(); ok {
		t.Error("ReadPolicyApplyStatus accepted a file with no RESULT/TS")
	}
}
