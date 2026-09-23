// internal/elector/elector_sqlite_b291_test.go — B291 (2026-09-22).
//
// The HA elector ran PostgreSQL-only SQL against a SQLite install and every
// affected statement failed:
//
//	transitionNode      cluster_audit INSERT ... $3::jsonb   → near "::": syntax error
//	recommendFailover   detail->>'from_node_id'              → no such function / syntax error
//	                    created_at > NOW() - INTERVAL '5 minutes'
//
// evaluate() swallowed all of it (each failure logged, the tick returned nil), so
// a native install simply never transitioned a node and never recommended a
// failover. This test drives the REAL evaluate() against a REAL migrated SQLite
// database and asserts both halves of the tick land.
package elector

import (
	"context"
	"database/sql"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

// newB291ElectorDB opens a migrated, private in-memory SQLite database.
func newB291ElectorDB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("file:b291_elector_" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations(sqlite): %v", err)
	}
	return d
}

// TestB291_ElectorTickWorksOnSQLite is the regression for the whole elector tick:
// a failed primary + a ready standby must produce both a node_health transition
// row and a failover_recommend row, and the second tick must not duplicate the
// recommendation (the Go-side dedup must see the row it just wrote).
func TestB291_ElectorTickWorksOnSQLite(t *testing.T) {
	d := newB291ElectorDB(t)

	// The FK target for cluster_node.
	if _, err := d.Exec(
		`INSERT INTO cluster (id, name, chain) VALUES ('skygate-staging', 'staging', '[]')`); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}

	now := time.Unix(1_790_100_000, 0).UTC()
	stale := now.Add(-10 * time.Minute)
	// Both timestamp shapes SQLite can hold: the pre-B291 writers stored Go's
	// time.Time String() form, the dialect writers store RFC3339.
	seedNode := func(id, host, state, roles string, lastSeen, joined any) {
		t.Helper()
		if _, err := d.Exec(`
			INSERT INTO cluster_node
			  (id, cluster_id, hostname, tailscale_ip, roles, state, skygate_version, joined_at, last_seen_at)
			VALUES ($1, 'skygate-staging', $2, '100.64.0.1', $3, $4, 'v1.5.55', $5, $6)`,
			id, host, roles, state, joined, lastSeen); err != nil {
			t.Fatalf("seed cluster_node %s: %v", host, err)
		}
	}
	// Ready primary whose heartbeat is 10 minutes stale → the elector must flip it
	// to failed (3 × 30s window).
	seedNode("node-primary", "primary", "ready", "{skygate}",
		stale.Format(time.RFC3339Nano), stale.Format("2006-01-02 15:04:05.999999999 -0700 MST"))
	// Ready standby with a fresh heartbeat → the failover target.
	seedNode("node-standby", "standby", "ready", "{skygate-standby}",
		now.Format(time.RFC3339Nano), now.Unix())

	var logged []string
	cfg := DefaultConfig()
	cfg.ClusterID = "skygate-staging"
	cfg.Now = func() time.Time { return now }
	cfg.Logger = func(format string, args ...any) { logged = append(logged, format) }
	e := NewElectorWithDB(cfg, d)

	if err := e.evaluate(context.Background()); err != nil {
		t.Fatalf("evaluate() on SQLite: %v (the whole tick used to fail here)", err)
	}

	// 1. The primary must be state=failed now.
	var state string
	if err := d.QueryRow(`SELECT state FROM cluster_node WHERE id = 'node-primary'`).Scan(&state); err != nil {
		t.Fatalf("read primary state: %v", err)
	}
	if state != "failed" {
		t.Errorf("primary state = %q, want failed — transitionNode's audit INSERT was "+
			"`$3::jsonb` (PostgreSQL-only) and rolled the whole transaction back", state)
	}

	// 2. The node_health audit row must exist and carry decodable JSON + time.
	var detailRaw any
	var createdRaw any
	if err := d.QueryRow(
		`SELECT detail, created_at FROM cluster_audit WHERE action = 'node_health'`).
		Scan(&detailRaw, &createdRaw); err != nil {
		t.Fatalf("node_health audit row: %v", err)
	}
	if ts, ok := skygatedb.ParseDBTime(createdRaw); !ok {
		t.Errorf("node_health created_at %#v did not decode", createdRaw)
	} else if ts.IsZero() {
		t.Error("node_health created_at decoded to the zero time")
	}

	// 3. The failover recommendation needs a SECOND tick: evaluate() snapshots the
	//    rows before transitioning them, so the primary only becomes visible as
	//    "failed" to the recommendation pass on the next pass. That second pass is
	//    where the PostgreSQL-only dedup query (`detail->>'from_node_id'` +
	//    `created_at > NOW() - INTERVAL '5 minutes'`) used to abort the whole
	//    recommendation.
	if err := e.evaluate(context.Background()); err != nil {
		t.Fatalf("second evaluate() on SQLite: %v", err)
	}
	var recs int
	if err := d.QueryRow(
		`SELECT count(*) FROM cluster_audit WHERE action = 'failover_recommend'`).Scan(&recs); err != nil {
		t.Fatalf("count failover_recommend: %v", err)
	}
	if recs != 1 {
		t.Fatalf("failover_recommend rows = %d, want 1 (the dedup query itself used "+
			"jsonb `->>` + `NOW() - INTERVAL`, both PostgreSQL-only); log=%v", recs, logged)
	}
	var recDetail string
	if err := d.QueryRow(
		`SELECT detail FROM cluster_audit WHERE action = 'failover_recommend'`).Scan(&recDetail); err != nil {
		t.Fatalf("read recommend detail: %v", err)
	}
	if !containsSub(recDetail, `"from_node_id":"node-primary"`) ||
		!containsSub(recDetail, `"to_node_id":"node-standby"`) {
		t.Errorf("recommend detail = %s, want from_node_id=node-primary to_node_id=node-standby", recDetail)
	}

	// 4. A THIRD tick must not duplicate the recommendation — this is what the
	//    Go-side dedup has to see, and it reads its own row back through the
	//    dialect helpers.
	if err := e.evaluate(context.Background()); err != nil {
		t.Fatalf("third evaluate() on SQLite: %v", err)
	}
	if err := d.QueryRow(
		`SELECT count(*) FROM cluster_audit WHERE action = 'failover_recommend'`).Scan(&recs); err != nil {
		t.Fatalf("count failover_recommend (3rd tick): %v", err)
	}
	if recs != 1 {
		t.Errorf("failover_recommend rows after the third tick = %d, want 1 (the 5-minute "+
			"dedup window must still hold)", recs)
	}
}

// containsSub is the local substring check (the package has no strings import in
// the test file above; keeping it here keeps that file's imports minimal).
func containsSub(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
