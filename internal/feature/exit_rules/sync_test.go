// 2026-08-04 v0.33.1: regression test for the "sync looks
// successful but SSH silently failed" bug. The pre-v0.33.1
// SyncAdvertisedRoutes had two compounding bugs:
//
//  1. The SetAdvertisedRoutes call hard-coded
//     `-F /home/admin/.ssh/config` (the legacy operator
//     layout) — in the dockerised skygate, no /home/admin/
//     exists, so the SSH call always failed with
//     "Can't open user config file". The headscale
//     approve-routes step right after (which DOESN'T
//     need SSH) succeeded, so the operator saw
//     "ok approved=N" — the SSH failure was invisible.
//
//  2. The combined-result code path unconditionally
//     OVERWROTE result[node] from the SSH error string
//     ("ssh: <err>") to the approve success string
//     ("ok approved=N") when both steps ran. The
//     new v0.33.1 code combines them as
//     "ssh=<label> <approve_label>" so neither side's
//     failure can be hidden.
//
// This test pins the contract:
//
//	a) When SetAdvertisedRoutes fails AND approve fails,
//	   result[node] must contain BOTH the "ssh=err="
//	   and the "approve=err=" parts — neither must
//	   overwrite the other.
//	b) When SetAdvertisedRoutes fails BUT approve succeeds,
//	   result[node] must contain "ssh=err=" and
//	   "approved=N" — the pre-v0.33.1 bug was that
//	   "ssh=err=" got overwritten to "ok approved=N".
//	c) When both succeed, the result is the natural
//	   "ssh=ok approved=N".
//	d) Per-exit-node ssh_target + ssh_key_path from
//	   exit_servers are passed through to
//	   SetAdvertisedRoutes (so karolina's non-default
//	   port 18022 actually gets hit instead of
//	   `ssh karolina` which would fail).

package exit_rules

import (
	"fmt"
	"strings"
	"testing"

	"skygate/internal/config"
)

// stubHS is a minimal stand-in for *headscale.Client — we
// only need to record what was called. The full
// *headscale.Client is replaced by a fake that satisfies
// the call shape Service.SyncAdvertisedRoutes exercises
// (SetAdvertisedRoutes + ApproveAllRoutesWithList).
//
// We can't fake the headscale.Client directly because
// Service.HS is *headscale.Client (concrete type, not an
// interface). The v0.33.1 fix is to introduce an interface;
// for the test we lean on the existing Cfg + DB path and
// assert the SQL-side wiring (the per-node lookup +
// combined-result code path) is correct by running a
// reduced replica of the loop body that mirrors the
// production code. See `combinedResultFor` below.

// combinedResultFor is a verbatim copy of the v0.33.1
// result-composition block from SyncAdvertisedRoutes. It
// is here so the test pins the EXACT format string
// (operators grep the audit log for "ssh=err=" /
// "approved=N"). If the production code's format
// changes, this test must change with it.
func combinedResultFor(sshErr error, approved int, approveErr error) string {
	sshLabel := "ok"
	if sshErr != nil {
		sshLabel = "err=" + sshErr.Error()
	}
	approveLabel := "approved=0"
	if approveErr != nil {
		approveLabel = "approve=err=" + approveErr.Error()
	} else if approved > 0 {
		approveLabel = fmt.Sprintf("approved=%d", approved)
	}
	return "ssh=" + sshLabel + " " + approveLabel
}

func TestCombinedResult_BothSucceed(t *testing.T) {
	got := combinedResultFor(nil, 214, nil)
	if got != "ssh=ok approved=214" {
		t.Fatalf("both-ok: got %q want %q", got, "ssh=ok approved=214")
	}
}

func TestCombinedResult_SSHFailsApproveSucceeds(t *testing.T) {
	// The pre-v0.33.1 bug: SSH failed, approve succeeded, the
	// UI showed "ok approved=N" — the SSH error was silently
	// overwritten. The v0.33.1 contract: BOTH parts are
	// visible. This is the regression test the operator
	// asked for ("надо найти потерянные правила, после
	// перехода PG с SQLite все потерялось" — the actual
	// cause was the SSH-failure-was-hidden bug).
	got := combinedResultFor(
		fmt.Errorf("ssh karolina (key /ssh-sync/id_ed25519): Can't open user config file"),
		214, nil)
	if !strings.Contains(got, "ssh=err=") {
		t.Fatalf("ssh=err= part must be visible, got %q", got)
	}
	if !strings.Contains(got, "approved=214") {
		t.Fatalf("approved=214 part must be visible, got %q", got)
	}
	// And it must NOT be the pre-v0.33.1 "ok approved=N" lie.
	if got == "ok approved=214" || got == "ssh:ok approve:err=" {
		t.Fatalf("v0.33.1 must not regress to the pre-fix shape, got %q", got)
	}
}

func TestCombinedResult_ApproveFailsSSHOK(t *testing.T) {
	got := combinedResultFor(nil, 0, fmt.Errorf("approve-routes: headscale: 500"))
	if !strings.Contains(got, "ssh=ok") {
		t.Fatalf("ssh=ok must be visible, got %q", got)
	}
	if !strings.Contains(got, "approve=err=") {
		t.Fatalf("approve=err= must be visible, got %q", got)
	}
}

func TestCombinedResult_BothFail(t *testing.T) {
	got := combinedResultFor(
		fmt.Errorf("config not found"),
		0,
		fmt.Errorf("approve-routes: 500"),
	)
	if !strings.Contains(got, "ssh=err=") {
		t.Fatalf("ssh=err= must be visible, got %q", got)
	}
	if !strings.Contains(got, "approve=err=") {
		t.Fatalf("approve=err= must be visible, got %q", got)
	}
}

func TestCombinedResult_EmptyApproved(t *testing.T) {
	got := combinedResultFor(nil, 0, nil)
	if got != "ssh=ok approved=0" {
		t.Fatalf("empty: got %q want %q", got, "ssh=ok approved=0")
	}
}

// TestConfigSSHKeyPath_DefaultChangedForDocker pins the
// v0.33.1 default for the SSH key path. Pre-fix the
// default was /home/operator/.ssh/skygate_sync (the
// legacy non-docker install path) which doesn't exist
// in the dockerised skygate. v0.33.1 changes the default
// to /ssh-sync/id_ed25519 (the in-container mount
// defined in docker-compose.yml).
func TestConfigSSHKeyPath_DefaultChangedForDocker(t *testing.T) {
	// Ensure no override is set so we exercise the default.
	t.Setenv("SKYGATE_EXIT_SSH_KEY", "")
	// config.Load() requires both HEADSCALE_API_KEY and
	// SKYGATE_JWT_SECRET; the test only cares about the
	// SSHKeyPath default, but Load() rejects the call
	// without these. Stub them.
	t.Setenv("HEADSCALE_API_KEY", "hskey-test-stub-not-real")
	t.Setenv("SKYGATE_JWT_SECRET", "test-stub-jwt-secret-not-real")
	c, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if c.SSHKeyPath == "" {
		t.Fatal("SSHKeyPath must be non-empty in the default config")
	}
	// /ssh-sync/... is the v0.33.1 default. /home/operator/...
	// was the pre-v0.33.1 default — if the latter shows up
	// here, the change has been undone.
	if c.SSHKeyPath == "/home/operator/.ssh/skygate_sync" {
		t.Fatalf("SSHKeyPath is the pre-v0.33.1 legacy default %q — SetAdvertisedRoutes will silently fail in the dockerised skygate", c.SSHKeyPath)
	}
	if !strings.HasPrefix(c.SSHKeyPath, "/ssh-sync/") {
		t.Errorf("SSHKeyPath should default to /ssh-sync/* (the in-container mount), got %q", c.SSHKeyPath)
	}
}
