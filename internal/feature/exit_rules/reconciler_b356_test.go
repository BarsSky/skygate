// reconciler_b356_test.go — B356 (2026-10-07).
//
// THE OPERATOR'S REPORT. When the relays `karolina` and `sharlotta` went offline the
// exit-node preference was NOT redistributed: devices stayed pinned to a relay that
// was serving nothing, their internet access broke, and nothing re-pointed them and
// no page or log said so.
//
// Measured live state: `prefix_owner` had 139 rows, every single one on `emilia`,
// while `device_exit_node_prefs` still carried `tag:dev-infra-karolina` for two
// devices (`basic` and `skyworker`, both `set_by_user_id = 0` — derived by the
// engine). Since B265 that preference is a permission FILTER on the per-device
// `autogroup:internet` grant, so the device owes its whole egress to a relay that
// owns nothing and is down.
//
// These tests drive the PURE planner, so they need no database. The property each one
// pins is named in its doc comment; together they are the contract:
//
//	(1) a stale DERIVED preference is re-pointed with a named reason;
//	(2) an OPERATOR preference is NEVER overwritten — it is surfaced;
//	(3) a healthy preferred relay that owns the prefixes keeps its value (no
//	    behaviour change for the devices that work today);
//	(4) every way of NOT deciding is a NAMED skip, never a silent `return nil, false`;
//	(5) running the planner on the repaired state changes nothing (idempotence).
package exit_rules

import "testing"

// b356State is the live shape: the stored preference names `karolina`, the data plane
// put every one of the device's prefixes on `emilia` (and emilia carries a per-node
// tag), and the monitor measured karolina as NOT usable.
func b356State() DevicePrefState {
	return DevicePrefState{
		UserID:                  1,
		Username:                "skyadmin",
		DeviceHostname:          "skyworker",
		ExistingPrefTag:         "tag:dev-infra-karolina",
		ExistingPrefVia:         true,
		ExistingPrefSetByUserID: 0, // derived by the engine
		PrefRelayKnown:          true,
		PrefRelayUsable:         false,
		PrefRelayState:          "offline",
		TotalRules:              21,
		DistinctExitNodes:       1,
		DominantExitHostname:    "karolina",
		CanonicalTag:            "tag:dev-infra-karolina",
		OwnerDistinct:           1,
		OwnerCanonicalTag:       "tag:dev-infra-emilia",
	}
}

// TestB356_StaleDerivedPrefIsRepointedWithNamedReason is property (1): the exact live
// state, with the engine as the author of the row, must be MOVED — to the relay the
// data plane actually chose, with a reason that names what happened.
func TestB356_StaleDerivedPrefIsRepointedWithNamedReason(t *testing.T) {
	ch, ok := PlanDevicePrefChange(b356State())
	if !ok || ch == nil {
		t.Fatal("a DERIVED preference naming an unusable relay must be repaired, not silently kept")
	}
	if ch.Action != "update" {
		t.Errorf("Action = %q, want update", ch.Action)
	}
	if ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("NewTag = %q, want tag:dev-infra-emilia (the owner prefix_owner gives these prefixes)", ch.NewTag)
	}
	if ch.Reason != StalePrefRelayUnusable {
		t.Errorf("Reason = %q, want %q — the audit trail and the operator's page must name one thing", ch.Reason, StalePrefRelayUnusable)
	}
	if ch.RelayState != "offline" {
		t.Errorf("RelayState = %q, want offline (the B273 state the decision was made on)", ch.RelayState)
	}
	if ch.OldTag != "tag:dev-infra-karolina" {
		t.Errorf("OldTag = %q, want the stored value so the audit row shows the move", ch.OldTag)
	}
}

// TestB356_OperatorPrefIsNeverOverwritten is property (2). `set_by_user_id != 0` is a
// human decision: the planner must return a VISIBLE skip (never an update), and it
// must carry the provenance so the notification and the page can say who set it.
func TestB356_OperatorPrefIsNeverOverwritten(t *testing.T) {
	t.Run("unusable relay", func(t *testing.T) {
		s := b356State()
		s.ExistingPrefSetByUserID = 7
		ch, ok := PlanDevicePrefChange(s)
		if !ok || ch == nil {
			t.Fatal("an operator's pin to an unusable relay must be SURFACED, not silently ignored")
		}
		if ch.Action != "skip" || ch.Reason != "stale-pref-human-pinned" {
			t.Fatalf("got action=%q reason=%q, want skip/stale-pref-human-pinned", ch.Action, ch.Reason)
		}
		if ch.SetByUserID != 7 {
			t.Errorf("SetByUserID = %d, want 7 (the surface must name the author)", ch.SetByUserID)
		}
		if ch.NewTag != "tag:dev-infra-emilia" {
			t.Errorf("NewTag = %q, want the candidate relay so the operator sees what the engine would pick", ch.NewTag)
		}
	})

	t.Run("healthy relay that owns another relay's prefixes", func(t *testing.T) {
		// The B345 shape, but authored by a human: the data plane's owner is emilia,
		// the stored value says karolina, karolina is UP. B345 repaired this
		// unconditionally; B356 keeps the repair for derived rows and refuses it here.
		s := b356State()
		s.ExistingPrefSetByUserID = 7
		s.PrefRelayUsable = true
		s.PrefRelayState = "online"
		ch, ok := PlanDevicePrefChange(s)
		if !ok || ch == nil || ch.Action != "skip" || ch.Reason != "stale-pref-human-non-owner" {
			t.Fatalf("got %+v, want a visible skip/stale-pref-human-non-owner", ch)
		}
	})

	t.Run("legacy tag form of the same relay is still normalised", func(t *testing.T) {
		// A human pinned emilia; the stored value is the LEGACY tag form (B188
		// renamed tag:exit-<host> → tag:dev-infra-<host>). That is not a change of
		// exit node — it is the same relay under a tag headscale does not know — so
		// the normalisation must still happen or the human's own pin stops working.
		s := b356State()
		s.ExistingPrefSetByUserID = 7
		s.ExistingPrefTag = "tag:exit-emilia"
		s.PrefRelayUsable = true
		s.PrefRelayState = "online"
		s.DominantExitHostname = "emilia"
		s.CanonicalTag = "tag:dev-infra-emilia"
		s.OwnerDistinct = 1
		s.OwnerCanonicalTag = "tag:dev-infra-emilia"
		ch, ok := PlanDevicePrefChange(s)
		if !ok || ch == nil {
			t.Fatal("the legacy tag form of the SAME relay must be normalised on every row")
		}
		if ch.Action != "update" || ch.NewTag != "tag:dev-infra-emilia" {
			t.Fatalf("got action=%q new=%q, want update/tag:dev-infra-emilia", ch.Action, ch.NewTag)
		}
	})
}

// TestB356_HealthyPrefThatOwnsItsPrefixesIsUntouched is property (3): the devices the
// operator says work today (`a71`, `cyborg` — both pinned to emilia, which owns the
// prefixes) must take EXACTLY the old path, for both provenances.
func TestB356_HealthyPrefThatOwnsItsPrefixesIsUntouched(t *testing.T) {
	for _, setBy := range []int64{0, 1} {
		s := b356State()
		s.ExistingPrefTag = "tag:dev-infra-emilia"
		s.ExistingPrefSetByUserID = setBy
		s.PrefRelayUsable = true
		s.PrefRelayState = "online"
		s.DominantExitHostname = "emilia"
		s.CanonicalTag = "tag:dev-infra-emilia"
		if ch, ok := PlanDevicePrefChange(s); ok && ch != nil {
			t.Errorf("set_by_user_id=%d: a preference naming the usable owner must not be touched; got action=%q new=%q reason=%q",
				setBy, ch.Action, ch.NewTag, ch.Reason)
		}
	}
}

// TestB356_EveryUndecidableStateIsANamedSkip is property (4). L-54: the difference
// between "nothing to do" and "I cannot decide" must never be a silent `nil, false`.
// Each branch gets its own test case so a future edit that swallows one of them goes
// red here rather than in production.
func TestB356_EveryUndecidableStateIsANamedSkip(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*DevicePrefState)
		want   string
	}{
		{
			name:   "no owner for the device's prefixes",
			mutate: func(s *DevicePrefState) { s.OwnerDistinct = 0; s.OwnerCanonicalTag = "" },
			want:   "stale-pref-no-owner",
		},
		{
			name:   "the prefixes are served by several relays",
			mutate: func(s *DevicePrefState) { s.OwnerDistinct = 2; s.OwnerCanonicalTag = "" },
			want:   "stale-pref-owner-split",
		},
		{
			name:   "one owner, but it carries no per-node tag",
			mutate: func(s *DevicePrefState) { s.OwnerCanonicalTag = "" },
			want:   "stale-pref-owner-untagged",
		},
		{
			name: "the assignment table still gives the prefixes to the very relay the preference names",
			mutate: func(s *DevicePrefState) {
				s.OwnerCanonicalTag = s.ExistingPrefTag
			},
			want: "stale-pref-owner-is-pref-relay",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := b356State()
			c.mutate(&s)
			ch, ok := PlanDevicePrefChange(s)
			if !ok || ch == nil {
				t.Fatalf("must produce a VISIBLE skip, got a silent no-op (reason %q)", c.want)
			}
			if ch.Action != "skip" || ch.Reason != c.want {
				t.Fatalf("got action=%q reason=%q, want skip/%s", ch.Action, ch.Reason, c.want)
			}
		})
	}
}

// TestB356_UnknownRelayIsNotBroken: a relay with NO health row has never been
// measured, and "unknown" must not drive a write — the same conservative rule the
// planner applies to a missing owner. The pre-B356 path is taken instead.
func TestB356_UnknownRelayIsNotBroken(t *testing.T) {
	s := b356State()
	s.PrefRelayKnown = false
	s.PrefRelayUsable = false
	s.PrefRelayState = ""
	// With no owner disagreement either, the stored value stands untouched.
	s.OwnerDistinct = 0
	s.OwnerCanonicalTag = ""
	s.DominantExitHostname = "karolina"
	s.CanonicalTag = "tag:dev-infra-karolina"
	if ch, ok := PlanDevicePrefChange(s); ok && ch != nil {
		t.Fatalf("a relay we have never measured must not trigger the B356 repair; got action=%q reason=%q", ch.Action, ch.Reason)
	}
}

// TestB356_RepairedStateIsAStableFixedPoint is property (5), the L-60 half: the pass
// runs on every maintenance tick, so applying the decision and re-planning the result
// must converge. Two consecutive evaluations of the repaired state change nothing.
func TestB356_RepairedStateIsAStableFixedPoint(t *testing.T) {
	ch, ok := PlanDevicePrefChange(b356State())
	if !ok || ch == nil || ch.Action != "update" {
		t.Fatalf("expected the repair first, got %+v", ch)
	}
	repaired := b356State()
	repaired.ExistingPrefTag = ch.NewTag
	repaired.PrefRelayState = "online"
	repaired.PrefRelayUsable = true
	repaired.DominantExitHostname = "emilia"
	repaired.CanonicalTag = ch.NewTag
	for i := 0; i < 2; i++ {
		if again, ok := PlanDevicePrefChange(repaired); ok && again != nil {
			t.Fatalf("pass %d after the repair produced another change (%s/%s) — the pass would not converge",
				i+1, again.Action, again.Reason)
		}
	}
}

// TestB356_SharedStalenessPredicateAgreesWithThePlanner pins the ONE predicate the
// page and the reconciler share: /admin/exit-nodes must not claim a device is fine
// while the engine is moving it, and must not warn about one the engine leaves alone.
//
// RENEGOTIATED 2026-10-07 (B361) — the SIGNATURE gained two facts, because the
// predicate could not see the a71 case without them: whether the preferred relay owns
// ANY prefix at all, and whether the device has any rules. The properties asserted
// below are unchanged and every case still pins the same reason it did before; what is
// new is the case the old signature could not express (a device with no rules pinned
// to a relay that owns nothing — section (5)).
func TestB356_SharedStalenessPredicateAgreesWithThePlanner(t *testing.T) {
	// The live shape: derived row on an unusable relay, one owner (emilia).
	reason, stale := StaleExitPrefReason("tag:dev-infra-karolina", true, true, false, true, 1, false)
	if !stale || reason != StalePrefRelayUnusable {
		t.Fatalf("StaleExitPrefReason = (%q, %v), want (%q, true)", reason, stale, StalePrefRelayUnusable)
	}
	planner, ok := PlanDevicePrefChange(b356State())
	if !ok || planner == nil || planner.Reason != reason {
		t.Fatalf("the page predicate (%q) and the planner (%v) disagree on the same state", reason, planner)
	}

	// A preference naming the owner, on a usable relay that owns prefixes: neither
	// surface may report it. (`prefNamesOwner = true` is the whole point of this case
	// — the owner IS the preferred relay, so there is nothing to warn about.)
	if r, st := StaleExitPrefReason("tag:dev-infra-emilia", true, true, true, true, 1, true); st {
		t.Fatalf("StaleExitPrefReason reported %q for a perfectly healthy preference", r)
	}
	// A human pin to a usable relay that owns prefixes but none of the device's:
	// the page reports the same shape the planner refuses to repair.
	if r, st := StaleExitPrefReason("tag:dev-infra-karolina", true, true, true, true, 1, false); !st || r != StalePrefOwnerOverrides {
		t.Fatalf("StaleExitPrefReason = (%q, %v), want (%q, true)", r, st, StalePrefOwnerOverrides)
	}
	// No preference at all is never stale.
	if r, st := StaleExitPrefReason("", false, false, false, true, 1, false); st {
		t.Fatalf("StaleExitPrefReason reported %q for a device with NO preference — that is the 'any exit node' state", r)
	}
	// (5) B361 — the a71 case: a device with NO rules, pinned to a relay that owns
	// NOTHING. It was invisible to both the health predicate (the relay is online)
	// and the owner comparison (there are no owners to compare), so the page said
	// nothing while `via=[emilia]` filtered the device away from every destination
	// karolina carried.
	if r, st := StaleExitPrefReason("tag:dev-infra-emilia", false, true, true, false, 0, false); !st || r != StalePrefNoRules {
		t.Fatalf("StaleExitPrefReason = (%q, %v), want (%q, true) for the a71 case", r, st, StalePrefNoRules)
	}
	// And a device WITH rules whose relay owns nothing at all is the neighbouring
	// case: named, and named differently, because the operator's fix differs.
	if r, st := StaleExitPrefReason("tag:dev-infra-emilia", false, true, true, true, 1, false); !st || r != StalePrefRelayOwnsNothing {
		t.Fatalf("StaleExitPrefReason = (%q, %v), want (%q, true) for a device with rules", r, st, StalePrefRelayOwnsNothing)
	}
	// And the planner names the same case for the same input.
	plannerNoRules, ok := PlanDevicePrefChange(DevicePrefState{
		UserID: 1, Username: "skyadmin", DeviceHostname: "a71",
		ExistingPrefTag: "tag:dev-infra-emilia", ExistingPrefVia: true,
		TotalRules: 0, PrefRelayKnown: true, PrefRelayUsable: true,
		PrefRelayOwnershipKnown: true, PrefRelayOwnsPrefix: false,
	})
	if !ok || plannerNoRules == nil || plannerNoRules.Reason != StalePrefNoRules {
		t.Fatalf("the planner does not name the a71 case: %+v", plannerNoRules)
	}
}
