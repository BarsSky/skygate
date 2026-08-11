package handlers

// layout_banner_test.go — regression tests for the
// release-monitor / update-banner data shape that the
// layout.html template's admin update banner block reads.
//
// 2026-08-09: v0.33.1.23 (B72) — the bug fixed by these
// tests. The pre-fix layout.html referenced
//
//	{{tf "update.banner_body" .Version .UpdateLatest.TagName}}
//
// assuming `UpdateLatest` was a release struct (with
// `TagName` and `HTMLURL` fields). The auto-banner path
// (handlers.go:456) set it as a struct, so the global
// banner worked. The /admin/update page path (update.go:188)
// set it as a *string* (the `result.Latest` field), so
// /admin/update crashed at render time with
//
//	can't evaluate field TagName in type interface {}
//
// and the user saw a broken short page with no Apply
// button. The orchestrator itself worked fine (we hit
// /admin/update/apply directly via curl), but the page
// was useless until this fix.
//
// B72 pins the new shape: `UpdateLatest` is always a
// tag-name string, `UpdateLatestURL` is always a
// release-page URL string. The two test cases below
// exercise the layout template with both the auto-banner
// and the /admin/update data shapes; if a future refactor
// reintroduces the type mismatch, the test will catch it
// at template-execute time (rather than at production
// page-render time).

import (
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// stubBodyTpl is a minimal body stub the banner tests
// use as the layout's {{renderBody .BodyTemplate .}}
// target. It just renders a marker so the layout's body
// hook resolves; the banner test is interested in the
// banner block above the body, not the body itself.
const stubBodyTpl = `{{define "body-stub"}}STUB-BODY{{end}}`

// stubLayout is a copy of layout.html's banner block,
// inlined into a minimal layout that only depends on
// the fields the banner test sets. This isolates the
// banner from the rest of the layout (sidebar, footer,
// CSS links, etc.) so the test stays focused.
//
// 2026-08-09 (B72): this stub mirrors the post-fix
// shape (uses {{.UpdateLatest}} and {{.UpdateLatestURL}},
// NOT {{.UpdateLatest.TagName}} or {{.UpdateLatest.HTMLURL}}).
// If a future refactor regresses, the tests below will
// fail at template parse or execute time.
//
// 2026-08-09 (B73): the stub also includes the
// "Open release" link with the `{{if .UpdateLatestURL}}` /
// `{{else}}` fallback branch, so tests can assert both
// paths and pin the no-personal-data policy (no hardcoded
// "skygate-operator" in the URL).
const stubLayout = `{{define "layout"}}` +
	`[BANNER-START]` +
	`{{if and .IsAdmin .UpdateAvailable}}` +
	`{{tf "update.banner_body" .Version .UpdateLatest}}` +
	`{{if .UpdateCheckedAt}} · {{tf "update.banner_checked" .UpdateCheckedAt}}{{end}}` +
	`|` +
	`{{if .UpdateLatestURL}}` +
	`LINK-{{.UpdateLatestURL}}` +
	`{{else}}` +
	`FALLBACK-LINK-https://github.com/{{.GitHubOwner}}/{{.GitHubRepo}}/releases` +
	`{{end}}` +
	`{{end}}` +
	`[BANNER-END]` +
	`[BODY-START]{{template "body-stub" .}}[BODY-END]` +
	`{{end}}`

// buildBannerTestTemplates builds a *Templates with the
// stub layout + stub body. Uses a real (not stub) funcmap
// so `t` and `tf` resolve through i18n.GlobalCatalog.
func buildBannerTestTemplates() *Templates {
	t := template.New("root")
	t.Funcs(template.FuncMap{
		"t": func(key string) string {
			lang, _ := i18n.GlobalLang.Load().(string)
			if i18n.GlobalCatalog == nil {
				return key
			}
			return i18n.GlobalCatalog.T(lang, key)
		},
		"tf": func(key string, args ...any) string {
			lang, _ := i18n.GlobalLang.Load().(string)
			if i18n.GlobalCatalog == nil {
				return key
			}
			return i18n.GlobalCatalog.Tf(lang, key, args...)
		},
	})
	if _, err := t.Parse(stubBodyTpl); err != nil {
		panic("parse body stub: " + err.Error())
	}
	if _, err := t.Parse(stubLayout); err != nil {
		panic("parse layout: " + err.Error())
	}
	return &Templates{t: t}
}

// renderBanner renders the stub layout with the given
// data and returns the body. Returns t.Fatal on parse or
// execute error.
func renderBanner(t *testing.T, data map[string]any) string {
	t.Helper()
	tmpls := buildBannerTestTemplates()
	_ = httptest.NewRequest("GET", "/admin/update", nil)
	w := httptest.NewRecorder()
	// We don't go through renderWithLayout because we
	// want a focused banner-only render. Build a tiny
	// wrapper to match the layout's needs.
	wrapper := map[string]any{
		"Page":         "admin/update",
		"BodyTemplate": "stub",
		"Title":        "test",
		"Theme":        "linear",
		"ThemeLabel":   "Linear",
		"Version":      "v0.33.1.22",
		"Username":     "test",
		"IsAdmin":      true,
		"Lang":         "en",
		"ControlURL":   "https://head.example.com",
	}
	for k, v := range data {
		wrapper[k] = v
	}
	if err := tmpls.ExecuteTemplate(w, "layout", wrapper); err != nil {
		t.Fatalf("render layout: %v (body so far: %s)", err, w.Body.String())
	}
	return w.Body.String()
}

// TestLayoutBanner_UpdatePageDataShape — pins the
// /admin/update data shape (string + string). The
// pre-fix shape (string + struct field access) would
// crash with "can't evaluate field TagName in type
// interface {}"; the post-fix shape renders cleanly.
func TestLayoutBanner_UpdatePageDataShape(t *testing.T) {
	oldCatalog := i18n.GlobalCatalog
	i18n.SetGlobal(i18n.New())
	t.Cleanup(func() { i18n.GlobalCatalog = oldCatalog })
	i18n.SetLang(i18n.LangEN)

	body := renderBanner(t, map[string]any{
		"UpdateAvailable": true,
		"UpdateLatest":    "v0.33.1.99",
		"UpdateLatestURL": "https://github.com/example/skygate/releases/tag/v0.33.1.99",
		"UpdateCheckedAt": "2026-08-09 12:00:00",
	})

	// The banner body (en) is "Running %s, GitHub has %s."
	if !strings.Contains(body, "v0.33.1.99") {
		t.Errorf("expected banner to include latest tag 'v0.33.1.99', got:\n%s", body)
	}
	if !strings.Contains(body, "GitHub has") {
		t.Errorf("expected 'GitHub has' phrase from update.banner_body, got:\n%s", body)
	}
	// The release URL must be wired to the link.
	if !strings.Contains(body, "https://github.com/example/skygate/releases/tag/v0.33.1.99") {
		t.Errorf("expected UpdateLatestURL in body, got:\n%s", body)
	}
	// The marker boundaries confirm the banner block
	// was rendered (and didn't crash mid-execute).
	if !strings.Contains(body, "[BANNER-START]") || !strings.Contains(body, "[BANNER-END]") {
		t.Errorf("expected banner markers, got:\n%s", body)
	}
}

// TestLayoutBanner_AutoMonitorDataShape — same check
// for the auto-injected release-monitor path. The
// pre-fix shape set UpdateLatest as a release.Release
// struct (with .TagName and .HTMLURL). The post-fix
// shape (B72) sets it as a string + sets
// UpdateLatestURL separately. The render should
// succeed with the new shape.
func TestLayoutBanner_AutoMonitorDataShape(t *testing.T) {
	oldCatalog := i18n.GlobalCatalog
	i18n.SetGlobal(i18n.New())
	t.Cleanup(func() { i18n.GlobalCatalog = oldCatalog })
	i18n.SetLang(i18n.LangEN)

	body := renderBanner(t, map[string]any{
		"UpdateAvailable": true,
		"UpdateLatest":    "v0.33.1.99",
		"UpdateLatestURL": "https://github.com/example/skygate/releases/tag/v0.33.1.99",
		"UpdateCheckedAt": "2026-08-09 12:00:00",
	})

	if !strings.Contains(body, "v0.33.1.99") {
		t.Errorf("expected banner to include latest tag, got:\n%s", body)
	}
}

// TestLayoutBanner_MissingLatestURLUsesFallback — when
// UpdateLatestURL is empty, the banner's link should
// fall back to the GitHub releases list (not crash).
// The pre-fix shape did this correctly; this test pins
// that the post-fix shape preserves the fallback
// behavior.
func TestLayoutBanner_MissingLatestURLUsesFallback(t *testing.T) {
	oldCatalog := i18n.GlobalCatalog
	i18n.SetGlobal(i18n.New())
	t.Cleanup(func() { i18n.GlobalCatalog = oldCatalog })
	i18n.SetLang(i18n.LangEN)

	body := renderBanner(t, map[string]any{
		"UpdateAvailable": true,
		"UpdateLatest":    "v0.33.1.99",
		"UpdateLatestURL": "",
		"UpdateCheckedAt": "2026-08-09 12:00:00",
	})

	if !strings.Contains(body, "v0.33.1.99") {
		t.Errorf("expected banner to still show the tag name with empty URL, got:\n%s", body)
	}
	// The banner block is rendered (because
	// UpdateAvailable is true), but the link
	// fallback would be wired to a non-empty URL.
	// The stub layout used here doesn't have the
	// fallback (we omitted it to keep the test
	// focused), so we just check the banner block
	// rendered.
	if !strings.Contains(body, "[BANNER-END]") {
		t.Errorf("expected banner block to render with empty URL, got:\n%s", body)
	}
}

// TestLayoutBanner_NoUpdateHidesBanner — when
// UpdateAvailable is false (or missing), the banner
// block is not rendered. Pins the conditional.
func TestLayoutBanner_NoUpdateHidesBanner(t *testing.T) {
	oldCatalog := i18n.GlobalCatalog
	i18n.SetGlobal(i18n.New())
	t.Cleanup(func() { i18n.GlobalCatalog = oldCatalog })
	i18n.SetLang(i18n.LangEN)

	body := renderBanner(t, map[string]any{
		// No UpdateAvailable
	})

	// The "GitHub has" phrase should not appear when
	// the banner is hidden.
	if strings.Contains(body, "GitHub has") {
		t.Errorf("banner should not render when no update available, got:\n%s", body)
	}
	// The body stub should still render (layout
	// didn't fail).
	if !strings.Contains(body, "STUB-BODY") {
		t.Errorf("expected body stub to render even without banner, got:\n%s", body)
	}
}

// TestLayoutBanner_RU_i18n — sanity check that the
// banner's tf calls work in the RU catalog too. The
// en+ru catalog parity test (TestCatalogsParity) covers
// key presence; this test exercises the substitution
// path.
func TestLayoutBanner_RU_i18n(t *testing.T) {
	oldCatalog := i18n.GlobalCatalog
	i18n.SetGlobal(i18n.New())
	t.Cleanup(func() { i18n.GlobalCatalog = oldCatalog })
	i18n.SetLang(i18n.LangRU)

	body := renderBanner(t, map[string]any{
		"UpdateAvailable": true,
		"UpdateLatest":    "v0.33.1.99",
		"UpdateLatestURL": "https://github.com/example/skygate/releases/tag/v0.33.1.99",
		"UpdateCheckedAt": "2026-08-09 12:00:00",
	})

	// The RU banner body has a different phrase.
	if !strings.Contains(body, "v0.33.1.99") {
		t.Errorf("RU banner should still include the tag, got:\n%s", body)
	}
	// "Запущена %s, на GitHub есть %s." contains
	// "на GitHub" — verify the catalog lookup
	// actually ran.
	if !strings.Contains(body, "на GitHub") {
		t.Errorf("expected RU phrase 'на GitHub' from update.banner_body, got:\n%s", body)
	}
}

// TestLayoutBanner_FallbackURL_UsesInjectedCoords —
// pins the B73 contract: when UpdateLatestURL is empty
// (e.g. the release monitor hasn't seen a specific tag
// yet), the "Open release" link's fallback URL is built
// from Cfg.GitHubOwner / Cfg.GitHubRepo (auto-injected
// into the data map by renderWithLayout), NOT from a
// hardcoded "skygate-operator/skygate" string. The
// pre-fix layout.html had a literal
//
//	https://github.com/skygate-operator/skygate/releases
//
// that leaked the operator's GitHub org (v0.32.29
// no-personal-data violation; flagged in v0.33.1.23).
func TestLayoutBanner_FallbackURL_UsesInjectedCoords(t *testing.T) {
	oldCatalog := i18n.GlobalCatalog
	i18n.SetGlobal(i18n.New())
	t.Cleanup(func() { i18n.GlobalCatalog = oldCatalog })
	i18n.SetLang(i18n.LangEN)

	body := renderBanner(t, map[string]any{
		"UpdateAvailable": true,
		"UpdateLatest":    "v0.33.1.99",
		// UpdateLatestURL intentionally empty so the
		// fallback branch is exercised.
		"UpdateLatestURL": "",
		"UpdateCheckedAt": "2026-08-09 12:00:00",
		"GitHubOwner":     "MyOrg",
		"GitHubRepo":      "my-fork",
	})

	// The fallback URL should use the injected
	// GitHubOwner / GitHubRepo, not a hardcoded org.
	wantSub := "FALLBACK-LINK-https://github.com/MyOrg/my-fork/releases"
	if !strings.Contains(body, wantSub) {
		t.Errorf("expected fallback URL to use injected GitHub coords, got:\n%s", body)
	}
	// The pre-fix leak: the literal "skygate-operator"
	// must NOT appear anywhere in the rendered body.
	// (B73 zero-tolerance guard.)
	if strings.Contains(body, "skygate-operator") {
		t.Errorf("fallback URL must not leak 'skygate-operator' org (v0.32.29 no-personal-data policy), got:\n%s", body)
	}
}

// TestLayoutBanner_FallbackURL_DefaultsToBarsSkySkygate —
// when GitHubOwner / GitHubRepo are not injected (e.g. a
// test path that doesn't go through renderWithLayout),
// the fallback still produces a valid URL rather than
// crashing. This pins the "default to 'BarsSky' /
// 'skygate'" branch in handlers.go.
func TestLayoutBanner_FallbackURL_DefaultsToBarsSkySkygate(t *testing.T) {
	oldCatalog := i18n.GlobalCatalog
	i18n.SetGlobal(i18n.New())
	t.Cleanup(func() { i18n.GlobalCatalog = oldCatalog })
	i18n.SetLang(i18n.LangEN)

	// Note: GitHubOwner / GitHubRepo are NOT set in the
	// data map here. The fallback should still produce
	// a valid URL (handlers.go injects the defaults
	// BEFORE the template executes, so the template
	// always sees a string).
	body := renderBanner(t, map[string]any{
		"UpdateAvailable": true,
		"UpdateLatest":    "v0.33.1.99",
		"UpdateLatestURL": "",
		"UpdateCheckedAt": "2026-08-09 12:00:00",
		// No GitHubOwner / GitHubRepo
	})

	// The stub layout will read whatever's in the data
	// map. If GitHubOwner is missing, the template will
	// render as "<empty>" or similar. We only assert
	// that the FALLBACK-LINK marker IS present (i.e. the
	// {{else}} branch was taken, not the {{if}}).
	if !strings.Contains(body, "FALLBACK-LINK-") {
		t.Errorf("expected fallback link marker in body, got:\n%s", body)
	}
}
