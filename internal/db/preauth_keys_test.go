package db

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// 2026-07-11: Этап 10 part 3 — tests for preauth_keys helpers.
// Same pattern as personal_api_tokens_test.go: openTestDB() gives
// a fresh sqlite with the full migration chain applied, then
// seedPreauthKey centralises the fixture INSERT.
//
// seedPreauthKey takes the natural schema columns; usedI=0
// means "not used" (schema default), expiresAt=0 means "no
// expiry". createdAt=0 means "let the schema default set it
// (strftime('%s','now'))".
func seedPreauthKey(t *testing.T, d *sql.DB, userID int64, key, headscaleID string, usedI, expiresAt int64) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO preauth_keys (user_id, key, headscale_preauth_id, used, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		userID, key, headscaleID, usedI, expiresAt)
	if err != nil {
		t.Fatalf("seedPreauthKey(key=%q): %v", key, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// --- ListPreauthKeysByUser ---

func TestListPreauthKeysByUser(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "alice", "h", false, 100)
	u2 := seedPortalUser(t, d, "bob", "h", false, 200)

	// alice: 2 keys, one with headscale id, one without
	idA1 := seedPreauthKey(t, d, u1, "key-a-1", "hs-a-1", 0, time.Now().Add(time.Hour).Unix())
	idA2 := seedPreauthKey(t, d, u1, "key-a-2", "", 1, 0) // used, no hs id, no expiry
	// bob: 1 key (must NOT appear in alice's list)
	seedPreauthKey(t, d, u2, "key-b-1", "hs-b-1", 0, 0)

	got, err := ListPreauthKeysByUser(d, u1)
	if err != nil {
		t.Fatalf("ListPreauthKeysByUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d keys, want 2", len(got))
	}
	if got == nil {
		t.Errorf("got nil slice, want []PreauthKey{} (non-nil)")
	}

	byID := map[int64]PreauthKey{}
	for _, k := range got {
		byID[k.ID] = k
	}

	// idA1: unused, has headscale id
	k1, ok := byID[idA1]
	if !ok {
		t.Fatalf("idA1 (%d) missing", idA1)
	}
	if k1.Key != "key-a-1" {
		t.Errorf("k1.Key = %q, want key-a-1", k1.Key)
	}
	if k1.HeadscalePreauthID != "hs-a-1" {
		t.Errorf("k1.HeadscalePreauthID = %q, want hs-a-1", k1.HeadscalePreauthID)
	}
	if k1.Used {
		t.Errorf("k1.Used = true, want false")
	}
	if k1.ExpiresAt == 0 {
		t.Errorf("k1.ExpiresAt = 0, want non-zero (seed used now+1h)")
	}

	// idA2: used, no headscale id, no expiry
	k2, ok := byID[idA2]
	if !ok {
		t.Fatalf("idA2 (%d) missing", idA2)
	}
	if k2.Used != true {
		t.Errorf("k2.Used = false, want true (seed used usedI=1)")
	}
	if k2.HeadscalePreauthID != "" {
		t.Errorf("k2.HeadscalePreauthID = %q, want empty (seed used '')", k2.HeadscalePreauthID)
	}
	if k2.ExpiresAt != 0 {
		t.Errorf("k2.ExpiresAt = %d, want 0 (seed used 0)", k2.ExpiresAt)
	}

	// CreatedAt populated by schema default for both
	for id, k := range byID {
		if k.CreatedAt == 0 {
			t.Errorf("key %d: CreatedAt = 0, want non-zero (schema default)", id)
		}
	}

	// Empty user → empty slice, not nil
	got, err = ListPreauthKeysByUser(d, 9999)
	if err != nil {
		t.Fatalf("ListPreauthKeysByUser empty: %v", err)
	}
	if got == nil {
		t.Errorf("empty user got nil, want []PreauthKey{} (non-nil)")
	}
	if len(got) != 0 {
		t.Errorf("empty user got %d keys, want 0", len(got))
	}
}

// --- GetPreauthKeyByID ---

func TestGetPreauthKeyByID(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "alice", "h", false, 100)
	u2 := seedPortalUser(t, d, "bob", "h", false, 200)
	idA := seedPreauthKey(t, d, u1, "key-a", "hs-a", 0, 0)
	seedPreauthKey(t, d, u2, "key-b", "hs-b", 0, 0)

	// Happy path
	k, err := GetPreauthKeyByID(d, idA, u1)
	if err != nil {
		t.Fatalf("GetPreauthKeyByID: %v", err)
	}
	if k.ID != idA {
		t.Errorf("ID = %d, want %d", k.ID, idA)
	}
	if k.UserID != u1 {
		t.Errorf("UserID = %d, want %d", k.UserID, u1)
	}
	if k.Key != "key-a" {
		t.Errorf("Key = %q, want key-a", k.Key)
	}
	if k.HeadscalePreauthID != "hs-a" {
		t.Errorf("HeadscalePreauthID = %q, want hs-a", k.HeadscalePreauthID)
	}
	if k.Used {
		t.Errorf("Used = true, want false")
	}

	// Cross-user probe: alice tries to read bob's key by id
	// (must NOT return bob's row — the user_id guard in the query
	// is the only thing standing between a user and a stranger's
	// preauth key data).
	_, err = GetPreauthKeyByID(d, 999, u1) // nonexistent id
	if !errors.Is(err, ErrPreauthKeyNotFound) {
		t.Errorf("nonexistent id err = %v, want ErrPreauthKeyNotFound", err)
	}

	// Cross-user attempt: bob's id with alice's user_id
	var bobID int64
	d.QueryRow(`SELECT id FROM preauth_keys WHERE user_id=$1`, u2).Scan(&bobID)
	_, err = GetPreauthKeyByID(d, bobID, u1)
	if !errors.Is(err, ErrPreauthKeyNotFound) {
		t.Errorf("cross-user err = %v, want ErrPreauthKeyNotFound (user_id filter must hold)", err)
	}
}

// --- InsertPreauthKey ---

func TestInsertPreauthKey(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", "h", false, 100)
	exp := time.Now().Add(time.Hour).Unix()

	id, err := InsertPreauthKey(d, uid, "newkey", exp, "hs-new")
	if err != nil {
		t.Fatalf("InsertPreauthKey: %v", err)
	}
	if id == 0 {
		t.Errorf("LastInsertId = 0, want non-zero")
	}

	// Round-trip via SELECT
	k, err := GetPreauthKeyByID(d, id, uid)
	if err != nil {
		t.Fatalf("GetPreauthKeyByID after insert: %v", err)
	}
	if k.Key != "newkey" {
		t.Errorf("Key = %q, want newkey", k.Key)
	}
	if k.HeadscalePreauthID != "hs-new" {
		t.Errorf("HeadscalePreauthID = %q, want hs-new", k.HeadscalePreauthID)
	}
	if k.ExpiresAt != exp {
		t.Errorf("ExpiresAt = %d, want %d", k.ExpiresAt, exp)
	}
	if k.UserID != uid {
		t.Errorf("UserID = %d, want %d", k.UserID, uid)
	}

	// Insert with empty headscale id (legacy keys before API field
	// started populating — must not error or store NULL weirdly).
	id2, err := InsertPreauthKey(d, uid, "legacy", 0, "")
	if err != nil {
		t.Fatalf("InsertPreauthKey (empty hsID): %v", err)
	}
	k2, _ := GetPreauthKeyByID(d, id2, uid)
	if k2.HeadscalePreauthID != "" {
		t.Errorf("legacy.HeadscalePreauthID = %q, want empty", k2.HeadscalePreauthID)
	}
	if k2.ExpiresAt != 0 {
		t.Errorf("legacy.ExpiresAt = %d, want 0", k2.ExpiresAt)
	}
}

// --- ExpirePreauthKey ---

func TestExpirePreauthKey(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "alice", "h", false, 100)
	u2 := seedPortalUser(t, d, "bob", "h", false, 200)
	idA := seedPreauthKey(t, d, u1, "key-a", "hs-a", 0, time.Now().Add(time.Hour).Unix())
	idB := seedPreauthKey(t, d, u2, "key-b", "hs-b", 0, time.Now().Add(time.Hour).Unix())

	// Set expires_at to a known past time so the dashboard's
	// 3-way split would reclassify it as Expired.
	past := time.Now().Add(-time.Hour).Unix()
	if err := ExpirePreauthKey(d, idA, u1, past); err != nil {
		t.Fatalf("ExpirePreauthKey: %v", err)
	}

	k, _ := GetPreauthKeyByID(d, idA, u1)
	if k.ExpiresAt != past {
		t.Errorf("after Expire: ExpiresAt = %d, want %d", k.ExpiresAt, past)
	}

	// Bob's row is untouched
	kB, _ := GetPreauthKeyByID(d, idB, u2)
	if kB.ExpiresAt == past {
		t.Errorf("bob's ExpiresAt was modified, want untouched (cross-user check failed)")
	}

	// Cross-user: alice tries to expire bob's key
	now := time.Now().Unix()
	if err := ExpirePreauthKey(d, idB, u1, now); err != nil {
		t.Fatalf("ExpirePreauthKey cross-user: %v", err)
	}
	kB, _ = GetPreauthKeyByID(d, idB, u2)
	if kB.ExpiresAt == now {
		t.Errorf("cross-user Expire modified bob's row, want untouched (user_id guard must hold)")
	}

	// Nonexistent id: no-op, no error
	if err := ExpirePreauthKey(d, 9999, u1, now); err != nil {
		t.Errorf("ExpirePreauthKey(nonexistent) error: %v", err)
	}
}

// --- MarkPreauthKeyUsedByHSID ---

func TestMarkPreauthKeyUsedByHSID(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice", "h", false, 100)
	id1 := seedPreauthKey(t, d, uid, "k1", "hs-1", 0, 0) // unused
	id2 := seedPreauthKey(t, d, uid, "k2", "hs-2", 1, 0) // ALREADY used
	id3 := seedPreauthKey(t, d, uid, "k3", "hs-3", 0, 0) // also unused

	// Mark hs-1 used
	if err := MarkPreauthKeyUsedByHSID(d, "hs-1"); err != nil {
		t.Fatalf("MarkPreauthKeyUsedByHSID: %v", err)
	}

	// id1 is now used
	k1, _ := GetPreauthKeyByID(d, id1, uid)
	if !k1.Used {
		t.Errorf("id1.Used = false, want true after Mark")
	}
	// id2 is still used (the AND used=0 guard means it stays 1)
	k2, _ := GetPreauthKeyByID(d, id2, uid)
	if !k2.Used {
		t.Errorf("id2.Used = false, want true (was already used; should stay used)")
	}
	// id3 is still unused (no headscale match)
	k3, _ := GetPreauthKeyByID(d, id3, uid)
	if k3.Used {
		t.Errorf("id3.Used = true, want false (no headscale match)")
	}

	// Unknown headscale id: no-op, no error
	if err := MarkPreauthKeyUsedByHSID(d, "nonexistent"); err != nil {
		t.Errorf("MarkPreauthKeyUsedByHSID(unknown) error: %v", err)
	}

	// Empty headscale id: no-op (defensive — don't run an UPDATE
	// that would touch every unused row)
	beforeCount := 0
	d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE used=0`).Scan(&beforeCount)
	if err := MarkPreauthKeyUsedByHSID(d, ""); err != nil {
		t.Errorf("MarkPreauthKeyUsedByHSID('') error: %v", err)
	}
	afterCount := 0
	d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE used=0`).Scan(&afterCount)
	if afterCount != beforeCount {
		t.Errorf("empty hsID changed used=0 count: before=%d after=%d (should be no-op)", beforeCount, afterCount)
	}
}

// --- DeletePreauthKeysByUserID (admin cascade) ---

func TestDeletePreauthKeysByUserID(t *testing.T) {
	d := openTestDB(t)
	u1 := seedPortalUser(t, d, "alice", "h", false, 100)
	u2 := seedPortalUser(t, d, "bob", "h", false, 200)
	// alice: 3 keys
	seedPreauthKey(t, d, u1, "ka1", "hs-a1", 0, 0)
	seedPreauthKey(t, d, u1, "ka2", "hs-a2", 1, 0)
	seedPreauthKey(t, d, u1, "ka3", "", 0, 0) // legacy, no hs id
	// bob: 1 key (must survive)
	seedPreauthKey(t, d, u2, "kb1", "hs-b1", 0, 0)

	n, err := DeletePreauthKeysByUserID(d, u1)
	if err != nil {
		t.Fatalf("DeletePreauthKeysByUserID: %v", err)
	}
	if n != 3 {
		t.Errorf("rows affected = %d, want 3 (alice had 3 keys)", n)
	}

	// alice has 0, bob has 1
	var aliceCount, bobCount int
	d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE user_id=$1`, u1).Scan(&aliceCount)
	d.QueryRow(`SELECT COUNT(*) FROM preauth_keys WHERE user_id=$1`, u2).Scan(&bobCount)
	if aliceCount != 0 {
		t.Errorf("alice keys left = %d, want 0", aliceCount)
	}
	if bobCount != 1 {
		t.Errorf("bob keys left = %d, want 1 (cascade over-reached)", bobCount)
	}

	// Second call: no-op, returns 0
	n, err = DeletePreauthKeysByUserID(d, u1)
	if err != nil {
		t.Fatalf("second DeletePreauthKeysByUserID: %v", err)
	}
	if n != 0 {
		t.Errorf("second call rows affected = %d, want 0", n)
	}

	// Unknown user: 0 rows, no error
	n, err = DeletePreauthKeysByUserID(d, 9999)
	if err != nil {
		t.Errorf("DeletePreauthKeysByUserID(unknown) error: %v", err)
	}
	if n != 0 {
		t.Errorf("unknown user rows affected = %d, want 0", n)
	}
}
