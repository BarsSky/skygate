// 2026-07-29: refactor-v0.30 Phase D2 — tests for
// the moved backfill function.
//
// The tests below cover the DB-only path of Backfill
// (the hs *headscale.Client is nil, so all AddTag
// calls are skipped). The full /my/devices flow
// that includes AddTag is exercised by the live
// validation on the VM (see check_v0.22.2.sh +
// check_v0.28.0.sh).
//
// Why a test exists here (even though the same DB
// helpers are tested in internal/db/): to pin the
// behaviour of the backfill function AS A WHOLE —
// the "given a node + a matching preauth key, the
// row gets inserted" contract. The db package
// tests pin the individual helpers; this test pins
// the orchestration.

package nodeownership

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"

	_ "github.com/mattn/go-sqlite3"
)

// openBackfillTestDB seeds the minimum schema the
// backfill function needs:
//   - portal_users (so the backfill can look up
//     the headscale_user_id for `GetOtherHSUserIDs`)
//   - preauth_keys (Strategy A + C match against this)
//   - node_owner_map (the snapshot table Backfill writes to)
//
// Mirrors the schema in migrations_v0.34.go (preauth_keys)
// + migrations_v0.48.go (node_owner_map with the new
// os + device_type columns). We don't run the full
// migrate() path because that pulls in 47+ other
// migrations; a focused schema is enough.
func openBackfillTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	stmts := []string{
		`CREATE TABLE portal_users (
			id              INTEGER PRIMARY KEY,
			username        TEXT NOT NULL,
			headscale_user_id INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE preauth_keys (
			id                   INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id              INTEGER NOT NULL,
			key                  TEXT NOT NULL,
			headscale_preauth_id TEXT NOT NULL DEFAULT '',
			expires_at           INTEGER NOT NULL DEFAULT 0,
			used                 INTEGER NOT NULL DEFAULT 0,
			created_at           INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE node_owner_map (
			node_id           TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username          TEXT NOT NULL DEFAULT '',
			tag               TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at         INTEGER NOT NULL DEFAULT 0,
			hostname          TEXT NOT NULL DEFAULT '',
			os                TEXT NOT NULL DEFAULT 'unknown',
			device_type       TEXT NOT NULL DEFAULT 'unknown'
		)`,
		// user_subnets is read by subnet.SyncStatus at the
		// tail of the backfill function. We add an empty
		// table (the test doesn't INSERT into it; the
		// SyncStatus call returns ErrNotFound and the
		// backfill logs + moves on). Without this, the
		// SyncStatus warning "no such table: user_subnets"
		// floods the test output.
		`CREATE TABLE user_subnets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			cidr TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			router_node_id TEXT NOT NULL DEFAULT '',
			router_hostname TEXT NOT NULL DEFAULT '',
			control_plane_url TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return d
}

// TestBackfill_StrategyA_TaglessNode_InsertsRow — the
// v0.22.2 hotfix scenario. A preauth key from skygate
// matches a fresh headscale node (no tags). The
// backfill must:
//   - INSERT a row in node_owner_map with tag="tag:private"
//     (NOT "tag:untagged" — that's the pre-v0.22.2 bug)
//   - run the v0.28.0 dev-tag application (skipped here
//     because hs is nil; the dev-tag is a no-op)
func TestBackfill_StrategyA_TaglessNode_InsertsRow(t *testing.T) {
	d := openBackfillTestDB(t)
	// Seed: portal user admin with headscale_user_id=1
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (1, 'admin', 1)`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Seed: a skygate-issued preauth key with the headscale ID captured
	if _, err := d.Exec(
		`INSERT INTO preauth_keys (user_id, key, headscale_preauth_id, used) VALUES (1, 'hskey', '98', 1)`,
	); err != nil {
		t.Fatalf("seed preauth: %v", err)
	}

	// Node: fresh registration via the preauth key above,
	// no tags yet (the v0.22.2 bug scenario).
	node := headscale.NodeView{
		ID:          "15",
		Hostname:    "workstation-3",
		UserID:      "1", // in admin's namespace
		PreAuthKeyID: "98",
		Tags:        nil, // fresh, no tags
	}

	// hs is nil — AddTag calls are skipped (this test
	// only covers the DB-write path).
	Backfill(d, nil, []headscale.NodeView{node}, 1, "admin")

	// Assert: a row was inserted with tag="tag:private"
	// (the v0.22.2 fix — pre-fix this would have been
	// "tag:untagged" and the test would fail).
	n, err := db.GetNodeOwner(d, "15")
	if err != nil {
		t.Fatalf("readback: %v (v0.22.2 fix may not have run)", err)
	}
	if n.Username != "admin" {
		t.Errorf("username = %q, want admin", n.Username)
	}
	if n.Tag != "tag:private" {
		t.Errorf("tag = %q, want tag:private (v0.22.2 fix)", n.Tag)
	}
	if n.Hostname != "workstation-3" {
		t.Errorf("hostname = %q, want workstation-3", n.Hostname)
	}
}

// TestBackfill_NonMatchingPreauth_NoInsert — the
// backfill must not insert rows for nodes that don't
// match any of this user's preauth keys (Strategy A
// strict-join miss + Strategy C temporal miss).
// Without this guard, a node registered via someone
// else's preauth would land in the wrong user's
// snapshot.
func TestBackfill_NonMatchingPreauth_NoInsert(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (1, 'admin', 1)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO preauth_keys (user_id, key, headscale_preauth_id, used) VALUES (1, 'hskey', '98', 1)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Node: same headscale_user_id but DIFFERENT preauth
	// key (someone else's key, e.g. user1's).
	node := headscale.NodeView{
		ID:          "20",
		Hostname:    "stranger",
		UserID:      "1",
		PreAuthKeyID: "999", // NOT our preauth
		Tags:        nil,
	}
	Backfill(d, nil, []headscale.NodeView{node}, 1, "admin")
	// Assert: no row inserted
	_, err := db.GetNodeOwner(d, "20")
	if err == nil {
		t.Errorf("expected no row, but row was inserted (backfill stole a stranger's node)")
	}
}

// TestBackfill_GCPassRemovesOrphanRows — the GC
// pass must drop snapshot rows for nodes that no
// longer exist in headscale. Without it, a user
// who deleted their device would keep seeing it on
// the dashboard forever (the original user1 "0/0"
// report symptom).
func TestBackfill_GCPassRemovesOrphanRows(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (1, 'admin', 1)`,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Pre-existing snapshot row for a node that no longer
	// exists in headscale. The backfill must remove it.
	if err := db.UpsertNodeOwner(d, "100", 1, "admin", "tag:private", 1); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	// Live node list: empty (the "100" node is gone).
	Backfill(d, nil, nil, 1, "admin")
	// Assert: snapshot row was removed.
	_, err := db.GetNodeOwner(d, "100")
	if err == nil {
		t.Errorf("orphan row not removed by GC pass")
	} else if !strings.Contains(err.Error(), "no row") {
		t.Errorf("unexpected error: %v", err)
	}
}
