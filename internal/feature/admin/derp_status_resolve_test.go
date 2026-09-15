// File: internal/feature/admin/derp_status_resolve_test.go
//
// 2026-09-15: v1.5.3 — B-bug-fix (hardcoded DERP port masked broken
// derper). Unit tests for resolveDERPPort / resolveSTUNPort /
// bundledDERPPortFromDB / shouldBelongToInfra.

package admin

import (
	"os"
	"testing"
)

func TestResolveDERPPort_EnvPriority(t *testing.T) {
	t.Setenv("DERP_HTTP_PORT", "8443")
	t.Setenv("DERP_STUN_PORT", "3479")
	// nil DB → falls back to env, then to "443" default
	if got := resolveDERPPort(nil); got != "8443" {
		t.Errorf("resolveDERPPort: env should win, got %q", got)
	}
	if got := resolveSTUNPort(nil); got != "3479" {
		t.Errorf("resolveSTUNPort: env should win, got %q", got)
	}
}

func TestResolveDERPPort_EmptyEnvFallsBackToDefault(t *testing.T) {
	// Clear the env so neither resolveDERPPort nor resolveSTUNPort
	// can rely on it.
	os.Unsetenv("DERP_HTTP_PORT")
	os.Unsetenv("DERP_STUN_PORT")
	if got := resolveDERPPort(nil); got != "" {
		t.Errorf("resolveDERPPort: no env, no DB → empty (caller treats as '443'), got %q", got)
	}
	if got := resolveSTUNPort(nil); got != "" {
		t.Errorf("resolveSTUNPort: no env → empty, got %q", got)
	}
}

// splitHostPort / bundledDERPPortFromDB tests would need a real *sql.DB
// or sqlite in-memory; we keep this file focused on the env-only
// resolveDERPPort / resolveSTUNPort paths and the pure-function
// shouldBelongToInfra rule engine. The DB-backed paths are exercised
// by scripts/check_derp_relays_auto.sh (live e2e against the operator's
// VM).

// TestShouldBelongToInfra_B251_Negative covers the B251 reserved-name
// change: pre-B251 shouldBelongToInfra also matched suffixed forms
// like `skygate-host-1`, `skygate-host-1-1`, `skygate-host-test`
// (any string with the `skygate-host-` prefix). B251 narrows the
// candidate detection to strict equality on `skygate-host`. The
// suffixed forms must NOT trigger infra-ownership violations
// anymore — they're treated as ordinary non-infra nodes.
func TestShouldBelongToInfra_B251_Negative(t *testing.T) {
	for _, hostname := range []string{
		"skygate-host-1",     // legacy v0.33.1.9 placeholder
		"skygate-host-1-1",   // post-B227.1 rename
		"skygate-host-test",  // hypothetical typo
		"skygate-host-foo",   // hypothetical operator typo
		"skyhosts-host",      // contains `skygate-host` but doesn't start with it
		"some-other-host",
	} {
		violations := shouldBelongToInfra(
			hostname,
			"tagged-devices",
			[]string{"tag:dev-skyadmin-skygate-host-1"},
		)
		if len(violations) != 0 {
			t.Errorf("hostname=%q should NOT trigger infra-ownership violations under B251 strict equality, got %v", hostname, violations)
		}
	}
}

func TestShouldBelongToInfra_SkygateHostOnGuest(t *testing.T) {
	// Live case 2026-09-15: skygate-host with wrong user + wrong tag.
	// B251: hostname match is strict equality (`hostname == "skygate-host"`),
	// not prefix match. The pre-B251 form `skygate-host-1-1` no longer
	// matches the infra-candidate check at all (a different B251
	// regression test below covers the negative path).
	violations := shouldBelongToInfra(
		"skygate-host",
		"tagged-devices", // currentUser != "infra"
		[]string{"tag:dev-skyadmin-skygate-host-1", "tag:private"}, // no tag:dev-infra-*
	)
	if len(violations) != 2 {
		t.Fatalf("expected 2 violations (user + tag), got %d: %v", len(violations), violations)
	}
	wantSet := map[string]bool{"should_be_infra_user": false, "should_have_tag_dev_infra": false}
	for _, v := range violations {
		if _, ok := wantSet[v]; !ok {
			t.Errorf("unexpected violation: %q", v)
		}
		wantSet[v] = true
	}
	for k, seen := range wantSet {
		if !seen {
			t.Errorf("missing violation: %q", k)
		}
	}
}

func TestShouldBelongToInfra_SkygateHostOnInfra(t *testing.T) {
	violations := shouldBelongToInfra(
		"skygate-host-1-1",
		"infra",
		[]string{"tag:dev-infra-skygate-host-1", "tag:private"},
	)
	if len(violations) != 0 {
		t.Errorf("expected 0 violations, got %d: %v", len(violations), violations)
	}
}

func TestShouldBelongToInfra_ExitNodeOnGuest(t *testing.T) {
	// Live case 2026-09-15: emilia/karolina/sharlotta have
	// tag:dev-infra-* but wrong user.
	violations := shouldBelongToInfra(
		"emilia",
		"tagged-devices",
		[]string{"tag:dev-infra-emilia", "tag:exit-node", "tag:private"},
	)
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation (user only — has correct tag), got %d: %v", len(violations), violations)
	}
	if violations[0] != "should_be_infra_user" {
		t.Errorf("unexpected violation: %q", violations[0])
	}
}

func TestShouldBelongToInfra_NonInfraNode(t *testing.T) {
	// Regular user device → no candidate, no violations.
	violations := shouldBelongToInfra(
		"karolinas-macbook",
		"michail",
		[]string{"tag:dev-michail-karolinas-macbook"},
	)
	if len(violations) != 0 {
		t.Errorf("expected 0 violations for non-infra candidate, got %d: %v", len(violations), violations)
	}
}

func TestShouldBelongToInfra_ExitNodeTagButUserNotInfra(t *testing.T) {
	// Edge case: tag:exit-node alone (no dev-infra tag) should still
	// flag should_be_infra_user.
	violations := shouldBelongToInfra(
		"some-exit",
		"skyadmin",
		[]string{"tag:exit-node"},
	)
	if len(violations) != 1 || violations[0] != "should_be_infra_user" {
		t.Errorf("expected [should_be_infra_user], got %v", violations)
	}
}