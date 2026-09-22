// preferred_check_b279_test.go — B279 (v1.5.46) — regression guards for
// the class-tag sentinel in the preferred-exit helpers.
//
// Live case (native host `aro`, 2026-09-22): the operator's preference
// stored the CLASS tag `tag:exit-node`, TagToHostname stripped
// "tag:exit-" and returned the hostname "node" — a relay that does not
// exist — and the "Use preferred" button then wrote it into 21 + 3
// device_rules rows. Everything downstream (route advertisement, route
// approval, the ACL `via` pin, the rule status badges) silently worked
// on a name that matched nothing.
package exit_rules

import (
	"testing"

	"skygate/internal/db"
)

// TestTagToHostname_ClassTagIsNotAHostname_B279 pins the sentinel
// behaviour. The empty return is the "any exit-node" signal the callers
// already understand: IsRuleApplicable → true, no mismatch banner, no
// bulk rewrite, route script falls back to the first healthy relay.
func TestTagToHostname_ClassTagIsNotAHostname_B279(t *testing.T) {
	for _, class := range []string{
		"tag:exit-node",
		"tag:public",
		"tag:private",
		"tag:subnet-router",
		"TAG:exit-node",
		"  tag:exit-node  ",
	} {
		if got := TagToHostname(class); got != "" {
			t.Errorf("TagToHostname(%q) = %q, want \"\" (a class tag names a role, not a node — %q was the live phantom)", class, got, got)
		}
	}
	// Per-node forms are unaffected.
	perNode := map[string]string{
		"tag:dev-infra-exit-node-vps": "exit-node-vps",
		"tag:dev-infra-emilia":        "emilia",
		"tag:exit-emilia":             "emilia",
		"exit-node-vps":               "exit-node-vps",
	}
	for in, want := range perNode {
		if got := TagToHostname(in); got != want {
			t.Errorf("TagToHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIsRuleApplicable_ClassTagPrefIsNotAMismatch_B279 — with the class
// tag neutralised to "", a rule must NOT be reported as pointing at the
// wrong relay. Reporting 23 phantom "dead rules" is what pushed the
// operator into clicking the button that broke them.
func TestIsRuleApplicable_ClassTagPrefIsNotAMismatch_B279(t *testing.T) {
	pref := TagToHostname("tag:exit-node") // "" after B279
	if !IsRuleApplicable("exit-node-vps", pref) {
		t.Errorf("IsRuleApplicable(exit-node-vps, TagToHostname(tag:exit-node)=%q) = false, want true (no preference = any relay)", pref)
	}
	// The old (buggy) value would have been a mismatch — the guard is
	// what makes this test meaningful: with the phantom hostname the
	// rule WAS reported as dead, which is what pushed the operator into
	// clicking the button that rewrote it.
	if IsRuleApplicable("exit-node-vps", "node") {
		t.Errorf("sanity: IsRuleApplicable(exit-node-vps, node) should be false (a genuine mismatch)")
	}
}

// TestPlanDevicePrefChange_ClearsClassTagPref_B279 — an existing
// preference that holds a class tag is repaired (dropped) instead of
// being left armed. "Any exit-node" is the absence of a preference.
func TestPlanDevicePrefChange_ClearsClassTagPref_B279(t *testing.T) {
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:               100,
		Username:             "daniil",
		DeviceHostname:       "workpc",
		ExistingPrefTag:      "tag:exit-node",
		ExistingPrefVia:      true,
		DistinctExitNodes:    1,
		DominantExitHostname: "exit-node-vps",
		TotalRules:           23,
		CanonicalTag:         "", // NormalizeExitNodeTag refuses the class tag
	})
	if !ok || ch == nil {
		t.Fatal("PlanDevicePrefChange(class-tag pref) = no change, want a clear change")
	}
	if ch.Action != "clear" {
		t.Errorf("action = %q, want \"clear\"", ch.Action)
	}
	if ch.Reason != "class-tag-pref-cleared" {
		t.Errorf("reason = %q, want class-tag-pref-cleared", ch.Reason)
	}
	if ch.OldTag != "tag:exit-node" {
		t.Errorf("OldTag = %q, want tag:exit-node", ch.OldTag)
	}
}

// TestPlanDevicePrefChange_RepairsClassTagPrefWhenPerNodeTagExists_B279
// — once the relay HAS its own tag, the preference is migrated to it
// rather than dropped, so the operator's intent survives the repair.
func TestPlanDevicePrefChange_RepairsClassTagPrefWhenPerNodeTagExists_B279(t *testing.T) {
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:               100,
		Username:             "daniil",
		DeviceHostname:       "workpc",
		ExistingPrefTag:      "tag:exit-node",
		ExistingPrefVia:      true,
		DistinctExitNodes:    1,
		DominantExitHostname: "exit-node-vps",
		TotalRules:           23,
		CanonicalTag:         "tag:dev-infra-exit-node-vps",
	})
	if !ok || ch == nil {
		t.Fatal("PlanDevicePrefChange = no change, want an update to the per-node tag")
	}
	if ch.Action != "update" {
		t.Errorf("action = %q, want \"update\"", ch.Action)
	}
	if ch.NewTag != "tag:dev-infra-exit-node-vps" {
		t.Errorf("NewTag = %q, want tag:dev-infra-exit-node-vps", ch.NewTag)
	}
}

// TestPlanDevicePrefChange_NeverDerivesPrefFromClassTag_B279 — a
// device with no preference and rules pointing at a class-tagged relay
// must NOT get a preference auto-created from that class tag.
func TestPlanDevicePrefChange_NeverDerivesPrefFromClassTag_B279(t *testing.T) {
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:               100,
		Username:             "daniil",
		DeviceHostname:       "workpc",
		ExistingPrefTag:      "",
		DistinctExitNodes:    1,
		DominantExitHostname: "exit-node-vps",
		TotalRules:           23,
		CanonicalTag:         "tag:exit-node", // defensive: the guard must hold even if a caller passes it
	})
	if ok || ch != nil {
		t.Errorf("PlanDevicePrefChange derived a preference from a class tag: %+v", ch)
	}
}

// TestTagToHostname_MatchesSharedPredicate_B279 — the exit_rules helper
// and db.IsClassTag must not drift apart again: that divergence is
// exactly how "node" was born (internal/acl had the guard, this package
// did not).
func TestTagToHostname_MatchesSharedPredicate_B279(t *testing.T) {
	for _, tag := range []string{
		"tag:exit-node", "tag:public", "tag:private", "tag:subnet-router",
		"tag:dev-infra-emilia", "tag:dev-daniil-workpc", "tag:exit-emilia", "",
	} {
		if db.IsClassTag(tag) && TagToHostname(tag) != "" {
			t.Errorf("db.IsClassTag(%q) = true but TagToHostname(%q) = %q — the two copies disagree", tag, tag, TagToHostname(tag))
		}
	}
}
