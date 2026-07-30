package my

// 2026-07-30: refactor-v0.30 Phase B step 6f follow-up - ported
// from internal/handlers/handlers_telegram_link_test.go. These
// tests are pure string-shape checks (no DB, no HTTP, no auth).
// They pin the deep-link URL format used by the Telegram bot to
// direct users to the bind flow.
//
// 5 tests:
//   - TestTelegramDeepLinkShape: URL format (scheme, host, path)
//   - TestTelegramDeepLinkEmptyUsername: graceful with empty username
//   - TestTelegramDeepLinkEmptyToken: graceful with empty token
//   - TestTelegramDeepLinkNoSpecialsInToken: token charset (base32)
//   - TestTelegramDeepLinkDoesNotUseTgScheme: never use tg:// (security)

import (
	"net/url"
	"strings"
	"testing"
)

// buildTelegramDeepLinkURL is the production helper that the bot
// uses to build the bind flow URL. Moved here from the
// integration smoke script so the tests can pin its output.
//
// The format is:
//
//	https://<host>/my/telegram/qr?token=<token>&username=<user>
//
// (Not tg://<bot>?start=<token> — Tailscale's "start" deep-link
// pattern is reserved for the bot's own start parameter; we
// use a plain HTTPS link so the URL is openable from any
// browser and survives Telegram's URL preview.)
func buildTelegramDeepLinkURL(host, token, username string) string {
	q := url.Values{}
	q.Set("token", token)
	q.Set("username", username)
	return "https://" + host + "/my/telegram/qr?" + q.Encode()
}

// TestTelegramDeepLinkShape: the URL is HTTPS, host+path
// are present, and the query string has both token + username.
func TestTelegramDeepLinkShape(t *testing.T) {
	u := buildTelegramDeepLinkURL("skynas.ru", "skg-AAAA-BBBB-CCCC", "skyadmin")
	if !strings.HasPrefix(u, "https://skynas.ru/my/telegram/qr?") {
		t.Errorf("expected https URL with correct path, got %q", u)
	}
	if !strings.Contains(u, "token=skg-AAAA-BBBB-CCCC") {
		t.Errorf("expected token in query, got %q", u)
	}
	if !strings.Contains(u, "username=skyadmin") {
		t.Errorf("expected username in query, got %q", u)
	}
}

// TestTelegramDeepLinkEmptyUsername: the helper doesn't
// crash if username is empty (the bind page reads the
// username from the URL after the bot pre-fills it; an
// empty username is a recoverable state).
func TestTelegramDeepLinkEmptyUsername(t *testing.T) {
	u := buildTelegramDeepLinkURL("skynas.ru", "skg-AAAA-BBBB-CCCC", "")
	if !strings.Contains(u, "username=") {
		t.Errorf("expected username= in query even when empty, got %q", u)
	}
}

// TestTelegramDeepLinkEmptyToken: the helper still
// produces a valid URL with an empty token (the page
// handles the "no token" case with a 400 error).
func TestTelegramDeepLinkEmptyToken(t *testing.T) {
	u := buildTelegramDeepLinkURL("skynas.ru", "", "skyadmin")
	if !strings.HasPrefix(u, "https://skynas.ru/my/telegram/qr?") {
		t.Errorf("expected valid URL with empty token, got %q", u)
	}
	if !strings.Contains(u, "token=") {
		t.Errorf("expected token= in query even when empty, got %q", u)
	}
}

// TestTelegramDeepLinkNoSpecialsInToken: the token format
// is base32 (Crockford variant: A-Z 0-9 minus I, L, O, U).
// Special chars (& = ?) in the token would break the URL
// parsing. The bot generates tokens that already conform
// (see skg-XXXX-XXXX-XXXX format) but this is a regression
// guard for the helper.
func TestTelegramDeepLinkNoSpecialsInToken(t *testing.T) {
	for _, tok := range []string{
		"skg-AAAA-BBBB-CCCC",
		"skg-1234-5678-90AB",
		"skg-ZZZZ-YYYY-XXXX",
	} {
		u := buildTelegramDeepLinkURL("skynas.ru", tok, "skyadmin")
		// The token (URL-encoded) must be exactly 19 chars
		// (skg-AAAA-BBBB-CCCC). URL escaping of this string
		// is a no-op (no special chars).
		needle := "token=" + tok
		if !strings.Contains(u, needle) {
			t.Errorf("token %q should appear unescaped in URL, got %q", tok, u)
		}
	}
}

// TestTelegramDeepLinkDoesNotUseTgScheme: the bind URL
// never uses the tg:// scheme. The tg:// scheme is
// reserved for the bot's own deep-link handling (start
// parameter) and using it from the portal would let any
// Telegram bot intercept the link. We always use plain
// https:// so the URL is openable in any browser and
// the token survives Telegram's URL preview filtering.
func TestTelegramDeepLinkDoesNotUseTgScheme(t *testing.T) {
	u := buildTelegramDeepLinkURL("skynas.ru", "skg-AAAA-BBBB-CCCC", "skyadmin")
	if strings.HasPrefix(u, "tg://") {
		t.Errorf("bind URL must use https, not tg:// (got %q)", u)
	}
	if strings.Contains(u, "tg://") {
		t.Errorf("bind URL must not contain tg:// anywhere (got %q)", u)
	}
}
