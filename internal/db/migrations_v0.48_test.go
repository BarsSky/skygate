// 2026-07-29: v0.31.x — per-device OS + device_type
// migration. Adds 2 columns to node_owner_map with
// 'unknown' defaults. Idempotent on re-run (catches
// the duplicate-column error).
//
// The migration is small and self-contained so the
// test runs in <1s against an in-memory SQLite.

package db

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigrateV048_AddsOSAndDeviceTypeColumns runs the
// v0.48 migration against a fresh in-memory DB and
// asserts that node_owner_map has both new columns.
func TestMigrateV048_AddsOSAndDeviceTypeColumns(t *testing.T) {
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	// Build a minimal node_owner_map with the
	// pre-v0.48 columns only. We don't run the full
	// migrate() path because that pulls in 47
	// other migrations. A focused test is enough.
	if _, err := d.Exec(`
		CREATE TABLE node_owner_map (
			node_id TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			tag TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			hostname TEXT NOT NULL DEFAULT ''
		)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Insert one row to test the default.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, hostname) VALUES (?, ?)`, "node-1", "DESKTOP-XYZ"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Run v0.48.
	if err := migrateV048(d); err != nil {
		t.Fatalf("migrateV048: %v", err)
	}

	// Both columns must exist.
	for _, col := range []string{"os", "device_type"} {
		var n int
		if err := d.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('node_owner_map') WHERE name = ?`,
			col,
		).Scan(&n); err != nil {
			t.Fatalf("pragma %s: %v", col, err)
		}
		if n != 1 {
			t.Errorf("expected column %q in node_owner_map after v0.48, got count=%d", col, n)
		}
	}

	// Default value: existing rows must get 'unknown' for both.
	var os, dt string
	if err := d.QueryRow(
		`SELECT os, device_type FROM node_owner_map WHERE node_id = ?`, "node-1",
	).Scan(&os, &dt); err != nil {
		t.Fatalf("select: %v", err)
	}
	if os != "unknown" {
		t.Errorf("default os = %q, want 'unknown'", os)
	}
	if dt != "unknown" {
		t.Errorf("default device_type = %q, want 'unknown'", dt)
	}

	// Idempotency: run v0.48 again, no error.
	if err := migrateV048(d); err != nil {
		t.Errorf("migrateV048 second run: %v", err)
	}
}

// TestSetDeviceMetaNodeOwner_UpdatesRow exercises the
// admin override path: set os + device_type, read
// back, confirm the row was updated.
func TestSetDeviceMetaNodeOwner_UpdatesRow(t *testing.T) {
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if _, err := d.Exec(`
		CREATE TABLE node_owner_map (
			node_id TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			tag TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			hostname TEXT NOT NULL DEFAULT '',
			os TEXT NOT NULL DEFAULT 'unknown',
			device_type TEXT NOT NULL DEFAULT 'unknown'
		)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id) VALUES ('node-1')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := SetDeviceMetaNodeOwner(d, "node-1", "windows", "client"); err != nil {
		t.Fatalf("SetDeviceMetaNodeOwner: %v", err)
	}

	var os, dt string
	if err := d.QueryRow(`SELECT os, device_type FROM node_owner_map WHERE node_id='node-1'`).Scan(&os, &dt); err != nil {
		t.Fatalf("select: %v", err)
	}
	if os != "windows" || dt != "client" {
		t.Errorf("got os=%q dt=%q, want os=windows dt=client", os, dt)
	}
}

// TestUpdateDeviceMetaAutoDetect_RespectsManualOverride
// exercises the "first auto-detect wins" rule. After
// the admin sets explicit values, the auto-detect
// UPDATE must be a no-op.
func TestUpdateDeviceMetaAutoDetect_RespectsManualOverride(t *testing.T) {
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if _, err := d.Exec(`
		CREATE TABLE node_owner_map (
			node_id TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			tag TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			hostname TEXT NOT NULL DEFAULT '',
			os TEXT NOT NULL DEFAULT 'unknown',
			device_type TEXT NOT NULL DEFAULT 'unknown'
		)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Two rows. One is the "auto-detect eligible" state
	// (both columns are 'unknown' / the default). The
	// other is the "admin manually set" state (operator
	// already set 'windows').
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, os, device_type) VALUES ('auto', 'unknown', 'unknown')`); err != nil {
		t.Fatalf("insert auto: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, os, device_type) VALUES ('manual', 'windows', 'client')`); err != nil {
		t.Fatalf("insert manual: %v", err)
	}

	// Auto-detect runs and would assign 'linux' / 'client'
	// to both. The WHERE clause must skip the manual row.
	if err := UpdateDeviceMetaAutoDetect(d, "auto", "linux", "client"); err != nil {
		t.Fatalf("UpdateDeviceMetaAutoDetect(auto): %v", err)
	}
	if err := UpdateDeviceMetaAutoDetect(d, "manual", "linux", "client"); err != nil {
		t.Fatalf("UpdateDeviceMetaAutoDetect(manual): %v", err)
	}

	// 'auto' should now be 'linux' / 'client'.
	var os, dt string
	if err := d.QueryRow(`SELECT os, device_type FROM node_owner_map WHERE node_id='auto'`).Scan(&os, &dt); err != nil {
		t.Fatalf("select auto: %v", err)
	}
	if os != "linux" || dt != "client" {
		t.Errorf("auto: got os=%q dt=%q, want os=linux dt=client", os, dt)
	}

	// 'manual' should still be 'windows' / 'client' (admin
	// override preserved).
	if err := d.QueryRow(`SELECT os, device_type FROM node_owner_map WHERE node_id='manual'`).Scan(&os, &dt); err != nil {
		t.Fatalf("select manual: %v", err)
	}
	if os != "windows" || dt != "client" {
		t.Errorf("manual: got os=%q dt=%q, want os=windows dt=client (admin override preserved)", os, dt)
	}
}

// TestListDeviceMetaMissing_FindsUnknownRows confirms the
// "needs auto-detect" query returns the right set.
func TestListDeviceMetaMissing_FindsUnknownRows(t *testing.T) {
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()

	if _, err := d.Exec(`
		CREATE TABLE node_owner_map (
			node_id TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			tag TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			hostname TEXT NOT NULL DEFAULT '',
			os TEXT NOT NULL DEFAULT 'unknown',
			device_type TEXT NOT NULL DEFAULT 'unknown'
		)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// One row needs auto-detect, one is admin-set.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id) VALUES ('needs-detect')`); err != nil {
		t.Fatalf("insert needs: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, os, device_type) VALUES ('admin-set', 'android', 'phone')`); err != nil {
		t.Fatalf("insert admin: %v", err)
	}
	// One row has only os set (partial state — should
	// still show up as "needs detect" because the
	// device_type side is 'unknown').
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, os) VALUES ('partial', 'linux')`); err != nil {
		t.Fatalf("insert partial: %v", err)
	}

	rows, err := ListDeviceMetaMissing(d)
	if err != nil {
		t.Fatalf("ListDeviceMetaMissing: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.NodeID)
	}
	if len(ids) != 2 {
		t.Errorf("got %d missing rows, want 2 (ids=%v)", len(ids), ids)
	}
	if !containsAll(ids, "needs-detect") || !containsAll(ids, "partial") {
		t.Errorf("expected 'needs-detect' and 'partial' in missing set, got %v", ids)
	}
	if containsAll(ids, "admin-set") {
		t.Errorf("admin-set should not be in missing set, got %v", ids)
	}
}

// containsAll is a tiny helper so the test doesn't
// import strings just for one substring check.
func containsAll(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}

// Compile-time guard: keep the unused import warning
// happy if the file's import list shrinks during
// future edits.
var _ = strings.TrimSpace
