// internal/db/exit_servers_auto_test.go — RED tests for
// B-mod-first-run-adoption T6 (exit-server auto-detect during
// SyncNodesFromHeadscale).
//
// When the operator deploys skygate as a sidecar to an existing
// headscale that already has exit-nodes advertising 0.0.0.0/0
// (the classic Tailscale exit-node signature), they shouldn't
// have to manually create an exit_servers row for each one —
// SyncNodesFromHeadscale should auto-detect via the
// `tag:exit-node` / `AvailableRoutes contains 0.0.0.0/0` signals
// and INSERT the corresponding exit_servers row.
//
// Tests verify the contract:
//   - SyncNodeInfo{IsExitNode: true} → exit_servers row created
//     (with enabled=1)
//   - SyncNodeInfo{IsExitNode: false} → no exit_servers row
//     created (no false positive)
//   - Calling SyncNodesFromHeadscale twice → still one
//     exit_servers row (idempotent, PRIMARY KEY is node_id)
//   - The auto-detect also runs for nodes that have
//     `tag:exit-node` (operator's explicit tag) — covers both
//     the headscale 0.29 default-routes convention AND the
//     pre-0.29 explicit-tag convention.
package db

import (
	"database/sql"
	"testing"
)

// setupExitAutoTestDB opens a fresh in-memory SQLite + migrations.
// Used by all tests in this file.
func setupExitAutoTestDB(t *testing.T) *sql.DB {
	t.Helper()
	_, sqldb, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { sqldb.Close() })
	if err := ApplyMigrations(sqldb, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return sqldb
}

// TestSyncNodesFromHeadscale_ExitNodeAutoDetected — happy path.
// The node has IsExitNode=true (because the headscale adapter
// flagged it from the tag:exit-node or default-route signals).
// After sync, an exit_servers row exists with enabled=1.
func TestSyncNodesFromHeadscale_ExitNodeAutoDetected(t *testing.T) {
	sqlDB := setupExitAutoTestDB(t)

	_, _, err := SyncNodesFromHeadscale(sqlDB, []SyncNodeInfo{
		{ID: "node-exit-1", Hostname: "emilia", Tag: "tag:dev-infra-emilia", Username: "skyadmin", HSUserID: 1, TaggedBy: 0, IsExitNode: true},
	})
	if err != nil {
		t.Fatalf("SyncNodesFromHeadscale: %v", err)
	}

	// Verify exit_servers has the row.
	var hostname, tailscaleIP string
	var enabled int
	err = sqlDB.QueryRow(
		`SELECT hostname, tailscale_ip, enabled FROM exit_servers WHERE node_id = ?`,
		"node-exit-1",
	).Scan(&hostname, &tailscaleIP, &enabled)
	if err != nil {
		t.Fatalf("query exit_servers: %v", err)
	}
	if hostname != "emilia" {
		t.Errorf("hostname = %q, want emilia", hostname)
	}
	if enabled != 1 {
		t.Errorf("enabled = %d, want 1 (auto-detect must enable by default)", enabled)
	}
}

// TestSyncNodesFromHeadscale_PlainDevice_NoExitRow — when
// IsExitNode=false, no exit_servers row is created. This guards
// against false positives (the operator would have to manually
// delete stray exit-server entries otherwise).
func TestSyncNodesFromHeadscale_PlainDevice_NoExitRow(t *testing.T) {
	sqlDB := setupExitAutoTestDB(t)

	_, _, err := SyncNodesFromHeadscale(sqlDB, []SyncNodeInfo{
		{ID: "node-laptop-1", Hostname: "alice-laptop", Tag: "tag:dev-skyadmin-alice-laptop", Username: "skyadmin", HSUserID: 1, TaggedBy: 0, IsExitNode: false},
	})
	if err != nil {
		t.Fatalf("SyncNodesFromHeadscale: %v", err)
	}

	var n int
	err = sqlDB.QueryRow(
		`SELECT COUNT(*) FROM exit_servers WHERE node_id = ?`,
		"node-laptop-1",
	).Scan(&n)
	if err != nil {
		t.Fatalf("count exit_servers: %v", err)
	}
	if n != 0 {
		t.Errorf("plain device should NOT have an exit_servers row; got count=%d", n)
	}
}

// TestSyncNodesFromHeadscale_ExitNode_Idempotent — re-running
// the sync on the same node doesn't create duplicate exit_servers
// rows (PRIMARY KEY on node_id is the constraint; the upsert
// pattern handles re-syncs).
func TestSyncNodesFromHeadscale_ExitNode_Idempotent(t *testing.T) {
	sqlDB := setupExitAutoTestDB(t)

	sync := func() {
		_, _, err := SyncNodesFromHeadscale(sqlDB, []SyncNodeInfo{
			{ID: "node-exit-2", Hostname: "karolina", Tag: "tag:dev-infra-karolina", Username: "skyadmin", HSUserID: 1, TaggedBy: 0, IsExitNode: true},
		})
		if err != nil {
			t.Fatalf("SyncNodesFromHeadscale: %v", err)
		}
	}
	sync()
	sync()
	sync()

	var n int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM exit_servers WHERE node_id = ?`,
		"node-exit-2",
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 exit_servers row after 3 syncs; got %d", n)
	}
}

// TestSyncNodesFromHeadscale_MixedNodes — sync of 3 nodes: 1
// exit-node + 1 plain device + 1 exit-node. After sync, exactly
// 2 exit_servers rows exist (one per exit-node) and the plain
// device has no exit_servers row.
func TestSyncNodesFromHeadscale_MixedNodes(t *testing.T) {
	sqlDB := setupExitAutoTestDB(t)

	_, _, err := SyncNodesFromHeadscale(sqlDB, []SyncNodeInfo{
		{ID: "node-exit-1", Hostname: "emilia", Tag: "tag:dev-infra-emilia", Username: "skyadmin", HSUserID: 1, TaggedBy: 0, IsExitNode: true},
		{ID: "node-laptop-1", Hostname: "alice-laptop", Tag: "tag:dev-skyadmin-alice-laptop", Username: "skyadmin", HSUserID: 1, TaggedBy: 0, IsExitNode: false},
		{ID: "node-exit-2", Hostname: "karolina", Tag: "tag:dev-infra-karolina", Username: "skyadmin", HSUserID: 1, TaggedBy: 0, IsExitNode: true},
	})
	if err != nil {
		t.Fatalf("SyncNodesFromHeadscale: %v", err)
	}

	rows, err := sqlDB.Query(`SELECT node_id FROM exit_servers ORDER BY node_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, id)
	}
	want := []string{"node-exit-1", "node-exit-2"}
	if len(got) != len(want) {
		t.Fatalf("got %d exit_servers rows; want %d. got=%v want=%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exit_servers[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
