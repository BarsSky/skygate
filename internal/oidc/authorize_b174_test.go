package oidc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"skygate/internal/auth"
)

// TestReadSession_ParsesJWT (B174) is the regression
// guard for the pre-B174 production bug. Pre-B174 the
// OIDC readSession tried to parse the skygate_session
// cookie as "<uid>:<username>:<email>:<expires_unix>"
// (a colon-separated format that PostLogin NEVER
// wrote) and ALWAYS returned nil — which made the
// /oidc/authorize handler think the user was
// unauthenticated even right after a successful login,
// causing the "password is reset on login" loop the
// operator reported on 2026-08-25. B174 rewires
// readSession to use auth.ParseJWT, the same helper
// feature/auth uses, so the cookie is recognized.
//
// The 5 subtests below cover the 5 critical paths:
//
//   1. Valid JWT → skygateSession with the claims
//   2. Missing cookie → nil
//   3. Empty cookie → nil
//   4. Invalid JWT (bad signature) → nil
//   5. Expired JWT → nil
func TestReadSession_ParsesJWT(t *testing.T) {
	const secret = "b174-test-secret"
	s := &Service{JWTSecret: secret}
	// Stub the UserLookup so we can verify the
	// OIDC handler maps the JWT uid → DB username
	// + email correctly. B174 wired this through
	// main.go via db.GetUserNameByID.
	s.UserLookup = func(uid int64) (string, string, error) {
		if uid != 42 {
			t.Errorf("UserLookup: got uid=%d, want 42", uid)
		}
		return "alice", "alice@skygate.local", nil
	}

	// 1. Valid JWT — must produce a skygateSession
	//    with the JWT claims + the DB-side username
	//    + email. This is the path that was broken
	//    pre-B174.
	t.Run("ValidJWT", func(t *testing.T) {
		tok, err := auth.IssueJWT(secret, 42, "alice-from-jwt", false, 24)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		req := httptest.NewRequest("GET", "/oidc/authorize", nil)
		req.AddCookie(&http.Cookie{Name: "skygate_session", Value: tok})
		got := s.readSession(req)
		if got == nil {
			t.Fatal("readSession: got nil, want skygateSession (pre-B174 this was the production bug)")
		}
		if got.UserID != 42 {
			t.Errorf("UserID: got %d, want 42", got.UserID)
		}
		// Username comes from UserLookup (the DB),
		// NOT the JWT. The JWT's usr claim is only
		// used as a fallback when UserLookup is nil
		// (see the UserLookup-is-nil subtest below).
		if got.Username != "alice" {
			t.Errorf("Username: got %q, want %q (from UserLookup)", got.Username, "alice")
		}
		if got.Email != "alice@skygate.local" {
			t.Errorf("Email: got %q, want %q (from UserLookup)", got.Email, "alice@skygate.local")
		}
	})

	// 2. Missing cookie — must return nil.
	t.Run("NoCookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/oidc/authorize", nil)
		if got := s.readSession(req); got != nil {
			t.Errorf("readSession: got %+v, want nil (no cookie set)", got)
		}
	})

	// 3. Empty cookie — must return nil. The
	//    pre-B174 implementation would have tried
	//    to split "" by ":", got a 1-element
	//    slice, and returned nil (correctly), but
	//    only by accident. B174's auth.ParseJWT
	//    returns an error on an empty string and
	//    we return nil.
	t.Run("EmptyCookie", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/oidc/authorize", nil)
		req.AddCookie(&http.Cookie{Name: "skygate_session", Value: ""})
		if got := s.readSession(req); got != nil {
			t.Errorf("readSession: got %+v, want nil (empty cookie)", got)
		}
	})

	// 4. Invalid JWT (bad signature) — must return
	//    nil. Pre-B174 this was treated as an
	//    "obviously not a JWT, just split on :"
	//    and (for a string with the right number
	//    of colons) would have parsed a fake user
	//    identity from arbitrary text. B174
	//    delegates signature verification to
	//    auth.ParseJWT, which rejects bad
	//    signatures.
	t.Run("InvalidJWT_BadSignature", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/oidc/authorize", nil)
		req.AddCookie(&http.Cookie{Name: "skygate_session", Value: "not.a.valid.jwt.at.all"})
		if got := s.readSession(req); got != nil {
			t.Errorf("readSession: got %+v, want nil (invalid JWT must be rejected)", got)
		}
	})

	// 5. Expired JWT — must return nil. The
	//    operator could leave a browser tab open
	//    for days; the session cookie is in the
	//    browser's cookie jar with the original
	//    ExpiresAt, and a TLS restart picks it up
	//    again. auth.ParseJWT enforces the
	//    ExpiresAt claim and returns an error on
	//    expired tokens.
	t.Run("ExpiredJWT", func(t *testing.T) {
		tok, err := auth.IssueJWT(secret, 42, "alice", false, -1) // 1h ago expiry
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		req := httptest.NewRequest("GET", "/oidc/authorize", nil)
		req.AddCookie(&http.Cookie{Name: "skygate_session", Value: tok})
		if got := s.readSession(req); got != nil {
			t.Errorf("readSession: got %+v, want nil (expired JWT must be rejected)", got)
		}
	})

	// 6. UserLookup returns nil — the OIDC
	//    readSession should still work, falling
	//    back to the JWT's usr claim. This is
	//    the unit-test path (no DB) and matches
	//    what the e2e test does. Pre-B174 the
	//    email field would have been the JWT
	//    payload (whatever the password manager
	//    put in it), which was a security hole.
	t.Run("UserLookupNil_FallsBackToJWT", func(t *testing.T) {
		s2 := &Service{JWTSecret: secret} // no UserLookup
		tok, err := auth.IssueJWT(secret, 42, "alice", false, 24)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		req := httptest.NewRequest("GET", "/oidc/authorize", nil)
		req.AddCookie(&http.Cookie{Name: "skygate_session", Value: tok})
		got := s2.readSession(req)
		if got == nil {
			t.Fatal("readSession: got nil, want skygateSession (UserLookup-nil fallback path)")
		}
		if got.UserID != 42 {
			t.Errorf("UserID: got %d, want 42", got.UserID)
		}
		if got.Username != "alice" {
			t.Errorf("Username: got %q, want %q (from JWT usr claim)", got.Username, "alice")
		}
		// Email is left empty when UserLookup is
		// nil — the JWT doesn't carry it.
		if got.Email != "" {
			t.Errorf("Email: got %q, want empty (no UserLookup, no DB)", got.Email)
		}
	})

	// 7. UserLookup returns an error — the OIDC
	//    readSession should treat the user as
	//    unauthenticated (return nil) and let the
	//    handler redirect to /login. This matches
	//    the "user was deleted but the cookie
	//    still has a valid uid" edge case.
	t.Run("UserLookupError", func(t *testing.T) {
		s3 := &Service{JWTSecret: secret}
		s3.UserLookup = func(uid int64) (string, string, error) {
			return "", "", errUserLookupFailed
		}
		tok, err := auth.IssueJWT(secret, 42, "alice", false, 24)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		req := httptest.NewRequest("GET", "/oidc/authorize", nil)
		req.AddCookie(&http.Cookie{Name: "skygate_session", Value: tok})
		if got := s3.readSession(req); got != nil {
			t.Errorf("readSession: got %+v, want nil (UserLookup error must NOT leak the JWT user)", got)
		}
	})
}

// errUserLookupFailed is a sentinel for the
// UserLookupError subtest. We use a tiny error type
// (rather than errors.New) so the test doesn't
// depend on the import.
var errUserLookupFailed = &userLookupError{}

type userLookupError struct{}

func (e *userLookupError) Error() string { return "user lookup failed" }

// TestReadSession_PreB174FormatRejected is the
// focused regression guard. Pre-B174 the OIDC
// readSession tried to parse the cookie value as
// "<uid>:<username>:<email>:<expires_unix>" — a
// format PostLogin never wrote. This test pins
// that the pre-B174 format IS rejected by the
// B174 implementation (i.e. an attacker can't
// forge a session by setting a colon-separated
// cookie value).
func TestReadSession_PreB174FormatRejected(t *testing.T) {
	const secret = "b174-test-secret"
	s := &Service{JWTSecret: secret}
	req := httptest.NewRequest("GET", "/oidc/authorize", nil)
	// The exact format pre-B174 readSession
	// expected. auth.ParseJWT will reject it
	// (it doesn't have 3 dot-separated base64
	// segments) and readSession will return nil.
	req.AddCookie(&http.Cookie{Name: "skygate_session", Value: "42:alice:alice@example.com:9999999999"})
	if got := s.readSession(req); got != nil {
		t.Errorf("readSession: pre-B174 cookie format must be rejected (got %+v, want nil — an attacker could otherwise forge a session by setting a colon-separated cookie)", got)
	}
}
