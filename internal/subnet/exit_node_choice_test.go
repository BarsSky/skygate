package subnet

// exit_node_choice_test.go — v0.19.0 tests for the
// preferred exit-node per user_subnet.
//
// The schema migration (preferred_exit_node_id column
// on user_subnets) is exercised end-to-end by the
// integration tests (TestGetAdminUserSubnet_* +
// TestPostAdminUserSubnet*). This file focuses on
// the unit-level helpers in exit_node_choice.go so
// the Set / Clear / List paths are covered even when
// the schema is freshly migrated.
//
// 2026-07-20.

import (
	"database/sql"
	"testing"
)

// testDBForExitNode is a minimal in-memory SQLite
// with the user_subnets table (and the new
// preferred_exit_node_id column). We use the
// shared in-memory cache so the connection pool
// sees the same DB.
func testDBForExitNode(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", "file:exit_node_choice_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// portal_users is required for FK CASCADE
	// (user_subnets.user_id → portal_users.id).
	// The new column is created here so the test
	// doesn't depend on the production migration
	// having run.
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS portal_users (
			id INTEGER PRIMARY KEY,
			username TEXT NOT NULL DEFAULT '',
			headscale_url TEXT NOT NULL DEFAULT '',
			headscale_api_key_enc TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS user_subnets (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL UNIQUE,
			cidr TEXT NOT NULL UNIQUE,
			subnet_bits INTEGER NOT NULL DEFAULT 24,
			control_plane_url TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			router_node_id TEXT NOT NULL DEFAULT '',
			router_container_id TEXT NOT NULL DEFAULT '',
			router_hostname TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			preferred_exit_node_id TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			t.Fatalf("schema %q: %v", s, err)
		}
	}
	return d
}

func seedPortalUserForExitNode(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(`INSERT INTO portal_users (username) VALUES (?)`, username)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedUserSubnetForExitNode(t *testing.T, d *sql.DB, userID int64, cidr string) {
	t.Helper()
	_, err := d.Exec(`
		INSERT INTO user_subnets (user_id, cidr, created_at, updated_at)
		VALUES (?, ?, 0, 0)
	`, userID, cidr)
	if err != nil {
		t.Fatalf("seed subnet: %v", err)
	}
}

// TestGetPreferredExitNode_EmptyByDefault — a fresh
// subnet has preferred_exit_node_id = ''.
func TestGetPreferredExitNode_EmptyByDefault(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid := seedPortalUserForExitNode(t, d, "alice")
	seedUserSubnetForExitNode(t, d, uid, "10.0.1.0/24")
	got, err := GetPreferredExitNode(d, uid)
	if err != nil {
		t.Fatalf("GetPreferredExitNode: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty choice for fresh subnet, got %q", got)
	}
}

// TestSetPreferredExitNode_Roundtrip — Set then Get
// returns the same value. Cross-row isolation: a
// different user's choice doesn't leak.
func TestSetPreferredExitNode_Roundtrip(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid1 := seedPortalUserForExitNode(t, d, "alice")
	uid2 := seedPortalUserForExitNode(t, d, "bob")
	seedUserSubnetForExitNode(t, d, uid1, "10.0.1.0/24")
	seedUserSubnetForExitNode(t, d, uid2, "10.0.2.0/24")
	if err := SetPreferredExitNode(d, uid1, "11"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got1, _ := GetPreferredExitNode(d, uid1)
	got2, _ := GetPreferredExitNode(d, uid2)
	if got1 != "11" {
		t.Errorf("alice: expected 11, got %q", got1)
	}
	if got2 != "" {
		t.Errorf("bob: expected empty (no cross-row leak), got %q", got2)
	}
}

// TestSetPreferredExitNode_Overwrites — calling
// Set a second time replaces the first value.
func TestSetPreferredExitNode_Overwrites(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid := seedPortalUserForExitNode(t, d, "alice")
	seedUserSubnetForExitNode(t, d, uid, "10.0.1.0/24")
	if err := SetPreferredExitNode(d, uid, "11"); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := SetPreferredExitNode(d, uid, "12"); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	got, _ := GetPreferredExitNode(d, uid)
	if got != "12" {
		t.Errorf("expected overwrite to 12, got %q", got)
	}
}

// TestSetPreferredExitNode_RejectsEmpty — passing ""
// is an error. Use ClearPreferredExitNode to unset.
func TestSetPreferredExitNode_RejectsEmpty(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid := seedPortalUserForExitNode(t, d, "alice")
	seedUserSubnetForExitNode(t, d, uid, "10.0.1.0/24")
	if err := SetPreferredExitNode(d, uid, ""); err == nil {
		t.Fatal("expected error for empty node_id")
	}
}

// TestSetPreferredExitNode_NoSubnet_ErrNotFound —
// setting a choice for a user with no subnet row
// returns ErrNotFound (you can't pick an exit-node
// for a subnet you don't have).
func TestSetPreferredExitNode_NoSubnet_ErrNotFound(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid := seedPortalUserForExitNode(t, d, "alice")
	// Note: no seedUserSubnetForExitNode call
	err := SetPreferredExitNode(d, uid, "11")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestClearPreferredExitNode_Roundtrip — Set then
// Clear then Get returns "".
func TestClearPreferredExitNode_Roundtrip(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid := seedPortalUserForExitNode(t, d, "alice")
	seedUserSubnetForExitNode(t, d, uid, "10.0.1.0/24")
	if err := SetPreferredExitNode(d, uid, "11"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := ClearPreferredExitNode(d, uid); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, _ := GetPreferredExitNode(d, uid)
	if got != "" {
		t.Errorf("expected empty after Clear, got %q", got)
	}
}

// TestClearPreferredExitNode_NoSubnet_ErrNotFound —
// clearing for a user with no subnet row is
// ErrNotFound (mirrors Set).
func TestClearPreferredExitNode_NoSubnet_ErrNotFound(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid := seedPortalUserForExitNode(t, d, "alice")
	err := ClearPreferredExitNode(d, uid)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// TestListUsersWithPreferredExitNode_OnlyNonEmpty —
// the list filter excludes users without a choice.
// v0.19.0 contract: the ACL builder iterates this
// list to populate dns.extra_records, so it MUST
// skip empty choices (otherwise the JSON would
// include `"value": ""` records).
func TestListUsersWithPreferredExitNode_OnlyNonEmpty(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid1 := seedPortalUserForExitNode(t, d, "alice")
	uid2 := seedPortalUserForExitNode(t, d, "bob")
	uid3 := seedPortalUserForExitNode(t, d, "charlie")
	seedUserSubnetForExitNode(t, d, uid1, "10.0.1.0/24")
	seedUserSubnetForExitNode(t, d, uid2, "10.0.2.0/24")
	seedUserSubnetForExitNode(t, d, uid3, "10.0.3.0/24")
	// alice + bob pick, charlie doesn't.
	_ = SetPreferredExitNode(d, uid1, "11")
	_ = SetPreferredExitNode(d, uid2, "12")
	choices, err := ListUsersWithPreferredExitNode(d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(choices) != 2 {
		t.Errorf("expected 2 choices (alice + bob), got %d", len(choices))
	}
	// Order is by user_id ASC. The first-seeded
	// user is uid1 (alice), so the first choice
	// should be alice's.
	if choices[0].UserID != uid1 || choices[0].NodeID != "11" {
		t.Errorf("first choice: got %+v, want {uid1, 11}", choices[0])
	}
	if choices[1].UserID != uid2 || choices[1].NodeID != "12" {
		t.Errorf("second choice: got %+v, want {uid2, 12}", choices[1])
	}
}

// TestListUsersWithPreferredExitNode_Empty — no
// choices returns an empty slice (not nil error).
// The ACL builder's `len(records) > 0` guard
// depends on this — the for-range must be a no-op
// when the slice is empty.
func TestListUsersWithPreferredExitNode_Empty(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	uid := seedPortalUserForExitNode(t, d, "alice")
	seedUserSubnetForExitNode(t, d, uid, "10.0.1.0/24")
	choices, err := ListUsersWithPreferredExitNode(d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(choices) != 0 {
		t.Errorf("expected 0 choices, got %d", len(choices))
	}
}

// TestListUsersWithPreferredExitNode_NoSubnets —
// no user_subnets rows at all → empty list, no
// error. Same as above; just confirms the SQL
// handles the empty-table case.
func TestListUsersWithPreferredExitNode_NoSubnets(t *testing.T) {
	d := testDBForExitNode(t)
	defer d.Close()
	choices, err := ListUsersWithPreferredExitNode(d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(choices) != 0 {
		t.Errorf("expected 0 choices, got %d", len(choices))
	}
}
