// exit_node_prefs_b399_test.go — 2026-10-19 (B399): a preference the operator
// removed BY HAND must survive the next tick.
//
// THE REPORT (operator, verbatim): «теперь несмотря на то что для skyworker я
// отключаю strict mode по karolina он ставиться обратно».
//
// THE EVIDENCE (audit_log on the live installation, six hours):
//
//	19:08:56  skyadmin  my_device_preferred_exit_set   hostname=skyworker tag=
//	18:17:12  system    preferred_exit_reconciled      CREATE pref hostname=skyworker
//	          tag=tag:dev-infra-karolina via=1 reason=missing-pref-unanimous rules=269
//	17:30:39  skyadmin  my_device_preferred_exit_set   hostname=skyworker tag=
//	13:17:12  system    preferred_exit_reconciled      CREATE pref hostname=skyworker
//	          tag=tag:dev-infra-karolina via=1 reason=missing-pref-unanimous rules=224
//
// Clearing DELETED the row; the reconciler then saw "rules but no preference" and
// created one with via_enabled = 1 — restoring exactly the filter the operator had
// removed. These tests pin the two halves of the fix on a REAL SQLite schema (the
// migration chain, not a hand-written table): the decision is recorded, and a later
// explicit choice clears it again.
package db

import (
	"database/sql"
	"testing"
)

// b399OpenDB migrates a throwaway SQLite database through the real chain and seeds
// the portal user the preference queries join on.
func b399OpenDB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := OpenWithDialect("sqlite:" + t.TempDir() + "/b399.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                     VALUES (1, 'skyadmin', 'x', 1, 1)`); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_exit_node_prefs
	                        (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	                     VALUES (1, 'skyworker', 'tag:dev-infra-karolina', 0, 0, 1)`); err != nil {
		t.Fatalf("seed device_exit_node_prefs: %v", err)
	}
	return d
}

// TestB399_OptedOutColumnExists pins the migration itself: the column is what makes
// "the operator said no" distinguishable from "nobody has decided yet".
func TestB399_OptedOutColumnExists(t *testing.T) {
	h := b399OpenDB(t)
	pref, err := GetDeviceExitNodePref(h, 1, "skyworker")
	if err != nil {
		t.Fatalf("GetDeviceExitNodePref: %v", err)
	}
	if pref.OptedOut {
		t.Fatal("a derived row must not be opted out — the engine is allowed to manage it")
	}
	if pref.ExitNodeTag != "tag:dev-infra-karolina" || !pref.ViaEnabled {
		t.Fatalf("seeded row read back as tag=%q via=%t, want the seeded values", pref.ExitNodeTag, pref.ViaEnabled)
	}
}

// TestB399_ClearRecordsTheDecisionInsteadOfDeleting is the core contract: after a
// human clear the row still EXISTS, holds no tag, is not strict, and carries the
// opted_out flag plus who did it.
func TestB399_ClearRecordsTheDecisionInsteadOfDeleting(t *testing.T) {
	h := b399OpenDB(t)
	if err := ClearDeviceExitNodePref(h, 1, "skyworker", 7); err != nil {
		t.Fatalf("ClearDeviceExitNodePref: %v", err)
	}
	pref, err := GetDeviceExitNodePref(h, 1, "skyworker")
	if err != nil {
		t.Fatalf("GetDeviceExitNodePref after clear: %v", err)
	}
	if pref.ExitNodeTag != "" {
		t.Errorf("exit_node_tag = %q, want empty (the device takes any exit node)", pref.ExitNodeTag)
	}
	if pref.ViaEnabled {
		t.Error("via_enabled is still 1 — the strict pin the operator removed would be restored")
	}
	if !pref.OptedOut {
		t.Error("opted_out = 0 — nothing records the operator's decision, so the reconciler will re-create the row")
	}
	if pref.SetByUserID != 7 {
		t.Errorf("set_by_user_id = %d, want 7 (the human who removed it)", pref.SetByUserID)
	}
}

// TestB399_SetAfterClearClearsTheFlag — an explicit choice is a newer decision than
// the opt-out, so choosing a node must hand the row back to normal management.
func TestB399_SetAfterClearClearsTheFlag(t *testing.T) {
	h := b399OpenDB(t)
	if err := ClearDeviceExitNodePref(h, 1, "skyworker", 7); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := SetDeviceExitNodePref(h, 1, "skyworker", "tag:dev-infra-emilia", 7, false); err != nil {
		t.Fatalf("set after clear: %v", err)
	}
	pref, err := GetDeviceExitNodePref(h, 1, "skyworker")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if pref.OptedOut {
		t.Error("opted_out stayed 1 after an explicit choice — the device could never be pinned again")
	}
	if pref.ExitNodeTag != "tag:dev-infra-emilia" {
		t.Errorf("tag = %q, want tag:dev-infra-emilia", pref.ExitNodeTag)
	}
	if pref.ViaEnabled {
		t.Error("via_enabled = 1, want the posted 0 (strict pin is off by choice)")
	}
}

// TestB399_ClearOnAMissingRowStillRecordsIt — pressing ✕ on a device that had no
// preference row yet must still create the marker; otherwise the reconciler derives
// one on its next tick and the operator's action is silently undone.
func TestB399_ClearOnAMissingRowStillRecordsIt(t *testing.T) {
	h := b399OpenDB(t)
	if err := ClearDeviceExitNodePref(h, 1, "never-had-a-pref", 7); err != nil {
		t.Fatalf("clear a missing row: %v", err)
	}
	pref, err := GetDeviceExitNodePref(h, 1, "never-had-a-pref")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !pref.OptedOut || pref.ExitNodeTag != "" {
		t.Fatalf("row = (%q, opted_out=%t), want an empty, flagged marker", pref.ExitNodeTag, pref.OptedOut)
	}
}

// TestB399_ListReadersCarryTheFlag — the reconciler and both device pages read the
// flag through these list helpers; if either dropped it, the fix would be invisible
// where it matters.
func TestB399_ListReadersCarryTheFlag(t *testing.T) {
	h := b399OpenDB(t)
	if err := ClearDeviceExitNodePref(h, 1, "skyworker", 7); err != nil {
		t.Fatalf("clear: %v", err)
	}
	all, err := ListAllDeviceExitNodePrefs(h)
	if err != nil {
		t.Fatalf("ListAllDeviceExitNodePrefs: %v", err)
	}
	found := false
	for _, p := range all {
		if p.DeviceHostname == "skyworker" {
			found = true
			if !p.OptedOut {
				t.Error("ListAllDeviceExitNodePrefs lost opted_out — the reconciler would re-create the row")
			}
		}
	}
	if !found {
		t.Fatal("skyworker missing from ListAllDeviceExitNodePrefs")
	}
	mine, err := ListDeviceExitNodePrefsForUser(h, 1)
	if err != nil {
		t.Fatalf("ListDeviceExitNodePrefsForUser: %v", err)
	}
	for _, p := range mine {
		if p.DeviceHostname == "skyworker" && !p.OptedOut {
			t.Error("ListDeviceExitNodePrefsForUser lost opted_out — /my/devices would show a bare empty cell")
		}
	}
}
