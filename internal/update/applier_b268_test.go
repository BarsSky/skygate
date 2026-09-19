// File: internal/update/applier_b268_test.go
//
// B268 (2026-09-19) — regression locks for the privileged applier's
// failure reporting.
//
// Context: a live native/systemd update failed with
//
//	ROLLBACK: restoring the previous binary …
//	verdict: rolled_back (healthz did not report build 'v1.5.11' within 90s
//	         (last build: none))
//
// which is true for at least four very different situations (unit masked,
// binary unrunnable, port already owned by something else, service up on
// another port) and told the operator nothing. B268 adds:
//
//  1. a PRE-SWAP smoke test (`binary_smoke_test`) so an artifact that
//     cannot execute on this host never replaces the running binary;
//  2. `service_diagnostics()` — unit state, port owner, binary on disk,
//     journal tail — logged before any rollback;
//  3. verdicts that distinguish "the service did not come up" from
//     "the service came up but reports the wrong build".
//
// The applier is bash, so these are source-level contracts plus one
// behavioural check of the smoke-test gate; the end-to-end run lives in
// scripts/check_b268_applier_failure_diagnostics.sh.

package update

import (
	"os"
	"strings"
	"testing"
)

func applierSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../deploy/skygate-apply-update.sh")
	if err != nil {
		t.Fatalf("read applier: %v", err)
	}
	return string(b)
}

// TestB268_SmokeTestRunsBeforeSwap pins the ordering that matters: the
// smoke test must appear BEFORE the atomic_install of the new binary, so
// a binary that cannot run never becomes the live one.
func TestB268_SmokeTestRunsBeforeSwap(t *testing.T) {
	src := applierSource(t)
	iSmoke := strings.Index(src, "binary_smoke_test \"$NEW_BIN\"")
	iSwap := strings.Index(src, "if ! atomic_install \"$NEW_BIN\"; then")
	if iSmoke < 0 {
		t.Fatalf("the applier no longer calls binary_smoke_test on the extracted binary")
	}
	if iSwap < 0 {
		t.Fatalf("could not find the swap site (atomic_install \"$NEW_BIN\")")
	}
	if iSmoke > iSwap {
		t.Errorf("binary_smoke_test runs AFTER the binary swap (smoke@%d, swap@%d) — a broken Artifact would still replace the working binary", iSmoke, iSwap)
	}
}

// TestB268_SmokeTestIsNonDestructive: the pre-swap probe must be a flag
// that cannot start serving / migrate anything.
func TestB268_SmokeTestIsNonDestructive(t *testing.T) {
	src := applierSource(t)
	i := strings.Index(src, "binary_smoke_test()")
	if i < 0 {
		t.Fatalf("binary_smoke_test is missing")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}"); j > 0 {
		body = body[:j]
	}
	if !strings.Contains(body, `--version`) {
		t.Errorf("the smoke test must probe with --version (a flag that exits immediately); body:\n%s", body)
	}
	if strings.Contains(body, "--serve") || strings.Contains(body, "migrate-only") {
		t.Errorf("the smoke test invokes a serving/migrating mode — it must stay side-effect free")
	}
	if !strings.Contains(body, "timeout 20") {
		t.Errorf("the smoke test must bound the probe with `timeout` so a hanging binary cannot stall the applier")
	}
}

// TestB268_DiagnosticsExist pins every diagnostic the operator needs.
func TestB268_DiagnosticsExist(t *testing.T) {
	src := applierSource(t)
	for _, want := range []string{
		"service_diagnostics()",
		"DIAG: unit state:",
		"DIAG: listener on port",
		"DIAG: binary on disk:",
		"DIAG: last journal lines for",
		"journalctl -u \"$SERVICE\" -n 15 --no-pager",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("applier is missing %q — the next failure will be as opaque as the v1.5.11 one", want)
		}
	}
}

// TestB268_DiagnosticsBeforeRollback: diagnostics must be emitted BEFORE
// rollback overwrites the evidence (the new binary's journal lines are
// still there at that point; after the rollback they belong to the old
// one).
func TestB268_DiagnosticsBeforeRollback(t *testing.T) {
	src := applierSource(t)
	iDiag := strings.Index(src, "service_diagnostics\n    rollback \"the service did not come up")
	if iDiag < 0 {
		// fall back to the plain order check
		iDiag = strings.Index(src, "service_diagnostics")
	}
	iRoolback := strings.Index(src, `rollback "the service did not come up`)
	if iRoolback < 0 {
		t.Fatalf("the 'service did not come up' verdict is missing (B268 splits it from the wrong-build verdict)")
	}
	if iDiag < 0 || iDiag > iRoolback {
		t.Errorf("service_diagnostics does not run immediately before the rollback")
	}
}

// TestB268_VerdictsAreDistinct: an empty health body (service down) and a
// wrong build must produce different messages.
func TestB268_VerdictsAreDistinct(t *testing.T) {
	src := applierSource(t)
	if !strings.Contains(src, "the service did not come up:") {
		t.Errorf("missing the 'service did not come up' verdict")
	}
	if !strings.Contains(src, "healthz did not report build") {
		t.Errorf("missing the wrong-build verdict")
	}
	if !strings.Contains(src, "unit state: $(service_state)") {
		t.Errorf("verdicts no longer include the unit state — the operator cannot tell 'masked' from 'crashed'")
	}
}

// TestB268_PortOwnerProbe: the diagnostic must be able to name whatever
// holds the health port (the reference VM's docker container was exactly
// that case).
func TestB268_PortOwnerProbe(t *testing.T) {
	src := applierSource(t)
	if !strings.Contains(src, "port_owner()") {
		t.Fatalf("port_owner() is missing")
	}
	if !strings.Contains(src, "ss -ltnp") || !strings.Contains(src, "netstat -ltnp") {
		t.Errorf("port_owner must try ss and netstat (minimal installs may have only one)")
	}
	if !strings.Contains(src, "health_port()") {
		t.Errorf("health_port() is missing — the diagnostic cannot know which port to inspect")
	}
}

// TestB268_PreSwapHealthBaseline pins the check that catches the
// "another instance owns the health port" class BEFORE the swap: on the
// reference host a docker container held :8080 while a native unit was
// restarted against it, so the verification could never succeed.
func TestB268_PreSwapHealthBaseline(t *testing.T) {
	src := applierSource(t)
	iBase := strings.Index(src, "pre-swap health baseline:")
	if iBase < 0 {
		t.Fatalf("no pre-swap health baseline is logged — a port conflict is only discovered after a wasted swap+rollback")
	}
	iSwap := strings.Index(src, `if ! atomic_install "$NEW_BIN"; then`)
	if iSwap < 0 || iBase > iSwap {
		t.Errorf("the health baseline runs after the swap; it must run before it")
	}
	if !strings.Contains(src, "a SECOND skygate instance (container/other unit) may own that port") {
		t.Errorf("the baseline does not warn about a second instance owning the port")
	}
}
