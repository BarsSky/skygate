// B367 — the two `internal/db` statements that carried `COALESCE(roles, ”)`.
//
// These are the WORST two of the five: `FindClusterPrimary` and `NodeRoles` are
// called by the failover transaction (`cluster_failover.go`) and by the failover
// DRILL (`cluster_drill.go`). On PostgreSQL the statement is rejected at PARSE
// time — `ERROR: malformed array literal: "" (SQLSTATE 22P02)` — so a real
// failover could not even find the current primary, while the SQLite install
// (where `roles` is the same `{a,b}` TEXT literal) behaved perfectly. Measured
// live on the reference PostgreSQL deployment 2026-10-09 while diagnosing the
// operator's drain+remove error; both statements answer correctly once the empty
// value is the empty ARRAY literal `'{}'`.
//
// The PostgreSQL half SKIPs without SKYGATE_TEST_PG_DSN (AGENTS rule 1); CI's
// PG job runs `go test ./...` with the DSN set, so it is exercised there.
package db

import (
	"database/sql"
	"testing"
)

// seedB367Cluster inserts the two rows the failover lookups walk: a ready node
// with the exact role "skygate" and a standby.
func seedB367Cluster(t *testing.T, d *sql.DB) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO cluster (id, name) VALUES ('c-b367', 'c-b367')`); err != nil {
		t.Fatalf("insert cluster: %v", err)
	}
	ins := `INSERT INTO cluster_node (id, cluster_id, hostname, roles, state, skygate_version)
	        VALUES ($1, 'c-b367', $2, ` + ActiveDialect().CastTextArray("$3") + `, $4, '')`
	if _, err := d.Exec(ins, "b367-primary", "primary", TextArrayLiteral([]string{"skygate"}), "ready"); err != nil {
		t.Fatalf("insert primary: %v", err)
	}
	if _, err := d.Exec(ins, "b367-standby", "standby", TextArrayLiteral([]string{"skygate-standby"}), "ready"); err != nil {
		t.Fatalf("insert standby: %v", err)
	}
}

// TestB367_FailoverLookupsWorkOnSQLite is the native-install half: the same two
// statements must keep working where `roles` is a TEXT literal.
func TestB367_FailoverLookupsWorkOnSQLite(t *testing.T) {
	_, d, err := OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := ApplyMigrations(d, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	seedB367Cluster(t, d)

	id, host, err := FindClusterPrimary(d)
	if err != nil {
		t.Fatalf("FindClusterPrimary on SQLite: %v", err)
	}
	if host != "primary" || id != "b367-primary" {
		t.Fatalf("FindClusterPrimary picked %q/%q, want the node whose roles contain exactly \"skygate\"", id, host)
	}
	roles, err := NodeRoles(d, id)
	if err != nil {
		t.Fatalf("NodeRoles on SQLite: %v", err)
	}
	if !RolesContain(roles, "skygate") {
		t.Fatalf("NodeRoles returned %v", roles)
	}
}

// TestB367_FailoverLookupsWorkOnPostgres is the regression: on PostgreSQL both
// statements used to die with SQLSTATE 22P02 before touching a row.
func TestB367_FailoverLookupsWorkOnPostgres(t *testing.T) {
	d := OpenTestPG(t)
	seedB367Cluster(t, d)

	id, host, err := FindClusterPrimary(d)
	if err != nil {
		t.Fatalf("FindClusterPrimary on PostgreSQL: %v (was: malformed array literal: \"\")", err)
	}
	if host != "primary" || id != "b367-primary" {
		t.Fatalf("FindClusterPrimary picked %q/%q", id, host)
	}
	roles, err := NodeRoles(d, id)
	if err != nil {
		t.Fatalf("NodeRoles on PostgreSQL: %v (was: malformed array literal: \"\")", err)
	}
	if !RolesContain(roles, "skygate") {
		t.Fatalf("NodeRoles returned %v", roles)
	}
	// The write side of the same round-trip.
	if err := SetNodeRoles(d, id, RolesRemove(roles, "skygate")); err != nil {
		t.Fatalf("SetNodeRoles on PostgreSQL: %v", err)
	}
	after, err := NodeRoles(d, id)
	if err != nil {
		t.Fatalf("NodeRoles after the rewrite: %v", err)
	}
	if RolesContain(after, "skygate") {
		t.Fatalf("SetNodeRoles did not remove the role: %v", after)
	}
}
