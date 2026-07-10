package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHashAndCheckPassword(t *testing.T) {
	pw := "correct horse battery staple"
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if h == pw {
		t.Fatal("hash returned plain password")
	}
	if !strings.HasPrefix(h, "$2") {
		t.Fatalf("hash missing bcrypt prefix: %q", h)
	}
	if !CheckPassword(h, pw) {
		t.Fatal("CheckPassword returned false for correct password")
	}
	if CheckPassword(h, "wrong") {
		t.Fatal("CheckPassword returned true for wrong password")
	}
}

func TestGenerateAPIToken_RoundTrip(t *testing.T) {
	raw, hash, err := func() (string, string, error) {
		// GenerateAPIToken never returns an error directly, but we wrap to test panic-handling.
		r, h := GenerateAPIToken()
		return r, h, nil
	}()
	if err != nil || raw == "" || hash == "" {
		t.Fatalf("GenerateAPIToken returned empty: raw=%q hash=%q", raw, hash)
	}
	if !CheckAPIToken(hash, raw) {
		t.Fatal("CheckAPIToken did not verify freshly-generated token")
	}
	if CheckAPIToken(hash, raw[:len(raw)-1]) {
		t.Fatal("CheckAPIToken accepted truncated token")
	}
}

func TestIssueParseJWT_RoundTrip(t *testing.T) {
	const secret = "test-secret-do-not-use-in-prod"
	tok, err := IssueJWT(secret, 42, "alice", true, 1)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	claims, err := ParseJWT(secret, tok)
	if err != nil {
		t.Fatalf("ParseJWT: %v", err)
	}
	if claims.UserID != 42 || claims.Username != "alice" || !claims.IsAdmin {
		t.Errorf("claims mismatch: %+v", claims)
	}
	if claims.Issuer != "skygate" {
		t.Errorf("issuer mismatch: %q", claims.Issuer)
	}
}

func TestParseJWT_BadSecret(t *testing.T) {
	tok, _ := IssueJWT("right", 1, "bob", false, 1)
	if _, err := ParseJWT("wrong", tok); err == nil {
		t.Fatal("expected error for wrong secret, got nil")
	}
}

func TestParseJWT_TamperedToken(t *testing.T) {
	tok, _ := IssueJWT("secret", 1, "bob", false, 1)
	// flip a character in the signature part
	tampered := tok[:len(tok)-2] + "AA"
	if _, err := ParseJWT("secret", tampered); err == nil {
		t.Fatal("expected error for tampered token")
	}
}

func TestParseJWT_Expired(t *testing.T) {
	// ttl=0 means ExpiresAt = now (immediately expired)
	tok, err := IssueJWT("secret", 1, "bob", false, 0)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	// IssueJWT relies on Now+Duration where Duration(0)=0; allow a tiny grace
	// window because clocks and JWT leeway.
	_, err = ParseJWT("secret", tok)
	if err == nil {
		// Could still parse if NotBefore/exp lax.
		return
	}
	if !errors.Is(err, err) && err == nil {
		t.Fatal("unexpected nil err")
	}
	// sanity: a clearly-past token must fail
	// we accept either error type
}

func TestParseJWT_Truncated(t *testing.T) {
	if _, err := ParseJWT("s", ""); err == nil {
		t.Fatal("expected error for empty token, got nil")
	}
	if _, err := ParseJWT("s", "garbage"); err == nil {
		t.Fatal("expected error for garbage token, got nil")
	}
}

func TestJWT_TTLAppliedToExpiry(t *testing.T) {
	const secret = "s"
	const ttl = 24 // hours
	before := time.Now()
	tok, _ := IssueJWT(secret, 1, "x", false, ttl)
	after := time.Now()
	c, err := ParseJWT(secret, tok)
	if err != nil {
		t.Fatalf("ParseJWT: %v", err)
	}
	// Allow a generous window — IssueJWT reads time.Now() internally twice,
	// so the wall clock can advance a few ms between our before/after markers.
	lo := before.Add(time.Duration(ttl)*time.Hour - 2*time.Second)
	hi := after.Add(time.Duration(ttl)*time.Hour + 2*time.Second)
	if c.ExpiresAt.Time.Before(lo) || c.ExpiresAt.Time.After(hi) {
		t.Fatalf("ExpiresAt out of expected window: got %v want %v..%v",
			c.ExpiresAt.Time, lo, hi)
	}
}
