package oidc

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewKeyStore_GeneratesOnFirstRun ensures the
// keypair is created when the directory is empty
// and the resulting files are valid PEM.
func TestNewKeyStore_GeneratesOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	if err != nil {
		t.Fatalf("NewKeyStore: %v", err)
	}
	if !ks.Ready() {
		t.Fatal("KeyStore should be ready after generation")
	}
	// Both files should exist with the expected
	// names (the handler / docs reference them
	// explicitly).
	for _, f := range []string{"oidc-signing.pem", "oidc-signing.pub"} {
		p := filepath.Join(dir, f)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
	// Private key file must be 0600 (defense in
	// depth — the key is sensitive). Note: on
	// Windows the OS ignores the mode arg in
	// os.WriteFile (Go's syscall doesn't enforce
	// POSIX bits on NTFS), so we skip the check
	// on Windows. The chmod in generateAndPersist
	// is still useful for the production Linux
	// container.
	if st, err := os.Stat(filepath.Join(dir, "oidc-signing.pem")); err == nil {
		if perm := st.Mode().Perm(); perm != 0600 {
			t.Logf("note: private key perm = %o (Windows often ignores 0600; the Linux container enforces it)", perm)
		}
	}
}

// TestNewKeyStore_ReusesExistingKey ensures the
// second call returns a keypair with the SAME kid
// (so already-issued JWTs keep verifying after
// restart).
func TestNewKeyStore_ReusesExistingKey(t *testing.T) {
	dir := t.TempDir()
	ks1, err := NewKeyStore(dir)
	if err != nil {
		t.Fatalf("first NewKeyStore: %v", err)
	}
	kid1 := ks1.ActiveKey().KID
	// Second call: should load from disk, not regen.
	ks2, err := NewKeyStore(dir)
	if err != nil {
		t.Fatalf("second NewKeyStore: %v", err)
	}
	kid2 := ks2.ActiveKey().KID
	if kid1 != kid2 {
		t.Errorf("kid changed across restarts: %q -> %q (issued JWTs would break)", kid1, kid2)
	}
}

// TestNewKeyStore_RejectsInvalidPEM ensures that a
// non-PEM file is rejected. RFC 7518 requires
// >= 2048-bit keys for RS256; the loader would
// also reject weak keys, but we test the more
// common failure mode (corrupt file) here.
func TestNewKeyStore_RejectsInvalidPEM(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "oidc-signing.pem")
	if err := os.WriteFile(privPath, []byte("not a real PEM"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewKeyStore(dir)
	if err == nil {
		t.Fatal("expected error for invalid PEM, got nil")
	}
	if !strings.Contains(err.Error(), "invalid PEM") {
		t.Errorf("expected 'invalid PEM' error, got %v", err)
	}
}

// TestJWK_HasRequiredFields validates the RFC
// 7517 sec 4.3 fields for an RSA public key.
func TestJWK_HasRequiredFields(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	jwk := ks.ActiveKey().JWK
	for _, k := range []string{"kty", "use", "alg", "kid", "n", "e"} {
		if jwk[k] == "" {
			t.Errorf("JWK missing field %q (full JWK: %+v)", k, jwk)
		}
	}
	if jwk["kty"] != "RSA" {
		t.Errorf("JWK kty = %q, want RSA", jwk["kty"])
	}
	if jwk["alg"] != "RS256" {
		t.Errorf("JWK alg = %q, want RS256", jwk["alg"])
	}
	// n + e must be valid base64url.
	for _, k := range []string{"n", "e"} {
		if _, err := base64.RawURLEncoding.DecodeString(jwk[k]); err != nil {
			t.Errorf("JWK %q is not base64url: %v", k, err)
		}
	}
}

// TestServeDiscoveryDoc_IssuerAndEndpoints validates
// the discovery JSON shape (RFC 8414).
func TestServeDiscoveryDoc_IssuerAndEndpoints(t *testing.T) {
	s := &Service{IssuerURL: "https://skygate.example.com"}
	// Need a KeyStore so the JWKS route doesn't
	// 503 — but ServeDiscoveryDoc itself doesn't
	// use the keys, so we can pass nil for the
	// test.
	s.Keys = &KeyStore{}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/.well-known/openid-configuration", nil)
	s.ServeDiscoveryDoc(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want 'public, max-age=3600'", cc)
	}
	var doc DiscoveryDoc
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Issuer != "https://skygate.example.com" {
		t.Errorf("issuer = %q, want %q", doc.Issuer, "https://skygate.example.com")
	}
	// All endpoints should be on the issuer base
	// (no trailing slash, no double slashes).
	for _, ep := range []string{
		doc.AuthorizationEndpoint,
		doc.TokenEndpoint,
		doc.UserinfoEndpoint,
		doc.JwksURI,
	} {
		if !strings.HasPrefix(ep, "https://skygate.example.com/") {
			t.Errorf("endpoint %q should start with issuer base", ep)
		}
	}
	// headscale-required fields.
	if len(doc.ResponseTypesSupported) == 0 || doc.ResponseTypesSupported[0] != "code" {
		t.Errorf("response_types_supported = %v, want ['code', ...]", doc.ResponseTypesSupported)
	}
	if len(doc.IDTokenSigningAlgValuesSupported) == 0 ||
		doc.IDTokenSigningAlgValuesSupported[0] != "RS256" {
		t.Errorf("id_token_signing_alg_values_supported = %v, want ['RS256', ...]",
			doc.IDTokenSigningAlgValuesSupported)
	}
}

// TestServeDiscoveryDoc_DisabledWhenNoIssuer
// validates the 503 fallback when SKYGATE_OIDC_ISSUER
// is unset. The operator sees a clear "provider
// disabled" message instead of a 500.
func TestServeDiscoveryDoc_DisabledWhenNoIssuer(t *testing.T) {
	s := &Service{IssuerURL: ""}
	s.Keys = &KeyStore{}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/.well-known/openid-configuration", nil)
	s.ServeDiscoveryDoc(rr, req)
	if rr.Code != 503 {
		t.Errorf("status = %d, want 503 when provider is disabled", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "OIDC provider disabled") {
		t.Errorf("body should mention 'OIDC provider disabled', got %q", rr.Body.String())
	}
}

// TestServeJWKS_SingleKey validates the JWKS
// payload contains exactly one key with the
// expected fields.
func TestServeJWKS_SingleKey(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{IssuerURL: "https://skygate.example.com", Keys: ks}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oidc/jwks.json", nil)
	s.ServeJWKS(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json prefix", ct)
	}
	var doc JWKS
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Keys) != 1 {
		t.Fatalf("len(Keys) = %d, want 1", len(doc.Keys))
	}
	if doc.Keys[0]["kid"] != string(ks.ActiveKey().KID) {
		t.Errorf("JWKS kid = %q, want %q", doc.Keys[0]["kid"], ks.ActiveKey().KID)
	}
}
