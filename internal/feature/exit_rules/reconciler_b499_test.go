package exit_rules

// reconciler_b499_test.go — B499 (2026-11-19).
//
// «ПОЧЕМУ ПРОДОЛЖАЕТ ВЫСТАВЛЯТЬСЯ НЕВЕРНАЯ НАСТРОЙКА» — the third answer.
//
// The operator's earlier decision stands: a phone is NOT force-pinned; it picks its
// own exit node, because it runs no `accept-routes` and the rules alone decide what
// it may reach. That decision is implemented three ways (via_enabled is opt-in, the
// ACL emits `via` only for via_enabled rows, and B398 refuses to let a DERIVED row
// become a filter) — and yet the database still said `via_enabled = 1` on the row the
// engine wrote for `basic`, because every CREATE branch hard-coded `ViaEnabled: true`.
//
// B398 made that harmless for the policy. It did NOT make it honest: the flag is what
// the pages read, so the operator saw a strict pin that did not exist and could not
// understand why it kept coming back. These tests pin the honest state.

import "testing"

// TestB499_DerivedCreateNeverClaimsStrictMode covers all three CREATE reasons. A
// table, not one case, because the branches are separate code and the bug was that
// ALL of them said `true` — fixing one would have left two.
func TestB499_DerivedCreateNeverClaimsStrictMode(t *testing.T) {
	cases := []struct {
		name  string
		state DevicePrefState
		// wantReason is the CREATE reason this state must produce; it doubles as the
		// proof that the test reaches the branch it claims to test.
		wantReason string
	}{
		{
			name: "unanimous rule relay",
			state: DevicePrefState{
				UserID: 6, Username: "michail", DeviceHostname: "basic",
				TotalRules: 11, DistinctExitNodes: 1, CanonicalTag: "tag:dev-infra-karolina",
			},
			wantReason: "missing-pref-unanimous",
		},
		{
			name: "no rule names a relay, the assignment table does",
			state: DevicePrefState{
				UserID: 1, Username: "skyadmin", DeviceHostname: "cyborg",
				TotalRules: 3, DistinctExitNodes: 0,
				OwnerCanonicalTag: "tag:dev-infra-emilia", OwnerDistinct: 1,
			},
			wantReason: "missing-pref-owner-derived",
		},
		{
			name: "the owner contradicts the rule",
			state: DevicePrefState{
				UserID: 1, Username: "skyadmin", DeviceHostname: "msi",
				TotalRules: 4, DistinctExitNodes: 1, CanonicalTag: "tag:dev-infra-karolina",
				OwnerCanonicalTag: "tag:dev-infra-emilia", OwnerDistinct: 1,
				PrefRelayOwnershipKnown: true,
			},
			wantReason: "owner-overrides-rule-relay",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, ok := PlanDevicePrefChange(tc.state)
			if !ok || ch.Action != "create" {
				t.Fatalf("plan = %+v, want a create", ch)
			}
			if ch.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q (the test must reach the branch it claims)", ch.Reason, tc.wantReason)
			}
			if ch.ViaEnabled {
				t.Fatalf("%s: CREATE wrote via_enabled=true — a derived row must not claim strict mode", ch.Reason)
			}
		})
	}
}

// TestB499_UpdateStillCarriesTheOperatorsFlag is the other side of the same coin: B499
// changes what SKYGATE writes, never what a human chose. An UPDATE must keep the stored
// flag — including `true` — and an opted-out row must stay untouched.
func TestB499_UpdateStillCarriesTheOperatorsFlag(t *testing.T) {
	for _, via := range []bool{false, true} {
		ch, ok := PlanDevicePrefChange(DevicePrefState{
			UserID: 1, Username: "skyadmin", DeviceHostname: "skyworker",
			ExistingPrefTag:   "tag:dev-infra-emilia",
			ExistingPrefVia:   via,
			TotalRules:        9,
			DistinctExitNodes: 1,
			CanonicalTag:      "tag:dev-infra-karolina",
		})
		if !ok || ch.Action != "update" {
			t.Fatalf("via=%v: plan = %+v, want an update", via, ch)
		}
		if ch.ViaEnabled != via {
			t.Fatalf("via=%v: UPDATE wrote via=%v — B499 must not touch a human's switch", via, ch.ViaEnabled)
		}
	}

	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID: 1, Username: "skyadmin", DeviceHostname: "cyborg",
		ExistingPrefOptedOut: true, TotalRules: 3, DistinctExitNodes: 1,
		CanonicalTag: "tag:dev-infra-emilia",
	})
	if !ok || ch.Action != "skip" || ch.Reason != "operator-opted-out" {
		t.Fatalf("an opted-out row must stay untouched, got %+v", ch)
	}
}
