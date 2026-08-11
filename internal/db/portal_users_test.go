package db

import (
	"database/sql"
	"errors"
	"sort"
	"testing"
)

// 2026-07-11: Этап 10 part 1 — tests for portal_users helpers. Pattern
// matches Этап 9 (db_helpers_test.go, db_helpers_part2_test.go):
// openTestDB() returns a fresh sqlite with the full migration chain
// applied, so the helpers are exercised against the real schema.
//
// Each test seeds a known fixture, runs the helper, and asserts the
// return value. We don't try to test every error path — the schema
// constraints (UNIQUE on username) give us coverage "for free" via
// SQL errors when we pass invalid input.

// seedPortalUser inserts one portal_users row and returns its id.
// Centralising the INSERT keeps the individual tests focused on
// what they're actually testing.
//
// headscale_user_id is nullable in the schema. Callers that want to
// model "user exists but no headscale link yet" should use
// seedPortalUserNoHS, which writes NULL. Using seedPortalUser with
// hsID=0 stores 0 — which Scan() into sql.NullInt64 will report
// as Valid=true, Int64=0. The two are not interchangeable and
// the tests below are careful to use the right one.
func seedPortalUser(t *testing.T, d *sql.DB, username, hash string, isAdmin bool, hsID int64) int64 {
	t.Helper()
	adminI := 0
	if isAdmin {
		adminI = 1
	}
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id) VALUES (?,?,?,?)`,
		username, hash, adminI, hsID)
	if err != nil {
		t.Fatalf("seedPortalUser(%q): %v", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// seedPortalUserNoHS is the NULL-hs-link variant. The schema
// (migrations_v0.25.go) declares headscale_user_id as nullable
// without a default, so omitting the column from the INSERT writes
// NULL — which Scan() into sql.NullInt64 reports as Valid=false.
// That matches the "user exists but no headscale link yet" case
// that handlers like my_preauth and my_keys use to short-circuit
// with 400 "no headscale user linked".
func seedPortalUserNoHS(t *testing.T, d *sql.DB, username, hash string, isAdmin bool) int64 {
	t.Helper()
	adminI := 0
	if isAdmin {
		adminI = 1
	}
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES (?,?,?)`,
		username, hash, adminI)
	if err != nil {
		t.Fatalf("seedPortalUserNoHS(%q): %v", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// --- GetUserCredentials ---

func TestGetUserCredentials(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice", "hash-alice", false, 100)
	seedPortalUser(t, d, "admin", "hash-admin", true, 1)

	// Regular user
	id, hash, isAdmin, err := GetUserCredentials(d, "alice")
	if err != nil {
		t.Fatalf("GetUserCredentials alice: %v", err)
	}
	if id == 0 || hash != "hash-alice" || isAdmin {
		t.Errorf("alice = (%d, %q, %v), want (non-zero, hash-alice, false)", id, hash, isAdmin)
	}

	// Admin
	id, hash, isAdmin, err = GetUserCredentials(d, "admin")
	if err != nil {
		t.Fatalf("GetUserCredentials admin: %v", err)
	}
	if hash != "hash-admin" || !isAdmin {
		t.Errorf("admin = (%d, %q, %v), want (non-zero, hash-admin, true)", id, hash, isAdmin)
	}

	// Not found
	_, _, _, err = GetUserCredentials(d, "nobody")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("nobody err = %v, want ErrUserNotFound", err)
	}
}

// --- GetUserIDByName ---

func TestGetUserIDByName(t *testing.T) {
	d := openTestDB(t)
	id := seedPortalUser(t, d, "bob", "h", false, 0)

	got, err := GetUserIDByName(d, "bob")
	if err != nil {
		t.Fatalf("GetUserIDByName: %v", err)
	}
	if got != id {
		t.Errorf("got id = %d, want %d", got, id)
	}

	_, err = GetUserIDByName(d, "ghost")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("ghost err = %v, want ErrUserNotFound", err)
	}
}

// --- GetUserNameByID ---

func TestGetUserNameByID(t *testing.T) {
	d := openTestDB(t)
	id := seedPortalUser(t, d, "carol", "h", false, 0)

	got, err := GetUserNameByID(d, id)
	if err != nil {
		t.Fatalf("GetUserNameByID: %v", err)
	}
	if got != "carol" {
		t.Errorf("got %q, want carol", got)
	}

	_, err = GetUserNameByID(d, 9999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("9999 err = %v, want ErrUserNotFound", err)
	}
}

// --- GetPasswordHashByID ---

func TestGetPasswordHashByID(t *testing.T) {
	d := openTestDB(t)
	id := seedPortalUser(t, d, "dave", "secret-hash", false, 0)

	got, err := GetPasswordHashByID(d, id)
	if err != nil {
		t.Fatalf("GetPasswordHashByID: %v", err)
	}
	if got != "secret-hash" {
		t.Errorf("got %q, want secret-hash", got)
	}

	_, err = GetPasswordHashByID(d, 9999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("9999 err = %v, want ErrUserNotFound", err)
	}
}

// --- GetHSIDByID ---

func TestGetHSIDByID(t *testing.T) {
	d := openTestDB(t)
	withHs := seedPortalUser(t, d, "erin", "h", false, 42)
	noHs := seedPortalUserNoHS(t, d, "frank", "h", false)

	// Linked user
	got, err := GetHSIDByID(d, withHs)
	if err != nil {
		t.Fatalf("GetHSIDByID linked: %v", err)
	}
	if !got.Valid || got.Int64 != 42 {
		t.Errorf("linked got = %+v, want Valid=true Int64=42", got)
	}

	// Unlinked user — headscale_user_id is NULL → Valid=false
	got, err = GetHSIDByID(d, noHs)
	if err != nil {
		t.Fatalf("GetHSIDByID unlinked: %v", err)
	}
	if got.Valid {
		t.Errorf("unlinked got.Valid = true, want false (column was NULL)")
	}

	// Non-existent user — handler treats this the same as "no link"
	// (both should fail the preauth-key flow), so the helper returns
	// the zero-value NullInt64 with no error. Callers that need to
	// distinguish should use GetUserNameByID first.
	got, err = GetHSIDByID(d, 9999)
	if err != nil {
		t.Errorf("non-existent should NOT error, got %v", err)
	}
	if got.Valid {
		t.Errorf("non-existent Valid = true, want false")
	}
}

// --- GetUserNameAndHSByID ---

func TestGetUserNameAndHSByID(t *testing.T) {
	d := openTestDB(t)
	withHs := seedPortalUser(t, d, "gina", "h", false, 7)
	noHs := seedPortalUserNoHS(t, d, "henry", "h", false)

	// Linked
	name, hs, err := GetUserNameAndHSByID(d, withHs)
	if err != nil {
		t.Fatalf("GetUserNameAndHSByID linked: %v", err)
	}
	if name != "gina" || !hs.Valid || hs.Int64 != 7 {
		t.Errorf("linked = (%q, %+v), want (gina, Valid=true Int64=7)", name, hs)
	}

	// Unlinked — name is "henry", hs is invalid (NULL)
	name, hs, err = GetUserNameAndHSByID(d, noHs)
	if err != nil {
		t.Fatalf("GetUserNameAndHSByID unlinked: %v", err)
	}
	if name != "henry" || hs.Valid {
		t.Errorf("unlinked = (%q, %+v), want (henry, Valid=false)", name, hs)
	}

	// Not found
	_, _, err = GetUserNameAndHSByID(d, 9999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("9999 err = %v, want ErrUserNotFound", err)
	}
}

// --- GetUserHSByID ---

func TestGetUserHSByID(t *testing.T) {
	d := openTestDB(t)
	withHs := seedPortalUser(t, d, "iris", "h", false, 9)

	// Linked
	hs, name, err := GetUserHSByID(d, withHs)
	if err != nil {
		t.Fatalf("GetUserHSByID linked: %v", err)
	}
	if !hs.Valid || hs.Int64 != 9 {
		t.Errorf("linked hs = %+v, want Valid=true Int64=9", hs)
	}
	if name != "iris" {
		t.Errorf("linked name = %q, want iris", name)
	}

	// Unlinked — headscale_user_id NULL
	noHs := seedPortalUserNoHS(t, d, "jack", "h", false)
	hs, name, err = GetUserHSByID(d, noHs)
	if err != nil {
		t.Fatalf("GetUserHSByID unlinked: %v", err)
	}
	if hs.Valid {
		t.Errorf("unlinked hs.Valid = true, want false")
	}
	if name != "jack" {
		t.Errorf("unlinked name = %q, want jack", name)
	}
}

// --- GetAllPortalUsers ---

func TestGetAllPortalUsers(t *testing.T) {
	d := openTestDB(t)
	// 2026-08-10 v0.33.1.39: V054 auto-creates the 'infra' user
	// at startup (a system account for skygate-host-* devices).
	// The test DB goes through the same migration chain, so the
	// initial state has 1 user (infra), not 0.
	//
	// 2026-08-10 v0.33.1.41 #2: V054 uses a reserved id=99
	// (system user) so it doesn't collide with AUTOINCREMENT
	// ids in fresh test DBs. The first seeded user ("u1") now
	// gets id=100 (or higher), not id=1 — look up by name, not
	// by index.
	users, err := GetAllPortalUsers(d)
	if err != nil {
		t.Fatalf("GetAllPortalUsers empty: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("empty got %d users, want 1 (the auto-created 'infra')", len(users))
	}
	if users[0].Username != "infra" {
		t.Errorf("users[0] = %+v, want Username=infra", users[0])
	}
	if users[0].ID != 99 {
		t.Errorf("users[0].ID = %d, want 99 (V054 reserved id)", users[0].ID)
	}

	// Three more users, mix of admin + linked
	seedPortalUser(t, d, "u1", "h", false, 0)
	seedPortalUser(t, d, "u2", "h", true, 1)
	seedPortalUser(t, d, "u3", "h", false, 100)

	users, err = GetAllPortalUsers(d)
	if err != nil {
		t.Fatalf("GetAllPortalUsers: %v", err)
	}
	if len(users) != 4 {
		t.Fatalf("got %d users, want 4 (1 infra + 3 seeded)", len(users))
	}
	// Lookup by name — id ordering is infra(99), u1(100), u2(101),
	// u3(102) which is non-trivial, so use a name→user map.
	got := map[string]User{}
	for _, u := range users {
		got[u.Username] = u
	}
	if got["u1"].IsAdmin || got["u1"].HeadscaleUserID != 0 {
		t.Errorf("u1 = %+v, want isAdmin=false hsID=0", got["u1"])
	}
	if !got["u2"].IsAdmin || got["u2"].HeadscaleUserID != 1 {
		t.Errorf("u2 = %+v, want isAdmin=true hsID=1", got["u2"])
	}
	if got["u3"].IsAdmin || got["u3"].HeadscaleUserID != 100 {
		t.Errorf("u3 = %+v, want isAdmin=false hsID=100", got["u3"])
	}
	if got["infra"].ID != 99 {
		t.Errorf("infra.ID = %d, want 99", got["infra"].ID)
	}
	// PasswordHash must be empty (we don't read it from DB and mustn't
	// leak it through the struct even by accident).
	for _, u := range users {
		if u.PasswordHash != "" {
			t.Errorf("%s.PasswordHash = %q, want empty", u.Username, u.PasswordHash)
		}
	}
}

// --- v0.16.6 subnets denorm columns ---

// TestGetAllPortalUsers_PopulatesSubnetDenorm — the GetAllPortalUsers
// query should populate the new subnet_cidr / subnet_status /
// subnet_router_node_id denorm columns so /admin/users can show
// "10.0.42.0/24 · active" without a JOIN. Regression guard for
// v0.16.6: the GetAllPortalUsers query was extended from 6 to 9
// columns; a typo in the column list would silently leave the
// subnet fields empty.
//
// 2026-08-10: v0.33.1.41 #2 — V054 auto-creates the 'infra' user
// at the reserved id=99 (system user). The test DB now starts
// with 1 user (infra, id=99). After seeding alice (auto-increment
// → id=100), the result has 2 users: infra + alice. The test
// looks up alice by name (not by index) because infra is now
// the first row, not alice.
func TestGetAllPortalUsers_PopulatesSubnetDenorm(t *testing.T) {
	d := openTestDB(t)
	id := seedPortalUser(t, d, "alice", "h", false, 0)
	// Simulate manager denorm sync (what subnet.Create does).
	_, err := d.Exec(`UPDATE portal_users SET subnet_cidr=?, subnet_status=?, subnet_router_node_id=? WHERE id=?`,
		"10.0.42.0/24", "active", "11", id)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	users, err := GetAllPortalUsers(d)
	if err != nil {
		t.Fatalf("GetAllPortalUsers: %v", err)
	}
	// 1 auto-created infra + 1 alice = 2 users
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2 (1 infra + 1 alice)", len(users))
	}
	// Find alice by name (sorted by id ASC: infra=99, alice=100)
	var u User
	for _, x := range users {
		if x.Username == "alice" {
			u = x
			break
		}
	}
	if u.Username != "alice" {
		t.Fatalf("alice not found in users: %+v", users)
	}
	if u.SubnetCIDR != "10.0.42.0/24" {
		t.Errorf("SubnetCIDR = %q, want 10.0.42.0/24", u.SubnetCIDR)
	}
	if u.SubnetStatus != "active" {
		t.Errorf("SubnetStatus = %q, want active", u.SubnetStatus)
	}
	if u.SubnetRouterNodeID != 11 {
		t.Errorf("SubnetRouterNodeID = %d, want 11", u.SubnetRouterNodeID)
	}
}

// TestGetAllPortalUsers_EmptyDenormDefaults — when no subnet has been
// allocated, the denorm columns should be empty string / 0, not crash
// or read garbage.
//
// 2026-08-10 v0.33.1.41 #2: V054 inserts the 'infra' row at id=99,
// so the first row returned by GetAllPortalUsers is infra (id=99),
// not the seeded "bob". Lookup by name.
func TestGetAllPortalUsers_EmptyDenormDefaults(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "bob", "h", false, 0)

	users, err := GetAllPortalUsers(d)
	if err != nil {
		t.Fatalf("GetAllPortalUsers: %v", err)
	}
	// Find bob by name (infra=99 is the first row, bob=100 is second).
	var u User
	for _, x := range users {
		if x.Username == "bob" {
			u = x
			break
		}
	}
	if u.Username != "bob" {
		t.Fatalf("bob not found in users: %+v", users)
	}
	if u.SubnetCIDR != "" {
		t.Errorf("SubnetCIDR = %q, want empty", u.SubnetCIDR)
	}
	// status is "none" by default in the migration
	if u.SubnetStatus != "none" && u.SubnetStatus != "" {
		t.Errorf("SubnetStatus = %q, want none or empty", u.SubnetStatus)
	}
	if u.SubnetRouterNodeID != 0 {
		t.Errorf("SubnetRouterNodeID = %d, want 0", u.SubnetRouterNodeID)
	}
}

// --- GetPortalUsernames ---

func TestGetPortalUsernames(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "a", "h", false, 0)
	seedPortalUser(t, d, "b", "h", false, 0)
	seedPortalUser(t, d, "c", "h", false, 0)

	names, err := GetPortalUsernames(d)
	if err != nil {
		t.Fatalf("GetPortalUsernames: %v", err)
	}
	// 2026-08-10: v0.33.1.41 #2 — V054 auto-creates 'infra' at id=1
	// (alphabetically before 'a'), so the sorted list is now
	// [a, b, c, infra] (4 names), not [a, b, c] (3 names).
	// Verify the seeded ones are present + 'infra' is in the list.
	want := map[string]bool{"a": true, "b": true, "c": true, "infra": true}
	if len(names) != len(want) {
		t.Fatalf("got %d names, want %d (1 infra + 3 seeded)", len(names), len(want))
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected name in list: %q", n)
		}
	}
}

// --- GetOtherHSUserIDs ---

func TestGetOtherHSUserIDs(t *testing.T) {
	d := openTestDB(t)
	me := seedPortalUser(t, d, "me", "h", true, 100)
	seedPortalUser(t, d, "other1", "h", false, 200)
	// The SELECT has "headscale_user_id != ''", which only filters
	// the empty string. NULL is filtered by the IS NOT NULL clause.
	// Integer 0 is neither (it's a real value, just zero) so it
	// passes through — callers that need to treat "0" as "no link"
	// filter downstream. This test documents that behaviour.
	seedPortalUser(t, d, "other2", "h", false, 0)

	ids, err := GetOtherHSUserIDs(d, me)
	if err != nil {
		t.Fatalf("GetOtherHSUserIDs: %v", err)
	}
	sort.Strings(ids)
	want := []string{"0", "200"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

// --- InsertPortalUser ---

func TestInsertPortalUser(t *testing.T) {
	d := openTestDB(t)

	id, err := InsertPortalUser(d, "newone", "newhash", true, 999)
	if err != nil {
		t.Fatalf("InsertPortalUser: %v", err)
	}
	if id == 0 {
		t.Errorf("got id = 0, want > 0")
	}

	// Read back to verify
	var username, hash string
	var adminI, hsID int64
	if err := d.QueryRow(`SELECT username, password_hash, is_admin, headscale_user_id FROM portal_users WHERE id = ?`, id).
		Scan(&username, &hash, &adminI, &hsID); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if username != "newone" || hash != "newhash" || adminI != 1 || hsID != 999 {
		t.Errorf("got (%q, %q, %d, %d), want (newone, newhash, 1, 999)", username, hash, adminI, hsID)
	}

	// isAdmin=false → adminI must be 0 (not -1 or omitted)
	id2, err := InsertPortalUser(d, "nonadmin", "h", false, 0)
	if err != nil {
		t.Fatalf("InsertPortalUser non-admin: %v", err)
	}
	var nonAdminI int
	if err := d.QueryRow(`SELECT is_admin FROM portal_users WHERE id = ?`, id2).Scan(&nonAdminI); err != nil {
		t.Fatalf("read admin flag: %v", err)
	}
	if nonAdminI != 0 {
		t.Errorf("non-admin is_admin = %d, want 0", nonAdminI)
	}

	// Duplicate username → error (UNIQUE constraint)
	_, err = InsertPortalUser(d, "newone", "otherhash", false, 0)
	if err == nil {
		t.Errorf("duplicate username should error, got nil")
	}
}

// --- UpdatePasswordHash ---

func TestUpdatePasswordHash(t *testing.T) {
	d := openTestDB(t)
	id := seedPortalUser(t, d, "upd", "old-hash", false, 0)

	affected, err := UpdatePasswordHash(d, id, "new-hash")
	if err != nil {
		t.Fatalf("UpdatePasswordHash: %v", err)
	}
	if affected != 1 {
		t.Errorf("affected = %d, want 1", affected)
	}

	// Read back
	got, err := GetPasswordHashByID(d, id)
	if err != nil {
		t.Fatalf("GetPasswordHashByID after update: %v", err)
	}
	if got != "new-hash" {
		t.Errorf("after update got %q, want new-hash", got)
	}

	// Update non-existent id → affected = 0, no error
	affected, err = UpdatePasswordHash(d, 9999, "x")
	if err != nil {
		t.Errorf("UpdatePasswordHash missing id: %v", err)
	}
	if affected != 0 {
		t.Errorf("missing id affected = %d, want 0", affected)
	}
}

// --- DeletePortalUserByID ---

func TestDeletePortalUserByID(t *testing.T) {
	d := openTestDB(t)
	id := seedPortalUser(t, d, "del", "h", false, 0)

	affected, err := DeletePortalUserByID(d, id)
	if err != nil {
		t.Fatalf("DeletePortalUserByID: %v", err)
	}
	if affected != 1 {
		t.Errorf("affected = %d, want 1", affected)
	}

	// Already gone — second delete is a no-op
	affected, err = DeletePortalUserByID(d, id)
	if err != nil {
		t.Errorf("second delete: %v", err)
	}
	if affected != 0 {
		t.Errorf("second delete affected = %d, want 0", affected)
	}

	// Verify gone via GetUserNameByID
	_, err = GetUserNameByID(d, id)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("after delete err = %v, want ErrUserNotFound", err)
	}
}
