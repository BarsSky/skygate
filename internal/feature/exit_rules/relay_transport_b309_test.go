// internal/feature/exit_rules/relay_transport_b309_test.go — B309 (v1.5.74).
//
// The live case these tests pin: `karolina` was online in headscale (so the
// exit-node health table called it healthy) but every route application to it
// died on `ssh … Operation timed out`. The prefix assignment therefore kept
// giving it 75 prefixes nobody could advertise, and the operator's table showed
// "нет маршрута" forever. B309 records the transport result per relay and takes
// it into account when choosing owners.
package exit_rules

import (
	"strings"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
	"skygate/internal/prefixowner"
)

func TestParseRelayApplyState_B309(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		wantAt int64
		wantOK bool
		want   string
	}{
		{"empty is never-recorded", "", 0, false, ""},
		{"success", "1700000000|ok", 1700000000, true, ""},
		{"failure keeps the reason", "1700000000|err|ssh … timed out", 1700000000, false, "ssh … timed out"},
		{"failure without reason is named", "1700000000|err|", 1700000000, false, "route application failed (no detail recorded)"},
		// A storage hiccup must never look like a failure: unparseable ⇒ healthy.
		{"garbage is never-recorded", "not-a-timestamp|err|boom", 0, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ParseRelayApplyState(tc.raw)
			if st.At != tc.wantAt || st.OK != tc.wantOK {
				t.Fatalf("ParseRelayApplyState(%q) = {At:%d OK:%v}, want {At:%d OK:%v}",
					tc.raw, st.At, st.OK, tc.wantAt, tc.wantOK)
			}
			if st.Detail != tc.want {
				t.Fatalf("ParseRelayApplyState(%q).Detail = %q, want %q", tc.raw, st.Detail, tc.want)
			}
		})
	}
}

func TestRelayApplyFailedFromOutcome_B309(t *testing.T) {
	cases := []struct {
		name    string
		out     relayApplyOutcome
		failed  bool
		wantSub string
	}{
		{"ssh ok is a success", relayApplyOutcome{Label: "ssh=ok"}, false, ""},
		{"local ok is a success", relayApplyOutcome{Label: "local=ok via helper"}, false, ""},
		{"ssh error is a failure", relayApplyOutcome{Label: "ssh=err=ssh: connect to host 100.64.0.2 port 18022: Operation timed out"}, true, "Operation timed out"},
		{"local error is a failure", relayApplyOutcome{Label: "local=err=no privilege ladder"}, true, "no privilege ladder"},
		// The transport may succeed while approval fails: the prefix still is not
		// served, so it must not keep an owner either.
		{"approval error is a failure", relayApplyOutcome{Label: "ssh=ok", ApproveErr: errStubB309("route not permitted")}, true, "route not permitted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failed, detail := relayApplyFailedFromOutcome(tc.out)
			if failed != tc.failed {
				t.Fatalf("failed = %v, want %v (label %q)", failed, tc.failed, tc.out.Label)
			}
			if tc.wantSub != "" && !containsSub(detail, tc.wantSub) {
				t.Fatalf("detail = %q, want it to mention %q", detail, tc.wantSub)
			}
		})
	}
}

type errStubB309 string

func (e errStubB309) Error() string { return string(e) }

func containsSub(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// TestB309_UnreachableRelayLosesPrefixOwnership is the live regression: an
// unreachable relay must stop owning prefixes, and the health table must stay
// untouched (a relay can be a healthy tailnet node and still be unconfigurable).
func TestB309_UnreachableRelayLosesPrefixOwnership(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)

	const prefix = "104.16.0.0/12"

	// Baseline: both relays answer, the claiming relay owns its prefix.
	if _, _, err := prefixowner.Reconcile(d, healthyExitRelaysForAssignment(d)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if owner := prefixowner.OwnerByPrefix(d)[prefix]; owner != "karolina" {
		t.Fatalf("baseline owner = %q, want karolina (the rule claims it and it is healthy)", owner)
	}

	// The live failure: SSH to karolina times out.
	recordRelayApply(d, "karolina", relayApplyOutcome{
		Label: "ssh=err=ssh: connect to host 100.64.0.2 port 18022: Operation timed out",
	})

	healthy := healthyExitRelaysForAssignment(d)
	for _, h := range healthy {
		if h == "karolina" {
			t.Fatalf("karolina is still in the assignment health set after a failed apply: %v", healthy)
		}
	}
	if len(healthy) == 0 {
		t.Fatalf("the assignment health set is empty — emilia is reachable and must stay")
	}

	if _, _, err := prefixowner.Reconcile(d, healthy); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if owner := prefixowner.OwnerByPrefix(d)[prefix]; owner == "karolina" {
		t.Fatalf("owner is still karolina after its transport failed — the prefixes stay unadvertised")
	}

	// The exit-node health table must NOT have been rewritten: B309 is about the
	// transport, and B273's ladder stays the single answer to "is this relay up?".
	rows, err := skygatedb.ListExitNodeHealth(d)
	if err != nil {
		t.Fatalf("ListExitNodeHealth: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Hostname == "karolina" {
			seen["karolina"] = true
			if !r.Healthy {
				t.Fatalf("exit_node_health says karolina is unhealthy — B309 must not touch that table")
			}
		}
	}
	if !seen["karolina"] {
		t.Fatalf("karolina disappeared from exit_node_health")
	}
}

// TestB309_SuccessClearsTheFailure pins that the demotion is not permanent: one
// successful apply puts the relay back, with nothing to un-block by hand.
func TestB309_SuccessClearsTheFailure(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)

	const prefix = "104.16.0.0/12"

	recordRelayApply(d, "karolina", relayApplyOutcome{Label: "ssh=err=Operation timed out"})
	if !containsString(healthyExitRelaysForAssignment(d), "emilia") {
		t.Fatalf("emilia should be assignable while karolina is unreachable")
	}
	if containsString(healthyExitRelaysForAssignment(d), "karolina") {
		t.Fatalf("karolina should be excluded while unreachable")
	}

	recordRelayApply(d, "karolina", relayApplyOutcome{Label: "ssh=ok", Approved: 114})
	if !containsString(healthyExitRelaysForAssignment(d), "karolina") {
		t.Fatalf("a successful apply must clear the failure record immediately")
	}

	if _, _, err := prefixowner.Reconcile(d, healthyExitRelaysForAssignment(d)); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if owner := prefixowner.OwnerByPrefix(d)[prefix]; owner != "karolina" {
		t.Fatalf("owner = %q after karolina recovered, want karolina (its rule claims the prefix)", owner)
	}
}

// TestB309_OldFailureDoesNotDemoteForever pins the window: a failure older than
// RelayApplyFailureWindow stops counting, so a relay that was never applied to
// again (no rules) is not excluded on the strength of an ancient error.
func TestB309_OldFailureDoesNotDemoteForever(t *testing.T) {
	d := newB276DB(t)
	seedB276(t, d)

	old := time.Now().Add(-2 * RelayApplyFailureWindow).Unix()
	if err := skygatedb.SetGlobalSetting(d, RelayApplyStateKey("Karolina"),
		formatRelayApplyState(RelayApplyState{At: old, Detail: "Operation timed out"})); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The key is case-insensitive: the relay names come from device_rules in
	// whatever case the operator typed, and a mixed-case name must still match.
	if !containsString(healthyExitRelaysForAssignment(d), "karolina") {
		t.Fatalf("a failure older than the window must not keep the relay out of the healthy set")
	}

	// And a fresh failure with the SAME mixed-case key does demote it.
	if err := skygatedb.SetGlobalSetting(d, RelayApplyStateKey("Karolina"),
		formatRelayApplyState(RelayApplyState{At: time.Now().Unix(), Detail: "Operation timed out"})); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if containsString(healthyExitRelaysForAssignment(d), "karolina") {
		t.Fatalf("a fresh failure must demote the relay regardless of name case")
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
