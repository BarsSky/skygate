// Package i18n holds per-language catalogs and helpers.
//
// The MVP is intentionally simple: a single Catalog type maps lang -> key
// -> translated string. Templates receive a *Translations object through
// the renderWithLayout data map and call .T to look up strings.
//
// Supported languages: "ru" (default) and "en".
// The user's choice is stored in a cookie "lang" so it survives across
// requests. Default lang is read from the Accept-Language header and
// falls back to "ru".
package i18n

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	LangRU = "ru"
	LangEN = "en"
)

// Catalog maps language -> translation key -> text.
// Use T() to look up a key; missing keys return the key itself so
// templates degrade gracefully during partial translations.
type Catalog struct {
	translations map[string]map[string]string
}

// New constructs a Catalog with the bundled RU and EN strings.
func New() *Catalog {
	c := &Catalog{translations: make(map[string]map[string]string)}
	c.translations[LangRU] = ruCatalog
	c.translations[LangEN] = enCatalog
	return c
}

// LangFromRequest returns the user's preferred language, in priority order:
//   1. cookie "lang"
//   2. Accept-Language header
//   3. default (RU)
func (c *Catalog) LangFromRequest(r *http.Request) string {
	if cookie, err := r.Cookie("lang"); err == nil {
		l := strings.ToLower(strings.TrimSpace(cookie.Value))
		if l == LangEN {
			return LangEN
		}
		if l == LangRU {
			return LangRU
		}
	}
	al := r.Header.Get("Accept-Language")
	if strings.Contains(strings.ToLower(al), "en") {
		return LangEN
	}
	return LangRU
}

// T returns the translation for a key in the given language.
// Falls back to the RU string if the requested language doesn't have
// the key, and to the key itself if RU also lacks it.
func (c *Catalog) T(lang, key string) string {
	if m, ok := c.translations[lang]; ok {
		if s, ok := m[key]; ok {
			return s
		}
	}
	if s, ok := c.translations[LangRU][key]; ok {
		return s
	}
	return key
}

// Tf is T with printf-style argument substitution. Use when a translation
// contains placeholders like "Delete %s?".
func (c *Catalog) Tf(lang, key string, args ...any) string {
	return fmt.Sprintf(c.T(lang, key), args...)
}

// All returns the full map for a language (lang -> key -> string).
// Used by templates that need to call Tr through dot syntax:
//   {{.Tr "key"}}
type Translations struct {
	Catalog *Catalog
	Lang    string
}

// 2026-07-11: renamed T → Tr. Go's html/template engine refuses to
// resolve a single-letter method named "T" on a struct pointer
// ("T is not a method but has arguments"). Multi-letter names like
// "Tr" or "Translate" work fine. Same rename applies to Tf → Trf.
func (t *Translations) Tr(key string) string {
	if t == nil || t.Catalog == nil {
		return key
	}
	return t.Catalog.T(t.Lang, key)
}

// Trf is the formatted variant: substitutes args into the translation.
func (t *Translations) Trf(key string, args ...any) string {
	if t == nil || t.Catalog == nil {
		return key
	}
	return t.Catalog.Tf(t.Lang, key, args...)
}
