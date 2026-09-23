// Package oidc — B161.1 (v1.5.0) — minimal
// OpenID Connect provider for headscale integration.
//
// Why this exists
// ---------------
// headscale 0.20+ supports OIDC as an alternative to
// preauth keys for Tailscale client registration.
// The Tailscale client gets a 302 redirect to the
// OIDC issuer, logs in there, and headscale
// auto-creates the headscale user + node from the
// OIDC claims.
//
// Operator 2026-08-20: "также возможно ли сделать
// перехват запроса к head.skyna.ru от клиента
// tailscale чтобы обернуть запрос на аутентификацию
// через логин в skygate и уже регестрирование
// устройства ключом через пользователя skygate?"
// Yes: make skygate the OIDC issuer for headscale.
//
// B161.1 (this file)
// ------------------
// RSA keypair generation + persistence. The public
// key is exposed via /oidc/jwks.json so headscale
// can verify the id_token's RS256 signature. The
// private key is used in B161.3 to sign id_tokens.
//
// The keypair is persisted to disk so that JWT
// signatures remain valid across restarts. A new
// keypair is generated on first start if the files
// don't exist. The key directory defaults to
// ./data/oidc-keys/ (configurable via
// SKYGATE_OIDC_KEY_DIR).
//
// What B161.1 does NOT include
// ----------------------------
// - /oidc/authorize handler (B161.2) — renders the
//   skygate login UI (or uses an existing cookie
//   session) and issues an auth code
// - /oidc/token handler (B161.3) — exchanges the
//   auth code for id_token + access_token (RS256
//   signed with the private key from this file)
// - /oidc/userinfo handler (B161.3) — returns the
//   OIDC user claims (sub, email, name) for the
//   access_token's subject
//
// Discovery + JWKS are enough to ship a safe B161.1
// commit that operators can verify the OIDC
// endpoints are reachable. The next iteration adds
// the actual auth flow.
package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// KeyID is a short, URL-safe identifier for the
// RSA keypair (e.g. "abc123def4"). It's used in
// the JWKS ("kid" field) and in the JWT header
// ("kid" claim) so headscale can pick the right
// public key when verifying the signature. The kid
// is derived from the last 8 bytes of the modulus
// — short, stable, deterministic. The same kid is
// generated on every load of the same keypair, so
// a restart doesn't invalidate already-issued JWTs.
type KeyID string

// SigningKey wraps an RSA private key with the
// kid + JWK representation needed for the JWKS
// endpoint and for JWT signing (B161.3).
//
// The JWK fields are pre-computed at load time so
// /oidc/jwks.json is a constant-time string
// template + the cached JWK slice — no RSA math on
// the hot path of every JWKS request.
type SigningKey struct {
	Private *rsa.PrivateKey
	KID     KeyID

	// Cached JWK representation (RFC 7517 sec 4):
	//   {
	//     "kty": "RSA",
	//     "use": "sig",
	//     "alg": "RS256",
	//     "kid": "<KID>",
	//     "n":   "<base64url(modulus)>",
	//     "e":   "<base64url(AQAB exponent)>"
	//   }
	// The map is a JSON-serializable view; the
	// /oidc/jwks.json handler marshals it directly.
	JWK map[string]string
}

// KeyStore is the thread-safe holder for the
// current signing key. B161.1 ships a single
// keypair (no rotation); future B-checks may add
// rotation + a kid lookup for the JWT header.
//
// The mutex is required because the
// /oidc/jwks.json handler runs concurrently with
// the /oidc/authorize handler (B161.2) which may
// sign id_tokens (B161.3) — both read the same
// SigningKey. Read-mostly access is fine but
// rotations would need write-lock; for now we
// expose a single, immutable-at-runtime key.
type KeyStore struct {
	mu    sync.RWMutex
	key   *SigningKey
	dir   string
	ready bool
}

// NewKeyStore loads the RSA keypair from dir, or
// generates a new one if dir is empty. Returns a
// ready-to-use *KeyStore.
//
// The key files are:
//   <dir>/oidc-signing.pem   — PKCS#1 PEM (RSA PRIVATE KEY)
//   <dir>/oidc-signing.pub   — PKCS#8 PEM (PUBLIC KEY)
//
// We persist both so the public key is available
// for offline inspection + so the JWKS endpoint
// can serve the same kid after a restart.
//
// On a production deploy the key directory should
// be on a persistent volume (./data/oidc-keys/ is
// bind-mounted in the operator's docker-compose).
// Losing the key dir means re-issuing all JWTs
// (headscale will re-auth on the next OIDC flow).
func NewKeyStore(dir string) (*KeyStore, error) {
	if dir == "" {
		dir = "./data/oidc-keys"
	}
	key, err := loadOrGenerateKey(dir)
	if err != nil {
		return nil, err
	}
	ks := &KeyStore{dir: dir, key: key, ready: true}
	return ks, nil
}

// loadOrGenerateKey prepares dir and returns the signing key held there: the
// existing pair when present, a freshly generated RSA-2048 pair otherwise.
//
// B304: extracted from NewKeyStore so a RELOAD (the panel changing key_dir) and
// a boot agree on every step — mkdir, generate-if-missing, load, and the
// weak-key refusal are the same code path for both.
func loadOrGenerateKey(dir string) (*SigningKey, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("oidc: mkdir %s: %w", dir, err)
	}
	privPath := filepath.Join(dir, "oidc-signing.pem")
	pubPath := filepath.Join(dir, "oidc-signing.pub")
	if _, err := os.Stat(privPath); os.IsNotExist(err) {
		log.Printf("oidc: no keypair at %s — generating new RSA-2048", privPath)
		priv, err := generateAndPersist(privPath, pubPath)
		if err != nil {
			return nil, fmt.Errorf("oidc: generate: %w", err)
		}
		return buildSigningKey(priv), nil
	} else if err != nil {
		return nil, fmt.Errorf("oidc: stat %s: %w", privPath, err)
	}
	priv, err := loadPrivateKeyFromDisk(privPath)
	if err != nil {
		return nil, fmt.Errorf("oidc: load: %w", err)
	}
	return buildSigningKey(priv), nil
}

// Reload (B304) points the store at another key directory and activates the
// keypair found (or created) there, WITHOUT a restart.
//
// Why: /admin/oidc lets the operator set key_dir, but the pre-B304 applier only
// pushed issuer/client_id/secret/redirect_uris into the running provider — so a
// key_dir change looked accepted and changed nothing until the next restart,
// which is the "this still needs an env/restart step" complaint that made the
// page feel read-only.
//
// Safety: the new key is loaded FIRST and swapped in under the write lock, so a
// wrong path (uncreatable, unreadable, weak key) leaves the running key — and
// therefore /oidc/jwks.json — exactly as it was. The error says which path
// failed and why; the caller renders it.
func (ks *KeyStore) Reload(dir string) error {
	if ks == nil {
		return fmt.Errorf("oidc: key store is not initialised (the boot-time key dir could not be created) — fix the path and save again")
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("oidc: key dir is empty — set an absolute path such as /var/lib/skygate/oidc-keys")
	}
	if !filepath.IsAbs(dir) {
		// B270's live failure was a relative key dir resolved against a systemd
		// CWD; refusing it here keeps that class of bug from coming back through
		// the panel.
		return fmt.Errorf("oidc: key dir must be an absolute path (got %q) — a relative path is resolved against the service working directory", dir)
	}
	key, err := loadOrGenerateKey(dir)
	if err != nil {
		return err
	}
	ks.mu.Lock()
	ks.key = key
	ks.dir = dir
	ks.ready = true
	ks.mu.Unlock()
	log.Printf("oidc: key store moved to %s (kid=%s) — live, no restart", dir, key.KID)
	return nil
}

// Dir (B304) reports the directory the store is currently using, so the panel
// can show the RESOLVED path instead of only the value someone typed.
func (ks *KeyStore) Dir() string {
	if ks == nil {
		return ""
	}
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.dir
}

// KID (B304) reports the active key id ("" when no key is loaded), so the panel
// can show which keypair the JWKS endpoint is actually serving.
func (ks *KeyStore) KID() string {
	if ks == nil {
		return ""
	}
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if ks.key == nil {
		return ""
	}
	return string(ks.key.KID)
}


// Ready returns true once a keypair is loaded.
// While the keypair is generating (cold start on
// a new volume), the OIDC routes return 503.
//
// B270: nil-safe on purpose — a Service whose key
// store could not be created keeps Keys == nil so
// that the HTTP surface answers 503 instead of
// panicking (pre-B270 the process died at boot).
func (ks *KeyStore) Ready() bool {
	if ks == nil {
		return false
	}
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.ready
}

// ActiveKey returns the current SigningKey. Safe
// for concurrent use.
func (ks *KeyStore) ActiveKey() *SigningKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.key
}

// generateAndPersist creates a fresh RSA-2048
// keypair and writes the private key (PKCS#1 PEM)
// + public key (PKCS#8 PEM) to disk. RSA-2048 is
// the minimum for RS256 (per RFC 7518 sec 3.3).
//
// B304: returns the private key instead of mutating the store, so both the
// boot path and Reload share it (see loadOrGenerateKey).
func generateAndPersist(privPath, pubPath string) (*rsa.PrivateKey, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("rsa.GenerateKey: %w", err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	if err := os.WriteFile(privPath, privPEM, 0600); err != nil {
		return nil, fmt.Errorf("write private: %w", err)
	}
	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("marshal public: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	if err := os.WriteFile(pubPath, pubPEM, 0644); err != nil {
		return nil, fmt.Errorf("write public: %w", err)
	}
	return priv, nil
}

// loadPrivateKeyFromDisk reads the PKCS#1 private key from disk and returns it,
// refusing anything weaker than 2048 bits (RFC 7518 sec 3.3).
//
// B304: the B161.1 version assigned ks.key itself; the parse is now a pure
// function so Reload can validate a NEW directory without touching the running
// key when the file there is unusable.
func loadPrivateKeyFromDisk(privPath string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(privPath)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		return nil, fmt.Errorf("invalid PEM (want 'RSA PRIVATE KEY', got %q)", blockType(block))
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#1: %w", err)
	}
	if priv.N.BitLen() < 2048 {
		return nil, fmt.Errorf("refusing weak key: %d bits (RFC 7518 requires >= 2048)", priv.N.BitLen())
	}
	return priv, nil
}

// buildSigningKey derives the kid + JWK from the
// private key. The kid is the last 8 bytes of the
// modulus encoded as hex — short, stable, easy to
// read in headscale's logs. The JWK n + e are
// base64url-encoded (no padding), per RFC 7517.
func buildSigningKey(priv *rsa.PrivateKey) *SigningKey {
	modBytes := priv.N.Bytes()
	// Kid: last 8 bytes of the modulus as hex.
	tail := modBytes
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	kid := KeyID(fmt.Sprintf("%x", tail))
	// JWK n: modulus as base64url-no-pad.
	nB64 := base64.RawURLEncoding.EncodeToString(modBytes)
	// JWK e: 65537 as base64url-no-pad (always "AQAB"
	// for RSA keys, but we encode it dynamically in
	// case the operator generates a non-standard
	// key).
	eB64 := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.E)).Bytes())
	return &SigningKey{
		Private: priv,
		KID:     kid,
		JWK: map[string]string{
			"kty": "RSA",
			"use": "sig",
			"alg": "RS256",
			"kid": string(kid),
			"n":   nB64,
			"e":   eB64,
		},
	}
}

func blockType(b *pem.Block) string {
	if b == nil {
		return ""
	}
	return b.Type
}
