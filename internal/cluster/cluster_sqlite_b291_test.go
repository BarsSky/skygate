// internal/cluster/cluster_sqlite_b291_test.go — B291 (2026-09-22).
//
// The whole cluster/HA write path was PostgreSQL-shaped and therefore dead on
// SQLite. Live on the native `aro` host:
//
//	cluster.discovery.error  insert discovered node: SQL logic error:
//	                         near "['skygate-standby']": syntax error   (every 5 min)
//	/admin/cluster, /admin/ha  empty
//
// These tests run the REAL entry points on a real (migrated, in-memory) SQLite
// database, so "it compiles" can no longer stand in for "it works".
package cluster

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
)

func newB291SQLite(t *testing.T) *sql.DB {
	t.Helper()
	// A NAMED in-memory database: `file::memory:?cache=shared` is shared by every
	// connection in the process, so two tests in one binary would see each
	// other's rows and the second AddNode("primary") would hit a UNIQUE
	// constraint. The test name makes each database private.
	dsn := "file:b291_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	_, d, err := db.OpenWithDialect(dsn)
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.ApplyMigrations(d, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return d
}

// TestB291_DiscoveryInsertWorksOnSQLite is the regression for the recurring
// `cluster.discovery.error … near "['skygate-standby']"` line in the operator's
// audit log.
func TestB291_DiscoveryInsertWorksOnSQLite(t *testing.T) {
	d := newB291SQLite(t)

	// EnsureCluster is the bootstrap row every cluster_node FKs to; it used to
	// write `'[]'::jsonb`, a PostgreSQL literal.
	if err := EnsureCluster(d, DefaultClusterID, "staging"); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	if err := EnsureDiscoveredNode(d, DefaultClusterID, "workpc", "100.64.0.9", "system"); err != nil {
		t.Fatalf("EnsureDiscoveredNode: %v (this is the live `aro` error)", err)
	}
	// Idempotent: a second discovery of the same peer must not fail.
	if err := EnsureDiscoveredNode(d, DefaultClusterID, "workpc", "100.64.0.9", "system"); err != nil {
		t.Fatalf("EnsureDiscoveredNode (second call): %v", err)
	}

	node, err := LookupNode(d, DefaultClusterID, "workpc")
	if err != nil {
		t.Fatalf("LookupNode: %v", err)
	}
	if node.State != "pending" {
		t.Errorf("state = %q, want pending", node.State)
	}
	if len(node.Roles) != 1 || node.Roles[0] != "skygate-standby" {
		t.Errorf("roles = %v, want [skygate-standby] (the literal must survive the TEXT column)", node.Roles)
	}
	// The discovery run-level audit row must land too (cluster_audit used a
	// `$N::jsonb` cast).
	var audits int
	if err := d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE action = 'node_discovered'`).Scan(&audits); err != nil {
		t.Fatalf("count cluster_audit: %v", err)
	}
	if audits == 0 {
		t.Error("no node_discovered audit row — cluster_audit writes are still broken on SQLite")
	}
}

// TestB291_NodeLifecycleWorksOnSQLite covers the node CRUD helpers: generated
// ids, role literals, the audit trail and the timestamp updates.
func TestB291_NodeLifecycleWorksOnSQLite(t *testing.T) {
	d := newB291SQLite(t)
	if err := EnsureCluster(d, DefaultClusterID, "staging"); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}

	id, err := AddNode(d, DefaultClusterID, "primary", "100.64.0.1", []string{"skygate"}, "v1.5.55")
	if err != nil {
		t.Fatalf("AddNode: %v (the generated-id expression was PostgreSQL-only)", err)
	}
	if len(id) != len("node-")+12 {
		t.Errorf("generated id = %q, want node-<12 hex>", id)
	}
	if _, err := UpsertNode(d, DefaultClusterID, "standby", "100.64.0.2", []string{"skygate-standby"}, "v1.5.55"); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}
	// Upsert again: the ON CONFLICT … DO UPDATE SET roles = EXCLUDED.roles path.
	if _, err := UpsertNode(d, DefaultClusterID, "standby", "100.64.0.2", []string{"skygate-standby", "patroni-replica"}, "v1.5.55"); err != nil {
		t.Fatalf("UpsertNode (update): %v", err)
	}
	n, err := LookupNode(d, DefaultClusterID, "standby")
	if err != nil {
		t.Fatalf("LookupNode(standby): %v", err)
	}
	if len(n.Roles) != 2 {
		t.Errorf("roles after upsert = %v, want two roles", n.Roles)
	}

	// ApproveNode stamps last_seen_at (was `last_seen_at = NOW()`).
	if err := ApproveNode(d, DefaultClusterID, "standby", "operator"); err != nil {
		t.Fatalf("ApproveNode: %v", err)
	}
	// DrainNode reads the roles literal for its audit detail (was
	// `array_to_string(roles, ',')`).
	if err := DrainNode(d, DefaultClusterID, "standby", "operator", "maintenance"); err != nil {
		t.Fatalf("DrainNode: %v", err)
	}
	// The audit detail must keep the comma-joined role form the PostgreSQL
	// `array_to_string(roles, ',')` produced — the port must not turn the
	// operator-facing detail into a `{a,b}` literal.
	var drainDetail string
	if err := d.QueryRow(
		`SELECT detail FROM cluster_audit WHERE action = 'node_drain'`).Scan(&drainDetail); err != nil {
		t.Fatalf("read node_drain detail: %v", err)
	}
	if !containsSub(drainDetail, `"roles":"skygate-standby,patroni-replica"`) {
		t.Errorf("node_drain detail = %s, want roles as a comma-joined list", drainDetail)
	}
	// Rejoin + remove, both of which touch the audit trail.
	if err := RejoinNode(d, DefaultClusterID, "standby", "operator"); err != nil {
		t.Fatalf("RejoinNode: %v", err)
	}
	if err := RemoveNode(d, DefaultClusterID, "standby"); err != nil {
		t.Fatalf("RemoveNode: %v", err)
	}
}

// TestB291_FailoverAndDrillWorkOnSQLite: the promote/demote role surgery was
// `ANY(roles)` + `unnest(roles || ARRAY[...])` + `array_remove(...)`.
func TestB291_FailoverAndDrillWorkOnSQLite(t *testing.T) {
	d := newB291SQLite(t)
	if err := EnsureCluster(d, DefaultClusterID, "staging"); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	primaryID, err := AddNode(d, DefaultClusterID, "primary", "100.64.0.1", []string{"skygate"}, "v1")
	if err != nil {
		t.Fatalf("AddNode(primary): %v", err)
	}
	standbyID, err := AddNode(d, DefaultClusterID, "standby", "100.64.0.2", []string{"skygate-standby"}, "v1")
	if err != nil {
		t.Fatalf("AddNode(standby): %v", err)
	}
	// Both must be state=ready for the failover to consider them.
	for _, id := range []string{primaryID, standbyID} {
		if _, err := d.Exec(`UPDATE cluster_node SET state = 'ready' WHERE id = $1`, id); err != nil {
			t.Fatalf("set ready: %v", err)
		}
	}

	fromID, toID, err := db.FailoverClusterNode(d, standbyID, "operator", "primary is down")
	if err != nil {
		t.Fatalf("FailoverClusterNode: %v (the role surgery was PostgreSQL-only)", err)
	}
	if fromID != primaryID || toID != standbyID {
		t.Fatalf("failover moved %s -> %s, want %s -> %s", fromID, toID, primaryID, standbyID)
	}
	promoted, err := LookupNode(d, DefaultClusterID, "standby")
	if err != nil {
		t.Fatalf("LookupNode(standby): %v", err)
	}
	if !contains(promoted.Roles, "skygate") || !contains(promoted.Roles, "skygate-standby") {
		t.Errorf("promoted roles = %v, want skygate + skygate-standby", promoted.Roles)
	}
	demoted, err := LookupNode(d, DefaultClusterID, "primary")
	if err != nil {
		t.Fatalf("LookupNode(primary): %v", err)
	}
	if contains(demoted.Roles, "skygate") {
		t.Errorf("demoted roles = %v, want the skygate role removed", demoted.Roles)
	}
	if demoted.State != "draining" {
		t.Errorf("demoted state = %q, want draining", demoted.State)
	}

	// The drill runs the same surgery and must work after the roles have been
	// rewritten (a second pass over the literal).
	if _, _, err := db.DrillClusterNode(d, primaryID, "operator", "drill"); err == nil {
		// primary is draining with no skygate role → not eligible; that error is
		// the CORRECT outcome, not a SQL failure. Re-arm it so the drill has a
		// legal target and assert the SQL path itself runs.
	} else if isSQLSyntaxError(err) {
		t.Fatalf("DrillClusterNode failed with a SQL syntax error (%v) — the dialect port is incomplete", err)
	}
	if _, err := d.Exec(`UPDATE cluster_node SET state = 'ready', roles = $1 WHERE id = $2`,
		db.TextArrayLiteral([]string{"skygate-standby"}), primaryID); err != nil {
		t.Fatalf("re-arm primary: %v", err)
	}
	if _, _, err := db.DrillClusterNode(d, primaryID, "operator", "drill"); err != nil {
		t.Fatalf("DrillClusterNode: %v", err)
	}
	var drills int
	if err := d.QueryRow(`SELECT count(*) FROM cluster_audit WHERE action = 'node_drill'`).Scan(&drills); err != nil {
		t.Fatalf("count drills: %v", err)
	}
	if drills == 0 {
		t.Error("no node_drill audit row")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// isSQLSyntaxError distinguishes "the SQL does not parse on this dialect" from a
// legitimate domain error (not eligible, already primary, …).
func isSQLSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, needle := range []string{"syntax error", "no such function", "unrecognized token", "near \""} {
		if len(msg) >= len(needle) && (msg == needle || containsSub(msg, needle)) {
			return true
		}
	}
	return false
}

func containsSub(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
