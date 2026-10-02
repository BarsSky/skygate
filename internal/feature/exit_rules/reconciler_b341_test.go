// internal/feature/exit_rules/reconciler_b341_test.go — B341 (2026-10-01).
//
// The hole this pins: a device whose rules name NO relay was invisible to the
// preferred-exit reconciler — ReconcileDeviceExitNodePrefs filtered it out of the
// pair list (`AND exit_node_id <> ”`), and a device that reached the planner fell
// into a silent `return nil, false`. Live case: `cyborg` had 11 youtube rules with
// an empty relay, the ACL pinned those prefixes to emilia anyway (B275 takes the
// pin from prefix_owner), and the device still had no exit node, so the rule
// existed and did nothing — while `skyworker`, whose rules do name a relay,
// worked.
//
// The planner is a pure function, so these tests need no database.
package exit_rules

import "testing"

// The live shape of `cyborg`: 11 enabled rules (10 resolved youtube CIDRs + the
// domain row), none of them naming a relay, no preference row, and the assignment
// table giving one owner (emilia) for those prefixes.
func b341Cyborg() DevicePrefState {
	return DevicePrefState{
		UserID:            1,
		Username:          "skyadmin",
		DeviceHostname:    "cyborg",
		ExistingPrefTag:   "",
		TotalRules:        11,
		DistinctExitNodes: 0,
		OwnerDistinct:     1,
		OwnerCanonicalTag: "tag:dev-infra-emilia",
	}
}

// TestPlan_B341_OwnerDerivedPreference: one owner, tagged → CREATE that owner.
// This is the device half of the decision the ACL already made, so it is a
// derivation and not a guess.
func TestPlan_B341_OwnerDerivedPreference(t *testing.T) {
	ch, ok := PlanDevicePrefChange(b341Cyborg())
	if !ok || ch == nil {
		t.Fatalf("expected a change (the device has rules, no pref and one owner), got ok=%v ch=%v", ok, ch)
	}
	if ch.Action != "create" {
		t.Errorf("action = %q, want create", ch.Action)
	}
	if ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("NewTag = %q, want tag:dev-infra-emilia", ch.NewTag)
	}
	if ch.Reason != "missing-pref-owner-derived" {
		t.Errorf("reason = %q, want missing-pref-owner-derived (the audit trail must say where the value came from)", ch.Reason)
	}
	if ch.RuleCount != 11 {
		t.Errorf("RuleCount = %d, want 11", ch.RuleCount)
	}
}

// TestPlan_B341_NoRelayAndNoOwner_SkipsVisibly: nothing to derive from, but the
// operator must SEE it — this is the population that used to be silent.
func TestPlan_B341_NoRelayAndNoOwner_SkipsVisibly(t *testing.T) {
	s := b341Cyborg()
	s.OwnerDistinct = 0
	s.OwnerCanonicalTag = ""
	ch, ok := PlanDevicePrefChange(s)
	if !ok || ch == nil {
		t.Fatal("a device with rules and no derivable relay must produce a VISIBLE skip, not a silent no-op")
	}
	if ch.Action != "skip" || ch.Reason != "missing-pref-no-relay" {
		t.Errorf("got action=%q reason=%q, want skip/missing-pref-no-relay", ch.Action, ch.Reason)
	}
}

// TestPlan_B341_OwnerSplit_Skips: the device's prefixes are served by different
// relays → no single exit node to pin it to; the operator picks.
func TestPlan_B341_OwnerSplit_Skips(t *testing.T) {
	s := b341Cyborg()
	s.OwnerDistinct = 2
	s.OwnerCanonicalTag = ""
	ch, ok := PlanDevicePrefChange(s)
	if !ok || ch == nil {
		t.Fatal("split owners must produce a visible skip")
	}
	if ch.Action != "skip" || ch.Reason != "missing-pref-owner-split" {
		t.Errorf("got action=%q reason=%q, want skip/missing-pref-owner-split", ch.Action, ch.Reason)
	}
}

// TestPlan_B341_OwnerUntagged_Skips: one owner but its node carries no per-node
// tag yet. Deriving a tag from a class tag here is exactly the B279 defect, so
// this must skip rather than invent a value.
func TestPlan_B341_OwnerUntagged_Skips(t *testing.T) {
	s := b341Cyborg()
	s.OwnerCanonicalTag = ""
	ch, ok := PlanDevicePrefChange(s)
	if !ok || ch == nil {
		t.Fatal("an untagged owner must produce a visible skip")
	}
	if ch.Action != "skip" || ch.Reason != "missing-pref-owner-untagged" {
		t.Errorf("got action=%q reason=%q, want skip/missing-pref-owner-untagged", ch.Action, ch.Reason)
	}
}

// TestPlan_B341_ExistingPreferenceIsNeverOverwritten: the new path lives strictly
// inside the "no existing preference" branch. A device that already has one must
// behave EXACTLY as before, even when the assignment table disagrees — otherwise
// this change would silently re-route devices that work today.
func TestPlan_B341_ExistingPreferenceIsNeverOverwritten(t *testing.T) {
	s := b341Cyborg()
	s.ExistingPrefTag = "tag:dev-infra-karolina"
	s.ExistingPrefVia = true
	ch, ok := PlanDevicePrefChange(s)
	if ok && ch != nil {
		t.Fatalf("an existing, non-class preference must not be touched by the B341 path; got action=%q new=%q reason=%q",
			ch.Action, ch.NewTag, ch.Reason)
	}
}

// TestPlan_B341_RelayNamedButUntagged_IsAVisibleSkip: the old silent
// `return nil, false` also hid "this device's relay has no per-node tag yet",
// which is actionable (the B77 autoupdater has not run, or the relay was just
// added).
func TestPlan_B341_RelayNamedButUntagged_IsAVisibleSkip(t *testing.T) {
	s := DevicePrefState{
		UserID:               6,
		Username:             "michail",
		DeviceHostname:       "basic",
		TotalRules:           5,
		DistinctExitNodes:    1,
		DominantExitHostname: "emilia",
		CanonicalTag:         "", // relay exists, no per-node tag resolvable
	}
	ch, ok := PlanDevicePrefChange(s)
	if !ok || ch == nil {
		t.Fatal("a named but untagged relay must produce a visible skip")
	}
	if ch.Action != "skip" || ch.Reason != "missing-pref-relay-untagged" {
		t.Errorf("got action=%q reason=%q, want skip/missing-pref-relay-untagged", ch.Action, ch.Reason)
	}
}

// TestPlan_B341_UnanimousRelayStillCreates: the pre-existing behaviour is
// unchanged for the devices that work today — the new branch must not intercept
// the "rules name a relay" population.
func TestPlan_B341_UnanimousRelayStillCreates(t *testing.T) {
	s := DevicePrefState{
		UserID:               6,
		Username:             "michail",
		DeviceHostname:       "basic",
		TotalRules:           41,
		DistinctExitNodes:    1,
		DominantExitHostname: "emilia",
		CanonicalTag:         "tag:dev-infra-emilia",
	}
	ch, ok := PlanDevicePrefChange(s)
	if !ok || ch == nil {
		t.Fatal("the unanimous-relay case must still create")
	}
	if ch.Action != "create" || ch.Reason != "missing-pref-unanimous" || ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("got action=%q reason=%q new=%q, want create/missing-pref-unanimous/tag:dev-infra-emilia",
			ch.Action, ch.Reason, ch.NewTag)
	}
}
