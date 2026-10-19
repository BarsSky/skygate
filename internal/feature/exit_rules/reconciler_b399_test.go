// reconciler_b399_test.go — 2026-10-19 (B399): the operator's "no" must survive the
// next tick, and re-pointing a device must not silently restore a pin.
//
// WHY THESE TESTS EXIST (measured, not hypothetical)
//
// Operator, verbatim: «теперь несмотря на то что для skyworker я отключаю strict mode
// по karolina он ставиться обратно». The live audit log for those six hours:
//
//	19:08:56  skyadmin  my_device_preferred_exit_set   hostname=skyworker tag=
//	18:17:12  system    preferred_exit_reconciled      CREATE pref hostname=skyworker
//	          tag=tag:dev-infra-karolina via=1 reason=missing-pref-unanimous rules=269
//
// The clear DELETED the row, so `PlanDevicePrefChange` took the "no existing pref"
// branch and created one with via_enabled = 1. These tests pin the decision at the
// pure-function boundary, where the bug lived.
package exit_rules

import "testing"

// TestB399_OptedOutIsNeverRecreated is the core contract: a device whose preference
// the operator removed BY HAND is a decision, and the planner must SKIP it with a
// named reason instead of deriving a fresh row.
func TestB399_OptedOutIsNeverRecreated(t *testing.T) {
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:               1,
		Username:             "skyadmin",
		DeviceHostname:       "skyworker",
		ExistingPrefTag:      "", // the operator's clear leaves no tag…
		ExistingPrefOptedOut: true,
		// …and every fact below would create a preference if the flag were ignored
		// (269 rules that unanimously name karolina — the live shape).
		TotalRules:        269,
		DistinctExitNodes: 1,
		CanonicalTag:      "tag:dev-infra-karolina",
		OwnerDistinct:     1,
		OwnerCanonicalTag: "tag:dev-infra-karolina",
	})
	if !ok {
		t.Fatal("PlanDevicePrefChange(opted-out device) = no change; want a VISIBLE skip, not silence")
	}
	if ch.Action == "create" || ch.Action == "update" {
		t.Fatalf("action = %q — the reconciler re-created a preference the operator removed (the exact live loop)", ch.Action)
	}
	if ch.Reason != "operator-opted-out" {
		t.Errorf("reason = %q, want operator-opted-out", ch.Reason)
	}
	if !ch.OptedOut {
		t.Error("OptedOut = false — the caller cannot tell why nothing happened")
	}
}

// TestB399_OptedOutBeatsEveryDerivationPath walks the three ways the planner can
// create a preference and asserts the flag wins in all of them: a unanimous tag, an
// owner-derived tag, and an owner that overrides the rule's relay.
func TestB399_OptedOutBeatsEveryDerivationPath(t *testing.T) {
	cases := []struct {
		name  string
		state DevicePrefState
	}{
		{"unanimous", DevicePrefState{TotalRules: 5, DistinctExitNodes: 1, CanonicalTag: "tag:dev-infra-karolina"}},
		{"owner-derived", DevicePrefState{
			TotalRules: 5, DistinctExitNodes: 0, CanonicalTag: "",
			OwnerDistinct: 1, OwnerCanonicalTag: "tag:dev-infra-emilia",
		}},
		{"owner-overrides-rule", DevicePrefState{
			TotalRules: 5, DistinctExitNodes: 1, CanonicalTag: "tag:dev-infra-karolina",
			OwnerDistinct: 1, OwnerCanonicalTag: "tag:dev-infra-emilia",
		}},
	}
	for _, c := range cases {
		c.state.UserID, c.state.Username, c.state.DeviceHostname = 1, "skyadmin", "skyworker"
		c.state.ExistingPrefOptedOut = true
		t.Run(c.name, func(t *testing.T) {
			ch, ok := PlanDevicePrefChange(c.state)
			if !ok || ch.Action != "skip" || ch.Reason != "operator-opted-out" {
				got := "<no change>"
				if ok {
					got = ch.Action + "/" + ch.Reason
				}
				t.Fatalf("plan = %s, want skip/operator-opted-out — the %s path re-created the row", got, c.name)
			}
		})
	}
}

// TestB399_DerivedRowIsStillManaged — the flag must not freeze the engine's own rows:
// a device nobody has touched keeps being derived and re-pointed, which is what the
// reconciler exists for.
func TestB399_DerivedRowIsStillManaged(t *testing.T) {
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID: 1, Username: "skyadmin", DeviceHostname: "basic",
		TotalRules: 11, DistinctExitNodes: 1, CanonicalTag: "tag:dev-infra-emilia",
	})
	if !ok || ch.Action != "create" {
		t.Fatalf("plan = %+v, want a create — a derived device must still be managed", ch)
	}
	if !ch.ViaEnabled {
		t.Error("a CREATE must default to via_enabled=true (the pre-B399 behaviour)")
	}
}

// TestB399_UpdatePreservesTheStrictFlag is the second half of the report: re-pointing
// a device used to write via_enabled=true unconditionally, so a strict pin somebody
// switched off came back on the next time the relay moved.
func TestB399_UpdatePreservesTheStrictFlag(t *testing.T) {
	for _, via := range []bool{false, true} {
		ch, ok := PlanDevicePrefChange(DevicePrefState{
			UserID: 1, Username: "skyadmin", DeviceHostname: "skyworker",
			ExistingPrefTag: "tag:dev-infra-emilia",
			ExistingPrefVia: via, // the operator's own switch
			TotalRules:      9, DistinctExitNodes: 1, CanonicalTag: "tag:dev-infra-karolina",
		})
		if !ok || ch.Action != "update" {
			t.Fatalf("plan = %+v, want an update (the tag changed)", ch)
		}
		if ch.ViaEnabled != via {
			t.Errorf("via_enabled out = %t, want %t — an UPDATE must carry the existing flag, not re-enable it", ch.ViaEnabled, via)
		}
	}
}

// TestB399_HumanRowKeepsItsOwnRelay — B356's guard must survive: an opted-out flag is
// not a licence to rewrite a HUMAN preference either.
func TestB399_HumanRowKeepsItsOwnRelay(t *testing.T) {
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID: 1, Username: "skyadmin", DeviceHostname: "a71",
		ExistingPrefTag:         "tag:dev-infra-relay-1",
		ExistingPrefVia:         true,
		ExistingPrefSetByUserID: 1, // a human set it
		TotalRules:              1, DistinctExitNodes: 1, CanonicalTag: "tag:dev-infra-relay-2",
		PrefRelayOwnershipKnown: true, // relay-1 owns nothing → B361 would move a DERIVED row
	})
	if ok && (ch.Action == "update" || ch.Action == "create") {
		t.Fatalf("action = %q (%s) — a human's relay was rewritten", ch.Action, ch.Reason)
	}
}
