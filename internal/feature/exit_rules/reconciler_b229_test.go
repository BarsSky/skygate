package exit_rules

// reconciler_b229_test.go — v1.5.2 (B229) — unit tests
// for the preferred-exit auto-reconciler.
//
// Coverage:
//   - PreferredExitReconcilerLive: env reading (default
//     off, "true"/"1"/"yes" on, anything else off).
//   - shouldAlert: rate-limit + window-reset + per-reason
//     bucketing.
//   - PlanDevicePrefChange: pure decision function, the
//     5 outcomes (create / split-skip / no-canonical-skip
//     / stale-tag-update / via-disabled-update / no-op).
//
// We intentionally don't have an in-memory DB layer
// here (no modernc.org/sqlite / sqlmock in go.mod). The
// DB-touching function ReconcileDeviceExitNodePrefs is
// a thin wrapper that collects DevicePrefState from the
// DB and calls PlanDevicePrefChange per pair. The pure
// function is what's worth testing; the SQL queries are
// pinned in code review + live-verify on the agent.

import (
	"os"
	"testing"
	"time"
)

// TestPreferredExitReconcilerLive_DefaultOff pins the
// default behaviour: env unset → false (dry-run).
func TestPreferredExitReconcilerLive_DefaultOff(t *testing.T) {
	if err := os.Unsetenv("SKYGATE_PREFERRED_RECONCILER_LIVE"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	if PreferredExitReconcilerLive() {
		t.Fatal("unset env should yield false (dry-run)")
	}
}

// TestPreferredExitReconcilerLive_TrueVariants pins the
// accepted env values: "true" / "1" / "yes" (case
// insensitive) all flip live-mode on.
func TestPreferredExitReconcilerLive_TrueVariants(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "1", "yes", "YES"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("SKYGATE_PREFERRED_RECONCILER_LIVE", v)
			if !PreferredExitReconcilerLive() {
				t.Fatalf("env=%q should yield live=true", v)
			}
		})
	}
}

// TestPreferredExitReconcilerLive_OtherValuesOff pins
// that unknown values are treated as off (defensive).
func TestPreferredExitReconcilerLive_OtherValuesOff(t *testing.T) {
	for _, v := range []string{"", "0", "false", "no", "off", "garbage"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("SKYGATE_PREFERRED_RECONCILER_LIVE", v)
			if PreferredExitReconcilerLive() {
				t.Fatalf("env=%q should yield live=false", v)
			}
		})
	}
}

// TestShouldAlert_RateLimit_1hWindow pins the B227-style
// 1h rate limit. 3 calls inside 1h → only the first alerts.
func TestShouldAlert_RateLimit_1hWindow(t *testing.T) {
	ResetAlertThrottle()
	now := time.Unix(1_700_000_000, 0)
	if !shouldAlert("cyborg", "create", now) {
		t.Fatal("first call should alert")
	}
	if shouldAlert("cyborg", "create", now.Add(10*time.Minute)) {
		t.Fatal("second call within 1h should NOT alert")
	}
	if shouldAlert("cyborg", "create", now.Add(30*time.Minute)) {
		t.Fatal("third call within 1h should NOT alert")
	}
	// Past the 1h window — should alert again.
	if !shouldAlert("cyborg", "create", now.Add(61*time.Minute)) {
		t.Fatal("call after 1h should alert")
	}
}

// TestShouldAlert_DifferentReasonsAreIndependent pins the
// per-(hostname, reason) bucketing.
func TestShouldAlert_DifferentReasonsAreIndependent(t *testing.T) {
	ResetAlertThrottle()
	now := time.Unix(1_700_000_000, 0)
	if !shouldAlert("cyborg", "create", now) {
		t.Fatal("create should alert on first call")
	}
	// Same hostname, DIFFERENT reason — separate bucket,
	// should alert.
	if !shouldAlert("cyborg", "stale-tag", now) {
		t.Fatal("stale-tag should alert independently of create")
	}
	// Same hostname + create — still in window, no alert.
	if shouldAlert("cyborg", "create", now) {
		t.Fatal("second create within window should NOT alert")
	}
}

// TestShouldAlert_DifferentHostnamesAreIndependent pins
// that the rate-limit bucket is keyed on hostname (so a
// bulk reconcile with 100 different hostnames produces
// 100 alerts, not 1).
func TestShouldAlert_DifferentHostnamesAreIndependent(t *testing.T) {
	ResetAlertThrottle()
	now := time.Unix(1_700_000_000, 0)
	if !shouldAlert("cyborg", "create", now) {
		t.Fatal("cyborg should alert")
	}
	if !shouldAlert("karolina", "create", now) {
		t.Fatal("karolina should alert independently of cyborg")
	}
	if !shouldAlert("sharlotta", "create", now) {
		t.Fatal("sharlotta should alert independently of cyborg+karolina")
	}
}

// TestPlanDevicePrefChange_Create_MissingPrefUnanimous
// is the happy path 1 of the live cyborg case:
//   - device_rules: cyborg → emilia (1 rule).
//   - node_owner_map: emilia → tag:dev-infra-emilia
//     (CanonicalTag populated).
//   - device_exit_node_prefs: empty (ExistingPrefTag="").
//   - DistinctExitNodes=1 (all rules at the same exit).
//   - PlanDevicePrefChange should return:
//     Action=create, NewTag=tag:dev-infra-emilia,
//     Reason=missing-pref-unanimous.
func TestPlanDevicePrefChange_Create_MissingPrefUnanimous(t *testing.T) {
	state := DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "",
		ExistingPrefVia:     false,
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          1,
		CanonicalTag:        "tag:dev-infra-emilia",
	}
	ch, ok := PlanDevicePrefChange(state)
	if !ok {
		t.Fatal("expected a change; got none")
	}
	if ch.Action != "create" {
		t.Errorf("Action = %q, want create", ch.Action)
	}
	if ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("NewTag = %q, want tag:dev-infra-emilia", ch.NewTag)
	}
	if ch.Reason != "missing-pref-unanimous" {
		t.Errorf("Reason = %q, want missing-pref-unanimous", ch.Reason)
	}
	if ch.OldTag != "" {
		t.Errorf("OldTag = %q, want \"\" (create)", ch.OldTag)
	}
	if ch.RuleCount != 1 {
		t.Errorf("RuleCount = %d, want 1", ch.RuleCount)
	}
}

// TestPlanDevicePrefChange_Skip_SplitRules pins the
// split-rule behaviour: when device_rules has rules
// pointing at MORE than one distinct exit_node, the
// reconciler skips (rather than auto-deriving a pref
// that would lock the device to the wrong exit_node
// half the time). The operator gets a "skip" change
// in the audit log + the change list returned to the
// caller.
func TestPlanDevicePrefChange_Skip_SplitRules(t *testing.T) {
	state := DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "",
		ExistingPrefVia:     false,
		DistinctExitNodes:   2, // emilia + karolina
		DominantExitHostname: "emilia",
		TotalRules:          10,
		CanonicalTag:        "tag:dev-infra-emilia",
	}
	ch, ok := PlanDevicePrefChange(state)
	if !ok {
		t.Fatal("expected a skip change; got none")
	}
	if ch.Action != "skip" {
		t.Errorf("Action = %q, want skip", ch.Action)
	}
	if ch.Reason != "missing-pref-split" {
		t.Errorf("Reason = %q, want missing-pref-split", ch.Reason)
	}
}

// TestPlanDevicePrefChange_Skip_NoCanonicalTag pins the
// "headscale node not yet tagged" guard. The B77
// autoupdater applies the dev-tag; if the rule points
// at an exit_node whose hostname isn't in
// node_owner_map yet (CanonicalTag=""), the reconciler
// silently skips.
func TestPlanDevicePrefChange_Skip_NoCanonicalTag(t *testing.T) {
	state := DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "",
		ExistingPrefVia:     false,
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          1,
		CanonicalTag:        "", // exit_node not in node_owner_map yet
	}
	ch, ok := PlanDevicePrefChange(state)
	if ok || ch != nil {
		t.Fatalf("expected nil change for no-canonical-tag; got %+v", ch)
	}
}

// TestPlanDevicePrefChange_Update_StaleTag pins the
// legacy tag migration. A pre-existing pref with the
// legacy `tag:exit-X` form (that somehow survived
// V061 because the hostname wasn't in node_owner_map
// at migration time) is updated to the canonical
// `tag:dev-infra-X` form.
func TestPlanDevicePrefChange_Update_StaleTag(t *testing.T) {
	state := DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "tag:exit-emilia", // legacy
		ExistingPrefVia:     true,
		DistinctExitNodes:   0, // ignored when pref exists
		DominantExitHostname: "",
		TotalRules:          0, // ignored when pref exists
		CanonicalTag:        "tag:dev-infra-emilia",
	}
	ch, ok := PlanDevicePrefChange(state)
	if !ok {
		t.Fatal("expected a stale-tag change; got none")
	}
	if ch.Action != "update" {
		t.Errorf("Action = %q, want update", ch.Action)
	}
	if ch.Reason != "stale-tag" {
		t.Errorf("Reason = %q, want stale-tag", ch.Reason)
	}
	if ch.OldTag != "tag:exit-emilia" {
		t.Errorf("OldTag = %q, want tag:exit-emilia", ch.OldTag)
	}
	if ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("NewTag = %q, want tag:dev-infra-emilia", ch.NewTag)
	}
}

// TestPlanDevicePrefChange_Update_ViaDisabledButCanonical
// pins the re-enable path. The pref is canonical but
// via_enabled=0 (e.g. an older row that the V061
// migration left at 0 because the tag wasn't
// resolvable at migration time, but a later
// node_owner_map update made it resolvable). B229
// re-enables via_enabled=1 to restore the pin.
func TestPlanDevicePrefChange_Update_ViaDisabledButCanonical(t *testing.T) {
	state := DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "tag:dev-infra-emilia", // already canonical
		ExistingPrefVia:     false,                  // but via=0
		DistinctExitNodes:   0,
		DominantExitHostname: "",
		TotalRules:          0,
		CanonicalTag:        "tag:dev-infra-emilia",
	}
	ch, ok := PlanDevicePrefChange(state)
	if !ok {
		t.Fatal("expected a via-disabled update; got none")
	}
	if ch.Action != "update" {
		t.Errorf("Action = %q, want update", ch.Action)
	}
	if ch.Reason != "via-disabled-but-canonical" {
		t.Errorf("Reason = %q, want via-disabled-but-canonical", ch.Reason)
	}
	if ch.NewTag != "tag:dev-infra-emilia" {
		t.Errorf("NewTag = %q, want tag:dev-infra-emilia (no change)", ch.NewTag)
	}
	if ch.OldTag != "tag:dev-infra-emilia" {
		t.Errorf("OldTag = %q, want tag:dev-infra-emilia (informational only)", ch.OldTag)
	}
}

// TestPlanDevicePrefChange_NoOp_CanonicalAndPinned pins
// the no-op path: pref is canonical AND via_enabled=1,
// so the reconciler has nothing to do.
func TestPlanDevicePrefChange_NoOp_CanonicalAndPinned(t *testing.T) {
	state := DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "cyborg",
		ExistingPrefTag:     "tag:dev-infra-emilia",
		ExistingPrefVia:     true,
		DistinctExitNodes:   1,
		DominantExitHostname: "emilia",
		TotalRules:          5,
		CanonicalTag:        "tag:dev-infra-emilia",
	}
	ch, ok := PlanDevicePrefChange(state)
	if ok || ch != nil {
		t.Fatalf("expected nil change (no-op); got %+v", ch)
	}
}

// TestPlanDevicePrefChange_Skip_HostnameDeleted pins the
// defensive guard: if the device's hostname is no
// longer in node_owner_map (device unregistered), the
// existing pref is left alone (we don't know if the
// operator wants to clear it, so we don't clobber).
func TestPlanDevicePrefChange_Skip_HostnameDeleted(t *testing.T) {
	state := DevicePrefState{
		UserID:              1,
		Username:            "skyadmin",
		DeviceHostname:      "old-device",
		ExistingPrefTag:     "tag:dev-infra-emilia",
		ExistingPrefVia:     true,
		DistinctExitNodes:   0,
		DominantExitHostname: "",
		TotalRules:          0,
		CanonicalTag:        "", // device not in node_owner_map
	}
	ch, ok := PlanDevicePrefChange(state)
	if ok || ch != nil {
		t.Fatalf("expected nil change for missing canonical tag; got %+v", ch)
	}
}
