package db

import (
	"path/filepath"
	"testing"
)

func TestTelegramFingerprint(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"not-a-token", "?"},      // no colon
		{"123:abc", "123:?short"}, // secret < 4 chars
		{"1234567890:AGt34wtHxY", "1234567890:tHxY"}, // last 4 chars of "AGt34wtHxY"
		{"42:LKJHGFDSAqwerty", "42:erty"},
		// Real-shaped example: secret="AAGt34wtHYXB...secret" len=23, last 4 = "cret"
		{"123456789:AAGt34wtHYXB...secret", "123456789:cret"},
	}
	for _, c := range cases {
		got := TelegramFingerprint(c.in)
		if got != c.want {
			t.Errorf("TelegramFingerprint(%q) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestSaveLoadDeleteTelegramToken(t *testing.T) {
	d := openTestDB(t)

	// Initial: nothing configured.
	if _, _, ok, err := LoadTelegramToken(d); err != nil || ok {
		t.Errorf("expected ok=false err=nil on empty config; got ok=%v err=%v", ok, err)
	}

	// Save both atomically.
	if err := SaveTelegramToken(d, "1234567890:REAL_SECRET_VALUE", "12345"); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	token, chatID, ok, err := LoadTelegramToken(d)
	if err != nil || !ok {
		t.Fatalf("expected ok=true after save; got ok=%v err=%v", ok, err)
	}
	if token != "1234567890:REAL_SECRET_VALUE" {
		t.Errorf("token round-trip mismatch: %q", token)
	}
	if chatID != "12345" {
		t.Errorf("chatID round-trip mismatch: %q", chatID)
	}

	// Save only chat_id — token must persist.
	if err := SaveTelegramToken(d, "", "-1009876543210"); err != nil {
		t.Fatalf("SaveTelegramToken (chat-only): %v", err)
	}
	token2, chatID2, ok2, _ := LoadTelegramToken(d)
	if !ok2 || token2 != "1234567890:REAL_SECRET_VALUE" || chatID2 != "-1009876543210" {
		t.Errorf("chat-only save must preserve token: token=%q chat=%q", token2, chatID2)
	}

	// Save only token — chat must persist.
	if err := SaveTelegramToken(d, "999:NEW_SECRET", ""); err != nil {
		t.Fatalf("SaveTelegramToken (token-only): %v", err)
	}
	token3, chatID3, ok3, _ := LoadTelegramToken(d)
	if !ok3 || token3 != "999:NEW_SECRET" || chatID3 != "-1009876543210" {
		t.Errorf("token-only save must preserve chat: token=%q chat=%q", token3, chatID3)
	}

	// Save both empty must error.
	if err := SaveTelegramToken(d, "", ""); err == nil {
		t.Error("empty save must fail")
	}

	// Delete removes both.
	if err := DeleteTelegramToken(d); err != nil {
		t.Fatalf("DeleteTelegramToken: %v", err)
	}
	_, _, okAfter, _ := LoadTelegramToken(d)
	if okAfter {
		t.Error("ok must be false after DeleteTelegramToken")
	}

	// Idempotent delete.
	if err := DeleteTelegramToken(d); err != nil {
		t.Errorf("DeleteTelegramToken (idempotent): %v", err)
	}
}

func TestSaveTelegramToken_WhitespaceTrim(t *testing.T) {
	d := openTestDB(t)
	if err := SaveTelegramToken(d, "  1234567890:ABCDEF  ", "  42  "); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	token, chatID, _, _ := LoadTelegramToken(d)
	if token != "1234567890:ABCDEF" || chatID != "42" {
		t.Errorf("whitespace not trimmed: token=%q chat=%q", token, chatID)
	}
}

func TestRandomConfirmationToken(t *testing.T) {
	t1, err := RandomConfirmationToken(6)
	if err != nil || len(t1) != 12 {
		t.Errorf("RandomConfirmationToken(6) = %q (len %d), err=%v", t1, len(t1), err)
	}
	// Two consecutive tokens must differ.
	t2, _ := RandomConfirmationToken(6)
	if t1 == t2 {
		t.Error("two consecutive tokens must differ")
	}
	// n=0 must clamp to 1.
	short, _ := RandomConfirmationToken(0)
	if len(short) != 2 {
		t.Errorf("n=0 should clamp to 1 → 2 hex chars, got len=%d", len(short))
	}
	// Larger n must be bounded.
	big, _ := RandomConfirmationToken(64)
	if len(big) != 32 {
		t.Errorf("n>16 should clamp to 16 → 32 hex chars, got len=%d", len(big))
	}
}

func TestLoadTelegramToken_DBError(t *testing.T) {
	// Closed DB connection must surface as an error, not a silent ok=true.
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	d.Close()
	_, _, _, err = LoadTelegramToken(d)
	if err == nil {
		t.Error("expected error on closed DB")
	}
}

// helper for whatever reason we may want to verify fingerprint plumbing
// without going through the UI: confirm the fingerprint shortens the
// secret even when it is very small.
func TestTelegramFingerprint_RealShape(t *testing.T) {
	// Use a 6-char secret so the last-4 suffix is well-defined.
	in := "123456789:" + "AAAA" + "BB" // secret="AAAABB", last 4 = "AABB"
	fp := TelegramFingerprint(in)
	want := "123456789:AABB"
	if fp != want {
		t.Errorf("expected %q, got %q", want, fp)
	}
	if fp == in {
		t.Error("fingerprint leaked the full secret")
	}
}
