package setupstate

// snapshot_b395_2_pg_test.go — B395.2: the ACL acknowledgement must work on
// POSTGRESQL, not only on SQLite.
//
// Measured live on the operator's host (PostgreSQL backend) the first time the new
// button was pressed: `snapshot: ERROR: invalid input syntax for type integer:
// "setup-wizard" (SQLSTATE 22P02)`. The insert wrote the literal string "setup-wizard"
// into `acl_snapshots.version`, which is `INTEGER NOT NULL` in BOTH migration chains —
// SQLite accepted it because its columns are dynamically typed, so every local docker
// scenario and every unit test passed while the feature was dead on the backend the
// operator actually runs. `Result.LastInsertId()` was the second half: pgx does not
// support it, so even a correct insert would have lost the id.
//
// The PostgreSQL leg goes through db.OpenTestPG — the ONE helper every PG-gated test in
// the tree uses: it SKIPs without SKYGATE_TEST_PG_DSN (AGENTS rule 1), creates a unique
// schema and runs the REAL migration chain, so the insert is checked against the schema
// the operator actually has rather than a hand-written copy of it. CI's `test-pg` job
// sets the DSN, and scripts/check_b334_pg_test_coverage.sh watches the package set
// (internal/setupstate joined it with this file).

import (
	"context"
	"database/sql"
	"testing"

	"skygate/internal/db"

	_ "modernc.org/sqlite"
)

// TestSnapshotForACL_PostgresIsIntegerVersion_B395_2 is the regression: the version
// column is an INTEGER, the id must come back from the INSERT, and two calls must
// produce consecutive versions.
func TestSnapshotForACL_PostgresIsIntegerVersion_B395_2(t *testing.T) {
	d := db.OpenTestPG(t) // skips without SKYGATE_TEST_PG_DSN
	ctx := context.Background()

	before, err := maxVersionPG(t, ctx, d)
	if err != nil {
		t.Fatalf("read the starting version: %v", err)
	}

	id1, err := SnapshotForACL(ctx, d, `{"acls":[]}`, "test-b395-2")
	if err != nil {
		t.Fatalf("SnapshotForACL on PostgreSQL failed: %v (this is the live defect: `setup-wizard` "+
			"was written into an INTEGER column, which SQLite accepts and PostgreSQL rejects)", err)
	}
	if id1 <= 0 {
		t.Fatalf("SnapshotForACL returned id=%d — the id must come from RETURNING id, not LastInsertId", id1)
	}
	id2, err := SnapshotForACL(ctx, d, `{"acls":[]}`, "test-b395-2")
	if err != nil {
		t.Fatalf("second SnapshotForACL failed: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("second snapshot id %d is not after %d", id2, id1)
	}

	var v1, v2 int
	if err := d.QueryRowContext(ctx, `SELECT version FROM acl_snapshots WHERE id = $1`, id1).Scan(&v1); err != nil {
		t.Fatalf("read back version of id %d: %v", id1, err)
	}
	if err := d.QueryRowContext(ctx, `SELECT version FROM acl_snapshots WHERE id = $1`, id2).Scan(&v2); err != nil {
		t.Fatalf("read back version of id %d: %v", id2, err)
	}
	if v1 != before+1 || v2 != v1+1 {
		t.Errorf("versions %d, %d are not consecutive after MAX(version)=%d", v1, v2, before)
	}

	// applied_success = 0: this is the PRE-apply copy, not an applied policy.
	var applied sql.NullInt64
	if err := d.QueryRowContext(ctx, `SELECT applied_success FROM acl_snapshots WHERE id = $1`, id1).Scan(&applied); err != nil {
		t.Fatalf("read applied_success: %v", err)
	}
	if applied.Valid && applied.Int64 != 0 {
		t.Errorf("applied_success = %v, want 0 (a pre-apply snapshot)", applied.Int64)
	}
}

func maxVersionPG(t *testing.T, ctx context.Context, d *sql.DB) (int, error) {
	t.Helper()
	var max int
	err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM acl_snapshots`).Scan(&max)
	return max, err
}

// TestSnapshotForACL_SQLiteStillWorks_B395_2 is the other half: the fix must not
// trade the PostgreSQL bug for a SQLite one. `RETURNING id` is a SQLite 3.35 feature
// and the driver binds `$N` placeholders by position, so both dialects have to be
// exercised — the whole point of B395.2 is that "it works on SQLite" was never
// evidence about the backend the operator runs.
func TestSnapshotForACL_SQLiteStillWorks_B395_2(t *testing.T) {
	d, err := sql.Open("sqlite", "file:b395_2_snapshot?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `CREATE TABLE acl_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version INTEGER NOT NULL,
		config TEXT NOT NULL,
		created_by TEXT NOT NULL,
		applied_success INTEGER DEFAULT NULL,
		error_msg TEXT DEFAULT '',
		created_at INTEGER DEFAULT (strftime('%s', 'now'))
	)`); err != nil {
		t.Fatalf("create acl_snapshots: %v", err)
	}

	id1, err := SnapshotForACL(ctx, d, `{"acls":[]}`, "test-b395-2")
	if err != nil {
		t.Fatalf("SnapshotForACL on SQLite failed: %v", err)
	}
	id2, err := SnapshotForACL(ctx, d, `{"acls":[]}`, "test-b395-2")
	if err != nil {
		t.Fatalf("second SnapshotForACL on SQLite failed: %v", err)
	}
	if id1 <= 0 || id2 <= id1 {
		t.Fatalf("ids %d, %d do not increase — RETURNING id did not work on this driver", id1, id2)
	}
	var v1, v2 int
	if err := d.QueryRowContext(ctx, `SELECT version FROM acl_snapshots WHERE id = $1`, id1).Scan(&v1); err != nil {
		t.Fatalf("read back version: %v", err)
	}
	if err := d.QueryRowContext(ctx, `SELECT version FROM acl_snapshots WHERE id = $1`, id2).Scan(&v2); err != nil {
		t.Fatalf("read back version: %v", err)
	}
	if v1 != 1 || v2 != 2 {
		t.Errorf("versions %d, %d, want 1 and 2", v1, v2)
	}
}
