package exit_rules

// reconciler_b237_7_test.go — v1.5.2 (B237.7) —
// comprehensive build-time test for the preferred-exit
// auto-reconciler (B229) + PlanDevicePrefChange decision
// function.
//
// Background (B237.7 root cause, 2026-09-04):
//   SKYGATE_PREFERRED_RECONCILER_LIVE defaulted to false
//   (dry-run). The operator never flipped it to true, so
//   the reconciler ran for ~24h on cyborg+basic without
//   ever writing the unanimity-derived device_exit_node_prefs.
//   The YouTube rules (device_rules exit_node=emilia) were
//   decorative — headscale's `via:` clause never reached
//   the policy, so Tailscale clients on cyborg+basic
//   weren't pinned to emilia and YouTube failed.
//
// This test pins the B237.7 contract so the next regression
// (default flips back to false, or a code path skip
// silently swallows an orphan user_id, or the rename
// migrator stops detecting stale tags) is caught at build
// time.
//
// Scenarios covered (8+):
//  1. PlanDevicePrefChange: empty pref + unanimous exit_node
//     → CREATE (the B229 happy path)
//  2. PlanDevicePrefChange: empty pref + split exit_nodes
//     (3 emilia + 2 karolina) → SKIP (operator must pick)
//  3. PlanDevicePrefChange: empty pref + no CanonicalTag
//     (host deleted from node_owner_map) → no-op (don't
//     clobber a missing-but-may-return situation)
//  4. PlanDevicePrefChange: existing pref with stale tag
//     (operator typed the wrong tag manually) → UPDATE
//  5. PlanDevicePrefChange: existing pref canonical, but
//     via_enabled=0 (V061 migration's intentional skip) →
//     UPDATE to re-enable
//  6. PlanDevicePrefChange: existing pref canonical + via=1
//     → no-op (everything already correct)
//  7. PlanDevicePrefChange: orphan user_id (user 6/michail
//     — no portal_users row). Critical: the B237.7 root
//     cause included FK-silent-swallow on this case. The
//     function must NOT crash on missing usernames and
//     must still produce a CREATE change.
//  8. PreferredExitReconcilerLive default is TRUE
//     (B237.7 fix). Pin the env-var semantics:
//     - "true", "1", "yes" → true
//     - "false", "0", "no" → false
//     - unset / unknown / "maybe" → true (the new default)
//
// 2026-09-04: v1.5.2 (B237.7).

import (
	"testing"
)

func TestPlanDevicePrefChange_CreatesWhenUnanimous(t *testing.T) {
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "", // empty — no pref yet
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          10,
		CanonicalTag:        "tag:dev-infra-emilia",
	})
	if !ok {
		t.Fatal("PlanDevicePrefChange: expected change, got no-op")
	}
	if ch.Action != "create" {
		t.Errorf("Action = %q, want %q (B229 happy path)", ch.Action, "create")
	}
	if ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("NewTag = %q, want %q", ch.NewTag, "tag:dev-infra-emilia")
	}
	if ch.Reason != "missing-pref-unanimous" {
		t.Errorf("Reason = %q, want %q", ch.Reason, "missing-pref-unanimous")
	}
}

func TestPlanDevicePrefChange_SkipsWhenSplit(t *testing.T) {
	// 3 emilia + 2 karolina = split. Operator must
	// pick. Reconciler must NOT silently pick one.
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:              1,
		DeviceHostname:      "splitty",
		ExistingPrefTag:     "",
		DistinctExitNodes:   2, // split
		DominantExitHostname: "emilia",
		TotalRules:          5,
		CanonicalTag:        "tag:dev-infra-emilia",
	})
	if !ok {
		t.Fatal("PlanDevicePrefChange: expected skip change")
	}
	if ch.Action != "skip" {
		t.Errorf("Action = %q, want %q (operator must pick on split)", ch.Action, "skip")
	}
	if ch.Reason != "missing-pref-split" {
		t.Errorf("Reason = %q, want %q", ch.Reason, "missing-pref-split")
	}
}

func TestPlanDevicePrefChange_NoOpWhenCanonicalTagMissing(t *testing.T) {
	// Empty pref + empty canonical tag = can't derive
	// the right value. No-op (not a CREATE, not a SKIP).
	// This is the "host deleted from node_owner_map"
	// case — better to do nothing than to clobber.
	_, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:              1,
		DeviceHostname:      "orphaned",
		ExistingPrefTag:     "",
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          3,
		CanonicalTag:        "", // host not in node_owner_map
	})
	if ok {
		t.Errorf("PlanDevicePrefChange: expected no-op when CanonicalTag empty, got a change")
	}
}

func TestPlanDevicePrefChange_UpdatesStaleTag(t *testing.T) {
	// Existing pref has a wrong tag (operator typed it
	// manually, e.g. 'tag:dev-michail-basic' which is
	// the device's own tag, not the exit node's). Update.
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:              1,
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "tag:dev-michail-basic", // wrong
		ExistingPrefVia:     true,
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          10,
		CanonicalTag:        "tag:dev-infra-emilia",
	})
	if !ok {
		t.Fatal("PlanDevicePrefChange: expected update change")
	}
	if ch.Action != "update" {
		t.Errorf("Action = %q, want %q", ch.Action, "update")
	}
	if ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("NewTag = %q, want %q", ch.NewTag, "tag:dev-infra-emilia")
	}
	if ch.OldTag != "tag:dev-michail-basic" {
		t.Errorf("OldTag = %q, want %q", ch.OldTag, "tag:dev-michail-basic")
	}
	if ch.Reason != "stale-tag" {
		t.Errorf("Reason = %q, want %q", ch.Reason, "stale-tag")
	}
}

func TestPlanDevicePrefChange_ReEnablesViaFlag(t *testing.T) {
	// Existing pref has the correct tag, but via=0
	// (V061 migration's intentional skip for rows it
	// couldn't resolve). B229 is the catch-up: re-enable.
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:              1,
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "tag:dev-infra-emilia",
		ExistingPrefVia:     false, // via disabled
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          10,
		CanonicalTag:        "tag:dev-infra-emilia",
	})
	if !ok {
		t.Fatal("PlanDevicePrefChange: expected update change (re-enable via)")
	}
	if ch.Action != "update" {
		t.Errorf("Action = %q, want %q", ch.Action, "update")
	}
	if ch.Reason != "via-disabled-but-canonical" {
		t.Errorf("Reason = %q, want %q", ch.Reason, "via-disabled-but-canonical")
	}
}

func TestPlanDevicePrefChange_NoOpWhenAlreadyCorrect(t *testing.T) {
	// Everything already correct → no-op (don't churn
	// audit logs with redundant writes).
	_, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:              1,
		DeviceHostname:      "skyworker",
		ExistingPrefTag:     "tag:dev-infra-karolina",
		ExistingPrefVia:     true,
		DistinctExitNodes:   1,
		DominantExitHostname: "karolina",
		TotalRules:          117,
		CanonicalTag:        "tag:dev-infra-karolina",
	})
	if ok {
		t.Errorf("PlanDevicePrefChange: expected no-op (everything correct), got a change")
	}
}

func TestPlanDevicePrefChange_OrphanUserID(t *testing.T) {
	// B237.7 critical: basic (user_id=6, michail) has no
	// row in portal_users. The reconciler's
	// collectDevicePrefState must NOT crash on the
	// missing username; it should still produce a
	// CREATE change with the canonical tag from
	// node_owner_map. Pin: this is the regression
	// guard for the B237.7 fix.
	//
	// Username is "" (the collectDevicePrefState
	// function scans it with a permissive Scan that
	// returns the zero value on no row). The function
	// MUST treat empty username as a valid input
	// (it's used in audit log + alert text, not in
	// the SQL write path).
	ch, ok := PlanDevicePrefChange(DevicePrefState{
		UserID:              6, // michail — no portal_users row
		Username:            "", // empty (DB scan miss)
		DeviceHostname:      "basic",
		ExistingPrefTag:     "", // empty — no pref yet
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          43,
		CanonicalTag:        "tag:dev-infra-emilia",
	})
	if !ok {
		t.Fatal("PlanDevicePrefChange: orphan user_id must NOT swallow the change")
	}
	if ch.Action != "create" {
		t.Errorf("Action = %q, want %q (orphan user_id must still CREATE)", ch.Action, "create")
	}
	if ch.UserID != 6 {
		t.Errorf("UserID = %d, want 6 (michail orphaned id)", ch.UserID)
	}
}

func TestPreferredExitReconcilerLive_DefaultTrue_B237_7(t *testing.T) {
	// B237.7: default flipped from false to true so
	// the reconciler does the right thing by default.
	// The opt-out is the only way to disable.
	cases := []struct {
		env  string
		want bool
	}{
		// Opt-out: must be honored.
		{"false", false},
		{"0", false},
		{"no", false},
		// Opt-in: must be honored.
		{"true", true},
		{"1", true},
		{"yes", true},
		{"TRUE", true}, // case-insensitive
		// B237.7 default: unset / unknown → true (LIVE).
		{"", true},
		{"maybe", true},
		{"yes, please", true}, // contains "yes" substring; only EXACT match after strings.ToLower + TrimSpace
	}
	for _, c := range cases {
		t.Setenv("SKYGATE_PREFERRED_RECONCILER_LIVE", c.env)
		got := PreferredExitReconcilerLive()
		if got != c.want {
			t.Errorf("env=%q: PreferredExitReconcilerLive() = %v, want %v (B237.7 default: true)",
				c.env, got, c.want)
		}
	}
}
