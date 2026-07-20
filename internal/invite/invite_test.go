// 2026-07-20: v0.21.0 — tests for the invite package.
//
// Coverage: code shape, generate uniqueness, full
// lifecycle (create → validate → consume → bridge),
// self-invite / not-for-you / expired / already-
// consumed / revoke. Uses db.OpenForTest so the
// test gets a fresh in-temp-dir SQLite DB with
// the full production migration chain applied.

package invite

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
)

// setupDB opens a fresh in-temp-dir SQLite DB
// and returns the *sql.DB. t.Cleanup closes
// it. The migration chain is the same one
// production runs (db.OpenForTest applies
// every migration_v*.go in order).
func setupDB(t *testing.T) *sql.DB {
	t.Helper()
	return db.OpenForTest(t)
}

func TestGenerateCodeShape(t *testing.T) {
	c, err := GenerateCode()
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if len(c) != CodeLength {
		t.Errorf("code length = %d, want %d", len(c), CodeLength)
	}
	for _, ch := range c {
		if !strings.ContainsRune(CodeAlphabet, ch) {
			t.Errorf("code contains rune %q outside CodeAlphabet %q", ch, CodeAlphabet)
		}
	}
}

func TestGenerateCodeUniqueness(t *testing.T) {
	// 100 codes from a 32^8 alphabet should
	// all be unique. (P[collision] ≈ 100^2 / 2 / 1.1e12
	// ≈ 5e-9 — vanishingly small.)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode: %v", err)
		}
		if seen[c] {
			t.Errorf("collision after %d iterations: %s", i, c)
		}
		seen[c] = true
	}
}

func TestCreateAndLookup(t *testing.T) {
	d := setupDB(t)

	// Need a grantor in portal_users (FK).
	res, err := d.Exec(`INSERT INTO portal_users(username, password_hash, is_admin) VALUES(?, ?, 0)`, "alice", "x")
	if err != nil {
		t.Fatalf("insert alice: %v", err)
	}
	aliceID, _ := res.LastInsertId()

	inv, err := CreateInvite(d, aliceID, "bob", 0, "join me")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.Status != StatusActive {
		t.Errorf("status = %q, want active", inv.Status)
	}
	if inv.GranteeUsername != "bob" {
		t.Errorf("grantee = %q, want bob", inv.GranteeUsername)
	}
	if inv.AuditMessage != "join me" {
		t.Errorf("message = %q, want 'join me'", inv.AuditMessage)
	}
	if inv.ExpiresAt.Before(inv.CreatedAt) {
		t.Errorf("expires %v before created %v", inv.ExpiresAt, inv.CreatedAt)
	}
	if time.Until(inv.ExpiresAt) < 6*24*time.Hour {
		t.Errorf("default TTL < 6 days: %v", time.Until(inv.ExpiresAt))
	}

	// LookupByCode round-trip
	got, err := LookupByCode(d, inv.Code)
	if err != nil {
		t.Fatalf("LookupByCode: %v", err)
	}
	if got.ID != inv.ID {
		t.Errorf("ID mismatch: %d vs %d", got.ID, inv.ID)
	}
}

func TestValidateCodeActive(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	aliceID := userIDByName(t, d, "alice")
	inv, _ := CreateInvite(d, aliceID, "bob", 0, "")

	// Insert "bob" so the consumer-username check passes
	insertUser(t, d, "bob")

	_, err := ValidateCode(d, inv.Code, "bob", userIDByName(t, d, "bob"))
	if err != nil {
		t.Errorf("ValidateCode should pass: %v", err)
	}
}

func TestValidateCodeNotForYou(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	insertUser(t, d, "bob")
	insertUser(t, d, "eve")
	aliceID := userIDByName(t, d, "alice")
	inv, _ := CreateInvite(d, aliceID, "bob", 0, "")

	// Eve tries to accept bob's invite
	_, err := ValidateCode(d, inv.Code, "eve", userIDByName(t, d, "eve"))
	if !errors.Is(err, ErrNotForYou) {
		t.Errorf("ValidateCode = %v, want ErrNotForYou", err)
	}
}

func TestValidateCodeSelfInvite(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	aliceID := userIDByName(t, d, "alice")
	inv, _ := CreateInvite(d, aliceID, "alice", 0, "")

	_, err := ValidateCode(d, inv.Code, "alice", aliceID)
	if !errors.Is(err, ErrSelfInvite) {
		t.Errorf("ValidateCode = %v, want ErrSelfInvite", err)
	}
}

func TestValidateCodeExpired(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	insertUser(t, d, "bob")
	aliceID := userIDByName(t, d, "alice")

	// 5-second TTL + 6s sleep — leaves
	// 1s margin over the second-precision
	// rounding in expires_at. A 1-2 second
	// TTL is too tight on the rounding edge
	// when the sleep duration is also
	// second-aligned.
	inv, _ := CreateInvite(d, aliceID, "bob", 5*time.Second, "")
	time.Sleep(6 * time.Second)

	_, err := ValidateCode(d, inv.Code, "bob", userIDByName(t, d, "bob"))
	if !errors.Is(err, ErrExpired) {
		t.Errorf("ValidateCode = %v, want ErrExpired", err)
	}
}

func TestConsumeCodeAtomic(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	insertUser(t, d, "bob")
	aliceID := userIDByName(t, d, "alice")
	bobID := userIDByName(t, d, "bob")
	inv, _ := CreateInvite(d, aliceID, "bob", 0, "")

	// First consume — success
	_, err := ConsumeCode(d, inv.Code, bobID)
	if err != nil {
		t.Fatalf("first ConsumeCode: %v", err)
	}
	// Second consume — already consumed
	_, err = ConsumeCode(d, inv.Code, bobID)
	if !errors.Is(err, ErrAlreadyConsumed) {
		t.Errorf("second ConsumeCode = %v, want ErrAlreadyConsumed", err)
	}
}

func TestRevokeInvite(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	aliceID := userIDByName(t, d, "alice")
	inv, _ := CreateInvite(d, aliceID, "bob", 0, "")

	if err := RevokeInvite(d, inv.Code); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	_, err := ValidateCode(d, inv.Code, "bob", 99)
	if !errors.Is(err, ErrAlreadyConsumed) {
		t.Errorf("after revoke, ValidateCode = %v, want ErrAlreadyConsumed", err)
	}
}

func TestListByGrantor(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	aliceID := userIDByName(t, d, "alice")
	for i := 0; i < 3; i++ {
		_, _ = CreateInvite(d, aliceID, "bob", 0, "")
	}
	invites, err := ListByGrantor(d, aliceID)
	if err != nil {
		t.Fatalf("ListByGrantor: %v", err)
	}
	if len(invites) != 3 {
		t.Errorf("len = %d, want 3", len(invites))
	}
}

func TestSweepExpired(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)
	aliceID := userIDByName(t, d, "alice")
	// 5-second TTL + 6s sleep (same
	// second-precision margin as
	// TestValidateCodeExpired).
	_, _ = CreateInvite(d, aliceID, "bob", 5*time.Second, "")
	time.Sleep(6 * time.Second)

	n, err := SweepExpired(d)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("swept = %d, want 1", n)
	}
	// After sweep, the status is 'expired'
	row := d.QueryRow(`SELECT status FROM invite_codes LIMIT 1`)
	var status string
	_ = row.Scan(&status)
	if status != StatusExpired {
		t.Errorf("status after sweep = %q, want expired", status)
	}
}

func TestResolveGranteeID(t *testing.T) {
	d := setupDB(t)
	insertAlice(t, d)

	id, err := ResolveGranteeID(d, "alice")
	if err != nil {
		t.Fatalf("ResolveGranteeID: %v", err)
	}
	if id <= 0 {
		t.Errorf("id = %d, want > 0", id)
	}

	id, err = ResolveGranteeID(d, "nobody")
	if err != nil {
		t.Fatalf("ResolveGranteeID (missing): %v", err)
	}
	if id != 0 {
		t.Errorf("id for missing user = %d, want 0", id)
	}
}

// --- helpers ---

func insertAlice(t *testing.T, d *sql.DB) {
	t.Helper()
	insertUser(t, d, "alice")
}

func insertUser(t *testing.T, d *sql.DB, name string) {
	t.Helper()
	_, err := d.Exec(`INSERT INTO portal_users(username, password_hash, is_admin) VALUES(?, ?, 0)`, name, "x")
	if err != nil {
		t.Fatalf("insert %s: %v", name, err)
	}
}

func userIDByName(t *testing.T, d *sql.DB, name string) int64 {
	t.Helper()
	var id int64
	if err := d.QueryRow(`SELECT id FROM portal_users WHERE username = ?`, name).Scan(&id); err != nil {
		t.Fatalf("userIDByName(%s): %v", name, err)
	}
	return id
}
