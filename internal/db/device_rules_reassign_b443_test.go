// device_rules_reassign_b443_test.go — B443. The transfer semantics are the contract: a
// rule belongs to the DEVICE's owner, and the two per-owner markers do not survive the
// move.
package db

import (
	"database/sql"
	"testing"
)

// b443Open builds a migrated SQLite database with the columns this block touches.
func b443Open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := OpenFastSQLiteForTest(t.TempDir() + "/b443.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return d
}

// TestB443_TransferMovesTheRulesAndClearsTheOwnerScopedMarkers is the live case: alice's
// two rules (one of them an all-devices rule in a named group) on a device the operator
// hands to bob.
func TestB443_TransferMovesTheRulesAndClearsTheOwnerScopedMarkers(t *testing.T) {
	d := b443Open(t)
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'alice', 'x', 0), (2, 'bob', 'x', 0)`)
	mustExec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled, user_name, all_devices, group_label, parent_domain, device_ip, device_hostname)
	          VALUES (1, 55, 'emilia', 'subnet', '8.8.8.0/24', 'accept', 1, 'alice', 0, 'work', '', '', 'basic'),
	                 (1, 55, 'emilia', 'domain', 'youtube.com', 'accept', 1, 'alice', 1, 'media', 'youtube.com', '', 'basic'),
	                 (1, 99, 'emilia', 'subnet', '1.1.1.0/24', 'accept', 1, 'alice', 0, 'other-device', '', '', 'other')`)

	res, err := ReassignDeviceRules(d, 55, 2, "bob")
	if err != nil {
		t.Fatalf("ReassignDeviceRules: %v", err)
	}
	if res.Moved != 2 {
		t.Fatalf("moved = %d, want 2 (only the transferred device's rules)", res.Moved)
	}
	if res.AllDevicesCleared != 1 {
		t.Errorf("all-devices markers cleared = %d, want 1", res.AllDevicesCleared)
	}
	if res.GroupsCleared != 2 {
		t.Errorf("group labels cleared = %d, want 2", res.GroupsCleared)
	}

	rows, err := d.Query(`SELECT user_id, user_name, all_devices, group_label FROM device_rules WHERE device_id = 55 ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var uid int64
		var name, group string
		var allDevices int
		if err := rows.Scan(&uid, &name, &allDevices, &group); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		if uid != 2 || name != "bob" {
			t.Errorf("row %d still belongs to %d/%q — the rules did not follow the device", n, uid, name)
		}
		if allDevices != 0 {
			t.Errorf("row %d kept all_devices=1 — it would fan out onto the NEW owner's devices", n)
		}
		if group != "" {
			t.Errorf("row %d kept the previous owner's group label %q", n, group)
		}
	}
	if n != 2 {
		t.Fatalf("scanned %d rows for the transferred device, want 2", n)
	}

	// The other device's rule must be untouched: a transfer is about ONE node.
	var otherOwner int64
	var otherGroup string
	if err := d.QueryRow(`SELECT user_id, group_label FROM device_rules WHERE device_id = 99`).Scan(&otherOwner, &otherGroup); err != nil {
		t.Fatalf("other device: %v", err)
	}
	if otherOwner != 1 || otherGroup != "other-device" {
		t.Errorf("an unrelated rule was rewritten: owner=%d group=%q", otherOwner, otherGroup)
	}

	// Idempotent: a second transfer moves nothing.
	again, err := ReassignDeviceRules(d, 55, 2, "bob")
	if err != nil {
		t.Fatalf("second ReassignDeviceRules: %v", err)
	}
	if again.Moved != 0 {
		t.Errorf("a repeated transfer moved %d row(s) again", again.Moved)
	}
}

// TestB443_TransferOfADeviceWithNoRulesIsQuiet pins the empty case: an adopted node has no
// rules yet, and reporting a failure there would make the common adoption path look broken.
func TestB443_TransferOfADeviceWithNoRulesIsQuiet(t *testing.T) {
	d := b443Open(t)
	res, err := ReassignDeviceRules(d, 12345, 7, "someone")
	if err != nil {
		t.Fatalf("ReassignDeviceRules on an empty device: %v", err)
	}
	if res.Moved != 0 {
		t.Errorf("moved = %d, want 0", res.Moved)
	}
}
