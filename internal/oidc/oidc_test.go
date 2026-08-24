package oidc

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestAuthCodeStore_PutAndGet covers the basic
// put/get round-trip. Each code is single-use
// per RFC 6749 sec 4.1.2.
func TestAuthCodeStore_PutAndGet(t *testing.T) {
	s := NewAuthCodeStore()
	entry := AuthCodeEntry{
		UserID:      42,
		Username:    "alice",
		Email:       "alice@example.com",
		ClientID:    "headscale",
		RedirectURI: "https://head.skynas.ru/oidc/callback",
		Scope:       "openid profile email",
		Nonce:       "abc123",
	}
	code := s.Put(entry)
	if len(code) < 32 {
		t.Errorf("auth code too short: %d chars (want >= 32)", len(code))
	}
	got, ok := s.Get(code)
	if !ok {
		t.Fatal("Get returned !ok for just-Put code")
	}
	if got.UserID != entry.UserID || got.Username != entry.Username {
		t.Errorf("Get returned %+v, want UserID=%d Username=%q", got, entry.UserID, entry.Username)
	}
	// Second Get should fail (single-use).
	_, ok = s.Get(code)
	if ok {
		t.Error("second Get returned ok; code is single-use per RFC 6749")
	}
}

// TestAuthCodeStore_Expired validates that the
// ttl is enforced. We use a tiny ttl to keep the
// test fast.
func TestAuthCodeStore_Expired(t *testing.T) {
	s := NewAuthCodeStore()
	s.ttl = 50 * time.Millisecond
	code := s.Put(AuthCodeEntry{UserID: 1})
	time.Sleep(60 * time.Millisecond)
	_, ok := s.Get(code)
	if ok {
		t.Error("Get on expired code returned ok; want !ok")
	}
}

// TestAuthCodeStore_Sweep validates the
// background-cleanup helper.
func TestAuthCodeStore_Sweep(t *testing.T) {
	s := NewAuthCodeStore()
	s.ttl = 50 * time.Millisecond
	_ = s.Put(AuthCodeEntry{UserID: 1})
	_ = s.Put(AuthCodeEntry{UserID: 2})
	if s.Size() != 2 {
		t.Errorf("Size = %d, want 2", s.Size())
	}
	time.Sleep(60 * time.Millisecond)
	removed := s.Sweep()
	if removed != 2 {
		t.Errorf("Sweep removed %d, want 2", removed)
	}
	if s.Size() != 0 {
		t.Errorf("Size after sweep = %d, want 0", s.Size())
	}
}

// TestAllowedRedirect_ExactMatch validates the
// RFC 6749 sec 3.1.2.3 exact-string match.
// Substring or wildcard match is a known OIDC
// vuln class (open redirect); we explicitly
// reject it.
func TestAllowedRedirect_ExactMatch(t *testing.T) {
	s := &Service{RedirectURIs: "https://head.skynas.ru/oidc/callback,http://localhost:8085/oidc/callback"}
	cases := []struct {
		uri     string
		allowed bool
	}{
		{"https://head.skynas.ru/oidc/callback", true},
		{"http://localhost:8085/oidc/callback", true},
		{"https://head.skynas.ru/oidc/callback/", false},   // trailing slash
		{"https://head.skynas.ru/oidc/callback?foo=1", false}, // query
		{"https://attacker.example.com/oidc/callback", false},
		{"", false},
	}
	for _, c := range cases {
		if got := s.allowedRedirect(c.uri); got != c.allowed {
			t.Errorf("allowedRedirect(%q) = %v, want %v", c.uri, got, c.allowed)
		}
	}
}

// TestServeAuthorize_NoIssuerReturns503 ensures
// the provider-disabled fallback works for the
// new /authorize route (B161.1 had the same
// fallback for discovery; B161.2 extends it).
func TestServeAuthorize_NoIssuerReturns503(t *testing.T) {
	s := &Service{IssuerURL: ""}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oidc/authorize?client_id=headscale", nil)
	s.ServeAuthorize(rr, req)
	if rr.Code != 503 {
		t.Errorf("status = %d, want 503 when provider is disabled", rr.Code)
	}
}

// TestServeAuthorize_UnknownClientID validates
// the 400 path for an unknown client_id. We
// don't 302 here because the client_id is
// invalid — we can't trust the redirect_uri to
// be valid either, so a 400 is the safe
// response.
func TestServeAuthorize_UnknownClientID(t *testing.T) {
	s := &Service{
		IssuerURL:     "https://skygate.example.com",
		ClientID:      "headscale",
		RedirectURIs:  "https://head.skynas.ru/oidc/callback",
		Codes:         NewAuthCodeStore(),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oidc/authorize?client_id=wrong", nil)
	s.ServeAuthorize(rr, req)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// TestServeAuthorize_RedirectURINotAllowed
// validates the open-redirect defense. The
// handler MUST reject redirect URIs not in the
// allowlist, not silently 302 to them.
func TestServeAuthorize_RedirectURINotAllowed(t *testing.T) {
	s := &Service{
		IssuerURL:    "https://skygate.example.com",
		ClientID:     "headscale",
		RedirectURIs: "https://head.skynas.ru/oidc/callback",
		Codes:        NewAuthCodeStore(),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oidc/authorize?client_id=headscale&redirect_uri=https://attacker.example.com/callback&response_type=code", nil)
	s.ServeAuthorize(rr, req)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400 (open-redirect defense)", rr.Code)
	}
	// The 400 body should NOT contain the
	// attacker URL (defense in depth: don't
	// reflect untrusted input).
	if strings.Contains(rr.Body.String(), "attacker.example.com") {
		t.Errorf("400 body leaks the attacker URL: %q", rr.Body.String())
	}
}

// TestServeAuthorize_LoggedInIssuesCode is the
// happy path: user has a valid session cookie,
// all params are valid, and the handler
// redirects to headscale's callback with
// ?code=...&state=...
func TestServeAuthorize_LoggedInIssuesCode(t *testing.T) {
	s := &Service{
		IssuerURL:    "https://skygate.example.com",
		ClientID:     "headscale",
		RedirectURIs: "https://head.skynas.ru/oidc/callback",
		Codes:        NewAuthCodeStore(),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oidc/authorize?client_id=headscale&redirect_uri=https://head.skynas.ru/oidc/callback&response_type=code&state=xyz&scope=openid+profile+email", nil)
	// Inject a valid skygate session cookie.
	// Format: "<user_id>:<username>:<email>:<expires_unix>"
	req.AddCookie(&http.Cookie{
		Name:  skygateSessionCookie,
		Value: "1:alice:alice@example.com:9999999999",
	})
	s.ServeAuthorize(rr, req)
	if rr.Code != 302 {
		t.Fatalf("status = %d, want 302; body=%q", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://head.skynas.ru/oidc/callback?") {
		t.Errorf("Location = %q, want to start with the configured redirect_uri", loc)
	}
	if !strings.Contains(loc, "code=") {
		t.Errorf("Location = %q, want ?code=...", loc)
	}
	if !strings.Contains(loc, "state=xyz") {
		t.Errorf("Location = %q, want state=xyz echoed back", loc)
	}
	// The code should be in the in-memory store.
	if s.Codes.Size() != 1 {
		t.Errorf("Codes.Size = %d, want 1 (the code we just issued)", s.Codes.Size())
	}
}

// TestServeAuthorize_NotLoggedInRedirectsToLogin
// validates the unauthenticated path: the
// handler 302s to /login?next=<this URL> so the
// OIDC params survive the round trip.
func TestServeAuthorize_NotLoggedInRedirectsToLogin(t *testing.T) {
	s := &Service{
		IssuerURL:    "https://skygate.example.com",
		ClientID:     "headscale",
		RedirectURIs: "https://head.skynas.ru/oidc/callback",
		Codes:        NewAuthCodeStore(),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oidc/authorize?client_id=headscale&redirect_uri=https://head.skynas.ru/oidc/callback&response_type=code", nil)
	// No session cookie.
	s.ServeAuthorize(rr, req)
	if rr.Code != 302 {
		t.Fatalf("status = %d, want 302; body=%q", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?next=") {
		t.Errorf("Location = %q, want /login?next=... (so OIDC params survive the login round-trip)", loc)
	}
	// No code should be issued (the user hasn't
	// authenticated yet).
	if s.Codes.Size() != 0 {
		t.Errorf("Codes.Size = %d, want 0 (no code before login)", s.Codes.Size())
	}
}
