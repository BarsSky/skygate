// B435 (2026-10-28) — every page's browser tab must carry the page's name.
//
// Two defects were measured before this test existed:
//
//   - `title.admin_cluster` was RETURNED by pageTitle but defined in NEITHER catalogue, so
//     /admin/cluster put the raw key in the tab (the operator: «title.* вместо описания»);
//   - 34 of the 59 templates had no case in pageTitle at all, so their tab said the generic
//     "Skygate" (/admin/update, /admin/tailscale, /admin/integrations, /my/notifications,
//     /my/telegram …).
//
// The assertion is about the RESULT, not the switch: for every embedded template, the key
// pageTitle returns must (a) not be the generic fallback and (b) exist in BOTH catalogues.
package handlers

import (
	"io/fs"
	"strings"
	"testing"

	"skygate/internal/i18n"
)

func TestB435_EveryPageHasATranslatedTabTitle(t *testing.T) {
	entries, err := fs.ReadDir(tplFS, "templates")
	if err != nil {
		t.Fatalf("read the embedded templates: %v", err)
	}
	_ = entries

	var files []string
	var walk func(dir string)
	walk = func(dir string) {
		items, err := fs.ReadDir(templatesFS, dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range items {
			p := dir + "/" + e.Name()
			if e.IsDir() {
				walk(p)
				continue
			}
			if strings.HasSuffix(e.Name(), ".html") {
				files = append(files, strings.TrimPrefix(p, "templates/"))
			}
		}
	}
	walk("templates")
	if len(files) < 40 {
		t.Fatalf("only %d templates found — the walk is broken, not the product", len(files))
	}

	cat := i18n.New()
	for _, name := range files {
		// layout.html and login.html are not rendered through pageTitle (login has its own
		// <title>, the layout is the shell around the others).
		if name == "layout.html" || name == "login.html" {
			continue
		}
		key := pageTitle(name)
		if key == "title.skygate" {
			t.Errorf("%s falls back to the generic title.skygate — the tab would not name the page", name)
			continue
		}
		for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
			if got := cat.T(lang, key); got == key || strings.TrimSpace(got) == "" {
				t.Errorf("%s → %s has no %s translation (T returned the key itself) — the tab shows %q",
					name, key, lang, key)
			}
		}
	}
}
