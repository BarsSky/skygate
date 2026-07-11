package i18n

import (
	"net/http"
	"net/http/httptest"
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
