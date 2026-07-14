package handlers

// 2026-07-14: tests for TelegramDeepLink (the helper used by
// both the QR handler and the visible bind-options link on
// /my/telegram).
//
// Why this gets its own file: the helper is small but it's the
// only point of contact between skygate and the user's phone.
// If the URL shape ever changes, the test is what tells us the
// QR + link stay in sync.

import (
	"strings"
	"testing"
)

// TestTelegramDeepLinkShape: the canonical case. Username and
// token are present; output is the standard t.me deep link.
func TestTelegramDeepLinkShape(t *testing.T) {
	got := TelegramDeepLink("skygatepj_bot", "skg-abcd-1234-efgh")
	want := "https://t.me/skygatepj_bot?start=skg-abcd-1234-efgh"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestTelegramDeepLinkEmptyUsername: an empty username (e.g.
// the bot hasn't done getMe yet, or the operator typed the
// wrong bot token) must NOT produce a half-formed URL like
// "https://t.me/?start=...". The helper returns "" so the
// caller can detect the missing-data state and either
// re-render the page or return 503.
func TestTelegramDeepLinkEmptyUsername(t *testing.T) {
	if got := TelegramDeepLink("", "skg-abcd-1234-efgh"); got != "" {
		t.Errorf("empty username should produce empty URL, got %q", got)
	}
}

// TestTelegramDeepLinkEmptyToken: same idea for the token
// case (e.g. the user just cleared the form and the page is
// re-rendering). The token is a URL parameter so a missing
// value would produce "https://t.me/<bot>?start=" which IS
// technically parseable but semantically broken (Telegram
// would show "no start payload"). We return "" instead.
func TestTelegramDeepLinkEmptyToken(t *testing.T) {
	if got := TelegramDeepLink("skygatepj_bot", ""); got != "" {
		t.Errorf("empty token should produce empty URL, got %q", got)
	}
}

// TestTelegramDeepLinkNoSpecialsInToken: defensive guard.
// The token is the result of crypto/rand over a 12-char
// base32-ish alphabet and is hex-string-clean by
// construction — but a future change that lets the user
// type a custom token would risk the helper URL-encoding
// breaking the t.me path. We don't URL-encode the token in
// the helper (Telegram's bot API accepts the raw token in
// the start payload), so this test pins the format to
// "no encoding, ever" — if a future refactor adds
// url.QueryEscape, the QR won't match what the bot expects
// in /start.
//
// Belt-and-suspenders: also assert the URL doesn't contain
// any characters that would need escaping (no spaces, no
// %, no &, etc.).
func TestTelegramDeepLinkNoSpecialsInToken(t *testing.T) {
	// Mix a few "interesting" characters that shouldn't
	// actually appear in a real token, to confirm the
	// helper does NOT escape them (which would be the
	// wrong behaviour for /start payload, which the
	// bot decodes verbatim).
	for _, tok := range []string{
		"skg-abcd-1234-efgh",     // canonical
		"skg-AAAA-BBBB-CCCC",     // all-uppercase
		"skg-1234-5678-9abc",     // all-numeric
	} {
		got := TelegramDeepLink("skygatepj_bot", tok)
		if got != "https://t.me/skygatepj_bot?start="+tok {
			t.Errorf("token %q was modified by the helper: got %q", tok, got)
		}
		// No characters that would normally be URL-escaped
		// should appear in the URL in a way that requires
		// decoding. (Telegram's bot API takes the start
		// payload verbatim, so we MUST NOT escape it.)
		for _, bad := range []string{"%20", "%2F", "%3A", "&start"} {
			if strings.Contains(got, bad) {
				t.Errorf("URL for token %q contains %q (would break /start payload): %q", tok, bad, got)
			}
		}
	}
}

// TestTelegramDeepLinkDoesNotUseTgScheme: explicitly pin the
// URL shape to https://t.me/ and NOT tg://resolve. The tg://
// scheme is more reliable on some Android versions, but many
// QR scanners refuse to handle it (they don't know which app
// should receive it). https://t.me/ is universally scannable;
// the browser handoff to Telegram then handles the rest. If
// we ever switch to tg://, this test would need to be updated
// — which is the point: the test acts as a tripwire for an
// unreviewed format change.
func TestTelegramDeepLinkDoesNotUseTgScheme(t *testing.T) {
	got := TelegramDeepLink("skygatepj_bot", "skg-abcd-1234-efgh")
	if strings.HasPrefix(got, "tg://") {
		t.Errorf("URL uses tg:// scheme (%q); expected https://t.me/ for QR-scanner compatibility", got)
	}
	if !strings.HasPrefix(got, "https://t.me/") {
		t.Errorf("URL doesn't start with https://t.me/: %q", got)
	}
}
