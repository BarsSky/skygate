package subnet

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"skygate/internal/db"
)

// openTestDB — fresh SQLite with all migrations
// applied. Mirrors the helper in manager_test.go
// (kept inline here to avoid a refactor of the
// existing helper's signature).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedPortalUserWithHS — variant of seedPortalUser
// (from manager_test.go) that also sets
// headscale_user_id, which the share tests need for
// the Grant/Revoke pre-check on headscale_user_id
// (used by the v0.17.1 ACL builder to gate access
// on the user having a real headscale identity).
func seedPortalUserWithHS(t *testing.T, d *sql.DB, username string, hsID int64) int64 {
	t.Helper()
	_, err := d.Exec(`INSERT INTO portal_users
		(username, password_hash, is_admin, headscale_user_id, created_at)
		VALUES (?, '', 0, ?, 0)`, username, hsID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var id int64
	if err := d.QueryRow(`SELECT id FROM portal_users WHERE username = ?`, username).Scan(&id); err != nil {
		t.Fatalf("get id: %v", err)
	}
	return id
}

// --- Grant / Revoke / List ---

func TestGrantRevoke_BasicRoundTrip(t *testing.T) {
	d := openTestDB(t)
	grantor := seedPortalUserWithHS(t, d, "alice", 100)
	grantee := seedPortalUserWithHS(t, d, "bob", 101)
	// alice has a subnet; bob doesn't (yet — sharing
	// doesn't require the grantee to have one, only
	// the grantor).
	if _, err := Create(d, grantor, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Grant alice → bob.
	if err := Grant(d, grantor, grantee); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// alice's "I shared" list contains bob.
	shared, err := ListSharedBy(d, grantor)
	if err != nil {
		t.Fatalf("ListSharedBy: %v", err)
	}
	if len(shared) != 1 || shared[0].GranteeUserID != grantee {
		t.Errorf("ListSharedBy(alice) = %+v, want 1 row for bob", shared)
	}
	// bob's "I have access to" list contains alice.
	sharedWith, err := ListSharedWith(d, grantee)
	if err != nil {
		t.Fatalf("ListSharedWith: %v", err)
	}
	if len(sharedWith) != 1 || sharedWith[0].GrantorUserID != grantor {
		t.Errorf("ListSharedWith(bob) = %+v, want 1 row for alice", sharedWith)
	}
	// Revoke alice → bob.
	if err := Revoke(d, grantor, grantee); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	shared, _ = ListSharedBy(d, grantor)
	if len(shared) != 0 {
		t.Errorf("after Revoke, ListSharedBy(alice) = %+v, want empty", shared)
	}
}

// TestGrant_Idempotent — Grant on an existing
// (grantor, grantee) pair is a no-op, not an error.
func TestGrant_Idempotent(t *testing.T) {
	d := openTestDB(t)
	grantor := seedPortalUserWithHS(t, d, "alice", 100)
	grantee := seedPortalUserWithHS(t, d, "bob", 101)
	if _, err := Create(d, grantor, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Grant(d, grantor, grantee); err != nil {
		t.Fatalf("first Grant: %v", err)
	}
	// Second Grant on the same pair — no error.
	if err := Grant(d, grantor, grantee); err != nil {
		t.Errorf("second Grant on same pair: %v, want nil (idempotent)", err)
	}
	// And there's still just one row.
	shared, _ := ListSharedBy(d, grantor)
	if len(shared) != 1 {
		t.Errorf("after double Grant, ListSharedBy = %+v, want 1 row", shared)
	}
}

// TestGrant_SelfShareErrors — sharing with yourself
// is a contract violation, not a silent no-op.
func TestGrant_SelfShareErrors(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUserWithHS(t, d, "alice", 100)
	if _, err := Create(d, uid, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Grant(d, uid, uid); err != ErrSelfShare {
		t.Errorf("Grant(self, self) = %v, want ErrSelfShare", err)
	}
}

// TestGrant_GrantorMustHaveSubnet — Grant on a
// grantor who hasn't allocated a subnet returns
// ErrNotFound (nothing to share).
func TestGrant_GrantorMustHaveSubnet(t *testing.T) {
	d := openTestDB(t)
	grantor := seedPortalUserWithHS(t, d, "alice", 100)
	grantee := seedPortalUserWithHS(t, d, "bob", 101)
	// alice has NO subnet row.
	if err := Grant(d, grantor, grantee); err != ErrNotFound {
		t.Errorf("Grant(no-subnet grantor) = %v, want ErrNotFound", err)
	}
}

// TestRevoke_NotFoundIsError — Revoke on a missing
// row returns ErrShareNotFound so the bot can show a
// useful reply.
func TestRevoke_NotFoundIsError(t *testing.T) {
	d := openTestDB(t)
	grantor := seedPortalUserWithHS(t, d, "alice", 100)
	grantee := seedPortalUserWithHS(t, d, "bob", 101)
	if err := Revoke(d, grantor, grantee); err != ErrShareNotFound {
		t.Errorf("Revoke(missing) = %v, want ErrShareNotFound", err)
	}
}

// TestRevoke_CascadeOnUserDelete — deleting the
// grantor or grantee user should CASCADE the share
// row (FK ON DELETE CASCADE).
func TestRevoke_CascadeOnUserDelete(t *testing.T) {
	d := openTestDB(t)
	grantor := seedPortalUserWithHS(t, d, "alice", 100)
	grantee := seedPortalUserWithHS(t, d, "bob", 101)
	if _, err := Create(d, grantor, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Grant(d, grantor, grantee); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// Delete bob. The FK CASCADE should drop the
	// share row.
	if _, err := d.Exec(`DELETE FROM portal_users WHERE id = ?`, grantee); err != nil {
		t.Fatalf("delete grantee: %v", err)
	}
	// alice's "I shared" list is now empty.
	shared, _ := ListSharedBy(d, grantor)
	if len(shared) != 0 {
		t.Errorf("after deleting grantee, ListSharedBy = %+v, want empty (FK CASCADE)", shared)
	}
}

