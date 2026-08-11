package db

// 2026-08-09: v0.33.1.19 — via_enabled column repair migration
// tests.
//
// SetUserExitNodePref (migrations_v0.45.go) and
// SetDeviceExitNodePref (migrations_v0.46.go) had a
// positional-mismatch bug in their INSERT clause. As a
// result, every row inserted by the v0.28.5 — v0.33.1.18
// code path had updated_at=0/1 and via_enabled=<unix
// timestamp>. The v0.33.1.19 migration (migrations_v0.52.go)
// walks both tables and swaps the two columns when the
// discriminant (updated_at in {0,1} AND via_enabled > 1e9)
// is satisfied.
//
// These tests pin:
//   1. Corrupt row (via_enabled=timestamp, updated_at=0/1)
//      is repaired: after migration, via_enabled=0/1 and
//      updated_at=<the original timestamp>.
//   2. Already-correct row (via_enabled=0/1, updated_at=<real
//      timestamp>) is left alone.
//   3. Idempotency: running the migration twice is a no-op.

import (
	"testing"
)

// TestMigrateV052_RepairsCorruptUserPref pins the v0.33.1.19
// data repair for user_exit_node_prefs.
func TestMigrateV052_RepairsCorruptUserPref(t *testing.T) {
	d := openTestDB(t)
	// Pre-seed: portal_users row (FK target) + a corrupt
	// user_exit_node_prefs row. The pre-fix INSERT would
	// have written via_enabled=1785949412 (timestamp) and
	// updated_at=0 (was supposed to be timestamp). Insert
	// the row directly in the corrupt shape to simulate
	// production state from a v0.28.5 — v0.33.1.18 run.
	//
	// 2026-08-10 v0.33.1.41 #2: V054 inserts the infra row
	// at reserved id=99, so AUTOINCREMENT is no longer at
	// 1 for fresh test DBs. Pin id=1 explicitly so the
	// test's user_id=1 FK references match.
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'alice', '', 0, 'linear')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		 VALUES (1, 'tag:exit-emilia', 1, 0, 1785949412)`,
	); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}

	if err := migrateV052(d); err != nil {
		t.Fatalf("migrateV052: %v", err)
	}

	var updatedAt, viaEnabled int64
	if err := d.QueryRow(
		`SELECT updated_at, via_enabled FROM user_exit_node_prefs WHERE user_id = 1`,
	).Scan(&updatedAt, &viaEnabled); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if updatedAt != 1785949412 {
		t.Errorf("updated_at not repaired: got %d, want 1785949412 (the mis-swapped timestamp)", updatedAt)
	}
	if viaEnabled != 0 {
		t.Errorf("via_enabled not repaired: got %d, want 0 (the original viaInt)", viaEnabled)
	}
}

// TestMigrateV052_RepairsCorruptDevicePref — same as the
// user test, but for device_exit_node_prefs.
func TestMigrateV052_RepairsCorruptDevicePref(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly (V054
	// reserves id=99 for the infra user, so AUTOINCREMENT
	// no longer starts at 1).
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'alice', '', 0, 'linear')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		 VALUES (1, 'skyworker', 'tag:exit-karolina', 1, 0, 1786000777)`,
	); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}

	if err := migrateV052(d); err != nil {
		t.Fatalf("migrateV052: %v", err)
	}

	var updatedAt, viaEnabled int64
	if err := d.QueryRow(
		`SELECT updated_at, via_enabled FROM device_exit_node_prefs WHERE user_id = 1 AND device_hostname = 'skyworker'`,
	).Scan(&updatedAt, &viaEnabled); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if updatedAt != 1786000777 {
		t.Errorf("updated_at not repaired: got %d, want 1786000777", updatedAt)
	}
	if viaEnabled != 0 {
		t.Errorf("via_enabled not repaired: got %d, want 0", viaEnabled)
	}
}

// TestMigrateV052_LeavesCorrectRowsAlone — a row that was
// inserted with the corrected INSERT (e.g. by a future-
// correct code path, or by a manual operator fix) must NOT
// be touched. The discriminant updated_at in {0,1} AND
// via_enabled > 1e9 ensures this: a real updated_at of
// 1.7e9 would be > 1e9, but the WHERE also requires
// updated_at in {0,1}, so it's skipped.
func TestMigrateV052_LeavesCorrectRowsAlone(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly (V054
	// reserves id=99 for the infra user).
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'alice', '', 0, 'linear')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Already-correct shape: updated_at=1785949412 (real
	// timestamp), via_enabled=1 (strict mode).
	if _, err := d.Exec(
		`INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		 VALUES (1, 'tag:exit-emilia', 1, 1785949412, 1)`,
	); err != nil {
		t.Fatalf("seed correct row: %v", err)
	}

	if err := migrateV052(d); err != nil {
		t.Fatalf("migrateV052: %v", err)
	}

	var updatedAt, viaEnabled int64
	if err := d.QueryRow(
		`SELECT updated_at, via_enabled FROM user_exit_node_prefs WHERE user_id = 1`,
	).Scan(&updatedAt, &viaEnabled); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if updatedAt != 1785949412 {
		t.Errorf("correct updated_at was clobbered: got %d, want 1785949412", updatedAt)
	}
	if viaEnabled != 1 {
		t.Errorf("correct via_enabled was clobbered: got %d, want 1", viaEnabled)
	}
}

// TestMigrateV052_Idempotent — running the migration twice
// must be a no-op. The second run finds no corrupt rows
// (the first run fixed them), so nothing changes.
func TestMigrateV052_Idempotent(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly.
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'alice', '', 0, 'linear')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		 VALUES (1, 'tag:exit-emilia', 1, 0, 1785949412)`,
	); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}

	// Run twice.
	if err := migrateV052(d); err != nil {
		t.Fatalf("first migrateV052: %v", err)
	}
	if err := migrateV052(d); err != nil {
		t.Fatalf("second migrateV052: %v", err)
	}

	var updatedAt, viaEnabled int64
	if err := d.QueryRow(
		`SELECT updated_at, via_enabled FROM user_exit_node_prefs WHERE user_id = 1`,
	).Scan(&updatedAt, &viaEnabled); err != nil {
		t.Fatalf("readback: %v", err)
	}
	// After the first migration: updated_at=1785949412,
	// via_enabled=0. The second migration must not flip
	// them again (the WHERE clause skips the row because
	// updated_at is no longer in {0,1}).
	if updatedAt != 1785949412 || viaEnabled != 0 {
		t.Errorf("idempotency violated: updated_at=%d via_enabled=%d, want (1785949412, 0)", updatedAt, viaEnabled)
	}
}

// TestMigrateV052_Threshold guards the discriminant
// constant. A legitimate via_enabled=0 (advisory mode)
// paired with updated_at=0 (which can happen on a fresh
// row that was just inserted) must NOT be flipped. The
// threshold 1e9 catches this: via_enabled=0 is not > 1e9.
func TestMigrateV052_Threshold(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly.
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'alice', '', 0, 'linear')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Both columns at 0: legitimate "fresh, never-set"
	// state. Must NOT be touched.
	if _, err := d.Exec(
		`INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		 VALUES (1, 'tag:exit-emilia', 1, 0, 0)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := migrateV052(d); err != nil {
		t.Fatalf("migrateV052: %v", err)
	}

	var updatedAt, viaEnabled int64
	if err := d.QueryRow(
		`SELECT updated_at, via_enabled FROM user_exit_node_prefs WHERE user_id = 1`,
	).Scan(&updatedAt, &viaEnabled); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if updatedAt != 0 || viaEnabled != 0 {
		t.Errorf("(0,0) row was touched: got (%d, %d), want (0, 0)", updatedAt, viaEnabled)
	}
}

// TestMigrateV052_DevicePrefMultipleRows — the migration
// must handle multiple rows in a single table (different
// per-device prefs, different users). Each row is repaired
// independently.
func TestMigrateV052_DevicePrefMultipleRows(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.41 #2: pin id=1 explicitly.
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin, theme) VALUES (1, 'alice', '', 0, 'linear')`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// 3 rows: 2 corrupt (different timestamps) + 1 already
	// correct. Migration must repair the 2 and leave the
	// 1 alone.
	rows := []struct {
		host    string
		tag     string
		upd     int64
		via     int64
		corrupt bool
	}{
		{"skyworker", "tag:exit-karolina", 0, 1786000777, true},
		{"cyborg", "tag:exit-emilia", 1, 1785964725, true},
		{"msi", "tag:exit-karolina", 1783610534, 1, false}, // already correct
	}
	for _, r := range rows {
		if _, err := d.Exec(
			`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
			 VALUES (1, $1, $2, 1, $3, $4)`,
			r.host, r.tag, r.upd, r.via,
		); err != nil {
			t.Fatalf("seed %s: %v", r.host, err)
		}
	}

	if err := migrateV052(d); err != nil {
		t.Fatalf("migrateV052: %v", err)
	}

	// Readback and verify.
	for _, r := range rows {
		var upd, via int64
		if err := d.QueryRow(
			`SELECT updated_at, via_enabled FROM device_exit_node_prefs WHERE user_id = 1 AND device_hostname = $1`,
			r.host,
		).Scan(&upd, &via); err != nil {
			t.Fatalf("readback %s: %v", r.host, err)
		}
		if r.corrupt {
			// Should be swapped: updated_at=original via, via_enabled=original upd
			if upd != r.via {
				t.Errorf("corrupt %s: updated_at=%d, want %d (was via_enabled)", r.host, upd, r.via)
			}
			if via != r.upd {
				t.Errorf("corrupt %s: via_enabled=%d, want %d (was updated_at)", r.host, via, r.upd)
			}
		} else {
			// Should be untouched.
			if upd != r.upd || via != r.via {
				t.Errorf("correct %s was touched: got (%d, %d), want (%d, %d)", r.host, upd, via, r.upd, r.via)
			}
		}
	}
}
