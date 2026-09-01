// v1.5.0+ (B203) — regression tests for GetClusterDatabase
// NULL handling. The B203 live test surfaced a bug where
// primary_node_id = NULL caused "converting NULL to string
// is unsupported" in the watchdog's every-5s read loop.
// Fix: COALESCE all NULL-able columns to their default
// empty value in the SELECT statement.
//
// These tests pin the contract:
//   1. Reads a row with primary_node_id = NULL without error
//      (the actual B203 live bug).
//   2. Reads a fully-populated row (smoke test).
//   3. Returns ErrClusterDatabaseNotFound for missing rows.
//   4. Reads a row with primary_node_id set to a valid
//      cluster_node FK (positive control for the COALESCE).
//
// Runs on a live PG instance via openTestDB (skipped when
// SKYGATE_TEST_PG_DSN is unset).

package db

import (
	"database/sql"
	"errors"
	"testing"
)

// b203SeedClusterDatabase inserts a cluster row + a
// cluster_database row. The cluster_id is derived from
// the test name (sanitized for PG identifiers) so parallel
// tests don't collide. Returns the database id.
func b203SeedClusterDatabase(t *testing.T, d *sql.DB, dbID, clusterID string, withPrimaryNode bool) (primaryNodeID string) {
	t.Helper()
	if _, err := d.Exec(
		`INSERT INTO cluster (id, name) VALUES ($1, $2)
		 ON CONFLICT (id) DO NOTHING`,
		clusterID, "test-"+dbID,
	); err != nil {
		t.Fatalf("seed cluster %q: %v", clusterID, err)
	}
	if withPrimaryNode {
		primaryNodeID = dbID + "-node-1"
		if _, err := d.Exec(
			`INSERT INTO cluster_node (id, cluster_id, hostname, state)
			 VALUES ($1, $2, $3, 'active')`,
			primaryNodeID, clusterID, "test-host",
		); err != nil {
			t.Fatalf("seed cluster_node %q: %v", primaryNodeID, err)
		}
	}
	// cluster_database — primary_node_id is the only NULL-able
	// column (others are NOT NULL DEFAULT ''). We test both
	// the NULL case (the B203 bug) and the populated case.
	var primaryArg interface{}
	if withPrimaryNode {
		primaryArg = primaryNodeID
	} else {
		primaryArg = nil
	}
	if _, err := d.Exec(
		`INSERT INTO cluster_database (
			id, cluster_id, primary_node_id, replica_node_ids,
			dsn_template, dbname, username, sslmode, current_dsn,
			updated_by
		) VALUES ($1, $2, $3, '{}', 'postgres://u:p@h/d', 'd', 'u', 'disable', 'postgres://u:p@h/d', 'tester')`,
		dbID, clusterID, primaryArg,
	); err != nil {
		t.Fatalf("seed cluster_database %q: %v", dbID, err)
	}
	return primaryNodeID
}

// TestGetClusterDatabase_NullPrimaryNode — the actual B203
// live bug. A row inserted with primary_node_id = NULL
// (because the admin hasn't picked a primary yet) used to
// crash the watchdog every 5s with "converting NULL to
// string is unsupported". The COALESCE on primary_node_id
// in the SELECT fixes this.
func TestGetClusterDatabase_NullPrimaryNode(t *testing.T) {
	d := openTestDB(t)
	if err := MigratePostgres(d); err != nil {
		t.Fatalf("MigratePostgres: %v", err)
	}
	dbID := "test-null-pn-b203"
	clusterID := "test-null-pn-cluster-b203"
	b203SeedClusterDatabase(t, d, dbID, clusterID, false)

	row, err := GetClusterDatabase(d, dbID)
	if err != nil {
		t.Fatalf("GetClusterDatabase with NULL primary_node_id: %v (B203 regression — COALESCE missing)", err)
	}
	if row.PrimaryNodeID != "" {
		t.Errorf("PrimaryNodeID = %q, want \"\" (NULL should COALESCE to empty)", row.PrimaryNodeID)
	}
	if row.ID != dbID {
		t.Errorf("ID = %q, want %q", row.ID, dbID)
	}
	if row.CurrentDSN != "postgres://u:p@h/d" {
		t.Errorf("CurrentDSN = %q", row.CurrentDSN)
	}
}

// TestGetClusterDatabase_PopulatedPrimaryNode — positive
// control. A row with a real primary_node_id reads back
// the right value, proving the COALESCE doesn't drop the
// real value to "".
func TestGetClusterDatabase_PopulatedPrimaryNode(t *testing.T) {
	d := openTestDB(t)
	if err := MigratePostgres(d); err != nil {
		t.Fatalf("MigratePostgres: %v", err)
	}
	dbID := "test-pop-pn-b203"
	clusterID := "test-pop-pn-cluster-b203"
	primaryID := b203SeedClusterDatabase(t, d, dbID, clusterID, true)

	row, err := GetClusterDatabase(d, dbID)
	if err != nil {
		t.Fatalf("GetClusterDatabase with populated primary_node_id: %v", err)
	}
	if row.PrimaryNodeID != primaryID {
		t.Errorf("PrimaryNodeID = %q, want %q", row.PrimaryNodeID, primaryID)
	}
}

// TestGetClusterDatabase_NotFound — missing id returns
// the sentinel, not a generic sql.ErrNoRows.
func TestGetClusterDatabase_NotFound(t *testing.T) {
	d := openTestDB(t)
	if err := MigratePostgres(d); err != nil {
		t.Fatalf("MigratePostgres: %v", err)
	}
	_, err := GetClusterDatabase(d, "does-not-exist-b203")
	if !errors.Is(err, ErrClusterDatabaseNotFound) {
		t.Errorf("err = %v, want ErrClusterDatabaseNotFound", err)
	}
}

// TestGetClusterDatabase_EmptyReplicaArray — the COALESCE
// on replica_node_ids fires when the array is the empty
// default '{}'. The Scan should receive an empty []string
// (not nil), which is what the watchdog's empty-DSN check
// wants.
func TestGetClusterDatabase_EmptyReplicaArray(t *testing.T) {
	d := openTestDB(t)
	if err := MigratePostgres(d); err != nil {
		t.Fatalf("MigratePostgres: %v", err)
	}
	dbID := "test-empty-replicas-b203"
	clusterID := "test-empty-replicas-cluster-b203"
	b203SeedClusterDatabase(t, d, dbID, clusterID, false)

	row, err := GetClusterDatabase(d, dbID)
	if err != nil {
		t.Fatalf("GetClusterDatabase: %v", err)
	}
	if row.ReplicaNodeIDs == nil {
		t.Error("ReplicaNodeIDs is nil; want non-nil empty array")
	}
	if len(row.ReplicaNodeIDs) != 0 {
		t.Errorf("ReplicaNodeIDs = %v, want empty", row.ReplicaNodeIDs)
	}
}
