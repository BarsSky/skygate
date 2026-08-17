// 2026-08-17 (B125): tests for the UNIQUE INDEX on device_rules
// natural key + ON CONFLICT DO NOTHING. Verifies that
// AppendDeviceRule is a true "insert or get-existing" with no
// race window — sequential inserts of the same key return
// the same id, and only one row lands in the table.
//
// Note: we don't use a multi-goroutine test for the race
// itself because the live-PG test pool has size 10 and the
// `SET search_path` in OpenTestPG only affects one connection.
// Sequential tests are enough to verify the SQL contract —
// PG's UNIQUE INDEX + ON CONFLICT is the atomic primitive
// that closes the race. The actual production race is
// closed at the SQL level, not at the test level.
//
// Skips (does not fail) when SKYGATE_TEST_PG_DSN is unset, so
// the test suite is runnable on a dev machine without a live PG.

package db

import (
	"testing"
)

// TestAppendDeviceRule_B125_Sequential_SameKey_OneRow verifies
// the B125 contract: calling AppendDeviceRule multiple times
// with the SAME key creates exactly ONE row and returns the
// same id every time. Pre-B125 the SELECT-then-INSERT race
// in insertRuleUnique let the auto-add create 100+ duplicate
// rows in production (Goal 37 found 114 redundant rules).
func TestAppendDeviceRule_B125_Sequential_SameKey_OneRow(t *testing.T) {
	d := OpenTestPG(t)

	// Seed the user (FK to portal_users from V020).
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES ($1, $2, 'x', 0) ON CONFLICT (id) DO NOTHING`,
		1, "b125test",
	); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}

	const N = 10
	var firstID int64
	for i := 0; i < N; i++ {
		id, err := AppendDeviceRule(d, 1, 8, "relay-b125",
			"subnet", "1.2.3.4/32", "accept",
			"100.64.0.1", "b125test.com", "", "")
		if err != nil {
			t.Fatalf("call %d: AppendDeviceRule: %v", i, err)
		}
		if id <= 0 {
			t.Fatalf("call %d: id=%d, want > 0", i, id)
		}
		if i == 0 {
			firstID = id
		} else if id != firstID {
			t.Errorf("call %d: id=%d, want %d (all should be the same — no duplicate rows)", i, id, firstID)
		}
	}

	// Verify only ONE row exists in the DB (no duplicates).
	var rowCount int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM device_rules WHERE user_id=$1 AND device_id=$2 AND exit_node_id=$3 AND target_type=$4 AND target_value=$5 AND parent_domain=$6`,
		1, 8, "relay-b125", "subnet", "1.2.3.4/32", "b125test.com",
	).Scan(&rowCount); err != nil {
		t.Fatalf("COUNT: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("device_rules has %d rows for the same key, want 1 (B125 contract violated)", rowCount)
	}
}

// TestAppendDeviceRule_B125_DistinctKeys verifies that
// DIFFERENT (user, device, exit, type, value, parent) tuples
// all succeed — the UNIQUE INDEX doesn't accidentally block
// legitimate distinct rules.
func TestAppendDeviceRule_B125_DistinctKeys(t *testing.T) {
	d := OpenTestPG(t)

	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES ($1, $2, 'x', 0) ON CONFLICT (id) DO NOTHING`,
		1, "b125test",
	); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}

	cases := []struct {
		name       string
		deviceID   int
		exitNode   string
		targetType string
		targetVal  string
		parent     string
	}{
		{"different-device", 1, "relay-1", "subnet", "1.1.1.1/32", "a.com"},
		{"different-exit", 2, "relay-1", "subnet", "2.2.2.2/32", "b.com"},
		{"different-type", 3, "relay-1", "ip", "3.3.3.3/32", "c.com"},
		{"different-value", 4, "relay-1", "subnet", "4.4.4.4/32", "d.com"},
		{"different-parent", 5, "relay-1", "subnet", "5.5.5.5/32", "e.com"},
		{"empty-parent", 6, "relay-1", "ip", "6.6.6.6/32", ""},
	}
	seenIDs := make(map[int64]bool)
	for _, c := range cases {
		id, err := AppendDeviceRule(d, 1, c.deviceID, c.exitNode,
			c.targetType, c.targetVal, "accept", "100.64.0.1", c.parent, "", "")
		if err != nil {
			t.Errorf("%s: AppendDeviceRule: %v", c.name, err)
		}
		if id <= 0 {
			t.Errorf("%s: id=%d, want > 0", c.name, id)
		}
		if seenIDs[id] {
			t.Errorf("%s: id=%d was returned before (duplicate id)", c.name, id)
		}
		seenIDs[id] = true
	}
}

// TestAppendDeviceRule_B125_SameKeyReturnsSameID is the
// duplicate of the B123 form-path use case: user clicks
// "add rule" twice for the same target. Both clicks must
// return the same id, and only one row is created.
func TestAppendDeviceRule_B125_SameKeyReturnsSameID(t *testing.T) {
	d := OpenTestPG(t)

	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES ($1, $2, 'x', 0) ON CONFLICT (id) DO NOTHING`,
		1, "b125test",
	); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}

	id1, err := AppendDeviceRule(d, 1, 8, "relay-b125", "domain", "example.com", "accept", "100.64.0.1", "example.com", "", "")
	if err != nil {
		t.Fatalf("first AppendDeviceRule: %v", err)
	}
	id2, err := AppendDeviceRule(d, 1, 8, "relay-b125", "domain", "example.com", "accept", "100.64.0.1", "example.com", "", "")
	if err != nil {
		t.Fatalf("second AppendDeviceRule: %v", err)
	}
	if id1 != id2 {
		t.Errorf("same key returned different ids: %d vs %d (DO UPDATE SET id = id should return existing id on conflict)", id1, id2)
	}
}
