// page_title_b452_test.go — B452 (2026-10-30). «не вся локализация покрыта».
//
// The resolver is the one place that decides what a page title means, so these tests are the whole
// contract: a known literal becomes Russian for a Russian reader, a key-shaped value becomes its
// translation (the B450 defect), and a title that is DATA is left alone.
package handlers

import (
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// loadCatalog switches the global catalogue to a language for the duration of a test.
func loadCatalog(t *testing.T, lang string) {
	t.Helper()
	i18n.SetLang(lang)
	t.Cleanup(func() { i18n.SetLang("ru") })
}

func TestResolvePageTitleTranslatesKnownLiterals_B452(t *testing.T) {
	loadCatalog(t, "ru")
	cases := map[string]string{
		"Exit Nodes":      "Exit-узлы",
		"Service control": "Управление сервисом",
		"Node Load":       "Нагрузка узлов",
		"Notifications":   "Уведомления",
		"Account":         "Аккаунт",
		"API Tokens":      "API-токены",
		"Мониторинг":      "Мониторинг",
		"My exit rules":   "Мои правила выхода",
	}
	for literal, want := range cases {
		if got := resolvePageTitle(literal); got != want {
			t.Errorf("resolvePageTitle(%q) = %q, want %q", literal, got, want)
		}
	}
}

func TestResolvePageTitleFixesAKey_B452(t *testing.T) {
	loadCatalog(t, "ru")
	// The B450 defect shape: a handler that forgot to translate. The layout no longer shows the key.
	got := resolvePageTitle("title.admin_update")
	if got == "title.admin_update" || strings.Contains(got, "title.") {
		t.Fatalf("a key-shaped title still renders as a key: %q", got)
	}
}

func TestResolvePageTitleLeavesDataAlone_B452(t *testing.T) {
	loadCatalog(t, "ru")
	// A module name, a user, a version — data, not a title key. Guessing would mangle it.
	for _, data := range []string{"nginx", "michail", "v1.5.200", "", "Exit Nodes (bundled)"} {
		if got := resolvePageTitle(data); got != data {
			t.Errorf("resolvePageTitle(%q) = %q, want it unchanged", data, got)
		}
	}
}

// TestPageTitleLiteralKeysExist_B452 keeps the closed list honest: every literal the tree passes maps
// to a key that exists, so the mapping cannot silently do nothing for RU readers.
func TestPageTitleLiteralKeysExist_B452(t *testing.T) {
	loadCatalog(t, "ru")
	if len(pageTitleLiteralKeys) < 10 {
		t.Fatalf("the literal table shrank to %d entries — check it was not emptied", len(pageTitleLiteralKeys))
	}
	for literal, key := range pageTitleLiteralKeys {
		got := i18n.T("ru", key)
		if got == key || got == "" {
			t.Errorf("literal %q maps to %q, which is not in the RU catalogue", literal, key)
		}
		en := i18n.T("en", key)
		if en == key || en == "" {
			t.Errorf("literal %q maps to %q, which is not in the EN catalogue", literal, key)
		}
	}
}

// TestLayoutRendersTheResolvedTitle_B452 is the wiring assertion: the layout must call the resolver,
// not print .Title directly — that is the difference between a fix and a helper nobody uses.
func TestLayoutRendersTheResolvedTitle_B452(t *testing.T) {
	raw, err := templatesFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatalf("read layout: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "{{pt .Title}} — Skygate") {
		t.Fatal("layout.html does not render the title through pt — the literals would stay English")
	}
	if strings.Contains(src, "<title>{{.Title}}") {
		t.Fatal("layout.html still prints .Title verbatim")
	}
}
