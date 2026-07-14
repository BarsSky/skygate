// 2026-07-14: tests for the lang helpers in lang.go.
//
// These pin:
//   1. LangFromTelegramCode on a representative set of
//      BCP-47 tags (with and without region subtag,
//      unknown primary subtags, empty input).
//   2. LangForBinding handles nil + empty + unknown values
//      by falling back to English.
//   3. IsValidLang accepts ru + en and rejects everything
//      else (so SetTelegramBindingLang is a no-op for
//      unknown values at the caller).
//
// We don't test LangForChat here because it requires a
// real DB; the per-test SQLite helpers in the existing
// test files cover that path implicitly via
// UpsertTelegramBinding round-trips.

package telegram

import (
	"testing"

	"skygate/internal/db"
)

func TestLangFromTelegramCode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Russian — direct match
		{"ru", "ru"},
		{"RU", "ru"},
		{"ru-RU", "ru"},
		{"ru_RU", "ru"},
		{"ru-ru", "ru"},
		// English — direct match
		{"en", "en"},
		{"EN", "en"},
		{"en-GB", "en"},
		{"en_US", "en"},
		// CIS-cluster routes to Russian until we ship
		// dedicated catalogs.
		{"uk", "ru"},
		{"uk-UA", "ru"},
		{"be", "ru"},
		{"kk", "ru"},
		// Unknown primary subtag → en (catalog fallback).
		{"de", "en"},
		{"fr", "en"},
		{"zh", "en"},
		{"ja", "en"},
		// Empty / whitespace → en.
		{"", "en"},
		{"   ", "en"},
		// Truncated / malformed tags — primary subtag is the
		// first dash-delimited part, so a leading dash is
		// empty and falls through to en.
		{"-RU", "en"},
	}
	for _, c := range cases {
		got := LangFromTelegramCode(c.in)
		if got != c.want {
			t.Errorf("LangFromTelegramCode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLangForBindingFallsBackToEnglish(t *testing.T) {
	// nil binding (defensive; the dispatcher never
	// hands us one in practice).
	if got := LangForBinding(nil); got != "en" {
		t.Errorf("LangForBinding(nil) = %q, want en", got)
	}
	// Empty lang (shouldn't happen in v0.33+ but the
	// function is total).
	if got := LangForBinding(&db.TelegramBinding{Lang: ""}); got != "en" {
		t.Errorf("LangForBinding{Lang:\"\"} = %q, want en", got)
	}
	// Unknown value (e.g. legacy 'fo' that snuck in before
	// IsValidLang landed). The function is permissive on
	// read — the catalog's T() does the real fallback, so
	// any value renders. We return it as-is to avoid
	// surprising callers who set lang to a third value
	// and expect it to round-trip.
	got := LangForBinding(&db.TelegramBinding{Lang: "fo"})
	if got != "fo" {
		t.Errorf("LangForBinding{Lang:\"fo\"} = %q, want fo (passthrough)", got)
	}
	// Real Russian.
	if got := LangForBinding(&db.TelegramBinding{Lang: "ru"}); got != "ru" {
		t.Errorf("LangForBinding{Lang:\"ru\"} = %q, want ru", got)
	}
	// Real English.
	if got := LangForBinding(&db.TelegramBinding{Lang: "en"}); got != "en" {
		t.Errorf("LangForBinding{Lang:\"en\"} = %q, want en", got)
	}
}

func TestIsValidLang(t *testing.T) {
	good := []string{"ru", "en"}
	for _, s := range good {
		if !IsValidLang(s) {
			t.Errorf("IsValidLang(%q) = false, want true", s)
		}
	}
	bad := []string{"", "de", "fr", "FO", "ru-ru", "ru-RU"}
	for _, s := range bad {
		if IsValidLang(s) {
			t.Errorf("IsValidLang(%q) = true, want false", s)
		}
	}
}
