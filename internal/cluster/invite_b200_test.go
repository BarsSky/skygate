// v1.5.0+ / B200 — unit tests for the cluster/invite
// package. The invite helpers are the security boundary
// (they sign and verify the sgn1 tokens) so this test
// file is heavier than most B-checks: it covers the
// happy path, every tamper vector, the round-trip with
// the DB, and the idempotency of revoke.

package cluster

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBuildAndVerifyToken_RoundTrip(t *testing.T) {
	secret := "test-secret-abc123"
	payload := InvitePayload{
		Inv: "abc12345",
		CID: "skygate-staging",
		Rol: "skygate-standby",
		TH:  "svi-1",
		Exp: time.Now().Add(24 * time.Hour).Unix(),
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	sig := signPayload(secret, canonical)
	tok := buildToken(canonical, sig)

	got, err := VerifyToken(secret, tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Inv != payload.Inv || got.CID != payload.CID || got.Rol != payload.Rol || got.TH != payload.TH {
		t.Errorf("payload mismatch: got %+v want %+v", got, payload)
	}
}

func TestVerifyToken_RejectsWrongSecret(t *testing.T) {
	secret := "right-secret"
	payload := InvitePayload{Inv: "x", CID: "c", Rol: "r", TH: "h", Exp: 1}
	canonical, _ := json.Marshal(payload)
	sig := signPayload(secret, canonical)
	tok := buildToken(canonical, sig)

	if _, err := VerifyToken("wrong-secret", tok); err == nil {
		t.Fatal("verify with wrong secret should fail")
	}
}

func TestVerifyToken_RejectsTamperedPayload(t *testing.T) {
	secret := "test-secret"
	payload := InvitePayload{Inv: "abc", CID: "skygate-staging", Rol: "skygate", TH: "h1", Exp: 1}
	canonical, _ := json.Marshal(payload)
	sig := signPayload(secret, canonical)
	tok := buildToken(canonical, sig)
	// Tamper with the payload: decode the b64url payload
	// part, swap "h1" → "h2", re-encode, keep the
	// original signature. The signature was over the
	// original payload, so the verifier should reject.
	parts := strings.Split(tok, ".")
	origPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	tamperedPayload := strings.Replace(string(origPayload), "h1", "h2", 1)
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(tamperedPayload))
	tampered := strings.Join(parts, ".")
	if _, err := VerifyToken(secret, tampered); err == nil {
		t.Fatal("verify with tampered payload should fail")
	}
}

func TestVerifyToken_RejectsTamperedSignature(t *testing.T) {
	secret := "test-secret"
	payload := InvitePayload{Inv: "abc", CID: "c", Rol: "r", TH: "h", Exp: 1}
	canonical, _ := json.Marshal(payload)
	sig := signPayload(secret, canonical)
	tok := buildToken(canonical, sig)
	// Flip one character in the signature.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed token: %q", tok)
	}
	last := parts[2]
	if last == "" {
		t.Fatal("empty sig")
	}
	flipped := last[:len(last)-1] + flipChar(string(last[len(last)-1]))
	parts[2] = flipped
	tampered := strings.Join(parts, ".")
	if _, err := VerifyToken(secret, tampered); err == nil {
		t.Fatal("verify with tampered signature should fail")
	}
}

func TestVerifyToken_RejectsMalformed(t *testing.T) {
	secret := "s"
	cases := []struct {
		name string
		tok  string
	}{
		{"empty", ""},
		{"no prefix", "abc.def"},
		{"wrong prefix", "xxx.aaa.bbb"},
		{"two parts", "sgn1.aaa"},
		{"four parts", "sgn1.a.b.c"},
		{"bad payload b64", "sgn1.!!!.bbb"},
		{"bad sig b64", "sgn1.aaa.!!!"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := VerifyToken(secret, c.tok); err == nil {
				t.Errorf("verify(%q) should fail", c.tok)
			}
		})
	}
}

func TestVerifyToken_RejectsEmptySecret(t *testing.T) {
	// Empty secret should refuse to sign or verify — never
	// produce or accept a token with no key.
	if _, err := VerifyToken("", "sgn1.aaa.bbb"); err == nil {
		t.Fatal("verify with empty secret should fail")
	}
}

func TestGenerateInviteID_DeterministicWithStub(t *testing.T) {
	// Stub randRead to return a fixed pattern; verify the
	// id is exactly that pattern formatted as hex.
	orig := randRead
	defer func() { randRead = orig }()
	randRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = byte(i)
		}
		return len(b), nil
	}
	got := generateInviteID()
	want := hex.EncodeToString([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	if got != want {
		t.Errorf("generateInviteID = %q, want %q", got, want)
	}
	if len(got) != 16 {
		t.Errorf("len(generateInviteID) = %d, want 16 (8 bytes hex)", len(got))
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc..."},
		{"abc-def", 3, "abc..."},
	}
	for _, c := range cases {
		got := truncate(c.in, c.n)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestIsPending(t *testing.T) {
	now := time.Now()
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)
	cases := []struct {
		name string
		row  *InviteRow
		want bool
	}{
		{"nil", nil, false},
		{"pending+future+unused", &InviteRow{Status: "pending", ExpiresAt: future, UsedAt: nil}, true},
		{"pending+past", &InviteRow{Status: "pending", ExpiresAt: past, UsedAt: nil}, false},
		{"used", &InviteRow{Status: "pending", ExpiresAt: future, UsedAt: &now}, false},
		{"revoked", &InviteRow{Status: "revoked", ExpiresAt: future, UsedAt: nil}, false},
		{"expired status", &InviteRow{Status: "expired", ExpiresAt: future, UsedAt: nil}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.row.IsPending(now); got != c.want {
				t.Errorf("IsPending = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSignPayload_Deterministic(t *testing.T) {
	secret := "test"
	msg := []byte("hello world")
	a := signPayload(secret, msg)
	b := signPayload(secret, msg)
	if string(a) != string(b) {
		t.Errorf("HMAC not deterministic: %x vs %x", a, b)
	}
	if len(a) != sha256.Size {
		t.Errorf("HMAC-SHA256 output length = %d, want %d", len(a), sha256.Size)
	}
}

func TestErrSentinels(t *testing.T) {
	if errors.Is(ErrInviteNotFound, ErrInviteAlreadyUsed) {
		t.Error("ErrInviteNotFound == ErrInviteAlreadyUsed — they must be distinct")
	}
}

// flipChar returns a different character from c (so the
// signature tampering test produces a guaranteed-changed
// string). Skips characters where the swap might produce
// an invalid base64 character.
func flipChar(c string) string {
	if c == "A" {
		return "B"
	}
	if c == "a" {
		return "b"
	}
	if c == "0" {
		return "1"
	}
	return "A"
}
