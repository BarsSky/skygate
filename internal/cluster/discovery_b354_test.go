// internal/cluster/discovery_b354_test.go — B354 (2026-10-06).
//
// THE LIVE CASE. Every five minutes the reference deployment logged:
//
//	🔎 discovery-ticker: ensure "karolina" failed: insert discovered node: ERROR:
//	   insert or update on table "cluster_node" violates foreign key constraint
//	   "cluster_node_cluster_id_fkey" (SQLSTATE 23503)
//
// three times per tick (karolina, sharlotta, emilia), and wrote an audit row with
// each one — 864 of each per day. Measured on the live database: `cluster` had
// **0 rows**, `cluster_node` had **0 rows**, and the FK really is
// `cluster_node_cluster_id_fkey -> cluster`. The discovery ticker inserts with
// `cluster_id = 'skygate-staging'` (cmd/skygate/main_helpers.go), and that row is
// created by `EnsureCluster` — which the B200 bootstrap calls from the ADMIN
// handlers' first-use paths (AddNode / IssueInvite) and the ticker never did. So an
// operator who never opened /admin/cluster got a permanently empty cluster tree and
// a permanent failure line, while the tree is supposed to bootstrap itself.
//
// FK enforcement is ON for SQLite in this project (internal/db/open_sqlite.go sets
// `foreign_keys=ON` per connection), so these tests reproduce the live failure on
// the dialect the unit suite runs.
package cluster

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
)

func newB354SQLite(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:b354_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
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

func b354Count(t *testing.T, d *sql.DB, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := d.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// TestB354_EnsureDiscoveredNodeBootstrapsTheCluster is the regression: on the live
// database the cluster table was EMPTY, and that is exactly the state this test
// starts from.
func TestB354_EnsureDiscoveredNodeBootstrapsTheCluster(t *testing.T) {
	d := newB354SQLite(t)
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster`); n != 0 {
		t.Fatalf("the fixture must start with no cluster row, got %d", n)
	}

	// Before B354 this returned the live error, because cluster_node FKs to a row
	// nobody had created.
	if err := EnsureDiscoveredNode(d, DefaultClusterID, "karolina", "100.64.0.2", "system"); err != nil {
		t.Fatalf("EnsureDiscoveredNode on an empty cluster table: %v (live: cluster_node_cluster_id_fkey)", err)
	}
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster WHERE id = $1`, DefaultClusterID); n != 1 {
		t.Fatalf("the bootstrap cluster row must exist after discovery, got %d", n)
	}
	node, err := LookupNode(d, DefaultClusterID, "karolina")
	if err != nil {
		t.Fatalf("the discovered node must be readable through the normal path: %v", err)
	}
	if node.State != "pending" {
		t.Errorf("state = %q, want pending", node.State)
	}
	if node.TailscaleIP != "100.64.0.2" {
		t.Errorf("tailscale_ip = %q, want 100.64.0.2", node.TailscaleIP)
	}

	// Idempotent: the next tick (five minutes later) must neither fail nor
	// duplicate the bootstrap row.
	if err := EnsureDiscoveredNode(d, DefaultClusterID, "karolina", "100.64.0.2", "system"); err != nil {
		t.Fatalf("second discovery: %v", err)
	}
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster`); n != 1 {
		t.Fatalf("a second tick created %d cluster rows, want 1", n)
	}
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster_node WHERE hostname = 'karolina'`); n != 1 {
		t.Fatalf("a second tick created %d node rows, want 1", n)
	}
}

// TestB354_AnExistingClusterRowIsNotClobbered: the bootstrap must be additive. An
// operator who named the cluster, or who already approved nodes in it, must not lose
// that to a discovery tick.
func TestB354_AnExistingClusterRowIsNotClobbered(t *testing.T) {
	d := newB354SQLite(t)
	if err := EnsureCluster(d, DefaultClusterID, "production"); err != nil {
		t.Fatalf("EnsureCluster: %v", err)
	}
	if err := EnsureDiscoveredNode(d, DefaultClusterID, "emilia", "100.64.0.3", "system"); err != nil {
		t.Fatalf("EnsureDiscoveredNode: %v", err)
	}
	var name string
	if err := d.QueryRow(`SELECT name FROM cluster WHERE id = $1`, DefaultClusterID).Scan(&name); err != nil {
		t.Fatalf("read cluster name: %v", err)
	}
	if name != "production" {
		t.Errorf("cluster name = %q, want production (the bootstrap must not rename it)", name)
	}
}

// TestB354_AllThreeRelaysOfTheLiveTailnetAreDiscovered is the end-to-end shape of
// the live failure: three peers in one tick, all three previously failing.
func TestB354_AllThreeRelaysOfTheLiveTailnetAreDiscovered(t *testing.T) {
	d := newB354SQLite(t)
	peers := []struct{ host, ip string }{
		{"karolina", "100.64.0.2"},
		{"sharlotta", "100.64.0.4"},
		{"emilia", "100.64.0.3"},
	}
	for _, p := range peers {
		if err := EnsureDiscoveredNode(d, DefaultClusterID, p.host, p.ip, "system"); err != nil {
			t.Fatalf("ensure %s: %v", p.host, err)
		}
	}
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster_node WHERE cluster_id = $1`, DefaultClusterID); n != len(peers) {
		t.Fatalf("cluster_node rows = %d, want %d (the live tree was empty because every insert failed)", n, len(peers))
	}
}
