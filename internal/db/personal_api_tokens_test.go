package db

import (
	"database/sql"
	"testing"
	"time"
)

// 2026-07-11: Этап 10 part 2 — tests for personal_api_tokens helpers.
// Same pattern as portal_users_test.go: openTestDB() gives us a fresh
// sqlite with the full migration chain applied, then we seed a
// fixture and exercise the helper.
//
// seedAPIToken centralises the INSERT so each test stays focused on
// what it's checking. lastUsedI=0 means "never used" (matches the
// schema default); pass a non-zero unix timestamp to simulate a
// previously-touched token.
func seedAPIToken(t *testing.T, d *sql.DB, userID int64, tokenHash, label string, lastUsedI int64) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO personal_api_tokens (user_id, token_hash, label, last_used_at) VALUES ($1,$2,$3,$4)`,
		userID, tokenHash, label, lastUsedI)
	if err != nil {
		t.Fatalf("seedAPIToken(hash=%q): %v", tokenHash, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// --- ListAPITokensByUser ---

func TestListAPITokensByUser(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "alice", "h", false, 100)
	u2 := seedPortalUser(t, d, "bob", "h", false, 200)

	// alice: 2 tokens, one touched, one not
	t1 := seedAPIToken(t, d, u1, "hash-a1", "laptop", 1700000000)
	t2 := seedAPIToken(t, d, u1, "hash-a2", "phone", 0)
	// bob: 1 token (sanity — must NOT appear in alice's list)
	seedAPIToken(t, d, u2, "hash-b1", "bobs", 0)

	got, err := ListAPITokensByUser(d, u1)
	if err != nil {
		t.Fatalf("ListAPITokensByUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d tokens, want 2", len(got))
	}

	// Both rows must be present. We don't assert a specific order
	// because both inserts happen within the same second (schema
	// default is strftime('%s','now') = 1-second resolution) and
	// SQLite breaks the DESC tie with insertion order, which is
	// not portable. The handler doesn't depend on order — the
	// template iterates. Just check the set of (id, label, lastUsed).
	byID := map[int64]APIToken{}
	for _, tk := range got {
		byID[tk.ID] = tk
	}
	if len(byID) != 2 {
		t.Fatalf("got %d unique IDs, want 2", len(byID))
	}

	// Token t1: was touched at 1700000000 → time.Time is not zero
	tk1, ok := byID[t1]
	if !ok {
		t.Fatalf("token t1 (id=%d) missing from result", t1)
	}
	if tk1.LastUsed.IsZero() {
		t.Errorf("t1.LastUsed is zero, want non-zero (seed used 1700000000)")
	}
	if tk1.LastUsed.Unix() != 1700000000 {
		t.Errorf("t1.LastUsed.Unix = %d, want 1700000000", tk1.LastUsed.Unix())
	}
	if tk1.Label != "laptop" {
		t.Errorf("t1.Label = %q, want laptop", tk1.Label)
	}

	// Token t2: never used → time.Time is zero
	tk2, ok := byID[t2]
	if !ok {
		t.Fatalf("token t2 (id=%d) missing from result", t2)
	}
	if !tk2.LastUsed.IsZero() {
		t.Errorf("t2.LastUsed = %v, want zero (seed used 0)", tk2.LastUsed)
	}
	if tk2.Label != "phone" {
		t.Errorf("t2.Label = %q, want phone", tk2.Label)
	}

	// CreatedAt is populated for both (schema default = strftime('%s','now'))
	for id, tk := range byID {
		if tk.CreatedAt.IsZero() {
			t.Errorf("token %d: CreatedAt is zero, want non-zero", id)
		}
	}

	// Empty user (no tokens) → empty slice, NOT nil
	got, err = ListAPITokensByUser(d, 9999)
	if err != nil {
		t.Fatalf("ListAPITokensByUser empty user: %v", err)
	}
	if got == nil {
		t.Errorf("empty user returned nil, want []APIToken{} (non-nil)")
	}
	if len(got) != 0 {
		t.Errorf("empty user got %d tokens, want 0", len(got))
	}
}

// --- ListAPITokenHashesForLookup ---

func TestListAPITokenHashesForLookup(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "admin", "h", true, 1)
	u2 := seedPortalUser(t, d, "alice", "h", false, 100)
	seedAPIToken(t, d, u1, "hash-admin-1", "ci-bot", 0)
	seedAPIToken(t, d, u2, "hash-alice-1", "laptop", 0)
	seedAPIToken(t, d, u2, "hash-alice-2", "phone", 0)

	got, err := ListAPITokenHashesForLookup(d)
	if err != nil {
		t.Fatalf("ListAPITokenHashesForLookup: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3", len(got))
	}

	// Find admin's row and verify the JOIN worked: username comes
	// from portal_users, not personal_api_tokens.
	var found *APITokenLookup
	for i := range got {
		if got[i].TokenHash == "hash-admin-1" {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("admin row not found in result")
	}
	if found.Username != "admin" {
		t.Errorf("Username = %q, want admin (joined from portal_users)", found.Username)
	}
	if found.UserID != u1 {
		t.Errorf("UserID = %d, want %d", found.UserID, u1)
	}
	if !found.IsAdmin {
		t.Errorf("IsAdmin = false, want true (portal_users.is_admin=1)")
	}

	// Empty system: no rows
	d2 := openTestDB(t)
	got, err = ListAPITokenHashesForLookup(d2)
	if err != nil {
		t.Fatalf("empty lookup: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("empty got = %+v, want empty non-nil slice", got)
	}
}

// --- InsertAPIToken ---

func TestInsertAPIToken(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", "h", false, 100)

	// 2026-07-16: v0.15.5 — InsertAPIToken now takes
	// (expiresAt, autoRotate). Pass 0/0 to preserve the
	// "never expires, no auto-rotate" pre-v0.15.5
	// behaviour; the rest of the test doesn't depend on
	// the new fields.
	id, err := InsertAPIToken(d, uid, "hash-new", "ci-runner", 0, false)
	if err != nil {
		t.Fatalf("InsertAPIToken: %v", err)
	}
	if id == 0 {
		t.Errorf("LastInsertId = 0, want non-zero")
	}

	// Verify the row round-trips
	var label, hash string
	if err := d.QueryRow(`SELECT label, token_hash FROM personal_api_tokens WHERE id = $1`, id).Scan(&label, &hash); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if label != "ci-runner" || hash != "hash-new" {
		t.Errorf("(%q, %q), want (ci-runner, hash-new)", label, hash)
	}
}

// --- DeleteAPITokenByUser ---

func TestDeleteAPITokenByUser(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "alice", "h", false, 100)
	u2 := seedPortalUser(t, d, "bob", "h", false, 200)
	idA := seedAPIToken(t, d, u1, "hash-a", "alice-token", 0)
	idB := seedAPIToken(t, d, u2, "hash-b", "bob-token", 0)

	// Happy path: alice deletes her own token
	n, err := DeleteAPITokenByUser(d, idA, u1)
	if err != nil {
		t.Fatalf("DeleteAPITokenByUser: %v", err)
	}
	if n != 1 {
		t.Errorf("rows affected = %d, want 1", n)
	}

	// Verify the row is gone, but bob's row survives
	var count int
	d.QueryRow(`SELECT COUNT(*) FROM personal_api_tokens WHERE id = $1`, idA).Scan(&count)
	if count != 0 {
		t.Errorf("alice token still present, want gone")
	}
	d.QueryRow(`SELECT COUNT(*) FROM personal_api_tokens WHERE id = $1`, idB).Scan(&count)
	if count != 1 {
		t.Errorf("bob token was deleted, want untouched (cross-user check failed)")
	}

	// Cross-user attempt: alice tries to delete bob's token by id
	n, err = DeleteAPITokenByUser(d, idB, u1)
	if err != nil {
		t.Fatalf("DeleteAPITokenByUser cross-user: %v", err)
	}
	if n != 0 {
		t.Errorf("cross-user rows affected = %d, want 0 (the WHERE user_id=? guard must hold)", n)
	}
	// Bob's row still there
	d.QueryRow(`SELECT COUNT(*) FROM personal_api_tokens WHERE id = $1`, idB).Scan(&count)
	if count != 1 {
		t.Errorf("bob token deleted by alice, want untouched")
	}
}

// --- TouchAPITokenLastUsed ---

func TestTouchAPITokenLastUsed(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", "h", false, 100)
	id := seedAPIToken(t, d, uid, "hash-x", "tok", 0)

	// Before: last_used_at = 0
	var lu int64
	d.QueryRow(`SELECT last_used_at FROM personal_api_tokens WHERE id = $1`, id).Scan(&lu)
	if lu != 0 {
		t.Errorf("before touch: last_used_at = %d, want 0", lu)
	}

	// Touch
	if err := TouchAPITokenLastUsed(d, "hash-x"); err != nil {
		t.Fatalf("TouchAPITokenLastUsed: %v", err)
	}

	// After: last_used_at ≈ now (allow a 2-second window for clock skew / sql lag)
	d.QueryRow(`SELECT last_used_at FROM personal_api_tokens WHERE id = $1`, id).Scan(&lu)
	now := time.Now().Unix()
	if lu == 0 {
		t.Errorf("after touch: last_used_at still 0")
	}
	if lu < now-2 || lu > now+2 {
		t.Errorf("after touch: last_used_at = %d, want ~%d (now)", lu, now)
	}

	// Unknown hash: no-op, no error
	if err := TouchAPITokenLastUsed(d, "nonexistent-hash"); err != nil {
		t.Errorf("TouchAPITokenLastUsed(unknown) error: %v", err)
	}
}

// --- DeleteAPITokensByUserID (admin cascade) ---

func TestDeleteAPITokensByUserID(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "alice", "h", false, 100)
	u2 := seedPortalUser(t, d, "bob", "h", false, 200)
	// alice: 2 tokens
	seedAPIToken(t, d, u1, "hash-a1", "laptop", 0)
	seedAPIToken(t, d, u1, "hash-a2", "phone", 0)
	// bob: 1 token (must survive)
	seedAPIToken(t, d, u2, "hash-b1", "bobs", 0)

	n, err := DeleteAPITokensByUserID(d, u1)
	if err != nil {
		t.Fatalf("DeleteAPITokensByUserID: %v", err)
	}
	if n != 2 {
		t.Errorf("rows affected = %d, want 2 (alice had 2 tokens)", n)
	}

	// alice has 0, bob has 1
	var aliceCount, bobCount int
	d.QueryRow(`SELECT COUNT(*) FROM personal_api_tokens WHERE user_id = $1`, u1).Scan(&aliceCount)
	d.QueryRow(`SELECT COUNT(*) FROM personal_api_tokens WHERE user_id = $1`, u2).Scan(&bobCount)
	if aliceCount != 0 {
		t.Errorf("alice tokens left = %d, want 0", aliceCount)
	}
	if bobCount != 1 {
		t.Errorf("bob tokens left = %d, want 1 (cascade over-reached)", bobCount)
	}

	// Second call: no-op, returns 0
	n, err = DeleteAPITokensByUserID(d, u1)
	if err != nil {
		t.Fatalf("second DeleteAPITokensByUserID: %v", err)
	}
	if n != 0 {
		t.Errorf("second call rows affected = %d, want 0", n)
	}

	// Unknown user: 0 rows, no error
	n, err = DeleteAPITokensByUserID(d, 9999)
	if err != nil {
		t.Errorf("DeleteAPITokensByUserID(unknown) error: %v", err)
	}
	if n != 0 {
		t.Errorf("unknown user rows affected = %d, want 0", n)
	}
}
