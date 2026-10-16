package i18n

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mergedCatalog is the New()-built catalog that holds the
// union of every per-feature map. Built once via the package
// init below and reused by every test (cheap, but avoids
// re-allocating the map 4×).
//
// refactor-v0.30 Phase C (2026-07-29): the previous tests
// read ruCatalog / enCatalog directly (package-level vars).
// Phase C moved the keys into per-feature files + a
// perFeatureRU / perFeatureEN slice; New() merges them. The
// tests now read the merged result via New() — same
// contract, different source-of-truth.
var (
	mergedRU = mergeMaps(perFeatureRU...)
	mergedEN = mergeMaps(perFeatureEN...)
)

// TestCatalogsParity — every key in the merged RU catalog
// must exist in the merged EN catalog and vice versa,
// otherwise pages show mixed-language strings.
func TestCatalogsParity(t *testing.T) {
	for k := range mergedRU {
		if _, ok := mergedEN[k]; !ok {
			t.Errorf("enCatalog missing key: %q", k)
		}
	}
	for k := range mergedEN {
		if _, ok := mergedRU[k]; !ok {
			t.Errorf("ruCatalog missing key: %q", k)
		}
	}
}

// TestBotCatalogHasNoBakedInEscaping — B389 (2026-10-16).
//
// This test replaces the old prefix-list TestHTMLSafeCatalog, whose
// premise was wrong in both directions and which therefore certified
// a broken channel.
//
// The old test asserted that every value under ten selected prefixes
// carried NO raw `<word>`, i.e. that the catalogue itself must be
// HTML-escaped. Consequences, both measured on the live host:
//
//	(a) four commands were silently dead — `bot.invite.usage_invite`
//	    ("Использование: /invite <username>") and three siblings had a
//	    RAW placeholder while their builder set parse_mode=HTML, so
//	    Telegram answered 400 can't parse entities and the reply never
//	    arrived. The prefixes with the bug were simply not on the list.
//	(b) 61 values carried `&lt;placeholder&gt;` and were sent WITHOUT
//	    parse_mode, so the user read `&lt;username&gt;` literally.
//
// Escaping now belongs to the SEND BOUNDARY (internal/telegram/htmlsafe.go
// sanitises every parse_mode=HTML body after trimming, so a new prefix
// cannot reintroduce the class), and this test enforces the matching
// catalogue rule: a bot value carries LOGICAL text. Write `<username>`,
// never `&lt;username&gt;`; a bare `<` is safe on both paths and renders
// correctly on each.
func TestBotCatalogHasNoBakedInEscaping(t *testing.T) {
	entities := []string{"&lt;", "&gt;", "&amp;", "&quot;", "&#"}
	check := func(name string, cat map[string]string) {
		for k, v := range cat {
			if !strings.HasPrefix(k, "bot.") {
				continue
			}
			for _, e := range entities {
				if strings.Contains(v, e) {
					t.Errorf("%s: %q contains the baked-in entity %q — write the logical text "+
						"instead; the Telegram send boundary escapes it for parse_mode=HTML "+
						"(internal/telegram/htmlsafe.go, B389)", name, k, e)
				}
			}
		}
	}
	check("ruBot", mergedRU)
	check("enBot", mergedEN)
}

// TestBotCatalogHasNoRichOnlyTags — a `bot.*` value is rendered either
// as plain text or with parse_mode=HTML, never as a rich_message.html
// body (the rich path builds its own HTML from the block IR). The tags
// only the rich vocabulary accepts are therefore a guaranteed HTTP 400
// if they ever appear in a catalogue value.
func TestBotCatalogHasNoRichOnlyTags(t *testing.T) {
	richOnly := []string{
		"<h1>", "<h2>", "<h3>", "<h4>", "<h5>", "<h6>",
		"<table", "<details", "<summary", "<footer", "<blockquote",
		"<ul>", "<ol>", "<li>", "<hr>", "<mark>", "<tg-time", "<divider",
	}
	check := func(name string, cat map[string]string) {
		for k, v := range cat {
			if !strings.HasPrefix(k, "bot.") {
				continue
			}
			for _, tag := range richOnly {
				if strings.Contains(v, tag) {
					t.Errorf("%s: %q uses the rich-only tag %q, which Telegram rejects with "+
						"parse_mode=HTML (B389)", name, k, tag)
				}
			}
		}
	}
	check("ruBot", mergedRU)
	check("enBot", mergedEN)
}

// TestPlaceholderOrder — placeholder counts must match between languages
// (printf would fail or substitute wrongly otherwise).
func TestPlaceholderOrder(t *testing.T) {
	for k, ruVal := range mergedRU {
		enVal, ok := mergedEN[k]
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
