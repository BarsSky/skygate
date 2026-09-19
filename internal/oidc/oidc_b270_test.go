// B270 (2026-09-19) — a broken OIDC key store must not kill the process.
//
// Live case (native/systemd VM): the unit was 'active' with NOTHING listening
// and the journal ended with
//
//	oidc: SKYGATE_OIDC_ISSUER not set — OIDC routes will return 503 until configured
//	oidc: init failed: oidc: mkdir ./data/oidc-keys: mkdir ./data: permission denied
//
// NewService's error was fatal in main.go, so the process exited BEFORE
// binding its HTTP port — a side feature nobody had configured took the whole
// control plane down, and the self-updater could only report that no build
// ever answered on the health URL.
package oidc

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestB270_NewServiceDegradesOnUnwritableKeyDir pins the contract that
// replaced log.Fatalf: an unusable key dir yields a usable Service with a nil
// key store and NO error.
func TestB270_NewServiceDegradesOnUnwritableKeyDir(t *testing.T) {
	// A path whose PARENT is a regular file cannot be created on any OS.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	svc, err := NewService("", "cid", "secret", filepath.Join(blocker, "oidc-keys"), "", "jwt")
	if err != nil {
		t.Fatalf("NewService returned a fatal error (%v); B270 requires the process to keep running", err)
	}
	if svc == nil {
		t.Fatal("NewService returned nil service + nil error — the caller would nil-panic")
	}
	if svc.Keys != nil {
		t.Error("Keys must stay nil when the key store could not be created")
	}
	if svc.KeyStoreErr == "" {
		t.Error("KeyStoreErr must carry the reason so the route can explain the 503")
	}
}

// TestB270_NilKeyStoreDoesNotPanic: every touch point of the OIDC surface must
// survive Keys == nil. Pre-B270 Ready() dereferenced the receiver.
func TestB270_NilKeyStoreDoesNotPanic(t *testing.T) {
	var ks *KeyStore
	if ks.Ready() {
		t.Error("nil KeyStore must not be ready")
	}

	svc := &Service{KeyStoreErr: "mkdir ./data: permission denied"}
	// The signing paths must return an error, not a panic.
	if _, err := svc.signIDToken(IDTokenClaims{}); err == nil {
		t.Error("signIDToken must fail on a nil key store")
	}
	if _, err := svc.signAccessToken("iss", "sub", "aud", "scope", "", "", "", 0, 0); err == nil {
		t.Error("signAccessToken must fail on a nil key store")
	}
}

// TestB270_JWKSAnswers503WithReason: the route must degrade to 503 (never a
// panic, never a 200 with an empty key set — headscale would cache that).
func TestB270_JWKSAnswers503WithReason(t *testing.T) {
	svc := &Service{}
	rec := httptest.NewRecorder()
	svc.ServeJWKS(rec, httptest.NewRequest(http.MethodGet, "/oidc/jwks.json", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("JWKS status = %d, want 503 on a nil key store", rec.Code)
	}
}

// TestB270_HealthyKeyDirStillWorks guards the happy path against the
// degradation logic swallowing real success.
func TestB270_HealthyKeyDirStillWorks(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService("", "cid", "secret", dir, "", "jwt")
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if svc.Keys == nil || !svc.Keys.Ready() {
		t.Fatal("a writable key dir must still produce a ready key store")
	}
	if svc.KeyStoreErr != "" {
		t.Errorf("KeyStoreErr = %q, want empty on success", svc.KeyStoreErr)
	}
	rec := httptest.NewRecorder()
	svc.ServeJWKS(rec, httptest.NewRequest(http.MethodGet, "/oidc/jwks.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("JWKS status = %d, want 200 on a healthy key store", rec.Code)
	}
}
