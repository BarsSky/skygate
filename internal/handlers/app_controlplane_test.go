// Tests for the v0.12.0 per-user headscale client router.
// Pins:
//   - HSForUser returns the global client when no override
//   - HSForUser returns a per-user client when override set
//   - HSForUser caches clients by url (rebuild on key rotation)
//   - HSGlobal always returns the same global instance
//   - InvalidateHSCache drops entries
//   - HSForUser falls through to global on corrupt ciphertext

package handlers

import (
	"database/sql"
	"sync"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

const cpTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// newControlplaneApp builds an App with the test DB + a
// fresh global headscale client. The HSForUser routing is
// independent of how the global client was built, so any
// valid *headscale.Client works.
func newControlplaneApp(t *testing.T) (*App, *fakeDB) {
	t.Helper()
	d := openControlplaneTestDB(t)
	hs := headscale.New("http://global-headscale:50444", "global-key")
	a := New(d, hs, "global-key", "jwt", "https://head.example.com", "", 24, nil)
	a.SecretKeyHex = cpTestKey
	return a, &fakeDB{d: d}
}

// fakeDB wraps a *sql.DB so we can stub out a cleanup
// method if needed. Currently just an opaque holder.
type fakeDB struct{ d interface{ Close() error } }

func (f *fakeDB) Close() { _ = f.d.Close() }

// TestHSForUser_NoOverride_ReturnsGlobal: a user with no
// per-user control plane row gets the global client.
func TestHSForUser_NoOverride_ReturnsGlobal(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	id := seedControlplaneUserInHandlers(t, a.DB, "alice")
	c := a.HSForUser(id)
	if c != a.HSGlobal() {
		t.Errorf("expected global client, got different instance")
	}
}

// TestHSForUser_WithOverride_ReturnsPerUser: a user with
// headscale_url set gets a per-user client (different
// from the global).
func TestHSForUser_WithOverride_ReturnsPerUser(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	id := seedControlplaneUserInHandlers(t, a.DB, "bob")
	if err := setControlplane(a.DB, id, "https://us.example.com", "us-key", cpTestKey); err != nil {
		t.Fatalf("set: %v", err)
	}
	c := a.HSForUser(id)
	if c == a.HSGlobal() {
		t.Errorf("expected per-user client, got global")
	}
	if c.ApiKeyForCache() != "us-key" {
		t.Errorf("client apiKey = %q, want us-key", c.ApiKeyForCache())
	}
}

// TestHSForUser_CachesClientByURL: a second call for the
// same user returns the cached client (same instance).
func TestHSForUser_CachesClientByURL(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	id := seedControlplaneUserInHandlers(t, a.DB, "carol")
	if err := setControlplane(a.DB, id, "https://eu.example.com", "eu-key", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c1 := a.HSForUser(id)
	c2 := a.HSForUser(id)
	if c1 != c2 {
		t.Errorf("expected same instance (cached), got different")
	}
}

// TestHSForUser_InvalidatesOnKeyRotation: rotating the
// per-user key drops the cached client.
func TestHSForUser_InvalidatesOnKeyRotation(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	id := seedControlplaneUserInHandlers(t, a.DB, "dave")
	if err := setControlplane(a.DB, id, "https://h.example.com", "k1", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c1 := a.HSForUser(id)
	// Admin rotates the key.
	if err := setControlplane(a.DB, id, "https://h.example.com", "k2", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c2 := a.HSForUser(id)
	if c1 == c2 {
		t.Errorf("expected new client after key rotation, got same instance")
	}
	if c2.ApiKeyForCache() != "k2" {
		t.Errorf("new client apiKey = %q, want k2", c2.ApiKeyForCache())
	}
}

// TestHSForUser_CorruptCiphertext_FallsBackToGlobal: when
// the stored key can't be decrypted (wrong SKYGATE_SECRET_KEY),
// the helper logs the error and returns the global client
// instead of 500-ing.
func TestHSForUser_CorruptCiphertext_FallsBackToGlobal(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	id := seedControlplaneUserInHandlers(t, a.DB, "eve")
	// Set with cpTestKey, then change the App's key to something
	// else — simulates "operator rotated SKYGATE_SECRET_KEY
	// without re-encrypting".
	if err := setControlplane(a.DB, id, "https://h.example.com", "k", cpTestKey); err != nil {
		t.Fatal(err)
	}
	a.SecretKeyHex = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	c := a.HSForUser(id)
	if c != a.HSGlobal() {
		t.Errorf("expected fall-through to global on corrupt key, got different")
	}
}

// TestHSForUser_EmptySecretKey_FallsBackToGlobal: a missing
// SKYGATE_SECRET_KEY env var means encryption isn't configured;
// HSForUser returns the global client.
func TestHSForUser_EmptySecretKey_FallsBackToGlobal(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	id := seedControlplaneUserInHandlers(t, a.DB, "frank")
	if err := setControlplane(a.DB, id, "https://h.example.com", "k", cpTestKey); err != nil {
		t.Fatal(err)
	}
	a.SecretKeyHex = ""
	c := a.HSForUser(id)
	if c != a.HSGlobal() {
		t.Errorf("expected fall-through to global when SecretKeyHex empty")
	}
}

// TestInvalidateHSCache_DropsAll: InvalidateHSCache("") drops
// every cached client.
func TestInvalidateHSCache_DropsAll(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	id1 := seedControlplaneUserInHandlers(t, a.DB, "u1")
	id2 := seedControlplaneUserInHandlers(t, a.DB, "u2")
	if err := setControlplane(a.DB, id1, "https://a.example.com", "k1", cpTestKey); err != nil {
		t.Fatal(err)
	}
	if err := setControlplane(a.DB, id2, "https://b.example.com", "k2", cpTestKey); err != nil {
		t.Fatal(err)
	}
	_ = a.HSForUser(id1)
	_ = a.HSForUser(id2)
	a.InvalidateHSCache("")
	a.hsCacheMu.Lock()
	defer a.hsCacheMu.Unlock()
	if len(a.hsCache) != 0 {
		t.Errorf("expected empty cache, got %d entries", len(a.hsCache))
	}
}

// TestHSGlobal_SameInstance: HSGlobal is a stable accessor.
func TestHSGlobal_SameInstance(t *testing.T) {
	a, db := newControlplaneApp(t)
	defer db.Close()
	c1 := a.HSGlobal()
	c2 := a.HSGlobal()
	if c1 != c2 {
		t.Errorf("HSGlobal should return the same instance")
	}
}

// ---------- helpers ----------

// openControlplaneTestDB returns a fresh in-memory SQLite
// with the v0.12.0 portal_users schema. We create the
// schema directly (not via the shared openNodeOwnerMapTestDB
// helper, which lives in the db package) so this test
// file doesn't need to import the db_test package internals.
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
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedControlplaneUserInHandlers inserts a portal_users row
// and returns the new id. Doesn't go through InsertPortalUser
// because we want the row to have NO per-user override
// (the default for SetUserHeadscaleConfig-with-empty-url).
func seedControlplaneUserInHandlers(t *testing.T, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ($1, $2, 0)`,
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

// setControlplane is the v0.12.0 admin-side write: stores
// the (url, key) override for the given user. Thin
// wrapper around db.SetUserHeadscaleConfig to keep the
// test self-contained.
func setControlplane(d *sql.DB, userID int64, url, key, keyHex string) error {
	return db.SetUserHeadscaleConfig(d, userID, url, key, keyHex)
}

// silence unused
var _ = sync.Mutex{}
