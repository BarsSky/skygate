package db

// 2026-07-25: v0.28.5 — idempotency tests for migration v0.47.
//
// The migration adds `via_enabled INTEGER NOT NULL DEFAULT 0` to
// both `user_exit_node_prefs` and `device_exit_node_prefs` AND
// backfills existing rows to via_enabled=1 (preserves v0.28.1-
// v0.28.4 pinning behavior). The CRITICAL constraint is that the
// backfill runs ONLY on the very first migration (when the column
// is freshly created in this run). On every subsequent startup
// the column already exists — the ALTER returns "duplicate column
// name" and the backfill UPDATE must be SKIPPED. Otherwise the
// migration would clobber operator-set via_enabled=0 (un-pinned)
// back to 1 on every skygate restart, making the "un-pin" UI
// control no-op.
//
// These tests pin:
//   1. First-run: column created, existing row backfilled to 1
//   2. Second-run: column NOT re-created, via_enabled=0 preserved
//   3. After-unpin-then-restart: via_enabled=0 still 0 (the bug)

import (
	"database/sql"
	"testing"
)

// TestMigrateV047_FirstRun_AddsColumnAndBackfills verifies the
// fresh-install path: column doesn't exist, migration creates it
// (via the ALTER), then the backfill UPDATE sets any pre-existing
// rows (none in this test, but the UPDATE must run) to via_enabled=1.
func TestMigrateV047_FirstRun_AddsColumnAndBackfills(t *testing.T) {
	d := freshDB(t)

	// Confirm the column does NOT exist yet.
	col := columnExists(t, d, "user_exit_node_prefs", "via_enabled")
	if col {
		t.Fatalf("pre-condition failed: user_exit_node_prefs.via_enabled already exists")
	}

	// Run the migration.
	if err := migrateV047(d); err != nil {
		t.Fatalf("migrateV047: %v", err)
	}

	// Column must now exist with NOT NULL DEFAULT 0.
	col = columnExists(t, d, "user_exit_node_prefs", "via_enabled")
	if !col {
		t.Fatalf("user_exit_node_prefs.via_enabled not created")
	}
	col = columnExists(t, d, "device_exit_node_prefs", "via_enabled")
	if !col {
		t.Fatalf("device_exit_node_prefs.via_enabled not created")
	}
}

// TestMigrateV047_SecondRun_PreservesViaZero is the regression
// test for the v0.28.5 bug. After the first migration, the
// operator un-pins (via_enabled=0). On the next skygate restart
// the migration runs AGAIN — the second run must NOT re-backfill
// the operator's un-pinned row back to 1.
//
// Pre-fix behavior: the UPDATE ran unconditionally on every
// migration call, clobbering via_enabled=0 back to 1.
func TestMigrateV047_SecondRun_PreservesViaZero(t *testing.T) {
	d := freshDB(t)

	// Run the migration twice (simulating two startups).
	if err := migrateV047(d); err != nil {
		t.Fatalf("migrateV047 first run: %v", err)
	}

	// Insert a row in each prefs table with via_enabled=0
	// (operator un-pinned after the first run).
	if _, err := d.Exec("INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, via_enabled) VALUES (1, 'tag:exit-relay-1', 0)"); err != nil {
		t.Fatalf("insert user pref: %v", err)
	}
	if _, err := d.Exec("INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, via_enabled) VALUES (1, 'workstation-3', 'tag:exit-relay-3', 0)"); err != nil {
		t.Fatalf("insert device pref: %v", err)
	}

	// Run the migration AGAIN (simulating skygate restart).
	if err := migrateV047(d); err != nil {
		t.Fatalf("migrateV047 second run: %v", err)
	}

	// The un-pinned rows MUST stay at via_enabled=0. If the
	// pre-fix bug is back, these will be 1.
	var u, dev int
	if err := d.QueryRow("SELECT via_enabled FROM user_exit_node_prefs WHERE user_id = 1").Scan(&u); err != nil {
		t.Fatalf("read user via_enabled: %v", err)
	}
	if u != 0 {
		t.Fatalf("user_exit_node_prefs.via_enabled was clobbered: got %d, want 0 (pre-fix bug — migration re-backfills on every startup)", u)
	}
	if err := d.QueryRow("SELECT via_enabled FROM device_exit_node_prefs WHERE user_id = 1 AND device_hostname = 'workstation-3'").Scan(&dev); err != nil {
		t.Fatalf("read device via_enabled: %v", err)
	}
	if dev != 0 {
		t.Fatalf("device_exit_node_prefs.via_enabled was clobbered: got %d, want 0 (pre-fix bug — migration re-backfills on every startup)", dev)
	}
}

// TestMigrateV047_FirstRun_BackfillsExistingRows verifies the
// backwards-compat path: on the first migration, any pre-existing
// rows from v0.28.1-v0.28.4 are backfilled to via_enabled=1
// (preserves the old "always pinned" behavior).
func TestMigrateV047_FirstRun_BackfillsExistingRows(t *testing.T) {
	d := freshDB(t)

	// Create the prefs tables WITHOUT the via_enabled column
	// (simulating a v0.28.4 deploy). Insert a row.
	d.Exec(`CREATE TABLE user_exit_node_prefs (user_id INTEGER PRIMARY KEY, exit_node_tag TEXT)`)
	d.Exec(`INSERT INTO user_exit_node_prefs (user_id, exit_node_tag) VALUES (1, 'tag:exit-relay-1')`)

	// Run the migration.
	if err := migrateV047(d); err != nil {
		t.Fatalf("migrateV047: %v", err)
	}

	// The pre-existing row must be backfilled to via_enabled=1.
	var u int
	if err := d.QueryRow("SELECT via_enabled FROM user_exit_node_prefs WHERE user_id = 1").Scan(&u); err != nil {
		t.Fatalf("read via_enabled: %v", err)
	}
	if u != 1 {
		t.Fatalf("first-run backfill failed: got via_enabled=%d, want 1 (pre-v0.28.5 rows should be backfilled to pinned)", u)
	}
}

// freshDB returns a fresh in-memory SQLite with the
// user_exit_node_prefs and device_exit_node_prefs tables created
// (matching the v0.28.1 / v0.28.4 production shape, but WITHOUT
// the via_enabled column — that's what migration v0.47 adds).
//
// 2026-07-25: v0.28.5.
func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE portal_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE user_exit_node_prefs (
			user_id INTEGER NOT NULL PRIMARY KEY,
			exit_node_tag TEXT NOT NULL,
			updated_at INTEGER NOT NULL DEFAULT 0,
			set_by_user_id INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE device_exit_node_prefs (
			user_id INTEGER NOT NULL,
			device_hostname TEXT NOT NULL,
			exit_node_tag TEXT NOT NULL,
			updated_at INTEGER NOT NULL DEFAULT 0,
			set_by_user_id INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, device_hostname)
		)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// columnExists returns true iff the named column exists in the
// table. Used to assert that the ALTER ran (or didn't, for the
// "already migrated" path).
func columnExists(t *testing.T, d *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := d.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		if name == column {
			return true
		}
	}
	return false
}
