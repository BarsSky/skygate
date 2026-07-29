// Tests for the per-user headscale client router.
// Originally in internal/handlers/app_controlplane_test.go
// (Phase D3, 2026-07-29: the Router moved to its own
// package; the test followed).
//
// Pins:
//   - ForUser returns the global client when no override
//   - ForUser returns a per-user client when override set
//   - ForUser caches clients by url (rebuild on key rotation)
//   - Global always returns the same global instance
//   - InvalidateCache drops entries
//   - ForUser falls through to global on corrupt ciphertext
//   - ForUser falls through to global when SecretKeyHex is empty

package controlplane

import (
	"database/sql"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

const cpTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// newTestRouter builds a Router backed by an in-memory
// SQLite + a fresh global headscale client. The routing
// is independent of how the global client was built, so
// any valid *headscale.Client works.
func newTestRouter(t *testing.T) (*Router, *sql.DB) {
	t.Helper()
	d := openControlplaneTestDB(t)
	hs := headscale.New("http://global-headscale:50444", "global-key")
	r := New(d, cpTestKey, hs)
	r.Init()
	return r, d
}

// TestRouter_ForUser_NoOverride_ReturnsGlobal: a user with
// no per-user control plane row gets the global client.
func TestRouter_ForUser_NoOverride_ReturnsGlobal(t *testing.T) {
	r, d := newTestRouter(t)
	t.Cleanup(func() { _ = d.Close() })
	id := seedControlplaneUserInTest(t, d, "alice")
	c := r.ForUser(id)
	if c != r.Global() {
		t.Errorf("expected global client, got different instance")
	}
}

// TestRouter_ForUser_WithOverride_ReturnsPerUser: a user
// with headscale_url set gets a per-user client (different
// from the global).
func TestRouter_ForUser_WithOverride_ReturnsPerUser(t *testing.T) {
	r, d := newTestRouter(t)
	t.Cleanup(func() { _ = d.Close() })
	id := seedControlplaneUserInTest(t, d, "bob")
	if err := db.SetUserHeadscaleConfig(d, id, "https://us.example.com", "us-key", cpTestKey); err != nil {
		t.Fatalf("set: %v", err)
	}
	c := r.ForUser(id)
	if c == r.Global() {
		t.Errorf("expected per-user client, got global")
	}
	if c.ApiKeyForCache() != "us-key" {
		t.Errorf("client apiKey = %q, want us-key", c.ApiKeyForCache())
	}
}

// TestRouter_ForUser_CachesClientByURL: a second call for
// the same user returns the cached client (same instance).
func TestRouter_ForUser_CachesClientByURL(t *testing.T) {
	r, d := newTestRouter(t)
	t.Cleanup(func() { _ = d.Close() })
	id := seedControlplaneUserInTest(t, d, "carol")
	if err := db.SetUserHeadscaleConfig(d, id, "https://eu.example.com", "eu-key", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c1 := r.ForUser(id)
	c2 := r.ForUser(id)
	if c1 != c2 {
		t.Errorf("expected same instance (cached), got different")
	}
}

// TestRouter_ForUser_InvalidatesOnKeyRotation: rotating
// the per-user key drops the cached client.
func TestRouter_ForUser_InvalidatesOnKeyRotation(t *testing.T) {
	r, d := newTestRouter(t)
	t.Cleanup(func() { _ = d.Close() })
	id := seedControlplaneUserInTest(t, d, "dave")
	if err := db.SetUserHeadscaleConfig(d, id, "https://h.example.com", "k1", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c1 := r.ForUser(id)
	// Admin rotates the key.
	if err := db.SetUserHeadscaleConfig(d, id, "https://h.example.com", "k2", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c2 := r.ForUser(id)
	if c1 == c2 {
		t.Errorf("expected new client after key rotation, got same instance")
	}
	if c2.ApiKeyForCache() != "k2" {
		t.Errorf("new client apiKey = %q, want k2", c2.ApiKeyForCache())
	}
}

// TestRouter_ForUser_CorruptCiphertext_FallsBackToGlobal:
// when the stored key can't be decrypted (wrong
// SKYGATE_SECRET_KEY), the helper returns the global
// client instead of 500-ing.
//
// We can't change the Router's secretKeyHex after
// construction in the production code path (New sets
// it once), so this test builds a second Router with
// a different key to simulate the rotation. The DB
// row was encrypted with cpTestKey; the second Router
// has a wrong key, so the decrypt fails and the
// fall-through fires.
func TestRouter_ForUser_CorruptCiphertext_FallsBackToGlobal(t *testing.T) {
	d := openControlplaneTestDB(t)
	t.Cleanup(func() { _ = d.Close() })
	hs := headscale.New("http://global-headscale:50444", "global-key")
	r := New(d, cpTestKey, hs)
	r.Init()
	id := seedControlplaneUserInTest(t, d, "eve")
	if err := db.SetUserHeadscaleConfig(d, id, "https://h.example.com", "k", cpTestKey); err != nil {
		t.Fatal(err)
	}
	// Simulate "operator rotated SKYGATE_SECRET_KEY without
	// re-encrypting" by swapping the router for one with
	// a different key.
	badKey := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	r2 := New(d, badKey, hs)
	r2.Init()
	c := r2.ForUser(id)
	if c != r2.Global() {
		t.Errorf("expected fall-through to global on corrupt key, got different")
	}
}

// TestRouter_ForUser_EmptySecretKey_FallsBackToGlobal: a
// missing SKYGATE_SECRET_KEY env var means encryption
// isn't configured; ForUser returns the global client.
func TestRouter_ForUser_EmptySecretKey_FallsBackToGlobal(t *testing.T) {
	d := openControlplaneTestDB(t)
	t.Cleanup(func() { _ = d.Close() })
	hs := headscale.New("http://global-headscale:50444", "global-key")
	r := New(d, "", hs) // empty secret
	r.Init()
	id := seedControlplaneUserInTest(t, d, "frank")
	if err := db.SetUserHeadscaleConfig(d, id, "https://h.example.com", "k", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c := r.ForUser(id)
	if c != r.Global() {
		t.Errorf("expected fall-through to global when SecretKeyHex empty")
	}
}

// TestRouter_InvalidateCache_DropsAll: InvalidateCache("")
// drops every cached client. The CacheSize() method is
// the public surface the pre-Phase-D3 test used to check
// via the private a.hsCache field; we keep the same
// contract via the public method.
func TestRouter_InvalidateCache_DropsAll(t *testing.T) {
	r, d := newTestRouter(t)
	t.Cleanup(func() { _ = d.Close() })
	id1 := seedControlplaneUserInTest(t, d, "u1")
	id2 := seedControlplaneUserInTest(t, d, "u2")
	if err := db.SetUserHeadscaleConfig(d, id1, "https://a.example.com", "k1", cpTestKey); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserHeadscaleConfig(d, id2, "https://b.example.com", "k2", cpTestKey); err != nil {
		t.Fatal(err)
	}
	_ = r.ForUser(id1)
	_ = r.ForUser(id2)
	if r.CacheSize() != 2 {
		t.Errorf("expected 2 cached clients before invalidate, got %d", r.CacheSize())
	}
	r.InvalidateCache("")
	if r.CacheSize() != 0 {
		t.Errorf("expected empty cache after InvalidateCache(\"\"), got %d entries", r.CacheSize())
	}
}

// TestRouter_Global_SameInstance: Global is a stable
// accessor.
func TestRouter_Global_SameInstance(t *testing.T) {
	r, d := newTestRouter(t)
	t.Cleanup(func() { _ = d.Close() })
	c1 := r.Global()
	c2 := r.Global()
	if c1 != c2 {
		t.Errorf("Global should return the same instance")
	}
}

// ---------- helpers ----------

// openControlplaneTestDB returns a fresh in-memory SQLite
// with the v0.12.0 portal_users schema. We create the
// schema directly (not via a shared helper, which lives
// in the db package) so this test file doesn't need to
// import db_test internals.
func openControlplaneTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE portal_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0,
			headscale_user_id INTEGER,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			theme TEXT NOT NULL DEFAULT 'linear',
			headscale_url TEXT NOT NULL DEFAULT '',
			headscale_api_key_enc TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			_ = d.Close()
			t.Fatalf("schema: %v", err)
		}
	}
	return d
}

// seedControlplaneUserInTest inserts a portal_users row
// and returns the new id. Doesn't go through db.InsertPortalUser
// because we want the row to have NO per-user override
// (the default for SetUserHeadscaleConfig-with-empty-url).
func seedControlplaneUserInTest(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES (?, ?, 0)`,
		username, "h",
	)
	if err != nil {
		t.Fatalf("seed %q: %v", username, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("lastid: %v", err)
	}
	return id
}
