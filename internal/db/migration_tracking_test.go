// Tests for migration_tracking.go (v0.32.19, the migration
// integrity feature).
//
// v1.3.0: Previously created a fresh in-memory SQLite via
// newTestDB → sql.Open("sqlite3", ":memory:"). The tests
// themselves are driver-agnostic (the helpers expose a *sql.DB
// and use ? placeholders that go-sqlite3 accepts), so the
// rewrite is mechanical: replace newTestDB with openTestDB
// (which is now PG-backed) and switch ? → $N in the test
// SQL strings. The test bodies themselves don't need to
// change beyond the fixture.
package db

import (
	"strings"
	"testing"
)

// openTestDB is now an alias for OpenTestPG (see db_test.go).
// Tests below use it for a fresh PG DB with the full migration
// chain applied (the applied_migrations table is created by
// ensureMigrationTrackingTable → migrateV049).

func TestComputeMigrationChecksum_Deterministic(t *testing.T) {
	sql1 := `CREATE TABLE foo (id INTEGER PRIMARY KEY, name TEXT)`
	sql2 := `   CREATE    TABLE   foo
	            (id INTEGER PRIMARY KEY, name TEXT)`
	c1 := ComputeMigrationChecksum(sql1)
	c2 := ComputeMigrationChecksum(sql2)
	if c1 != c2 {
		t.Errorf("checksum should be stable across whitespace; got %s vs %s", c1[:12], c2[:12])
	}
	if len(c1) != 64 {
		t.Errorf("sha256 hex should be 64 chars, got %d (%q)", len(c1), c1)
	}
}

func TestComputeMigrationChecksum_ChangesWithSemantics(t *testing.T) {
	sql1 := `ALTER TABLE foo ADD COLUMN bar TEXT NOT NULL DEFAULT ''`
	sql2 := `ALTER TABLE foo ADD COLUMN bar TEXT NOT NULL DEFAULT 'x'`
	c1 := ComputeMigrationChecksum(sql1)
	c2 := ComputeMigrationChecksum(sql2)
	if c1 == c2 {
		t.Errorf("different DEFAULT should change checksum; both = %s", c1[:12])
	}
}

func TestEnsureMigrationTrackingTable_Idempotent(t *testing.T) {
	d := openTestDB(t)
	for i := 0; i < 3; i++ {
		if err := ensureMigrationTrackingTable(d); err != nil {
			t.Fatalf("ensure #%d: %v", i, err)
		}
	}
	// Verify the table exists by inserting + querying.
	_, err := d.Exec(`INSERT INTO applied_migrations (version, sha256, first_seen) VALUES (1, 'abc', 'v0.32.19')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM applied_migrations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

func TestRecordAndGetMigration(t *testing.T) {
	d := openTestDB(t)
	if err := ensureMigrationTrackingTable(d); err != nil {
		t.Fatal(err)
	}
	// First record: not found, returns empty.
	sha, first, err := GetRecordedMigrationChecksum(d, 42)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if sha != "" || first != "" {
		t.Errorf("first get: expected empty, got sha=%q first=%q", sha, first)
	}
	// Record.
	if err := RecordMigrationApplied(d, 42, "deadbeef", "migrations_v0.42.go", "v0.32.19"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Now: should return the recorded values.
	sha, first, err = GetRecordedMigrationChecksum(d, 42)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if sha != "deadbeef" || first != "v0.32.19" {
		t.Errorf("after record: got sha=%q first=%q, want deadbeef/v0.32.19", sha, first)
	}
	// Re-record: should be a no-op (INSERT OR IGNORE).
	if err := RecordMigrationApplied(d, 42, "cafef00d", "evil_migration.go", "v9.9.9"); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	sha, _, _ = GetRecordedMigrationChecksum(d, 42)
	if sha != "deadbeef" {
		t.Errorf("re-record should be ignored, got sha=%q", sha)
	}
}

func TestVerifyMigrationChecksum_FirstRun(t *testing.T) {
	d := openTestDB(t)
	if err := ensureMigrationTrackingTable(d); err != nil {
		t.Fatal(err)
	}
	ok, recorded, current, err := VerifyMigrationChecksum(d, 99, `CREATE TABLE t (id INTEGER)`)
	if err != nil {
		t.Fatalf("verify first run: %v", err)
	}
	if !ok {
		t.Errorf("first run should be ok=true")
	}
	if recorded != "" {
		t.Errorf("first run: recorded=%q, want empty", recorded)
	}
	if current == "" {
		t.Errorf("first run: current should be set to the new sha")
	}
}

func TestVerifyMigrationChecksum_Match(t *testing.T) {
	d := openTestDB(t)
	if err := ensureMigrationTrackingTable(d); err != nil {
		t.Fatal(err)
	}
	sql := `CREATE TABLE match_test (id INTEGER PRIMARY KEY)`
	sha := ComputeMigrationChecksum(sql)
	if err := RecordMigrationApplied(d, 99, sha, "x.go", "v0.32.19"); err != nil {
		t.Fatal(err)
	}
	ok, recorded, current, err := VerifyMigrationChecksum(d, 99, sql)
	if err != nil {
		t.Fatalf("verify match: %v", err)
	}
	if !ok {
		t.Errorf("match should be ok=true")
	}
	if recorded != current {
		t.Errorf("recorded=%s current=%s should match", recorded[:12], current[:12])
	}
}

func TestVerifyMigrationChecksum_Mismatch_SoftMode(t *testing.T) {
	// Force soft mode (default, but be explicit).
	old := migrationIntegrityMode
	migrationIntegrityMode = IntegrityModeSoft
	t.Cleanup(func() { migrationIntegrityMode = old })

	d := openTestDB(t)
	if err := ensureMigrationTrackingTable(d); err != nil {
		t.Fatal(err)
	}
	// Recorded as one thing, current SQL differs.
	if err := RecordMigrationApplied(d, 99, "0000000000000000000000000000000000000000000000000000000000000000", "x.go", "v0.32.19"); err != nil {
		t.Fatal(err)
	}
	ok, recorded, current, err := VerifyMigrationChecksum(d, 99, `ALTER TABLE t ADD COLUMN x TEXT DEFAULT 'NEW'`)
	if err != nil {
		t.Fatalf("soft mode should not error, got: %v", err)
	}
	if !ok {
		t.Errorf("soft mode: mismatch should be ok=true (just a warning)")
	}
	if recorded == current {
		t.Errorf("soft mode: recorded and current should differ, both = %s", current[:12])
	}
}

func TestVerifyMigrationChecksum_Mismatch_HardMode(t *testing.T) {
	// Force hard mode.
	old := migrationIntegrityMode
	migrationIntegrityMode = IntegrityModeHard
	t.Cleanup(func() { migrationIntegrityMode = old })

	d := openTestDB(t)
	if err := ensureMigrationTrackingTable(d); err != nil {
		t.Fatal(err)
	}
	if err := RecordMigrationApplied(d, 99, "0000000000000000000000000000000000000000000000000000000000000000", "x.go", "v0.32.19"); err != nil {
		t.Fatal(err)
	}
	ok, _, _, err := VerifyMigrationChecksum(d, 99, `ALTER TABLE t ADD COLUMN x TEXT DEFAULT 'NEW'`)
	if ok {
		t.Errorf("hard mode: mismatch should be ok=false")
	}
	if err == nil {
		t.Errorf("hard mode: mismatch should return non-nil error")
	}
	if err != nil && !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("hard mode: error should mention checksum mismatch, got: %v", err)
	}
}

func TestAllMigrationsForAudit_OrderedByVersion(t *testing.T) {
	d := openTestDB(t)
	if err := ensureMigrationTrackingTable(d); err != nil {
		t.Fatal(err)
	}
	// Insert out of order.
	for _, v := range []int{3, 1, 4, 1, 5, 9, 2, 6} {
		if err := RecordMigrationApplied(d, v, "sha"+string(rune('0'+v)), "x.go", "v0.32.19"); err != nil {
			t.Fatal(err)
		}
	}
	records, err := AllMigrationsForAudit(d)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(records) != 7 {
		// 1 is deduped, so 3,1,4,5,9,2,6 = 7
		t.Errorf("records = %d, want 7", len(records))
	}
	// Verify ordering: 1,2,3,4,5,6,9
	for i := 0; i < len(records)-1; i++ {
		if records[i].Version >= records[i+1].Version {
			t.Errorf("not sorted: %d >= %d at index %d", records[i].Version, records[i+1].Version, i)
		}
	}
}

func TestIsIntegrityHard_DefaultAndOverride(t *testing.T) {
	old := migrationIntegrityMode
	defer func() { migrationIntegrityMode = old }()

	// Default: soft, so IsIntegrityHard() = false.
	migrationIntegrityMode = IntegrityModeSoft
	if IsIntegrityHard() {
		t.Errorf("soft mode: IsIntegrityHard() should be false")
	}

	migrationIntegrityMode = IntegrityModeHard
	if !IsIntegrityHard() {
		t.Errorf("hard mode: IsIntegrityHard() should be true")
	}
}
