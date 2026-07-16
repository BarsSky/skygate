package i18n

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestCatalogsParity — every key in ruCatalog must exist in enCatalog
// and vice versa, otherwise pages show mixed-language strings.
func TestCatalogsParity(t *testing.T) {
	for k := range ruCatalog {
		if _, ok := enCatalog[k]; !ok {
			t.Errorf("enCatalog missing key: %q", k)
		}
	}
	for k := range enCatalog {
		if _, ok := ruCatalog[k]; !ok {
			t.Errorf("ruCatalog missing key: %q", k)
		}
	}
}

// TestHTMLSafeCatalog — every bot.* key whose reply is sent in
// parse_mode=HTML must not contain a literal `<` or `>` that isn't
// part of a known HTML tag. Telegram rejects the whole sendMessage
// payload with HTTP 400 "can't parse entities: Unsupported start
// tag" if the message contains a `<word>` (or `</word>`) that isn't
// a valid HTML entity.
//
// 2026-07-16: v0.16.4 — added after the v0.16.3 release shipped
// HTML formatting for /help and Telegram rejected the /help reply
// with HTTP 400 because the bot.help.subtitle key had a literal
// "<команда>" placeholder (Cyrillic word in angle brackets). The
// fix was to HTML-escape the `<` to `&lt;`; this test pins that
// for every bot.* key that goes through parse_mode=HTML, so the
// regression is caught at unit-test time instead of in production.
//
// Keys covered (10 prefixes):
//   bot.help.*, bot.exit_nodes_health.*, bot.audit.*, bot.version.*,
//   bot.my_status.*, bot.my_nodes.*, bot.my_rules.*, bot.my_quota.*,
//   bot.myexitnodes.*, bot.add_device.*
//
// NOT covered (plain text):
//   bot.welcome.*, bot.start.*, bot.strict_locked.*, bot.unbind_self.*,
//   bot.help_detail.*, bot.ack.* (no markHTMLReply())
func TestHTMLSafeCatalog(t *testing.T) {
	htmlPrefixes := []string{
		"bot.help.",
		"bot.exit_nodes_health.",
		"bot.audit.",
		"bot.version.",
		"bot.my_status.",
		"bot.my_nodes.",
		"bot.my_rules.",
		"bot.my_quota.",
		"bot.myexitnodes.",
		"bot.add_device.",
	}
	// Tags Telegram accepts in parse_mode=HTML. Every other
	// `<word>` in the value is a violation (must be escaped
	// to `&lt;` and `&gt;`).
	allowedTags := map[string]bool{
		"b": true, "i": true, "u": true, "s": true,
		"strike": true, "del": true, "code": true,
		"pre": true, "a": true, "tg-spoiler": true,
		"blockquote": true,
	}
	// Reject <word> or </word> where word is not in allowedTags.
	// We use a permissive regex: < (or </) [a-zA-Z][a-zA-Z0-9-]*
	// — Cyrillic placeholders like <команда> don't match
	// [a-zA-Z] (so they're not caught by this regex), but
	// the live deploy already proved they fail HTML parsing
	// in production. We use a more permissive regex that
	// catches any Unicode letter so Cyrillic is covered too.
	badTagRE := regexp.MustCompile(`</?[\p{L}][\p{L}\p{N}-]*`)

	check := func(name string, cat map[string]string) {
		for k, v := range cat {
			isHTMLKey := false
			for _, p := range htmlPrefixes {
				if strings.HasPrefix(k, p) {
					isHTMLKey = true
					break
				}
			}
			if !isHTMLKey {
				continue
			}
			for _, m := range badTagRE.FindAllString(v, -1) {
				// m is the full match like "<code" or "</b".
				// Strip leading "<" or "</" to get the tag name.
				tag := strings.TrimPrefix(m, "</")
				tag = strings.TrimPrefix(tag, "<")
				if allowedTags[tag] {
					continue
				}
				t.Errorf("%s: %q has unsafe HTML tag %q — escape to &lt; / &gt;", name, k, m)
			}
		}
	}
	check("ruCatalog", ruCatalog)
	check("enCatalog", enCatalog)
}

// TestPlaceholderOrder — placeholder counts must match between languages
// (printf would fail or substitute wrongly otherwise).
func TestPlaceholderOrder(t *testing.T) {
	for k, ruVal := range ruCatalog {
		enVal, ok := enCatalog[k]
		if !ok {
			continue
		}
		ruPct := strings.Count(ruVal, "%")
		enPct := strings.Count(enVal, "%")
		if ruPct != enPct {
			t.Errorf("key %q: ru has %d %% placeholders, en has %d (ru=%q, en=%q)", k, ruPct, enPct, ruVal, enVal)
		}
	}
}

// TestTFormat — printf-style args are substituted correctly.
func TestTFormat(t *testing.T) {
	c := New()
	got := c.Tf(LangEN, "users.active_count", 5)
	want := "Active (5)"
	if got != want {
		t.Errorf("Tf(users.active_count, 5) = %q, want %q", got, want)
	}
}

// TestTFallback — missing key returns the key itself, never empty.
func TestTFallback(t *testing.T) {
	c := New()
	if got := c.T(LangEN, "nonexistent.key"); got != "nonexistent.key" {
		t.Errorf("T() for missing key = %q, want the key itself", got)
	}
}

// TestLangFromRequest — cookie wins, then Accept-Language, then default RU.
func TestLangFromRequest(t *testing.T) {
	c := New()
	mkReq := func(cookieVal, acceptLang string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if acceptLang != "" {
			r.Header.Set("Accept-Language", acceptLang)
		}
		if cookieVal != "" {
			r.AddCookie(&http.Cookie{Name: "lang", Value: cookieVal})
		}
		return r
	}
	// No cookie, Accept-Language has en
	if got := c.LangFromRequest(mkReq("", "en-US,en;q=0.9")); got != LangEN {
		t.Errorf("expected EN from Accept-Language, got %q", got)
	}
	// No cookie, no Accept-Language
	if got := c.LangFromRequest(mkReq("", "")); got != LangRU {
		t.Errorf("expected default RU, got %q", got)
	}
	// Cookie overrides Accept-Language
	if got := c.LangFromRequest(mkReq("en", "ru-RU")); got != LangEN {
		t.Errorf("cookie should override Accept-Language: got %q", got)
	}
	// Unknown cookie value falls back to Accept-Language
	if got := c.LangFromRequest(mkReq("xx", "ru-RU")); got != LangRU {
		t.Errorf("unknown cookie should fall back: got %q", got)
	}
}
