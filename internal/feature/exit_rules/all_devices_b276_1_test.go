// B276.1 — "all my devices" must survive a NEW device.
//
// /my/exit-rules has offered the option since B275.3, but the form only
// materialised the rule for the devices that existed at that moment: a laptop
// registered next week had no rule while the UI still described the rule as being
// for all of the user's devices. V074 stores the intent on the row
// (device_rules.all_devices) and the periodic propagation pass re-materialises it.
package exit_rules

import (
	"database/sql"
	"testing"

	skygatedb "skygate/internal/db"
)

func newB2761DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b2761.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedB2761 wires one user with two devices and two rules: one saved for "all my
// devices" (device 9 today) and one deliberately targeted at device 9 alone.
func seedB2761(t *testing.T, d *sql.DB) {
	t.Helper()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	      VALUES (1, 'skyadmin', 'x', 1, 1)`)
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	      VALUES ('9', 1, 'skyadmin', 'tag:dev-skyadmin-skyworker', 1, 0, 'skyworker')`)
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	      VALUES ('20', 1, 'skyadmin', 'tag:dev-skyadmin-newlaptop', 1, 0, 'newlaptop')`)
	// The rule the user saved for "all my devices", and a single-device one.
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, all_devices)
	      VALUES (1, 9, 'skyadmin', 'karolina', 'ip', '104.16.0.0/12', 'accept', 1, 1)`)
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, all_devices)
	      VALUES (1, 9, 'skyadmin', 'emilia', 'ip', '8.8.8.0/24', 'accept', 1, 0)`)
}

func countRulesFor(t *testing.T, d *sql.DB, deviceID int, value string) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE device_id = $1 AND target_value = $2`, deviceID, value).Scan(&n); err != nil {
		t.Fatalf("count rules: %v", err)
	}
	return n
}

// TestB2761_PropagationCoversADeviceRegisteredLater is the functional gap: the
// device exists now, the rule was saved before it did.
func TestB2761_PropagationCoversADeviceRegisteredLater(t *testing.T) {
	d := newB2761DB(t)
	seedB2761(t, d)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	// Before: device 20 has nothing.
	if n := countRulesFor(t, d, 20, "104.16.0.0/12"); n != 0 {
		t.Fatalf("device 20 already has %d row(s) — the fixture does not model a NEW device", n)
	}

	created, err := s.propagateAllDeviceRules()
	if err != nil {
		t.Fatalf("propagateAllDeviceRules: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1 (the all-devices rule, and ONLY it)", created)
	}
	if n := countRulesFor(t, d, 20, "104.16.0.0/12"); n != 1 {
		t.Errorf("device 20 has %d row(s) for the all-devices rule, want 1", n)
	}
	// The single-device rule must NOT be copied — that is the whole reason the
	// intent is stored explicitly instead of guessing from "the same rule on
	// several devices".
	if n := countRulesFor(t, d, 20, "8.8.8.0/24"); n != 0 {
		t.Errorf("a single-device rule was copied to another device (%d row(s))", n)
	}
	// The copy itself must carry the intent, or the NEXT pass would see it as a
	// hand-made single-device rule and stop covering devices added later.
	var marked int
	if err := d.QueryRow(`SELECT COALESCE(all_devices,0) FROM device_rules WHERE device_id = 20 AND target_value = '104.16.0.0/12'`).Scan(&marked); err != nil {
		t.Fatalf("read the copied row: %v", err)
	}
	if marked != 1 {
		t.Errorf("the copied row has all_devices=%d, want 1", marked)
	}
}

// TestB2761_PropagationIsIdempotent: the pass runs on every tick, so a second run
// must be free and must not create duplicates.
func TestB2761_PropagationIsIdempotent(t *testing.T) {
	d := newB2761DB(t)
	seedB2761(t, d)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	if _, err := s.propagateAllDeviceRules(); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	created, err := s.propagateAllDeviceRules()
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if created != 0 {
		t.Errorf("second pass created %d row(s), want 0", created)
	}
	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE target_value = '104.16.0.0/12'`).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Errorf("all-devices rule rows = %d, want 2 (one per device, no duplicates)", total)
	}
}

// TestB2761_ViewLabelsAllDeviceRows: the label is what makes the propagation
// visible — a user cannot otherwise tell a rule that follows their new laptop from
// one that does not.
func TestB2761_ViewLabelsAllDeviceRows(t *testing.T) {
	d := newB2761DB(t)
	seedB2761(t, d)
	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}

	keys := s.allDeviceRuleKeys(1)
	if len(keys) != 1 {
		t.Fatalf("allDeviceRuleKeys returned %d key(s), want 1: %v", len(keys), keys)
	}
	if !keys[allDeviceRuleKey("karolina", "ip", "104.16.0.0/12")] {
		t.Errorf("the all-devices key is missing from %v — the row would render without its badge", keys)
	}
	if keys[allDeviceRuleKey("emilia", "ip", "8.8.8.0/24")] {
		t.Errorf("a single-device rule was labelled as all-devices: %v", keys)
	}
}

// TestB2761_MarkGroupKeepsTheIntent: saving a rule for "all my devices" marks every
// row of that rule (the fan-out copies included), so the copies are not mistaken for
// hand-made single-device rules later.
func TestB2761_MarkGroupKeepsTheIntent(t *testing.T) {
	d := newB2761DB(t)
	seedB2761(t, d)
	// A second device already carries the same rule, saved before the marker
	// existed (all_devices = 0 in the fixture for that row).
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, all_devices)
	                     VALUES (1, 20, 'skyadmin', 'karolina', 'ip', '104.16.0.0/12', 'accept', 1, 0)`); err != nil {
		t.Fatalf("seed second device rule: %v", err)
	}
	n, err := skygatedb.MarkDeviceRulesAllDevices(d, 1, "karolina", "ip", "104.16.0.0/12")
	if err != nil {
		t.Fatalf("MarkDeviceRulesAllDevices: %v", err)
	}
	if n != 2 {
		t.Errorf("marked %d row(s), want 2 (both devices of that rule)", n)
	}
}
