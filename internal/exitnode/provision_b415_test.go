package exitnode

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestRun_B415_ReportsTheCauseNotTheBanner is the behavioural half of B415. The
// operator's failed run reported the harmless known-hosts banner as the REASON;
// this test makes ssh print that banner followed by the real cause and asserts the
// returned error names the cause (and the key fingerprint) instead.
//
// It runs a real `sh` (the same guard shape the `bash -n` test uses) rather than
// faking exec.Cmd, because a fake Command cannot be given a CombinedOutput.
func TestRun_B415_ReportsTheCauseNotTheBanner(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		sh = bashPath()
	}
	if sh == "" {
		t.Skip("no POSIX shell available to emulate ssh's stderr")
	}
	old := execCommandContext
	t.Cleanup(func() { execCommandContext = old })
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		script := "printf '%s\\n' \"Warning: Permanently added '192.0.2.10' (ED25519) to the list of known hosts.\" >&2; " +
			"printf '%s\\n' 'root@192.0.2.10: Permission denied (publickey).' >&2; exit 255"
		return exec.CommandContext(ctx, sh, "-c", script)
	}

	req := Request{Host: "192.0.2.10", User: "root", Port: 22, KeyPath: "/ssh-sync/skygate_sync", KeyFingerprint: b415FixtureFingerprint}
	_, runErr := Run(context.Background(), req, "true")
	if runErr == nil {
		t.Fatal("Run must report ssh's failure, not swallow it")
	}
	msg := runErr.Error()
	if !strings.Contains(msg, "Permission denied") {
		t.Fatalf("the error must carry ssh's OWN cause, got: %s", msg)
	}
	if strings.Contains(msg, "Permanently added") {
		t.Fatalf("the known-hosts banner must not leak into the reason, got: %s", msg)
	}
	if !strings.Contains(msg, b415FixtureFingerprint) {
		t.Fatalf("the error must name the key the panel used (%s), got: %s", b415FixtureFingerprint, msg)
	}
	if !strings.Contains(msg, "authorized_keys") {
		t.Fatalf("the error must say what to check, got: %s", msg)
	}
}

// TestRun_B415_SuccessPathUnchanged keeps the new diagnosis from touching a healthy run.
func TestRun_B415_SuccessPathUnchanged(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		sh = bashPath()
	}
	if sh == "" {
		t.Skip("no POSIX shell available")
	}
	old := execCommandContext
	t.Cleanup(func() { execCommandContext = old })
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, sh, "-c",
			"printf 'SKYGATE_STAGE preflight ok\\n'; printf 'SKYGATE_RESULT ok=1 ip=100.64.0.9\\n'; exit 0")
	}
	req := Request{Host: "192.0.2.10", User: "root", Port: 22, KeyPath: "/k"}
	res, runErr := Run(context.Background(), req, "true")
	if runErr != nil {
		t.Fatalf("a healthy run must not error: %v", runErr)
	}
	if !res.OK || len(res.Stages) != 1 || res.TailscaleIPv4 != "100.64.0.9" {
		t.Fatalf("healthy run parsed wrong: %+v", res)
	}
}
