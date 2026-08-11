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

// TestBackfill_StrategyD_TagFallback — the v0.33.1.36
// follow-up. The pre-fix Backfill only matched nodes
// registered via /my/preauth (Strategy A) or within 1
// hour of a /my/preauth key creation (Strategy C).
// Nodes registered with OPERATOR-ISSUED preauth keys
// (e.g. the skygate-host-1 node created via
// `headscale preauthkeys create --user 1 --reusable
// --expiration 720h`) are NOT in the local preauth_keys
// table, so neither A nor C fires. The node stays
// orphaned in node_owner_map until the operator
// manually applies the tag:dev-<user>-<device> tag.
//
// Strategy D closes this gap: if the node ALREADY has
// a tag:dev-<username>-* tag in headscale, AND the
// <username> portion matches the current portal user,
// we insert the node_owner_map row so the per-user ACL
// rule can match. The headscale-side tag was already
// applied (manually via `headscale nodes tag --force`);
// Strategy D only adds the DB-side row.
func TestBackfill_StrategyD_TagFallback(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (1, 'skyadmin', 1)`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// No preauth_keys entry — this is the operator-
	// issued key case.
	// Live node list: the new skygate-host-1 node
	// (id=32), with the tag:dev-skyadmin-skygate-vm
	// tag manually applied.
	node := headscale.NodeView{
		ID:           "32",
		Hostname:     "skygate-vm",
		UserID:       "tagged-devices", // synthetic, not a portal user
		PreAuthKeyID: "operator-issued", // not in our preauth_keys table
		Tags:         []string{"tag:dev-skyadmin-skygate-vm", "tag:private"},
	}
	Backfill(d, nil, []headscale.NodeView{node}, 1, "skyadmin")
	// Assert: a node_owner_map row was inserted.
	row, err := db.GetNodeOwner(d, "32")
	if err != nil {
		t.Fatalf("Strategy D should have inserted node_owner_map row, but got: %v", err)
	}
	if row.Username != "skyadmin" {
		t.Errorf("row.Username = %q, want skyadmin", row.Username)
	}
	if row.Tag != "tag:dev-skyadmin-skygate-vm" {
		t.Errorf("row.Tag = %q, want tag:dev-skyadmin-skygate-vm (Strategy D preserves the existing headscale tag)", row.Tag)
	}
}

// TestBackfill_StrategyD_OtherUserTag_NoMatch — the
// defensive check. A node with a tag:dev-<otheruser>-*
// tag must NOT be back-filled into the current user's
// snapshot (Strategy D only matches when the tag's
// username equals the current portal user).
func TestBackfill_StrategyD_OtherUserTag_NoMatch(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (1, 'skyadmin', 1)`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Node tagged for a DIFFERENT user.
	node := headscale.NodeView{
		ID:           "99",
		Hostname:     "michail-laptop",
		UserID:       "tagged-devices",
		PreAuthKeyID: "operator-issued",
		Tags:         []string{"tag:dev-michail-laptop", "tag:private"},
	}
	Backfill(d, nil, []headscale.NodeView{node}, 1, "skyadmin")
	// Assert: no row inserted (the tag's username is
	// "michail", not "skyadmin", so Strategy D doesn't
	// match).
	_, err := db.GetNodeOwner(d, "99")
	if err == nil {
		t.Errorf("Strategy D should NOT have inserted a row for another user's tag, but it did")
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

// TestBackfill_RenameUpdatesHostnameAndTag — the
// v0.33.1.20 fix. Pre-fix, the backfill would
// INSERT-OR-IGNORE a new row when a node's
// `tailscale up --hostname=X` value changed, but
// `INSERT-OR-IGNORE` is a no-op when a row already
// exists, so the stale hostname + stale
// `tag:dev-<user>-<oldHost>` stayed in node_owner_map
// forever. /admin/devices kept showing the old name,
// and headscale accumulated BOTH old AND new dev-tags
// (because AddTag never removes).
//
// v0.33.1.20 fix: the backfill detects the rename
// (existing.Hostname != n.Hostname), UPDATEs the
// row to the new hostname+tag, and (when hs != nil)
// calls UntagNode(oldTag) so headscale drops the
// stale tag.
//
// This test pins the DB half of the contract with
// hs=nil. The hs.UntagNode half is covered by the
// live verify on the VM (the /admin/devices/force-
// backfill-tags button click on 2026-08-09 produced
// the expected `tag:dev-skyadmin-svyatoslava-legacy`
// row in node_owner_map and removed the stale
// `tag:dev-skyadmin-svyatoslava` from headscale).
func TestBackfill_RenameUpdatesHostnameAndTag(t *testing.T) {
	d := openBackfillTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO portal_users (id, username, headscale_user_id) VALUES (1, 'admin', 1)`,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Preauth key for the admin user (Strategy A match)
	if _, err := d.Exec(
		`INSERT INTO preauth_keys (user_id, key, headscale_preauth_id, used) VALUES (1, 'hskey', '77', 1)`,
	); err != nil {
		t.Fatalf("seed preauth: %v", err)
	}
	// Pre-existing snapshot row with the OLD hostname
	// (the user registered their device as
	// `desktop-cj8t9me` long ago, then renamed it to
	// `cyborg` on the host via `tailscale up --hostname`).
	// UpsertNodeOwner leaves hostname='' (the column has
	// DEFAULT ''), so we follow it with
	// UpdateNodeOwnerHostnameAndTag to set the OLD value.
	if err := db.UpsertNodeOwner(d, "28", 1, "admin", "tag:dev-admin-desktop-cj8t9me", 1); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := db.UpdateNodeOwnerHostnameAndTag(d, "28", "desktop-cj8t9me", "tag:dev-admin-desktop-cj8t9me", 1); err != nil {
		t.Fatalf("seed hostname+tag: %v", err)
	}
	// Live node list: same node_id, but the NEW hostname
	// from headscale. PreAuthKeyID matches the seed
	// above (Strategy A), so matchedTag="tag:private"
	// and the loop reaches the rename-detection block.
	node := headscale.NodeView{
		ID:           "28",
		Hostname:     "cyborg",
		UserID:       "1",
		PreAuthKeyID: "77",
		Tags:         []string{"tag:private"},
	}
	// hs=nil — only the DB half of the fix is exercised
	// here. The hs.UntagNode(oldTag) call is gated on
	// hs!=nil so it's a no-op in this test, exactly the
	// behaviour the production code path takes when
	// headscale is unreachable (DB still gets updated;
	// the next backfill with a working hs cleans up the
	// stale headscale tag).
	Backfill(d, nil, []headscale.NodeView{node}, 1, "admin")
	// Assert: node_owner_map got the new hostname + new
	// dev-tag. Pre-v0.33.1.20 this would still be
	// `desktop-cj8t9me` / `tag:dev-admin-desktop-cj8t9me`
	// because INSERT-OR-IGNORE doesn't overwrite.
	got, err := db.GetNodeOwner(d, "28")
	if err != nil {
		t.Fatalf("readback: %v (rename fix may not have run)", err)
	}
	if got.Hostname != "cyborg" {
		t.Errorf("hostname = %q, want cyborg (v0.33.1.20 rename fix)", got.Hostname)
	}
	if got.Tag != "tag:dev-admin-cyborg" {
		t.Errorf("tag = %q, want tag:dev-admin-cyborg (v0.33.1.20 rename fix)", got.Tag)
	}
	// Assert: username + headscale_user_id are preserved
	// (the rename only changes hostname+tag).
	if got.Username != "admin" {
		t.Errorf("username = %q, want admin (rename must not change owner)", got.Username)
	}
}
