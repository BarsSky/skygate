// title_keys_b450_test.go — B450 (2026-10-30). «не вся локализация покрыта в строке состояния
// страницы висит переменная а не значение».
//
// The browser tab of /admin/update read `title.admin_update`: the handler put the CATALOG KEY
// into the page data instead of the translation, and the layout renders `<title>{{.Title}} — Skygate</title>`
// verbatim. Nothing caught it:
//
//   - TestCatalogsParity compares KEY SETS, and the key was present and translated;
//   - the B325 ratchet looks for hardcoded ENGLISH in templates, and this was a key, not English;
//   - every render test supplies its own Title, so no test ever saw the real handler's value.
//
// This test closes that hole for the whole tree: a `"Title": "<literal>"` assignment whose value
// IS an existing catalogue key is a slot that forgot to translate, whatever page it lives on.
// The English literals that remain (a separate, documented debt) are not key-shaped and stay
// visible instead of being silently counted as done.
package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// titleAssignment matches a page-data title slot with a quoted literal value.
var titleAssignment = regexp.MustCompile(`"Title"\s*:\s*"([^"]*)"`)

// keyShape is what an i18n key looks like: lowercase dotted segments, no spaces.
var keyShape = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+$`)

// keysOf returns the union of every key declared in the catalogues of THIS package.
func keysOf(t *testing.T) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "catalog_") || !strings.HasSuffix(name, ".go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, m := range regexp.MustCompile(`"([a-zA-Z0-9_.]+)"\s*:\s*"`).FindAllStringSubmatch(string(raw), -1) {
			keys[m[1]] = true
		}
	}
	if len(keys) < 100 {
		t.Fatalf("only %d keys found — the catalogue scan is broken, so this test would pass vacuously", len(keys))
	}
	return keys
}

// TestNoPageTitleIsARawKey_B450 walks the Go sources for a `"Title": "<key>"` slot.
func TestNoPageTitleIsARawKey_B450(t *testing.T) {
	keys := keysOf(t)

	// The package dir is <repo>/internal/i18n.
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	offenders := []string{}
	scanned := 0
	err := filepath.Walk(filepath.Join(repoRoot, "internal"), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		scanned++
		rel, _ := filepath.Rel(repoRoot, path)
		for _, m := range titleAssignment.FindAllStringSubmatch(string(raw), -1) {
			val := m[1]
			if !keyShape.MatchString(val) || !keys[val] {
				continue // plain text, or a key-shaped string that is not a catalogue key
			}
			offenders = append(offenders, rel+` -> "`+val+`"`)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk sources: %v", err)
	}
	if scanned < 100 {
		t.Fatalf("only %d source file(s) scanned — the walk is broken, so this test cannot prove anything", scanned)
	}
	for _, o := range offenders {
		t.Errorf("page title slot carries a CATALOG KEY instead of its translation: %s\n"+
			"  → wrap it: I18nT(c, <key>) / i18n.T(lang, <key>). The layout renders .Title verbatim, "+
			"so the browser tab shows the key (live: /admin/update read «title.admin_update»).", o)
	}
}

// TestPageTitleMapKeysExist_B450 pins the other half: every key the generic renderer maps a
// template name to must exist, or the fallback title is the key itself.
func TestPageTitleMapKeysExist_B450(t *testing.T) {
	keys := keysOf(t)

	raw, err := os.ReadFile(filepath.Join("..", "handlers", "handlers.go"))
	if err != nil {
		t.Fatalf("read handlers.go: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func pageTitle(")
	if start < 0 {
		t.Fatal("pageTitle not found — this test's subject moved, update it rather than deleting it")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	checked := 0
	for _, m := range regexp.MustCompile(`"(title\.[a-z0-9_]+)"`).FindAllStringSubmatch(body, -1) {
		checked++
		if !keys[m[1]] {
			t.Errorf("pageTitle maps a template to %q, which is not in the catalogues — the tab would show the key", m[1])
		}
	}
	if checked < 10 {
		t.Fatalf("only %d title key(s) found in pageTitle — the scan is broken", checked)
	}
}
