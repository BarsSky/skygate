package subnet

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"skygate/internal/db"
)

// setupTestDB creates a fresh file-backed SQLite with
// all migrations applied, then registers t.Cleanup to
// close it. We use a temp file (not ":memory:") because
// the migration runner is wired through db.Open which
// expects a real path; ":memory:" works for raw SQL
// but the Open path adds a couple of pragmas (WAL
// off, foreign_keys ON) that we want for the tests.
//
// 2026-07-17: v0.16.0 — the manager tests need a real
// SQLite (the UNIQUE(user_id) and UNIQUE(cidr) constraints
// are what we test against, and an allocator-only unit
// test wouldn't catch "the manager forgot to write the
// portal_users denormalized columns").
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedPortalUser inserts a row in portal_users and
// returns the new id. Used by every test that needs
// a valid user_id for Create / Get / SetStatus.
func seedPortalUser(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ($1, '', 0)`,
		username,
	)
	if err != nil {
		t.Fatalf("seed portal_user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestCreateAndGet pins the v0.16.0 contract: Create
// inserts a user_subnets row + the portal_users
// denormalized columns, Get reads them back, and the
// CIDR is the deterministic 10.0.<uid>.0/24.
func TestCreateAndGet(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	s, err := Create(d, uid, "", "skygate-subnet-alice")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.CIDR != "10.0.1.0/24" {
		t.Errorf("CIDR = %q, want 10.0.1.0/24 (user_id=1)", s.CIDR)
	}
	if s.Status != StatusPending {
		t.Errorf("Status = %q, want pending", s.Status)
	}
	if s.ControlPlaneURL != "" {
		t.Errorf("ControlPlaneURL = %q, want empty (global plane)", s.ControlPlaneURL)
	}
	// Get reads it back.
	got, err := Get(d, uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CIDR != s.CIDR || got.Status != s.Status {
		t.Errorf("Get returned different row: %+v vs %+v", got, s)
	}
	// Denormalized columns on portal_users must match.
	var dCIDR, dStatus string
	if err := d.QueryRow(`SELECT subnet_cidr, subnet_status FROM portal_users WHERE id = $1`, uid).Scan(&dCIDR, &dStatus); err != nil {
		t.Fatalf("read denorm: %v", err)
	}
	if dCIDR != s.CIDR {
		t.Errorf("portal_users.subnet_cidr = %q, want %q (denorm out of sync)", dCIDR, s.CIDR)
	}
	if dStatus != s.Status {
		t.Errorf("portal_users.subnet_status = %q, want %q (denorm out of sync)", dStatus, s.Status)
	}
}

// TestCreateDuplicateReturnsExisting pins the v0.16.0
// contract: a second Create call for the same user_id
// returns ErrAlreadyExists and the existing row
// (NOT a new one, NOT a conflict error).
//
// 2026-07-17: v0.16.0. The admin UI relies on this
// for "Opt-in" idempotency: clicking the button twice
// doesn't error, the second click just shows the
// existing row.
func TestCreateDuplicateReturnsExisting(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	first, err := Create(d, uid, "", "skygate-subnet-alice")
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := Create(d, uid, "", "skygate-subnet-alice")
	if err == nil {
		t.Fatalf("second Create returned no error, want ErrAlreadyExists")
	}
	if second == nil {
		t.Fatalf("second Create returned nil row, want the existing one")
	}
	// Compare key fields instead of the whole struct
	// (Go's `==` on structs with time.Time is fragile
	// across monotonic clock resets; we just want the
	// logical fields to match).
	if second.ID != first.ID {
		t.Errorf("ID: second=%d, first=%d", second.ID, first.ID)
	}
	if second.CIDR != first.CIDR {
		t.Errorf("CIDR: second=%q, first=%q", second.CIDR, first.CIDR)
	}
	if second.Status != first.Status {
		t.Errorf("Status: second=%q, first=%q", second.Status, first.Status)
	}
	if second.RouterHostname != first.RouterHostname {
		t.Errorf("RouterHostname: second=%q, first=%q", second.RouterHostname, first.RouterHostname)
	}
	// The error must be ErrAlreadyExists (not a raw
	// UNIQUE constraint error from SQLite).
	if !strings.Contains(err.Error(), "already has a subnet") {
		t.Errorf("error = %q, want 'already has a subnet' (ErrAlreadyExists)", err)
	}
	// Only one row in user_subnets (not two).
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM user_subnets WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("user_subnets rows = %d, want 1", n)
	}
}

// TestCreateUserNotFound pins the v0.16.0 contract:
// Create with a non-existent user_id rolls back the
// user_subnets row and returns an error. The
// portal_users UPDATE returning 0 rows triggers the
// rollback.
//
// We use a user_id that's in the allocator's range
// (0..255) but doesn't exist in portal_users — the
// allocator's "out of range" check would mask the
// "not in portal_users" check if we used a uid > 255.
func TestCreateUserNotFound(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	// Pick a uid that's in range (0..255) but not
	// seeded. The seed function above only inserts
	// one user (id=1), so id=2 is in-range and missing.
	_, err := Create(d, 2, "", "skygate-subnet-nobody")
	if err == nil {
		t.Fatalf("Create(2) returned no error, want one")
	}
	if !strings.Contains(err.Error(), "not found in portal_users") {
		t.Errorf("error = %q, want 'not found in portal_users'", err)
	}
	// user_subnets must have no row.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM user_subnets`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("user_subnets rows = %d, want 0 (rolled back)", n)
	}
}

// TestGetNotFound pins the v0.16.0 contract: Get on a
// user with no subnet returns ErrNotFound, not a raw
// sql.ErrNoRows.
func TestGetNotFound(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	_, err := Get(d, uid)
	if err == nil {
		t.Fatalf("Get on user without subnet returned no error")
	}
	if !strings.Contains(err.Error(), "no subnet") {
		t.Errorf("error = %q, want ErrNotFound", err)
	}
}

// TestSetStatusLifecycle pins the v0.16.0 contract:
// status transitions are reflected in both the
// user_subnets row and the portal_users denorm
// column. pending → active → disabled is the
// operator-driven lifecycle (the v0.16.1 sidecar
// automation will call the same function).
func TestSetStatusLifecycle(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// pending → active.
	if err := SetStatus(d, uid, StatusActive); err != nil {
		t.Fatalf("SetStatus(active): %v", err)
	}
	got, _ := Get(d, uid)
	if got.Status != StatusActive {
		t.Errorf("after SetStatus(active): Status = %q, want %q", got.Status, StatusActive)
	}
	// Denorm check.
	var dStatus string
	d.QueryRow(`SELECT subnet_status FROM portal_users WHERE id = $1`, uid).Scan(&dStatus)
	if dStatus != StatusActive {
		t.Errorf("portal_users.subnet_status = %q, want %q", dStatus, StatusActive)
	}
	// active → disabled.
	if err := SetStatus(d, uid, StatusDisabled); err != nil {
		t.Fatalf("SetStatus(disabled): %v", err)
	}
	got, _ = Get(d, uid)
	if got.Status != StatusDisabled {
		t.Errorf("after SetStatus(disabled): Status = %q, want %q", got.Status, StatusDisabled)
	}
}

// TestSetStatusInvalid pins the v0.16.0 contract:
// SetStatus rejects unknown status strings (defensive
// against a typo in a future caller).
func TestSetStatusInvalid(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SetStatus(d, uid, "frobnicated"); err == nil {
		t.Errorf("SetStatus(frobnicated) returned no error, want one")
	}
}

// TestListEmpty pins the v0.16.0 contract: List on an
// empty DB returns an empty slice (not nil, not error).
// The admin subnet map iterates this slice.
func TestListEmpty(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	got, err := List(d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List on empty DB = %d rows, want 0", len(got))
	}
}

// TestListMultiUsers pins the v0.16.0 contract: List
// returns all subnets sorted by user_id. The admin map
// relies on this for "show all subnets on one page".
func TestListMultiUsers(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	alice := seedPortalUser(t, d, "alice")
	bob := seedPortalUser(t, d, "bob")
	carol := seedPortalUser(t, d, "carol")
	for _, uid := range []int64{alice, bob, carol} {
		if _, err := Create(d, uid, "", ""); err != nil {
			t.Fatalf("Create(%d): %v", uid, err)
		}
	}
	all, err := List(d)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List = %d rows, want 3", len(all))
	}
	// CIDR must match the deterministic 10.0.<uid>.0/24.
	want := map[int64]string{
		alice: "10.0." + itoa(alice) + ".0/24",
		bob:   "10.0." + ito64(bob) + ".0/24",
		carol: "10.0." + ito64(carol) + ".0/24",
	}
	for _, s := range all {
		if got, want := s.CIDR, want[s.UserID]; got != want {
			t.Errorf("user_id=%d CIDR = %q, want %q", s.UserID, got, want)
		}
	}
}

// TestSetRouter pins the v0.16.0 stub: SetRouter fills
// router_node_id + router_container_id and syncs the
// denormalized portal_users column. The v0.16.1
// sidecar code will call this.
func TestSetRouter(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SetRouter(d, uid, "42", "container-abc123"); err != nil {
		t.Fatalf("SetRouter: %v", err)
	}
	got, _ := Get(d, uid)
	if got.RouterNodeID != "42" {
		t.Errorf("RouterNodeID = %q, want 42", got.RouterNodeID)
	}
	if got.RouterContainerID != "container-abc123" {
		t.Errorf("RouterContainerID = %q, want container-abc123", got.RouterContainerID)
	}
	// Denorm on portal_users.
	var dNodeID string
	d.QueryRow(`SELECT subnet_router_node_id FROM portal_users WHERE id = $1`, uid).Scan(&dNodeID)
	if dNodeID != "42" {
		t.Errorf("portal_users.subnet_router_node_id = %q, want 42 (denorm out of sync)", dNodeID)
	}
}

// ito64 wraps strconv.FormatInt for the test
// expectations. Avoids importing strconv just for the
// one call site above.
func ito64(n int64) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// seedNodeOwnerMap inserts a node_owner_map row for the
// named user. Used by the v0.22.3 SyncStatus tests to
// fabricate "user has N devices" without going through
// the full backfill pipeline (which lives in handlers/
// and would need a headscale stub). Keeps these tests
// focused on the SyncStatus contract alone.
//
// 2026-07-21: v0.22.3.
func seedNodeOwnerMap(t *testing.T, d *sql.DB, nodeID, username, tag string) {
	t.Helper()
	_, err := d.Exec(
		`INSERT OR REPLACE INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id)
			VALUES ($1, 0, $2, $3, 1)`,
		nodeID, username, tag,
	)
	if err != nil {
		t.Fatalf("seed node_owner_map: %v", err)
	}
}

// TestSyncStatus_PendingWhenNoDevices — the v0.22.3
// status contract: a user with a subnet row but zero
// devices in node_owner_map gets pending (not active).
//
// Pre-v0.22.3 this was the only way the user could be
// pending (no subnet-router). v0.22.3 makes it the
// natural state for "freshly created user, no devices
// added yet" — which is the operator's actual concern
// from the 2026-07-21 release discussion.
func TestSyncStatus_PendingWhenNoDevices(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No node_owner_map rows.
	got, err := SyncStatus(d, uid, false)
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if got != StatusPending {
		t.Errorf("SyncStatus with 0 devices = %q, want %q", got, StatusPending)
	}
	// And the DB row matches.
	sub, _ := Get(d, uid)
	if sub.Status != StatusPending {
		t.Errorf("DB status = %q, want %q", sub.Status, StatusPending)
	}
}

// TestSyncStatus_ActiveWhenDevicesExist — the v0.22.3
// contract: a user with ≥1 device in node_owner_map
// (and no router) gets active.
func TestSyncStatus_ActiveWhenDevicesExist(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedNodeOwnerMap(t, d, "node-1", "alice", "tag:private")
	seedNodeOwnerMap(t, d, "node-2", "alice", "tag:private")
	got, err := SyncStatus(d, uid, false)
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if got != StatusActive {
		t.Errorf("SyncStatus with 2 devices, no router = %q, want %q", got, StatusActive)
	}
}

// TestSyncStatus_RouterActiveWhenHasRouter — bonus
// status: devices + router = router_active.
func TestSyncStatus_RouterActiveWhenHasRouter(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedNodeOwnerMap(t, d, "node-1", "alice", "tag:private")
	got, err := SyncStatus(d, uid, true)
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if got != StatusRouterActive {
		t.Errorf("SyncStatus with 1 device + router = %q, want %q", got, StatusRouterActive)
	}
	// And the DB row matches.
	sub, _ := Get(d, uid)
	if sub.Status != StatusRouterActive {
		t.Errorf("DB status = %q, want %q", sub.Status, StatusRouterActive)
	}
}

// TestSyncStatus_DisabledPreserved — manual override
// wins over derived state. Admin clicks "Disable" to
// opt the user out, and re-runs of SyncStatus (which
// fire on every /my/devices load) must not resurrect
// the row.
func TestSyncStatus_DisabledPreserved(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SetStatus(d, uid, StatusDisabled); err != nil {
		t.Fatalf("SetStatus(disabled): %v", err)
	}
	// Now simulate "user has devices + has router" — the
	// derived state would normally be router_active. The
	// admin's disabled must win.
	seedNodeOwnerMap(t, d, "node-1", "alice", "tag:private")
	got, err := SyncStatus(d, uid, true)
	if err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if got != StatusDisabled {
		t.Errorf("SyncStatus with disabled override = %q, want %q (admin override must win)", got, StatusDisabled)
	}
}

// TestSyncStatus_NoSubnetRow — calling SyncStatus on a
// user without a user_subnets row returns ErrNotFound,
// not some other error. The handler that calls this
// catches ErrNotFound and silently skips (a user
// without a row is just "not opted in yet", not an
// error condition).
func TestSyncStatus_NoSubnetRow(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	got, err := SyncStatus(d, uid, false)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SyncStatus on user without row: err = %v, want ErrNotFound", err)
	}
	if got != "" {
		t.Errorf("SyncStatus on user without row: got = %q, want \"\"", got)
	}
}

// TestSyncStatus_Idempotent — re-running SyncStatus
// with the same hasRouter value is a no-op on the DB
// (no UPDATE fired). The contract is "derived state
// doesn't churn the DB".
func TestSyncStatus_Idempotent(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedNodeOwnerMap(t, d, "node-1", "alice", "tag:private")
	// First sync: pending → active.
	if _, err := SyncStatus(d, uid, false); err != nil {
		t.Fatalf("first SyncStatus: %v", err)
	}
	updatedAt1 := mustReadUpdatedAt(t, d, uid)
	// Sleep so any UPDATE would have a different updated_at
	// even with second-resolution clock. (User-visible
	// timestamps are unix seconds, so a same-second UPDATE
	// would still match — but a 1.1s sleep gives a guaranteed
	// wall-clock delta in seconds.)
	time.Sleep(1100 * time.Millisecond)
	// Second sync with same inputs: must NOT update.
	if _, err := SyncStatus(d, uid, false); err != nil {
		t.Fatalf("second SyncStatus: %v", err)
	}
	updatedAt2 := mustReadUpdatedAt(t, d, uid)
	if updatedAt1 != updatedAt2 {
		t.Errorf("idempotent re-sync fired an UPDATE: updated_at went %d → %d", updatedAt1, updatedAt2)
	}
}

// TestSetStatusAcceptsRouterActive — pin the contract
// that the new router_active string is a valid status
// value (SetStatus's switch case lists it explicitly).
// Without this, the v0.22.3 SyncStatus would set the
// DB column to "router_active" but a re-load via
// Get/SetStatus would reject the value.
func TestSetStatusAcceptsRouterActive(t *testing.T) {
	d := setupTestDB(t)
	defer d.Close()
	uid := seedPortalUser(t, d, "alice")
	if _, err := Create(d, uid, "", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SetStatus(d, uid, StatusRouterActive); err != nil {
		t.Fatalf("SetStatus(router_active): %v", err)
	}
	got, _ := Get(d, uid)
	if got.Status != StatusRouterActive {
		t.Errorf("after SetStatus(router_active): Status = %q, want %q", got.Status, StatusRouterActive)
	}
}

func mustReadUpdatedAt(t *testing.T, d *sql.DB, uid int64) int64 {
	t.Helper()
	var ts int64
	if err := d.QueryRow(`SELECT updated_at FROM user_subnets WHERE user_id = $1`, uid).Scan(&ts); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	return ts
}
