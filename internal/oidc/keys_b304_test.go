// B304 (v1.5.69) — the key store must be movable from the panel, live.
//
// Why this exists: /admin/oidc lets the operator set key_dir, but before B304 the
// live applier only pushed issuer/client_id/secret/redirect_uris into the running
// provider. A key_dir change therefore looked accepted and changed nothing until
// the next restart — the "this still needs an env edit and a restart" complaint.
// Reload moves the store (load-or-generate at the new path) and activates the new
// key under the write lock, so the running key — and /oidc/jwks.json — survive a
// bad path untouched.

package oidc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestKeyStoreReloadMovesTheStore_B304: Reload activates the keypair at the new
// directory (creating it if needed) and reports the resolved dir + kid.
func TestKeyStoreReloadMovesTheStore_B304(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := filepath.Join(t.TempDir(), "nested", "oidc-keys") // does not exist yet

	ks, err := NewKeyStore(dir1)
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	kid1 := ks.KID()
	if kid1 == "" {
		t.Fatal("no kid after boot")
	}

	if err := ks.Reload(dir2); err != nil {
		t.Fatalf("Reload to a fresh nested dir: %v", err)
	}
	if got := ks.Dir(); got != dir2 {
		t.Errorf("Dir() = %q, want %q", got, dir2)
	}
	if ks.KID() == "" || ks.KID() == kid1 {
		t.Errorf("kid = %q (was %q) — a fresh directory must carry a fresh key", ks.KID(), kid1)
	}
	priv := filepath.Join(dir2, "oidc-signing.pem")
	st, err := os.Stat(priv)
	if err != nil {
		t.Fatalf("private key was not written to the new dir: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0600 && runtime.GOOS != "windows" {
		t.Errorf("private key mode = %04o, want 0600", perm)
	} else if runtime.GOOS == "windows" {
		// os.WriteFile's perm argument is advisory on Windows (ACLs rule); the
		// 0600 assertion is meaningful on the Linux hosts this ships to.
		t.Logf("private key mode = %04o (not asserted on windows)", perm)
	}
	if ks.ActiveKey() == nil || ks.ActiveKey().Private.N.BitLen() != 2048 {
		t.Errorf("active key is not the RSA-2048 pair from the new dir")
	}
	if !ks.Ready() {
		t.Error("Ready() = false after a successful Reload")
	}
}

// TestKeyStoreReloadReusesAnExistingKeypair_B304: pointing the panel at a
// directory that already holds a keypair must ADOPT it (no silent regeneration —
// regenerating would invalidate every id_token already issued against that kid).
func TestKeyStoreReloadReusesAnExistingKeypair_B304(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	first, err := NewKeyStore(dir2) // materialises the pair in dir2
	if err != nil {
		t.Fatalf("NewKeyStore(dir2): %v", err)
	}
	ks, err := NewKeyStore(dir1)
	if err != nil {
		t.Fatalf("NewKeyStore(dir1): %v", err)
	}
	if err := ks.Reload(dir2); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if ks.KID() != first.KID() {
		t.Errorf("kid after Reload = %q, want the existing %q", ks.KID(), first.KID())
	}
}

// TestKeyStoreReloadRefusesRelativePath_B304: B270's live failure was a relative
// key dir resolved against a systemd working directory, which made an
// unconfigured feature kill the process. The panel must not be able to
// reintroduce it.
func TestKeyStoreReloadRefusesRelativePath_B304(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	before, dirBefore := ks.KID(), ks.Dir()

	err = ks.Reload("./data/oidc-keys")
	if err == nil {
		t.Fatal("Reload accepted a relative path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error = %v, want it to say the path must be absolute", err)
	}
	if ks.KID() != before || ks.Dir() != dirBefore {
		t.Errorf("a refused Reload changed the live store (dir %q -> %q)", dirBefore, ks.Dir())
	}
}

// TestKeyStoreReloadKeepsTheLiveKeyOnFailure_B304 is the no-downtime property:
// an unusable target path (here: a FILE where the directory should be) must
// leave the running keypair active, so /oidc/jwks.json keeps answering.
func TestKeyStoreReloadKeepsTheLiveKeyOnFailure_B304(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	kid := ks.KID()

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if err := ks.Reload(filepath.Join(blocker, "oidc-keys")); err == nil {
		t.Fatal("Reload succeeded on a path under a regular file")
	}
	if ks.KID() != kid {
		t.Errorf("kid changed after a failed Reload (%q -> %q) — the live JWKS would break", kid, ks.KID())
	}
	if ks.Dir() != dir {
		t.Errorf("Dir() = %q after a failed Reload, want the original %q", ks.Dir(), dir)
	}
	if !ks.Ready() {
		t.Error("Ready() = false after a failed Reload — the provider must stay up")
	}
}

// TestKeyStoreReloadNilSafe_B304: the B270 degrade path keeps Keys == nil, and
// the panel must be able to say so instead of panicking.
func TestKeyStoreReloadNilSafe_B304(t *testing.T) {
	var ks *KeyStore
	if err := ks.Reload(t.TempDir()); err == nil {
		t.Fatal("nil store accepted a Reload")
	}
	if ks.Dir() != "" || ks.KID() != "" || ks.Ready() {
		t.Error("nil store accessors must be inert")
	}
}

// TestKeyStoreReloadEmptyDir_B304: an empty value means "the operator cleared the
// field"; the store refuses it with a message naming a usable path rather than
// falling back to a CWD-relative default (that default is what broke B270).
func TestKeyStoreReloadEmptyDir_B304(t *testing.T) {
	ks, err := NewKeyStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	err = ks.Reload("   ")
	if err == nil {
		t.Fatal("Reload accepted an empty dir")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %v, want it to name the empty value", err)
	}
}
