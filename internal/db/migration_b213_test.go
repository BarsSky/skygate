// v1.5.0+ / B213 — unit tests for the migration
// framework refactor. The B213 changes:
//   - MigrationEntry struct (Version, Name, SourceFile, Run)
//   - pgMigrations slice (replaces the anonymous-function list)
//   - PGMigrations() public getter
//   - RecordMigrationApplied is now ON CONFLICT DO NOTHING
//     (truly idempotent — re-run is safe)
//   - MigratePostgres records each applied migration

package db

import (
	"os"
	"strings"
	"testing"
)

func TestPGMigrations_ReturnsNonEmptyList(t *testing.T) {
	// PGMigrations should return at least one entry.
	// The exact count varies as new B-blocks land, but
	// there should be the historical 47+ from before
	// B213 + at least 1 (B211 = v0.66) added in the
	// B211 commit.
	migs := PGMigrations()
	if len(migs) < 40 {
		t.Errorf("PGMigrations returned %d entries, expected at least 40", len(migs))
	}
}

func TestPGMigrations_OrderedByVersion(t *testing.T) {
	// B213 contract: PGMigrations preserves the
	// pre-B213 dependency order. The pre-B213
	// function list put V025 FIRST (not in strict
	// version order) because V020-V024 have
	// FOREIGN KEY → portal_users (which V025
	// creates). B213 preserves that ordering.
	// We assert the most important property:
	// V025 is the first entry, and V066 (the latest
	// migration) is the last entry. The exact
	// middle ordering is an internal implementation
	// detail (FK dependency + Version ASC).
	migs := PGMigrations()
	if len(migs) == 0 {
		t.Fatal("PGMigrations is empty")
	}
	// V025 must come first (FK constraint).
	if migs[0].Version != 25 {
		t.Errorf("PGMigrations[0].Version = %d, want 25 (FK ordering)", migs[0].Version)
	}
	// V066 (B211) was the most recent migration; V067
	// (B221) was added in B221 (Phase 4.1). V068 (B232)
	// was added in B232 to repair the device_rules natural
	// key UNIQUE INDEX shape drift. The framework's
	// natural state is to append new migrations at the
	// end.
	last := migs[len(migs)-1]
	// B235.3 added v0.69; any future migration bumps
	// this number. The test pins the contract (last is
	// monotonically increasing) rather than a specific
	// version, so adding new migrations doesn't break
	// the test.
	if last.Version < 68 {
		t.Errorf("PGMigrations[last].Version = %d, want >= 68 (B232 v0.68 is the minimum, later migrations bump it)", last.Version)
	}
}

func TestPGMigrations_HaveRequiredMetadata(t *testing.T) {
	// Every entry must have:
	//  - non-zero Version
	//  - non-empty Name
	//  - non-empty SourceFile
	//  - non-nil Run
	migs := PGMigrations()
	for i, m := range migs {
		if m.Version == 0 {
			t.Errorf("migrations[%d]: Version is 0", i)
		}
		if m.Name == "" {
			t.Errorf("migrations[%d] (v%d): Name is empty", i, m.Version)
		}
		if m.SourceFile == "" {
			t.Errorf("migrations[%d] (v%d): SourceFile is empty", i, m.Version)
		}
		if m.Run == nil {
			t.Errorf("migrations[%d] (v%d): Run is nil", i, m.Version)
		}
	}
}

func TestPGMigrations_UniqueVersions(t *testing.T) {
	// B213 contract: the bookkeeping table has version
	// as PRIMARY KEY. Two migrations with the same
	// version would conflict in applied_migrations.
	// The struct list must have unique versions.
	migs := PGMigrations()
	seen := make(map[int]bool, len(migs))
	for _, m := range migs {
		if seen[m.Version] {
			t.Errorf("PGMigrations has duplicate version v%d", m.Version)
		}
		seen[m.Version] = true
	}
}

func TestPGMigrations_IncludesB211V066(t *testing.T) {
	// B211 added v0.66 (UNIQUE constraint on
	// cluster_node). The B213 migration list must
	// include it.
	migs := PGMigrations()
	found := false
	for _, m := range migs {
		if m.Version == 66 && strings.Contains(m.Name, "cluster_node UNIQUE") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("PGMigrations missing v0.66 (B211 cluster_node UNIQUE)")
	}
}

func TestRecordMigrationApplied_IdempotentOnReRun(t *testing.T) {
	// B213: RecordMigrationApplied is now ON CONFLICT
	// DO NOTHING, so re-recording the same version is
	// a no-op (not an error). This is the contract
	// `skygate migrate up` re-run relies on.
	//
	// The pre-B213 implementation was a bare INSERT
	// without ON CONFLICT; re-recording would fail
	// with a UNIQUE-constraint violation. B213's
	// ON CONFLICT DO NOTHING is a hard requirement
	// for the up re-run to be safe.
	//
	// This test requires a real PG (openTestDB skips
	// if SKYGATE_TEST_PG_DSN isn't set). The source-
	// pin contracts in the B-check (P in check_b213.sh)
	// cover the SQL shape; the live-verify on the agent
	// exercises the actual re-run.
	if os.Getenv("SKYGATE_TEST_PG_DSN") == "" {
		t.Skip("SKYGATE_TEST_PG_DSN not set; skipping live PG test (set SKYGATE_TEST_PG_DSN=postgres://... to enable)")
	}
	d := openTestDB(t)
	if err := ensureMigrationTrackingTable(d); err != nil {
		t.Fatal(err)
	}
	// Record the same version 3 times.
	for i := 0; i < 3; i++ {
		if err := RecordMigrationApplied(d, 999, "sha", "test.go", "v0.test"); err != nil {
			t.Errorf("RecordMigrationApplied (call %d): %v", i, err)
		}
	}
	// Verify only 1 row exists for version 999.
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM applied_migrations WHERE version = 999`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 row for version 999, got %d", count)
	}
}

func TestMigrationEntry_FieldsPinned(t *testing.T) {
	// B213 contract: the field names are part of the
	// framework API. A refactor that renames Version
	// → MigrationVersion would break the bookkeeping
	// table integration; pin the field names.
	e := MigrationEntry{Version: 1, Name: "x", SourceFile: "y", Run: nil}
	_ = e.Version
	_ = e.Name
	_ = e.SourceFile
	_ = e.Run
	// (the above assignments are no-ops; the point is
	// that these field names must compile.)
}
