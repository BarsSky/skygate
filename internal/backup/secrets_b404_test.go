// B404 (v1.5.147) — the two backup credentials are secrets: they
// are write-only in the panel (never rendered back into the form)
// and encrypted at rest when SKYGATE_SECRET_KEY is configured.
//
// These tests run against a real SQLite DB (in-memory) because the
// thing under test IS the storage round trip: a mock would let a
// broken "enc:v1:" prefix, a wrong column list or a swallowed
// decrypt error pass. The dialect helpers in internal/db dispatch on
// the ACTIVE dialect, which opening the DB sets.
package backup

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
)

// b404TestKey is a valid 32-byte (64 hex chars) AES key. Deliberately
// NOT a realistic deployment value.
const b404TestKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// openB404DB returns an in-memory SQLite DB carrying just the
// global_settings table these functions touch.
func openB404DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS global_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		updated_at INTEGER DEFAULT 0
	)`); err != nil {
		t.Fatalf("create global_settings: %v", err)
	}
	return d
}

// rawSetting reads one value exactly as it sits in the DB.
func rawSetting(t *testing.T, d *sql.DB, key string) string {
	t.Helper()
	var v string
	if err := d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return v
}

// B404 E1 — with a master key configured, both secrets are written as
// ciphertext, the plaintext never appears in the stored bytes, and a
// load returns the original values. This is the contract that makes
// "encrypted at rest" a fact rather than a comment.
func TestB404_SecretsAreCiphertextAtRest(t *testing.T) {
	d := openB404DB(t)
	want := &Config{
		Destination: "/mnt/backups",
		Protocol:    ProtocolSMB,
		Username:    "backup_user",
		Password:    "p@ssw0rd-with-spaces ",
		S3SecretKey: "s3-secret-do-not-leak",
	}
	if err := SaveWithKey(d, want, b404TestKey); err != nil {
		t.Fatalf("SaveWithKey: %v", err)
	}
	for _, k := range []string{keyPassword, keyS3SecretKey} {
		raw := rawSetting(t, d, k)
		if !IsCiphertextSecret(raw) {
			t.Errorf("%s is not ciphertext: %q", k, raw)
		}
		if strings.Contains(raw, "p@ssw0rd") || strings.Contains(raw, "s3-secret-do-not-leak") {
			t.Errorf("%s still contains the plaintext secret: %q", k, raw)
		}
	}
	got, err := LoadWithKey(d, b404TestKey)
	if err != nil {
		t.Fatalf("LoadWithKey: %v", err)
	}
	if got.Password != want.Password {
		t.Errorf("password round trip: got %q want %q", got.Password, want.Password)
	}
	if got.S3SecretKey != want.S3SecretKey {
		t.Errorf("s3 secret round trip: got %q want %q", got.S3SecretKey, want.S3SecretKey)
	}
	if got.SecretsAtRest != SecretsAtRestEncrypted {
		t.Errorf("SecretsAtRest = %q, want %q", got.SecretsAtRest, SecretsAtRestEncrypted)
	}
	if got.SecretError != "" {
		t.Errorf("unexpected SecretError: %q", got.SecretError)
	}
}

// B404 E2 — without a master key nothing breaks (an install that never
// set SKYGATE_SECRET_KEY keeps working exactly as before), but the
// page is TOLD that the value sits in plaintext.
func TestB404_NoKeyKeepsPlaintextAndSaysSo(t *testing.T) {
	d := openB404DB(t)
	c := &Config{Destination: "/mnt/backups", Protocol: ProtocolSMB, Password: "legacy-pass"}
	if err := SaveWithKey(d, c, ""); err != nil {
		t.Fatalf("SaveWithKey(no key): %v", err)
	}
	if raw := rawSetting(t, d, keyPassword); raw != "legacy-pass" {
		t.Errorf("stored value = %q, want the plaintext legacy-pass", raw)
	}
	got, err := LoadWithKey(d, "")
	if err != nil {
		t.Fatalf("LoadWithKey(no key): %v", err)
	}
	if got.Password != "legacy-pass" {
		t.Errorf("plaintext round trip: got %q", got.Password)
	}
	if got.SecretsAtRest != SecretsAtRestPlaintext {
		t.Errorf("SecretsAtRest = %q, want %q (the page must warn)", got.SecretsAtRest, SecretsAtRestPlaintext)
	}
}

// B404 E3 — a ciphertext row read by a process WITHOUT the key reports
// the reason and clears the field, instead of handing an "enc:v1:…"
// blob to a mount command (which would fail as an auth error blaming
// the NAS).
func TestB404_CiphertextWithoutKeyIsUnreadableNotABlob(t *testing.T) {
	d := openB404DB(t)
	if err := SaveWithKey(d, &Config{Destination: "/x", Password: "real-pass"}, b404TestKey); err != nil {
		t.Fatalf("SaveWithKey: %v", err)
	}
	got, err := LoadWithKey(d, "")
	if err != nil {
		t.Fatalf("LoadWithKey must not fail the whole load: %v", err)
	}
	if got.Password != "" {
		t.Errorf("password = %q for an unreadable secret, want empty", got.Password)
	}
	if !strings.Contains(got.SecretError, keyPassword) {
		t.Errorf("SecretError = %q, want it to name %s", got.SecretError, keyPassword)
	}
	if !strings.Contains(got.SecretError, ErrSecretKeyMissing.Error()) {
		t.Errorf("SecretError = %q, want it to name the missing key", got.SecretError)
	}
	// The stored ciphertext is untouched by a read.
	if raw := rawSetting(t, d, keyPassword); !IsCiphertextSecret(raw) {
		t.Errorf("a read must not modify the stored row; got %q", raw)
	}
}

// B404 E4 — and the same process must not OVERWRITE what it cannot
// read: a save without a key against an encrypted row fails loudly and
// leaves the ciphertext in place (no silent downgrade to plaintext, no
// silently erased credential).
func TestB404_SaveWithoutKeyRefusesToDowngrade(t *testing.T) {
	d := openB404DB(t)
	if err := SaveWithKey(d, &Config{Destination: "/x", Password: "real-pass"}, b404TestKey); err != nil {
		t.Fatalf("SaveWithKey: %v", err)
	}
	before := rawSetting(t, d, keyPassword)
	err := SaveWithKey(d, &Config{Destination: "/y", Password: ""}, "")
	if err == nil {
		t.Fatal("SaveWithKey without a key overwrote an encrypted row; want an error")
	}
	if !strings.Contains(err.Error(), keyPassword) {
		t.Errorf("error = %q, want it to name %s", err, keyPassword)
	}
	if after := rawSetting(t, d, keyPassword); after != before {
		t.Errorf("stored credential changed on a refused save: %q -> %q", before, after)
	}
	// The rest of the config also rolled back with the transaction.
	var dest string
	if qerr := d.QueryRow(`SELECT value FROM global_settings WHERE key = ?`, keyDestination).Scan(&dest); qerr != nil {
		t.Fatalf("read destination: %v", qerr)
	}
	if dest != "/x" {
		t.Errorf("destination = %q after a refused save, want the old /x", dest)
	}
}

// B404 E5 — the fingerprint is the most the panel may show: the last
// four characters, nothing else, and nothing at all for a short value.
func TestB404_FingerprintRevealsOnlyTheTail(t *testing.T) {
	if got := SecretFingerprint("abcdefghijkl"); !strings.HasSuffix(got, "ijkl") {
		t.Errorf("fingerprint %q does not end with the last 4 chars", got)
	}
	if got := SecretFingerprint("abcdefghijkl"); strings.Contains(got, "abcdefgh") {
		t.Errorf("fingerprint %q leaks the head of the secret", got)
	}
	if got := SecretFingerprint("short"); got != "••••••••" {
		t.Errorf("short secret fingerprint = %q, want a full mask", got)
	}
	if got := SecretFingerprint(""); got != "" {
		t.Errorf("empty secret fingerprint = %q, want empty", got)
	}
}

// B404 E6 — a legacy plaintext row is upgraded to ciphertext by the
// next save that has a key, with the value preserved. This is the path
// an existing install takes, and it must not require a migration.
func TestB404_PlaintextRowUpgradesOnNextSave(t *testing.T) {
	d := openB404DB(t)
	if err := SaveWithKey(d, &Config{Password: "legacy-pass"}, ""); err != nil {
		t.Fatalf("initial plaintext save: %v", err)
	}
	if raw := rawSetting(t, d, keyPassword); raw != "legacy-pass" {
		t.Fatalf("precondition: stored %q, want plaintext", raw)
	}
	loaded, err := LoadWithKey(d, b404TestKey)
	if err != nil {
		t.Fatalf("LoadWithKey: %v", err)
	}
	if loaded.Password != "legacy-pass" {
		t.Fatalf("legacy plaintext was not readable with a key: %q", loaded.Password)
	}
	if err := SaveWithKey(d, loaded, b404TestKey); err != nil {
		t.Fatalf("re-save with key: %v", err)
	}
	raw := rawSetting(t, d, keyPassword)
	if !IsCiphertextSecret(raw) {
		t.Errorf("plaintext row was not upgraded: %q", raw)
	}
	back, err := LoadWithKey(d, b404TestKey)
	if err != nil {
		t.Fatalf("LoadWithKey after upgrade: %v", err)
	}
	if back.Password != "legacy-pass" {
		t.Errorf("value changed during the upgrade: %q", back.Password)
	}
}

// B404 E7 — mixed state is reported as mixed (one encrypted, one not),
// because a single "encrypted" sentence next to a plaintext row is a
// lie the operator would rely on.
func TestB404_MixedStorageIsReported(t *testing.T) {
	d := openB404DB(t)
	enc, err := EncryptSecretOptional("s3-secret", b404TestKey)
	if err != nil {
		t.Fatalf("EncryptSecretOptional: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES (?, ?)`, keyPassword, "plain-pass"); err != nil {
		t.Fatalf("seed plaintext password: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES (?, ?)`, keyS3SecretKey, enc); err != nil {
		t.Fatalf("seed encrypted s3 secret: %v", err)
	}
	got, err := LoadWithKey(d, b404TestKey)
	if err != nil {
		t.Fatalf("LoadWithKey: %v", err)
	}
	if got.SecretsAtRest != SecretsAtRestMixed {
		t.Errorf("SecretsAtRest = %q, want %q", got.SecretsAtRest, SecretsAtRestMixed)
	}
	if got.Password != "plain-pass" || got.S3SecretKey != "s3-secret" {
		t.Errorf("mixed load returned %q / %q", got.Password, got.S3SecretKey)
	}
}
