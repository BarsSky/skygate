package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"skygate/internal/config"
)

// b415Key and b415KeyOther are throwaway ed25519 PUBLIC keys, composed at runtime so
// no contiguous key-shaped literal exists in the tree (B407's lesson).
func b415Key() string {
	return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHMnYyaahQezwVLrLW+w0AA65pBjWXUsQD/" +
		"NGP61Y9YR b415-fixture"
}

func b415KeyOther() string {
	return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOrgEIK/PNQx3smfCP3Qgi1bgsfwUtsaFMqCMKNRBjtU " +
		"b415-fixture-2"
}

// TestSSHPublicKeyFor_PrefersThePrivateKey_B415 is the core of the operator's report:
// the card used to read only a `.pub` sibling, so a stale file (regenerated private
// key) was rendered as "the key" with a copy button, and authorising it produced
// `Permission denied (publickey)` with no other clue.
func TestSSHPublicKeyFor_PrefersThePrivateKey_B415(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "skygate_sync")
	if err := os.WriteFile(priv, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := derivePublicKeyExec
	t.Cleanup(func() { derivePublicKeyExec = old })
	derivePublicKeyExec = func(context.Context, string) ([]byte, error) {
		return []byte(b415Key() + "\n"), nil
	}

	t.Run("matching pub is not flagged", func(t *testing.T) {
		if err := os.WriteFile(priv+".pub", []byte(b415Key()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		svc := &Service{SSHKeyPath: priv}
		pk := svc.sshPublicKeyFor(priv)
		if pk.Key != b415Key() {
			t.Fatalf("Key = %q, want the derived key", pk.Key)
		}
		if !pk.Derived {
			t.Fatal("Derived must be true when the private key answered")
		}
		if pk.Fingerprint == "" {
			t.Fatal("a fingerprint must be rendered")
		}
		if pk.StalePub != "" {
			t.Fatalf("StalePub = %q, want empty for a matching .pub", pk.StalePub)
		}
	})

	t.Run("stale pub is named instead of shown", func(t *testing.T) {
		if err := os.WriteFile(priv+".pub", []byte(b415KeyOther()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		svc := &Service{SSHKeyPath: priv}
		pk := svc.sshPublicKeyFor(priv)
		if pk.Key != b415Key() {
			t.Fatalf("Key = %q — the stale .pub must never become the key the card shows", pk.Key)
		}
		if pk.StalePub != priv+".pub" {
			t.Fatalf("StalePub = %q, want %q", pk.StalePub, priv+".pub")
		}
	})

	t.Run("derivation unavailable falls back to the pub file", func(t *testing.T) {
		derivePublicKeyExec = func(context.Context, string) ([]byte, error) {
			return nil, os.ErrPermission // e.g. a passphrase-protected key
		}
		if err := os.WriteFile(priv+".pub", []byte(b415KeyOther()+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		svc := &Service{SSHKeyPath: priv}
		pk := svc.sshPublicKeyFor(priv)
		if pk.Key != b415KeyOther() {
			t.Fatalf("Key = %q, want the .pub contents as the fallback", pk.Key)
		}
		if pk.Derived {
			t.Fatal("Derived must be false when the fallback was used")
		}
		if pk.Source != priv+".pub" {
			t.Fatalf("Source = %q, want %q", pk.Source, priv+".pub")
		}
		if pk.Fingerprint == "" {
			t.Fatal("the fallback key must still get a fingerprint")
		}
	})

	t.Run("nothing resolvable yields an empty key", func(t *testing.T) {
		_ = os.Remove(priv + ".pub")
		svc := &Service{SSHKeyPath: priv, Cfg: &config.Config{SSHKeyPath: filepath.Join(dir, "absent")}}
		pk := svc.sshPublicKeyFor(priv)
		if pk.Key != "" {
			t.Fatalf("Key = %q, want empty when neither the key nor a .pub can be read", pk.Key)
		}
	})
}

// TestDerivePublicKey_RejectsGarbage keeps a misbehaving ssh-keygen from becoming a
// key on the page.
func TestDerivePublicKey_RejectsGarbage(t *testing.T) {
	old := derivePublicKeyExec
	t.Cleanup(func() { derivePublicKeyExec = old })
	derivePublicKeyExec = func(context.Context, string) ([]byte, error) {
		return []byte("not a key at all\n"), nil
	}
	if _, ok := derivePublicKey(""); ok {
		t.Fatal("an empty path must not derive")
	}
	if _, ok := derivePublicKey("/some/key"); ok {
		t.Fatal("garbage output must not be accepted as a public key")
	}
}
