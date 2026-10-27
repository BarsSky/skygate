// internal/db/sqlite_foreign_keys_b427_test.go — B427 (TD-35).
//
// TD-35 was recorded when B426.1 found that the per-test SQLite handle had lost
// busy_timeout: openSQLiteWith appends foreign_keys(1) + journal_mode(WAL) +
// busy_timeout(2000) to a FILE DSN, but it TRUSTS a DSN that already carries `_pragma=`,
// so B419's fast pragmas replaced the whole production set. The same replacement dropped
// foreign_keys(1) — and the `:memory:` branch had no pragmas at all. Consequence: every
// per-test database since v1.5.165 enforced NO foreign key, so all ~40 `FOREIGN KEY … ON
// DELETE CASCADE` clauses in the schema were lint, and a break that PostgreSQL would have
// refused could not fail a test.
//
// The tests below are BEHAVIOURAL on purpose (a DSN string match proves nothing about what
// the driver did with it): they read the pragma back from the live connection AND try the
// two inserts a schema with foreign keys distinguishes, and one contract proves the
// detector can fail by opening a handle WITHOUT the pragma and watching the same orphan
// insert succeed.
package db

import (
	"database/sql"
	"strings"
	"testing"
)

// b427OrphanInsert returns the error from inserting a device_rule whose owner does not
// exist — the smallest statement the schema distinguishes by its foreign key.
func b427OrphanInsert(t *testing.T, d *sql.DB) error {
	t.Helper()
	_, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value)
		VALUES (9999, 1, 'exit-node-vps', 'domain', 'openai.com')`)
	return err
}

func b427Migrated(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	_, d, err := OpenWithDialect(dsn)
	if err != nil {
		t.Fatalf("open %q: %v", dsn, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate %q: %v", dsn, err)
	}
	return d
}

// TestB427_FastHandleEnforcesForeignKeys: the B419/B426.1 fast handle keeps the
// constraint production keeps.
func TestB427_FastHandleEnforcesForeignKeys(t *testing.T) {
	d, err := OpenFastSQLiteForTest(t.TempDir() + "/fk.db")
	if err != nil {
		t.Fatalf("open fast test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// (a) the pragma is ON according to the connection itself, not according to the DSN.
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("PRAGMA foreign_keys on the fast per-test handle = %d, want 1 — the schema's foreign keys are not enforced", fk)
	}

	// (b) the two inserts a schema with foreign keys must distinguish.
	err = b427OrphanInsert(t, d)
	if err == nil {
		t.Fatal("a device_rule whose owner does not exist was ACCEPTED — the foreign key is not enforced")
	}
	if !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("the orphan insert failed for the wrong reason: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash) VALUES (1, 'daniil', 'x')`); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value)
		VALUES (1, 1, 'exit-node-vps', 'domain', 'openai.com')`); err != nil {
		t.Fatalf("a device_rule whose owner EXISTS was refused: %v", err)
	}
}

// TestB427_MemoryHandleEnforcesForeignKeys: `:memory:` — the DSN most of the suite uses —
// used to get no pragmas at all (measured: foreign_keys=0, busy_timeout=0).
func TestB427_MemoryHandleEnforcesForeignKeys(t *testing.T) {
	d := b427Migrated(t, ":memory:")
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Fatalf("PRAGMA foreign_keys on a :memory: handle = %d, want 1 — every test built on this DSN runs against a schema without foreign keys", fk)
	}
	if err := b427OrphanInsert(t, d); err == nil {
		t.Fatal("a :memory: database accepted an orphan device_rule")
	}
}

// TestB427_DetectorProvesTheDifference: a handle opened with ONLY the two B419 pragmas —
// i.e. exactly the DSN this block fixed — accepts the orphan. Without this half the check
// above could pass for the wrong reason (a schema that has no constraint at all).
func TestB427_DetectorProvesTheDifference(t *testing.T) {
	d := b427Migrated(t, t.TempDir()+"/nofk.db?_pragma=synchronous(0)&_pragma=journal_mode(memory)")
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read PRAGMA foreign_keys: %v", err)
	}
	if fk != 0 {
		t.Fatalf("the detector handle reports foreign_keys=%d, so this proof proves nothing", fk)
	}
	if err := b427OrphanInsert(t, d); err != nil {
		t.Fatalf("the detector handle refused the orphan (%v), so the enforcement tests could pass without the fix", err)
	}
}

// TestB427_SetClusterDatabaseWritesNullPrimaryNode: the write that foreign keys exposed.
// primary_node_id is the row's only nullable column; writing "" is a FK pointing at a node
// with an empty id, which PostgreSQL and a foreign-key-enabled SQLite both refuse.
func TestB427_SetClusterDatabaseWritesNullPrimaryNode(t *testing.T) {
	d, err := OpenFastSQLiteForTest(t.TempDir() + "/cd.db")
	if err != nil {
		t.Fatalf("open fast test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging', 'skygate-staging', '[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}

	// (a) "no primary chosen yet" must be storable, and must read back as empty.
	if err := SetClusterDatabase(d, &ClusterDatabase{
		ID: "skygate-staging", ClusterID: "skygate-staging",
		DSNTemplate: "postgres://skygate:%s@primary:5432/skygate?sslmode=disable",
		DBName:      "skygate", Username: "skygate", SSLMode: "disable",
	}); err != nil {
		t.Fatalf("SetClusterDatabase with no primary node: %v — the /admin/database apply button cannot store its row", err)
	}
	var nullCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM cluster_database WHERE primary_node_id IS NULL`).Scan(&nullCount); err != nil {
		t.Fatalf("count NULL primary_node_id: %v", err)
	}
	if nullCount != 1 {
		t.Fatalf("primary_node_id rows that are NULL = %d, want 1 — an empty string is a foreign key to a node with an empty id", nullCount)
	}
	row, err := GetClusterDatabase(d, "skygate-staging")
	if err != nil {
		t.Fatalf("GetClusterDatabase: %v", err)
	}
	if row.PrimaryNodeID != "" {
		t.Fatalf("PrimaryNodeID read back as %q, want empty (COALESCE of NULL)", row.PrimaryNodeID)
	}

	// (b) a primary node that does NOT exist must still be refused — proof the constraint
	// is what the fix works WITH, not something it switched off.
	if err := SetClusterDatabase(d, &ClusterDatabase{
		ID: "skygate-staging", ClusterID: "skygate-staging",
		PrimaryNodeID: "node-that-does-not-exist",
	}); err == nil {
		t.Fatal("SetClusterDatabase accepted a primary_node_id that does not exist in cluster_node")
	}
}
