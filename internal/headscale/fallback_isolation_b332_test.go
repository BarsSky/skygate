// internal/headscale/fallback_isolation_b332_test.go — B332 (2026-10-01).
//
// WHY THIS FILE EXISTS
// --------------------
// `GetACL()` walks three rungs when the REST API fails: the policy FILE
// headscale serves in `policy.mode: file`, then the CLI (`docker exec
// <container> headscale …`, else the local binary). Two tests asserted the
// failure path while assuming the other two rungs were unreachable:
//
//	TestGetACLNamesEveryRungInTheError_B294 — "with no API, no file and no CLI
//	the read must fail loudly"
//	TestGetACLAPIFailsNoContainer — "when both the API fails AND ExecContainer
//	is empty, the function returns the API error"
//
// That assumption held on a developer machine and was FALSE on the reference
// VM, where the `headscale` container is running and docker is on PATH — so
// `GetACL()` returned the LIVE policy and both tests failed. Measured:
//
//	--- FAIL: TestGetACLNamesEveryRungInTheError_B294
//	    acl_read_b294_test.go:76: with no API, no file and no CLI the read must fail loudly
//	--- FAIL: TestGetACLAPIFailsNoContainer
//	    headscale_test.go:576: expected error for 500 + no CLI, got nil
//
// Those are environment failures, not regressions, but the fix is NOT to skip
// them: a skipped test proves nothing on the host that matters. The fix is to
// make the premise true — isolate the fallbacks explicitly.
package headscale

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// isolateHeadscaleFallbacks makes the file rung and the CLI rung of GetACL
// unreachable for the duration of the test, on ANY host:
//
//   - PATH is a fresh empty directory, so `exec.LookPath("docker")` and
//     `exec.LookPath("headscale")` both fail — the same trick
//     TestB311_NativeInstallRunsTheLocalBinary uses in reverse;
//   - SKYGATE_HEADSCALE_POLICY_PATH and SKYGATE_HEADSCALE_CONFIG point at
//     non-existent temporary paths, so DiscoverPolicyPath cannot find the
//     host's real headscale configuration.
//
// t.Setenv restores everything at the end of the test.
func isolateHeadscaleFallbacks(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("SKYGATE_HEADSCALE_POLICY_PATH", filepath.Join(t.TempDir(), "absent.hujson"))
	t.Setenv("SKYGATE_HEADSCALE_CONFIG", filepath.Join(t.TempDir(), "absent-config.yaml"))
}

// TestB332_EmptyExecContainerNeverUsesDocker pins the semantic this block
// introduced: an empty ExecContainer means "no container", not "the container
// literally named headscale".
//
// The old code replaced an empty name with the default `headscale`, which made
// the field impossible to disable even though tags.go, preauth.go and nodes.go
// all read `ExecContainer == ""` as "the CLI path is not configured" and
// headscale_test.go documented `c.ExecContainer = ""` as "disable CLI
// fallback". On the reference VM that mismatch is what made two tests read the
// live policy instead of an error.
func TestB332_EmptyExecContainerNeverUsesDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH here — this contract is about NOT reaching docker when it exists")
	}
	c := New("http://127.0.0.1:1", "stub-key")
	c.ExecContainer = ""

	used := false
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		used = true
		return nil, os.ErrNotExist
	})

	// The result is irrelevant (a host with a local `headscale` binary may even
	// succeed); what must never happen is reaching the docker rung.
	_, _ = c.runHeadscaleCLI("nodes", "list")

	if used {
		t.Error("the docker rung ran for an EMPTY ExecContainer — the field must be possible to disable, " +
			"and three other files in this package already read empty as \"not configured\"")
	}
}

// TestB332_ConfiguredContainerDoesUseDocker is the positive half: a NON-empty
// container must still reach the docker rung, so the fix cannot be satisfied by
// disabling the fallback altogether.
func TestB332_ConfiguredContainerDoesUseDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH here")
	}
	c := New("http://127.0.0.1:1", "stub-key")
	c.ExecContainer = "skygate-test-container"

	var got []string
	c.SetDockerRunner(func(args ...string) ([]byte, error) {
		got = append([]string{}, args...)
		return []byte("ok"), nil
	})

	out, err := c.runHeadscaleCLI("nodes", "list")
	if err != nil {
		t.Fatalf("the docker rung must be used for a configured container: %v", err)
	}
	if string(out) != "ok" {
		t.Errorf("out = %q, want ok", out)
	}
	if len(got) < 3 || got[0] != "exec" || got[1] != "skygate-test-container" {
		t.Errorf("docker argv = %v, want [exec skygate-test-container …]", got)
	}
}
