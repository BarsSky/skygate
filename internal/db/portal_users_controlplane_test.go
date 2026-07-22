// Tests for the v0.12.0 per-user headscale control plane helpers
// and the AES-GCM envelope-encryption helpers. The encryption
// tests pin roundtrip + key-rotation behaviour; the DB tests pin
// the read / write helpers against a fresh in-memory SQLite.

package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// 32 bytes (256 bits) — AES-256-GCM's required key size.
const testKeyHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var _ = "imports" // anchor for the file's import block

// (the testKeyHex + newKeyHex are at the top of the file; the
//  seedControlplaneUser helper is at the bottom)

func newKeyHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// ---------- encryption ----------

// TestEncryptDecryptRoundtrip: encrypt → decrypt returns the
// original plaintext.
func TestEncryptDecryptRoundtrip(t *testing.T) {
	cases := []string{
		"hello",
		"a",
		strings.Repeat("x", 1024),
		"unicode: привет こんにちは 🚀",
		"",
	}
	for _, pt := range cases {
		ct, err := EncryptForColumn(pt, testKeyHex)
		if err != nil {
			t.Fatalf("encrypt %q: %v", pt, err)
		}
		got, err := DecryptForColumn(ct, testKeyHex)
		if err != nil {
			t.Fatalf("decrypt %q: %v", pt, err)
		}
		if got != pt {
			t.Errorf("roundtrip %q: got %q", pt, got)
		}
	}
}

// TestEncryptDifferentNoncePerCall: AES-GCM uses a fresh
// nonce per Encrypt, so two encrypts of the same plaintext
// produce different ciphertexts.
func TestEncryptDifferentNoncePerCall(t *testing.T) {
	ct1, _ := EncryptForColumn("same", testKeyHex)
	ct2, _ := EncryptForColumn("same", testKeyHex)
	if ct1 == ct2 {
		t.Errorf("expected different ciphertext for the same plaintext (fresh nonce)")
	}
}

// TestDecryptEmptyCiphertext: empty stored value decrypts
// to "" (the canonical "no secret set" path).
func TestDecryptEmptyCiphertext(t *testing.T) {
	got, err := DecryptForColumn("", testKeyHex)
	if err != nil {
		t.Errorf("empty ciphertext should be no-op, got err=%v", err)
	}
	if got != "" {
		t.Errorf("empty ciphertext should return empty plaintext, got %q", got)
	}
}

// TestDecryptBadBase64: malformed ciphertext returns
// ErrSecretCiphertextCorrupt.
func TestDecryptBadBase64(t *testing.T) {
	_, err := DecryptForColumn("not-base64-!!!", testKeyHex)
	if !errors.Is(err, ErrSecretCiphertextCorrupt) {
		t.Errorf("expected ErrSecretCiphertextCorrupt, got %v", err)
	}
}

// TestDecryptTamperedCiphertext: flipping a byte in the
// ciphertext makes GCM auth fail.
func TestDecryptTamperedCiphertext(t *testing.T) {
	ct, err := EncryptForColumn("hello", testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	// Flip the last char.
	tampered := ct[:len(ct)-1] + "A"
	if tampered == ct {
		tampered = ct[:len(ct)-1] + "B"
	}
	_, err = DecryptForColumn(tampered, testKeyHex)
	if !errors.Is(err, ErrSecretCiphertextCorrupt) {
		t.Errorf("expected ErrSecretCiphertextCorrupt on tampered ciphertext, got %v", err)
	}
}

// TestDecryptWrongKey: ciphertext encrypted with keyA
// cannot be decrypted with keyB.
func TestDecryptWrongKey(t *testing.T) {
	keyA := testKeyHex
	keyB := newKeyHex(t)
	ct, err := EncryptForColumn("secret", keyA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecryptForColumn(ct, keyB)
	if !errors.Is(err, ErrSecretCiphertextCorrupt) {
		t.Errorf("expected ErrSecretCiphertextCorrupt on wrong key, got %v", err)
	}
}

// TestEncryptEmptyKey: empty key returns an error (we
// don't fall through to a zero-key default).
func TestEncryptEmptyKey(t *testing.T) {
	_, err := EncryptForColumn("hello", "")
	if err == nil {
		t.Errorf("expected error for empty key, got nil")
	}
}

// TestEncryptWrongKeyLength: a key that decodes to
// something other than 32 bytes is rejected.
func TestEncryptWrongKeyLength(t *testing.T) {
	short := strings.Repeat("a", 4) // 2 bytes
	_, err := EncryptForColumn("hello", short)
	if err == nil {
		t.Errorf("expected error for short key, got nil")
	}
}

// ---------- DB helpers ----------

// TestSetGetUserHeadscaleConfig_Roundtrip: write via
// SetUserHeadscaleConfig, read back via GetUserHeadscaleConfig
// with the same key. The api_key round-trips intact.
func TestSetGetUserHeadscaleConfig_Roundtrip(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	id := seedControlplaneUser(t, d, "alice")

	if err := SetUserHeadscaleConfig(d, id, "https://head-us.example.com", "uskey-12345", testKeyHex); err != nil {
		t.Fatalf("set: %v", err)
	}
	cfg, err := GetUserHeadscaleConfig(d, id, testKeyHex)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cfg.URL != "https://head-us.example.com" {
		t.Errorf("URL = %q, want https://head-us.example.com", cfg.URL)
	}
	if cfg.APIKey != "uskey-12345" {
		t.Errorf("APIKey = %q, want uskey-12345", cfg.APIKey)
	}
	if !cfg.HasOverride() {
		t.Errorf("HasOverride should be true after Set")
	}
}

// TestGetUserHeadscaleConfig_NoOverride: a freshly-seeded
// user has no override; GetUserHeadscaleConfig returns
// ErrNoUserControlPlane.
func TestGetUserHeadscaleConfig_NoOverride(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	id := seedControlplaneUser(t, d, "bob")
	_, err := GetUserHeadscaleConfig(d, id, testKeyHex)
	if !errors.Is(err, ErrNoUserControlPlane) {
		t.Errorf("expected ErrNoUserControlPlane, got %v", err)
	}
}

// TestGetUserHeadscaleConfig_WrongKey: a value written
// with keyA is unreadable with keyB.
func TestGetUserHeadscaleConfig_WrongKey(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	id := seedControlplaneUser(t, d, "carol")
	if err := SetUserHeadscaleConfig(d, id, "https://h.example.com", "k", testKeyHex); err != nil {
		t.Fatal(err)
	}
	_, err := GetUserHeadscaleConfig(d, id, newKeyHex(t))
	if !errors.Is(err, ErrSecretCiphertextCorrupt) {
		t.Errorf("expected ErrSecretCiphertextCorrupt, got %v", err)
	}
}

// TestClearUserHeadscaleConfig: Clear sets both columns
// back to ''; subsequent Get returns ErrNoUserControlPlane.
func TestClearUserHeadscaleConfig(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	id := seedControlplaneUser(t, d, "dave")
	if err := SetUserHeadscaleConfig(d, id, "https://h.example.com", "k", testKeyHex); err != nil {
		t.Fatal(err)
	}
	if err := ClearUserHeadscaleConfig(d, id); err != nil {
		t.Fatalf("clear: %v", err)
	}
	_, err := GetUserHeadscaleConfig(d, id, testKeyHex)
	if !errors.Is(err, ErrNoUserControlPlane) {
		t.Errorf("expected ErrNoUserControlPlane after clear, got %v", err)
	}
}

// TestSetUserHeadscaleConfig_EmptyURLClears: passing
// empty url to Set is equivalent to Clear.
func TestSetUserHeadscaleConfig_EmptyURLClears(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	id := seedControlplaneUser(t, d, "eve")
	if err := SetUserHeadscaleConfig(d, id, "https://h.example.com", "k", testKeyHex); err != nil {
		t.Fatal(err)
	}
	if err := SetUserHeadscaleConfig(d, id, "", "", testKeyHex); err != nil {
		t.Fatalf("clear-via-set: %v", err)
	}
	_, err := GetUserHeadscaleConfig(d, id, testKeyHex)
	if !errors.Is(err, ErrNoUserControlPlane) {
		t.Errorf("expected ErrNoUserControlPlane, got %v", err)
	}
}

// TestSetUserHeadscaleConfig_URLWithoutKey: passing url
// but empty key is a config error (would result in a
// half-configured row).
func TestSetUserHeadscaleConfig_URLWithoutKey(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	id := seedControlplaneUser(t, d, "frank")
	err := SetUserHeadscaleConfig(d, id, "https://h.example.com", "", testKeyHex)
	if err == nil {
		t.Errorf("expected error for url-without-key, got nil")
	}
}

// TestAllUsersHeadscaleConfig_ListsEveryone: returns
// every portal user, with HasOverride set correctly.
func TestAllUsersHeadscaleConfig_ListsEveryone(t *testing.T) {
	d := openNodeOwnerMapTestDB(t)
	a := seedControlplaneUser(t, d, "alice")
	b := seedControlplaneUser(t, d, "bob")
	_ = seedControlplaneUser(t, d, "carol")
	_ = SetUserHeadscaleConfig(d, a, "https://us.example.com", "k1", testKeyHex)
	_ = SetUserHeadscaleConfig(d, b, "https://us.example.com", "k2", testKeyHex)

	rows, err := AllUsersHeadscaleConfig(d)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(rows))
	}
	overrideCount := 0
	for _, r := range rows {
		if r.HasOverride {
			overrideCount++
		}
	}
	if overrideCount != 2 {
		t.Errorf("expected 2 users with override, got %d", overrideCount)
	}
}

// TestSummariseControlPlanes: groups the per-user rows
// into per-plane buckets. The "global default" bucket
// comes first, then planes by URL (sorted).
func TestSummariseControlPlanes(t *testing.T) {
	rows := []PortalUserControlPlaneRow{
		{UserID: 1, Username: "alice", URL: "https://us.example.com", HasOverride: true},
		{UserID: 2, Username: "bob", URL: "https://us.example.com", HasOverride: true},
		{UserID: 3, Username: "carol", URL: "https://eu.example.com", HasOverride: true},
		{UserID: 4, Username: "dave", URL: "", HasOverride: false},
	}
	out := SummariseControlPlanes(rows, "https://head.example.com")
	if len(out) != 3 {
		t.Fatalf("expected 3 planes (default + 2), got %d", len(out))
	}
	if out[0].URL != "https://head.example.com" {
		t.Errorf("first plane should be global default, got URL=%q", out[0].URL)
	}
	if len(out[0].Users) != 1 || out[0].Users[0] != "dave" {
		t.Errorf("global default should have only dave, got %v", out[0].Users)
	}
	if out[1].URL != "https://eu.example.com" || out[2].URL != "https://us.example.com" {
		t.Errorf("planes should be sorted by URL after default: %s, %s", out[1].URL, out[2].URL)
	}
	if len(out[2].Users) != 2 {
		t.Errorf("us plane should have 2 users, got %v", out[2].Users)
	}
}

// seedControlplaneUser inserts one portal_users row with the given
// username. Returns the new id. Distinct from the seedPortalUser
// helper in portal_users_test.go (which has a different signature).
func seedControlplaneUser(t *testing.T, d interface {
	Exec(query string, args ...any) (sql.Result, error)
}, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ($1, $2, 0)`,
		username, "hash:"+username,
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
