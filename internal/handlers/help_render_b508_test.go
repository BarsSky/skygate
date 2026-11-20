package handlers

// help_render_b508_test.go — B508 (2026-11-20)
//
// Operator report: «на странице помощи много неправильно сделанных ссылок… в открытую
// висит html синтаксис».
//
// MEASURED: every help string that carries formatting (<b>, <code>, <a href>) was
// rendered with `{{t "…"}}`, and `t`/`tf` return a plain `string` — html/template
// escapes it, so the tags reached the browser as literal markup. Only three usages
// on the page piped `safeHTML`, which is why the defect looked random: a <b> in one
// card worked and the <a href> in the next did not.
//
// The fix is the `th` helper (catalogue markup trusted, ARGUMENTS escaped) used for
// every catalogue string on this page. These tests execute the REAL template with
// the REAL catalogue, which is the only way to see what the browser sees:
//   - no escaped tag survives into the output (`&lt;b&gt;`, `&lt;a href`);
//   - the links are real markup with a real href;
//   - `/docs/windows-client.md` (never a route) is gone;
//   - the admin-only cleanup card is hidden from a non-admin.

import (
	"html/template"
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// b508Funcs mirrors the production funcmap for the two helpers this page needs.
func b508Funcs() template.FuncMap {
	return template.FuncMap{
		"t": func(key string) string {
			lang, _ := i18n.GlobalLang.Load().(string)
			return i18n.GlobalCatalog.T(lang, key)
		},
		"tf": func(key string, args ...any) string {
			lang, _ := i18n.GlobalLang.Load().(string)
			return i18n.GlobalCatalog.Tf(lang, key, args...)
		},
		// Same contract as templates.go: markup from the catalogue is trusted,
		// arguments are escaped.
		"th": func(key string, args ...any) template.HTML {
			lang, _ := i18n.GlobalLang.Load().(string)
			if len(args) == 0 {
				return template.HTML(i18n.GlobalCatalog.T(lang, key))
			}
			esc := make([]any, len(args))
			for i, a := range args {
				esc[i] = template.HTMLEscapeString(toString(a))
			}
			return template.HTML(i18n.GlobalCatalog.Tf(lang, key, esc...))
		},
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// b508Render executes the help body for one language and one admin flag.
func b508Render(t *testing.T, lang string, isAdmin bool) string {
	t.Helper()
	i18n.SetLang(lang)
	t.Cleanup(func() { i18n.SetLang("ru") })

	body, err := templatesFS.ReadFile("templates/exit_rules_help.html")
	if err != nil {
		t.Fatalf("read the help template: %v", err)
	}
	tpl, err := template.New("help").Funcs(b508Funcs()).Parse(string(body))
	if err != nil {
		t.Fatalf("parse the help template: %v", err)
	}
	var sb strings.Builder
	if err := tpl.ExecuteTemplate(&sb, "body-exit_rules_help", map[string]any{
		"BaseURL": "https://panel.example.com",
		"IsAdmin": isAdmin,
	}); err != nil {
		t.Fatalf("execute the help template: %v", err)
	}
	return sb.String()
}

// TestB508_NoEscapedMarkupReachesTheBrowser is the defect itself.
func TestB508_NoEscapedMarkupReachesTheBrowser(t *testing.T) {
	for _, lang := range []string{"ru", "en"} {
		t.Run(lang, func(t *testing.T) {
			out := b508Render(t, lang, true)
			for _, bad := range []string{"&lt;b&gt;", "&lt;code&gt;", "&lt;a href", "&lt;/a&gt;", "&lt;i "} {
				if strings.Contains(out, bad) {
					t.Errorf("the rendered page contains %q — a catalogue tag reached the browser as text", bad)
				}
			}
			// And the markup that SHOULD be there is there.
			for _, want := range []string{"<code>", "<b>"} {
				if !strings.Contains(out, want) {
					t.Errorf("the page lost %q entirely — the catalogue's formatting must render", want)
				}
			}
			if !strings.Contains(out, `<a href="/my/tokens">`) {
				t.Error("the /my/tokens link is not an anchor in the rendered page")
			}
		})
	}
}

// TestB508_LinksPointAtRealPages — the route check lives in the shell contract
// (scripts/check_b508_help_page_markup.sh); what a Go test can prove is that no
// link claims a documentation file the server never serves.
func TestB508_NoDeadDocsLink(t *testing.T) {
	out := b508Render(t, "ru", true)
	if strings.Contains(out, "/docs/") {
		t.Error("the page links to /docs/… which no route serves (it answered 404 before B508)")
	}
	if !strings.Contains(out, "/my/exit-rules?script=1&amp;os=windows") {
		t.Error("the Windows step no longer links to the generated route script")
	}
}

// TestB508_AdminOnlyCleanupCard — a normal user must not be shown a link that 403s.
func TestB508_AdminOnlyCleanupCard(t *testing.T) {
	admin := b508Render(t, "ru", true)
	user := b508Render(t, "ru", false)
	key := i18n.GlobalCatalog.T("ru", "help.exit_rules_help.cleanup_h3")
	if !strings.Contains(admin, "/admin/exit-rules/cleanup") {
		t.Error("an admin no longer sees the cleanup link")
	}
	if strings.Contains(user, "/admin/exit-rules/cleanup") {
		t.Errorf("a non-admin is shown the admin-only cleanup card (%q)", key)
	}
}

// TestB508_ArgumentIsEscaped — the helper trusts the CATALOGUE, never its
// arguments: BaseURL comes from the request's Host header.
func TestB508_ArgumentIsEscaped(t *testing.T) {
	i18n.SetLang("ru")
	t.Cleanup(func() { i18n.SetLang("ru") })
	th := b508Funcs()["th"].(func(string, ...any) template.HTML)
	got := string(th("help.exit_rules_help.ai_base_url", `https://x/"><script>alert(1)</script>`))
	if strings.Contains(got, "<script>") {
		t.Fatalf("the argument was interpolated raw: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("the argument is not escaped the way html/template would escape it: %q", got)
	}
}
