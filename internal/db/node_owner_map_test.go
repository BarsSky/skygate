// Tests for the node_owner_map helpers in internal/db/node_owner_map.go.
//
// Этап 10 part 4 (2026-07-12). These tests cover the typed read /
// write helpers that replaced 17 raw SQL strings scattered across
// the handlers and telegram packages. The strategy is:
//
//   1. Build a fresh in-memory SQLite DB with a schema that matches
//      the production migration (migrations_v0.25 + v0.28).
//      We do NOT call the production migrate() because the test
//      needs the row shape without a real portal_users FK; the
//      schema here is the bare minimum.
//   2. Exercise each helper against the schema. Read helpers check
//      both the populated and empty cases; write helpers check that
//      the operation has the expected row shape after returning.

package db

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// openNodeOwnerMapTestDB opens a fresh in-memory DB with the
// production-shaped node_owner_map table. The schema is the
// v0.25 CREATE plus the v0.28 columns (headscale_user_id /
// username / tag / tagged_by_user_id / tagged_at) merged in
// — see internal/db/migrations_v0.25.go and v0.28.go for the
// authoritative version.
func openNodeOwnerMapTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE node_owner_map (
			node_id           TEXT PRIMARY KEY,
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			username          TEXT NOT NULL DEFAULT '',
			tag               TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			tagged_at         INTEGER NOT NULL DEFAULT 0,
			hostname          TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			_ = d.Close()
			t.Fatalf("schema: %v", err)
		}
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestUpsertNodeOwner_InsertsAndReplaces(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	// First insert.
	if err := UpsertNodeOwner(d, "node-1", 100, "alice", "tag:public", 1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	n, err := GetNodeOwner(d, "node-1")
	if err != nil {
		t.Fatalf("readback after first upsert: %v", err)
	}
	if n.Username != "alice" || n.Tag != "tag:public" || n.HeadscaleUserID != 100 {
		t.Errorf("unexpected row after first upsert: %+v", n)
	}
	// Re-upsert with new tag must replace, not silently no-op.
	if err := UpsertNodeOwner(d, "node-1", 200, "bob", "tag:private", 2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	n, err = GetNodeOwner(d, "node-1")
	if err != nil {
		t.Fatalf("readback after second upsert: %v", err)
	}
	if n.Username != "bob" || n.Tag != "tag:private" || n.HeadscaleUserID != 200 {
		t.Errorf("re-upsert did not replace: got %+v, want bob/tag:private/200", n)
	}
}

func TestInsertIgnoreNodeOwner_RespectsExistingRow(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	// Pre-seed: row exists with tag:public.
	if err := UpsertNodeOwner(d, "node-1", 100, "admin", "tag:public", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// InsertIgnore with a different tag must NOT overwrite.
	if err := InsertIgnoreNodeOwner(d, "node-1", 200, "alice", "tag:private", 2); err != nil {
		t.Fatalf("insertignore: %v", err)
	}
	n, err := GetNodeOwner(d, "node-1")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.Tag != "tag:public" || n.Username != "admin" {
		t.Errorf("InsertIgnore overwrote admin row: got %+v", n)
	}
}

func TestInsertIgnoreNodeOwner_InsertsWhenMissing(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	if err := InsertIgnoreNodeOwner(d, "node-1", 100, "alice", "tag:private", 1); err != nil {
		t.Fatalf("insertignore: %v", err)
	}
	n, err := GetNodeOwner(d, "node-1")
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if n.Tag != "tag:private" || n.Username != "alice" {
		t.Errorf("InsertIgnore did not insert: got %+v", n)
	}
}

func TestUpgradeStaleNodeOwnerToPrivate_OnlyTouchesUntagged(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	// 3 rows: empty, tag:untagged, tag:public. The first two should
	// upgrade; the third must stay.
	_ = UpsertNodeOwner(d, "n-empty", 0, "alice", "", 0)
	_ = UpsertNodeOwner(d, "n-untagged", 0, "alice", "tag:untagged", 0)
	_ = UpsertNodeOwner(d, "n-public", 0, "admin", "tag:public", 0)
	if err := UpgradeStaleNodeOwnerToPrivate(d, "n-empty", "tag:private", 1); err != nil {
		t.Fatalf("upgrade n-empty: %v", err)
	}
	if err := UpgradeStaleNodeOwnerToPrivate(d, "n-untagged", "tag:private", 1); err != nil {
		t.Fatalf("upgrade n-untagged: %v", err)
	}
	if err := UpgradeStaleNodeOwnerToPrivate(d, "n-public", "tag:private", 1); err != nil {
		t.Fatalf("upgrade n-public (should be no-op): %v", err)
	}
	for _, c := range []struct {
		nodeID, wantTag string
	}{
		{"n-empty", "tag:private"},
		{"n-untagged", "tag:private"},
		{"n-public", "tag:public"}, // admin-set, must NOT be clobbered
	} {
		n, err := GetNodeOwner(d, c.nodeID)
		if err != nil {
			t.Errorf("readback %s: %v", c.nodeID, err)
			continue
		}
		if n.Tag != c.wantTag {
			t.Errorf("%s: tag=%q, want %q", c.nodeID, n.Tag, c.wantTag)
		}
	}
}

func TestDeleteNodeOwnerByID(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "node-1", 0, "alice", "tag:private", 1)
	_ = UpsertNodeOwner(d, "node-2", 0, "alice", "tag:private", 1)
	if err := DeleteNodeOwnerByID(d, "node-1", "alice"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := GetNodeOwner(d, "node-1"); err != ErrNodeOwnerNotFound {
		t.Errorf("expected ErrNodeOwnerNotFound for deleted row, got %v", err)
	}
	// node-2 must still be there.
	if _, err := GetNodeOwner(d, "node-2"); err != nil {
		t.Errorf("node-2 unexpectedly deleted: %v", err)
	}
}

func TestDeleteNodeOwnerByNodeTag(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "node-1", 0, "alice", "tag:public", 1)
	if err := DeleteNodeOwnerByNodeTag(d, "node-1", "tag:public"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := GetNodeOwner(d, "node-1"); err != ErrNodeOwnerNotFound {
		t.Errorf("expected row to be deleted, got %v", err)
	}
}

func TestDeleteNodeOwnersByUser(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 0, "alice", "tag:private", 1)
	_ = UpsertNodeOwner(d, "n2", 0, "alice", "tag:public", 1)
	_ = UpsertNodeOwner(d, "n3", 0, "bob", "tag:private", 1)
	if err := DeleteNodeOwnersByUser(d, "alice"); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	for _, nid := range []string{"n1", "n2"} {
		if _, err := GetNodeOwner(d, nid); err != ErrNodeOwnerNotFound {
			t.Errorf("%s unexpectedly still present: %v", nid, err)
		}
	}
	if _, err := GetNodeOwner(d, "n3"); err != nil {
		t.Errorf("bob's row deleted: %v", err)
	}
}

func TestListNodeOwnerNodeIDsByUsername_Empty(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	got, err := ListNodeOwnerNodeIDsByUsername(d, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestListNodeOwnerNodeIDsByUsername_Populated(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 0, "alice", "tag:private", 1)
	_ = UpsertNodeOwner(d, "n2", 0, "alice", "tag:public", 1)
	_ = UpsertNodeOwner(d, "n3", 0, "bob", "tag:private", 1)
	got, err := ListNodeOwnerNodeIDsByUsername(d, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 rows for alice, got %d (%v)", len(got), got)
	}
}

func TestListNodeOwnersByUsername_ReturnsFullRows(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 7, "alice", "tag:private", 1)
	_ = UpsertNodeOwner(d, "n2", 8, "alice", "tag:public", 1)
	got, err := ListNodeOwnersByUsername(d, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	tags := map[string]string{}
	for _, n := range got {
		tags[n.NodeID] = n.Tag
	}
	if tags["n1"] != "tag:private" || tags["n2"] != "tag:public" {
		t.Errorf("unexpected tag mapping: %v", tags)
	}
}

func TestListAllNodeOwners_GroupedCorrectly(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 0, "alice", "tag:private", 1)
	_ = UpsertNodeOwner(d, "n2", 0, "bob", "tag:private", 1)
	_ = UpsertNodeOwner(d, "n3", 0, "alice", "tag:public", 1)
	got, err := ListAllNodeOwners(d)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 rows, got %d", len(got))
	}
}

func TestListExitNodeOwners_FiltersByTag(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 0, "alice", "tag:private", 1)
	_ = UpsertNodeOwner(d, "n2", 0, "bob", "tag:exit-node", 1)
	_ = UpsertNodeOwner(d, "n3", 0, "alice", "tag:exit-node", 1)
	got, err := ListExitNodeOwners(d)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 exit-nodes, got %d (%+v)", len(got), got)
	}
	for _, n := range got {
		if n.Tag != "tag:exit-node" {
			t.Errorf("non-exit-node leaked into ListExitNodeOwners: %+v", n)
		}
	}
}

func TestCountNodeOwnerByNodeUser(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 0, "alice", "tag:private", 1)
	n, err := CountNodeOwnerByNodeUser(d, "n1", "alice")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("expected count=1, got %d", n)
	}
	n, err = CountNodeOwnerByNodeUser(d, "n1", "bob")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("expected count=0 for wrong user, got %d", n)
	}
}

func TestGetNodeOwner_NotFound(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_, err := GetNodeOwner(d, "no-such-node")
	if err != ErrNodeOwnerNotFound {
		t.Errorf("expected ErrNodeOwnerNotFound, got %v", err)
	}
}

// 2026-07-15: Этап 14 v13 — tests for the lazy hostname backfill
// helpers (BackfillEmptyHostnames + AnyHostnameEmpty). The bot's
// /my_nodes and /nodes used to silently show bare node_ids when
// the migration-v0.34 hostname column was empty; these helpers
// let the read paths self-heal by pulling the friendly name from
// headscale and updating the rows that need it.

func TestBackfillEmptyHostnames_OnlyUpdatesEmpty(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	// Two rows: one with empty hostname (will be filled), one with
	// a non-empty hostname (must NOT be touched).
	_ = UpsertNodeOwner(d, "n-empty", 0, "alice", "tag:private", 1)
	_ = UpsertNodeOwner(d, "n-known", 0, "alice", "tag:private", 1)
	if _, err := d.Exec(`UPDATE node_owner_map SET hostname = 'old-name' WHERE node_id = 'n-known'`); err != nil {
		t.Fatalf("seed hostname: %v", err)
	}
	updated, err := BackfillEmptyHostnames(d, map[string]string{
		"n-empty":  "fresh-name",
		"n-known":  "would-clobber",
		"n-missing": "no-such-row", // not in the table; must be silently ignored
	})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 1 {
		t.Errorf("expected 1 row updated, got %d", updated)
	}
	got, _ := GetNodeOwner(d, "n-empty")
	if got.Hostname != "fresh-name" {
		t.Errorf("n-empty: hostname=%q, want fresh-name", got.Hostname)
	}
	got, _ = GetNodeOwner(d, "n-known")
	if got.Hostname != "old-name" {
		t.Errorf("n-known: hostname=%q, want old-name (must NOT be overwritten)", got.Hostname)
	}
}

func TestBackfillEmptyHostnames_EmptyMapIsNoop(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 0, "alice", "tag:private", 1)
	updated, err := BackfillEmptyHostnames(d, map[string]string{})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 0 {
		t.Errorf("expected 0 updates, got %d", updated)
	}
}

func TestBackfillEmptyHostnames_EmptyValueSkipped(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	_ = UpsertNodeOwner(d, "n1", 0, "alice", "tag:private", 1)
	// Map entry with empty value must be a no-op (we never write "").
	updated, err := BackfillEmptyHostnames(d, map[string]string{"n1": ""})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if updated != 0 {
		t.Errorf("expected 0 updates for empty value, got %d", updated)
	}
	got, _ := GetNodeOwner(d, "n1")
	if got.Hostname != "" {
		t.Errorf("hostname=%q, want \"\" (empty value must not be written)", got.Hostname)
	}
}

func TestAnyHostnameEmpty(t *testing.T) {
	cases := []struct {
		name   string
		owners []NodeOwner
		want   bool
	}{
		{"empty slice", nil, false},
		{"all set", []NodeOwner{{NodeID: "a", Hostname: "x"}}, false},
		{"one missing", []NodeOwner{{NodeID: "a", Hostname: "x"}, {NodeID: "b", Hostname: ""}}, true},
		{"all missing", []NodeOwner{{NodeID: "a", Hostname: ""}}, true},
	}
	for _, c := range cases {
		if got := AnyHostnameEmpty(c.owners); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
