package handlers

// handlers_admin_update_test.go — regression tests for the
// v0.29.3 auto-update admin page.
//
// The most expensive bug we want to guard against is the
// `{{.State.Status}}` format-string regression in
// startStuckSkygateContainer (v0.29.3.1). The old format is
// valid only for `docker inspect`; on `docker ps --format` it
// fails with "can't evaluate field Status in type string" and
// the helper silently no-ops, leaving a freshly-swapped
// container in `created` state with no recovery path.

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestStartStuckSkygateFormatStringIsDockerPsValid runs the
// exact `docker ps -a --filter ... --format <fmt>` invocation
// that startStuckSkygateContainer uses, and asserts the
// command exits 0. Skips on Windows (the function only runs
// inside the Linux skygate container) and when docker is
// unavailable (CI runners that don't have docker).
//
// On a clean machine with no skygate container, the output is
// empty but the command still exits 0 — the format string
// itself is what we're testing.
func TestStartStuckSkygateFormatStringIsDockerPsValid(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("startStuckSkygateContainer only runs inside the Linux skygate container")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available in test environment")
	}

	// The fix in v0.29.3.1: `{{.Status}}`, not `{{.State.Status}}`.
	const goodFormat = "{{.ID}} {{.Status}}"
	const badFormat = "{{.ID}} {{.State.Status}}"

	out, err := exec.Command("docker", "ps", "-a",
		"--filter", "label=com.docker.compose.service=skygate",
		"--filter", "label=com.docker.compose.project=skygate",
		"--format", goodFormat).CombinedOutput()
	if err != nil {
		t.Fatalf("goodFormat %q failed: %v\noutput: %s", goodFormat, err, out)
	}

	out, err = exec.Command("docker", "ps", "-a",
		"--filter", "label=com.docker.compose.service=skygate",
		"--filter", "label=com.docker.compose.project=skygate",
		"--format", badFormat).CombinedOutput()
	if err == nil {
		t.Fatalf("badFormat %q should have failed (regression of the v0.29.3.1 fix) but exited 0", badFormat)
	}
	combined := string(out)
	if !strings.Contains(combined, "Status") {
		t.Fatalf("badFormat %q failed for an unexpected reason: %s", badFormat, combined)
	}
}
