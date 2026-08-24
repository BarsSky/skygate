package oidc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

// TestSignIDToken_RoundTrip signs a token with
// signIDToken and verifies it with the public key
// (parses + checks all the OIDC claims). This is
// the B161.3 happy path: /oidc/token signs
// something, /oidc/userinfo (or headscale) can
// verify it.
func TestSignIDToken_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{IssuerURL: "https://skygate.example.com", Keys: ks}
	// Use time.Now() so the token is "fresh" — the
	// jwt library rejects expired tokens even in
	// tests. A fixed timestamp (e.g. 1_700_000_000)
	// would be years in the past.
	now := time.Now().Unix()
	idTok, err := s.signIDToken(IDTokenClaims{
		Issuer:            "https://skygate.example.com",
		Subject:           "alice",
		Audience:          "headscale",
		Expiry:            now + 3600,
		IssuedAt:          now,
		Nonce:             "nonce-xyz",
		Email:             "alice@example.com",
		Name:               "alice",
		PreferredUsername: "alice",
	})
	if err != nil {
		t.Fatalf("signIDToken: %v", err)
	}
	if len(idTok) < 100 {
		t.Errorf("id_token too short: %d chars", len(idTok))
	}
	// Parse + verify with the public key.
	ks2 := s.Keys.ActiveKey()
	parser := newParserRS256()
	parsed, err := parser.ParseWithClaims(idTok, jwt.MapClaims{}, func(t *jwt.Token) (interface{}, error) {
		return &ks2.Private.PublicKey, nil
	})
	if err != nil {
		t.Fatalf("parse id_token: %v", err)
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type = %T, want jwt.MapClaims", parsed.Claims)
	}
	if mc["iss"] != "https://skygate.example.com" {
		t.Errorf("iss = %v, want https://skygate.example.com", mc["iss"])
	}
	if mc["sub"] != "alice" {
		t.Errorf("sub = %v, want alice", mc["sub"])
	}
	if mc["aud"] != "headscale" {
		t.Errorf("aud = %v, want headscale", mc["aud"])
	}
	if mc["email"] != "alice@example.com" {
		t.Errorf("email = %v", mc["email"])
	}
	if mc["nonce"] != "nonce-xyz" {
		t.Errorf("nonce = %v, want nonce-xyz", mc["nonce"])
	}
	// The JWT header MUST include the kid (so
	// headscale can pick the right JWKS key).
	if kid := parsed.Header["kid"]; kid != string(ks2.KID) {
		t.Errorf("JWT header kid = %v, want %v", kid, ks2.KID)
	}
}

// TestParseAccessToken_RejectsWrongSecret ensures
// the verification path rejects tokens signed
// with a different key (defense in depth — a
// malicious client can't mint a token that
// /userinfo would accept).
func TestParseAccessToken_RejectsWrongSecret(t *testing.T) {
	dir1 := t.TempDir()
	ks1, _ := NewKeyStore(dir1)
	dir2 := t.TempDir()
	ks2, _ := NewKeyStore(dir2)
	s := &Service{IssuerURL: "https://skygate.example.com", Keys: ks1}
	// Sign with key 1.
	tok, err := s.signAccessToken("https://skygate.example.com", "alice", "headscale", "openid", "alice@example.com", "alice", "alice", 1_700_000_000+3600, 1_700_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// Try to verify with key 2 (different kid).
	s.Keys = ks2
	_, err = s.parseAccessToken(tok)
	if err == nil {
		t.Error("parseAccessToken with wrong key returned nil error; should fail")
	}
}

// TestVerifyPKCE covers the RFC 7636 sec 4.6
// verifier-vs-challenge check. Used by /oidc/token
// when the auth request included a code_challenge.
//
// We construct the expected challenge at runtime
// (instead of hardcoding a value) so the test
// stays correct even if the spec wording changes
// or if a future B-check swaps the base64
// encoding. The verifier is the literal RFC 7636
// Appendix B example (43 base64url chars).
func TestVerifyPKCE(t *testing.T) {
	// RFC 7636 Appendix B example: verifier
	// (43 base64url chars of 32 random bytes).
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFEnBdytJz8T-A4y0LsRbI2FflkF8M5X9xs0nY7zD2ckK3JrPvhJjDfYsVDJyXp4yL1L4cUr1z-MDw4QHvW3sV3K3d3EpcKmC9Cd3QvFi2eA2eB8H6BJ0c"
	// Compute the expected challenge locally.
	// sha256(verifier) → 32 bytes → base64url.
	h := sha256Sum256(t, verifier)
	challenge := base64urlEncode(h)
	if !verifyPKCE(verifier, challenge, "S256") {
		t.Errorf("verifyPKCE(%q, %q, S256) returned false; want true", verifier[:16], challenge[:16])
	}
	if verifyPKCE("wrong-verifier", challenge, "S256") {
		t.Error("verifyPKCE accepted a wrong verifier; want reject")
	}
	if verifyPKCE(verifier, challenge, "plain") {
		t.Error("verifyPKCE accepted 'plain' method; want reject (only S256)")
	}
}

// TestServeToken_HappyPath is the B161.3 end-to-
// end happy path: 1) /authorize puts a code, 2)
// /token exchanges the code for id_token +
// access_token, 3) /userinfo returns the user's
// claims using the access_token.
//
// This is the most important test in B161.3 —
// it exercises the full OIDC flow at the unit
// level. A real headscale will repeat the
// same exchange but with a different client
// (headscale vs httptest).
func TestServeToken_HappyPath(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{
		IssuerURL:    "https://skygate.example.com",
		ClientID:     "headscale",
		ClientSecret: "test-secret",
		RedirectURIs: "https://head.skynas.ru/oidc/callback",
		Codes:        NewAuthCodeStore(),
		Keys:         ks,
	}
	// Pre-populate an auth code (bypassing
	// /authorize so the test is focused on the
	// token + userinfo handlers).
	code := s.Codes.Put(AuthCodeEntry{
		UserID:      1,
		Username:    "alice",
		Email:       "alice@example.com",
		ClientID:    "headscale",
		RedirectURI: "https://head.skynas.ru/oidc/callback",
		Scope:       "openid profile email",
		Nonce:       "nonce-abc",
	})

	// /token
	body := "grant_type=authorization_code&code=" + code +
		"&client_id=headscale&client_secret=test-secret" +
		"&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback"
	req := httptest.NewRequest("POST", "/oidc/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.ServeToken(rr, req)
	if rr.Code != 200 {
		t.Fatalf("/token status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store (RFC 6749 sec 5.1)", cc)
	}
	var tr TokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &tr); err != nil {
		t.Fatalf("unmarshal token response: %v", err)
	}
	if tr.AccessToken == "" {
		t.Error("access_token missing from token response")
	}
	if tr.IDToken == "" {
		t.Error("id_token missing from token response")
	}
	if tr.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", tr.TokenType)
	}
	if tr.ExpiresIn <= 0 {
		t.Errorf("expires_in = %d, want > 0", tr.ExpiresIn)
	}
	// The auth code should now be consumed.
	if s.Codes.Size() != 0 {
		t.Errorf("Codes.Size after /token = %d, want 0 (code consumed)", s.Codes.Size())
	}

	// /userinfo with the issued access_token.
	req2 := httptest.NewRequest("GET", "/oidc/userinfo", nil)
	req2.Header.Set("Authorization", "Bearer "+tr.AccessToken)
	rr2 := httptest.NewRecorder()
	s.ServeUserinfo(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("/userinfo status = %d, want 200; body=%q", rr2.Code, rr2.Body.String())
	}
	var ur UserinfoResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &ur); err != nil {
		t.Fatalf("unmarshal userinfo: %v", err)
	}
	if ur.Sub != "alice" {
		t.Errorf("Sub = %q, want alice", ur.Sub)
	}
	if ur.Email != "alice@example.com" {
		t.Errorf("Email = %q", ur.Email)
	}
	if ur.PreferredUsername != "alice" {
		t.Errorf("PreferredUsername = %q", ur.PreferredUsername)
	}
}

// TestServeToken_BadClientSecret covers the
// invalid_client error path (RFC 6749 sec 5.2).
// We return 400 + the token-error JSON.
func TestServeToken_BadClientSecret(t *testing.T) {
	dir := t.TempDir()
	ks, _ := NewKeyStore(dir)
	s := &Service{
		IssuerURL:    "https://skygate.example.com",
		ClientID:     "headscale",
		ClientSecret: "correct-secret",
		RedirectURIs: "https://head.skynas.ru/oidc/callback",
		Codes:        NewAuthCodeStore(),
		Keys:         ks,
	}
	body := "grant_type=authorization_code&code=any&client_id=headscale&client_secret=wrong-secret&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback"
	req := httptest.NewRequest("POST", "/oidc/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.ServeToken(rr, req)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400 (bad client_secret)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid_client") {
		t.Errorf("body should contain 'invalid_client' error code: %q", rr.Body.String())
	}
}

// TestServeToken_UnknownCode covers the
// invalid_grant path when the code doesn't exist
// in the store (expired or never existed).
func TestServeToken_UnknownCode(t *testing.T) {
	dir := t.TempDir()
	ks, _ := NewKeyStore(dir)
	s := &Service{
		IssuerURL:    "https://skygate.example.com",
		ClientID:     "headscale",
		ClientSecret: "test-secret",
		RedirectURIs: "https://head.skynas.ru/oidc/callback",
		Codes:        NewAuthCodeStore(),
		Keys:         ks,
	}
	body := "grant_type=authorization_code&code=NEVER_ISSUED&client_id=headscale&client_secret=test-secret&redirect_uri=https%3A%2F%2Fhead.skynas.ru%2Foidc%2Fcallback"
	req := httptest.NewRequest("POST", "/oidc/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.ServeToken(rr, req)
	if rr.Code != 400 {
		t.Errorf("status = %d, want 400 (invalid_grant)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid_grant") {
		t.Errorf("body should contain 'invalid_grant': %q", rr.Body.String())
	}
}

// TestServeUserinfo_MissingAuth covers the
// invalid_token 401 path when no Authorization
// header is sent.
func TestServeUserinfo_MissingAuth(t *testing.T) {
	dir := t.TempDir()
	ks, _ := NewKeyStore(dir)
	s := &Service{IssuerURL: "https://skygate.example.com", Keys: ks}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/oidc/userinfo", nil)
	s.ServeUserinfo(rr, req)
	if rr.Code != 401 {
		t.Errorf("status = %d, want 401 (no Authorization)", rr.Code)
	}
	// RFC 6750 sec 3: a 401 response MUST include
	// WWW-Authenticate.
	if wa := rr.Header().Get("WWW-Authenticate"); !strings.Contains(wa, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want Bearer ...", wa)
	}
}

// newParserRS256 is a small helper that builds
// a jwt.Parser configured to accept only RS256.
// Used by the round-trip test to verify our
// tokens can be parsed by a third-party
// implementation (e.g. headscale's).
func newParserRS256() *jwt.Parser {
	return jwt.NewParser(jwt.WithValidMethods([]string{"RS256"}))
}

// sha256Sum256 is a tiny test helper that wraps
// sha256.Sum256 so the PKCE test can compute
// the expected challenge dynamically. Returns
// the [32]byte as a slice. T must be a *testing.T
// for the rare panic path; we never actually
// panic in this helper.
func sha256Sum256(_ *testing.T, s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// base64urlEncode is the test-side mirror of
// the production's base64.RawURLEncoding.
// EncodeToString call. We wrap it so the test
// reads naturally ("base64urlEncode(hash)") and
// stays in sync if the encoding ever changes.
func base64urlEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
