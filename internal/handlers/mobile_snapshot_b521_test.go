// mobile_snapshot_b521_test.go — renders the reworked pages to HTML so a REAL
// browser can measure them at a phone width.
//
// WHY THIS EXISTS: the project's mobile guards (B357, B410, B411, B435.1) are all
// static: they assert that tables sit inside `.table-wrap`, that no inline width
// exceeds a budget, and that the `nowrap` count does not grow. None of them can see
// what a browser actually does with the page at 390px — whether the CONTENT column
// overflows, or whether a control pushes the document sideways. The rework added a
// seven-column row table, a selection bar with four buttons and a numbered pager,
// so the question is worth a measurement rather than an argument.
//
// This test is a TOOL, not an assertion: it does nothing unless SKYGATE_MOBILE_DIR
// is set (so it never runs silently in CI), and writes self-contained HTML files
// there: the two user states (B516) and the two admin states (B523). The contract
// script then loads each one in a headless browser at 390 and 360 px and reads the
// overflow it reports.
package handlers

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
	"skygate/internal/i18n"
)

// b521KeyToken finds the page's catalogue keys in a rendered body. The render
// helpers below share their funcs with the STRUCTURAL tests, which stub `t` to
// echo the key precisely so they can assert "this block renders key K". Measuring
// that output would answer the wrong question: `exit_rules.target_type_subnet` is
// one 28-character unbreakable token, so a page full of keys overflows differently
// from the page the operator sees. The exporter therefore localizes afterwards,
// with the real catalogue, and only replaces a token the catalogue actually knows
// — which is also what keeps `class="table-wrap"`, `href="/my/exit-rules"` and
// `themes.css` from being touched.
var b521KeyToken = regexp.MustCompile(`[a-z][a-z0-9_]*\.[a-z][a-z0-9_.]*`)

// b521Localize replaces every known catalogue key in body with its Russian text.
// Longest keys first, so a key that is a prefix of another cannot shadow it.
//
// `tf` is the one helper whose fidelity cannot be recovered: the shared funcmaps
// stub it as `func(key string, args ...any) string { return key }` — the structural
// tests assert that the KEY renders, so the arguments are discarded before this
// function sees anything. Those strings still translate, but they reach the page
// with their verbs intact (`Группы %d–%d из %d`), which is both ugly in a review
// screenshot and unlike the page the operator sees. The verbs are therefore filled
// with a fixture value of the same width (a digit for %d/%v, a letter for %s, and
// %%, which the catalogue uses for literal percents, is protected first): the
// LAYOUT is what this tool measures, and the fixture counts are invented anyway.
func b521Localize(body string) string {
	cat := i18n.New()
	seen := map[string]string{}
	for _, tok := range b521KeyToken.FindAllString(body, -1) {
		if _, done := seen[tok]; done {
			continue
		}
		if tr := cat.T(i18n.LangRU, tok); tr != tok {
			seen[tok] = b521FillVerbs(tr)
		}
	}
	if len(seen) == 0 {
		return body
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	pairs := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		pairs = append(pairs, k, seen[k])
	}
	return strings.NewReplacer(pairs...).Replace(body)
}

// b521FillVerbs replaces printf verbs with same-width fixture values.
func b521FillVerbs(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	const pct = "\x00PCT\x00"
	s = strings.ReplaceAll(s, "%%", pct)
	s = strings.NewReplacer("%d", "1", "%v", "1", "%s", "x", "%q", "x").Replace(s)
	return strings.ReplaceAll(s, pct, "%")
}

// b521WrapPage builds a self-contained page: the install's own themes.css plus the
// rendered body, and a script that puts the measured horizontal overflow into the
// document title (headless Chrome/Edge `--dump-dom` prints it back to us).
func b521WrapPage(t *testing.T, body string) string {
	t.Helper()
	css, err := os.ReadFile(filepath.Join("..", "staticfs", "static", "css", "themes.css"))
	if err != nil {
		t.Fatalf("read themes.css: %v", err)
	}
	var b strings.Builder
	b.WriteString("<!doctype html><html lang=\"ru\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString("<title>W=unmeasured</title><style>")
	b.Write(css)
	// The phone layout: no fixed sidebar column (the drawer overlays content) and a
	// content padding comparable to the real shell.
	b.WriteString("\nhtml,body{margin:0;padding:0;}body{padding:8px;}main{display:block;}\n")
	b.WriteString("</style></head><body><main>")
	b.WriteString(body)
	b.WriteString("</main><script>")
	b.WriteString("function measure(){var d=document.documentElement;")
	b.WriteString("document.title='W='+(d.scrollWidth-d.clientWidth)+';SW='+d.scrollWidth+';CW='+d.clientWidth;}")
	b.WriteString("if(document.readyState==='complete'){measure();}else{window.addEventListener('load',measure);}")
	b.WriteString("</script></body></html>")
	return b.String()
}

// TestB521_ExportMobileSnapshots writes the three page states a phone can be looking
// at. Guarded by SKYGATE_MOBILE_DIR so it is a no-op in a normal test run.
func TestB521_ExportMobileSnapshots(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("SKYGATE_MOBILE_DIR"))
	if dir == "" {
		return // a tool, not an assertion: nothing to export
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}

	pages := map[string]string{
		// The group list (the default page since B516).
		"my-groups.html": renderMyRulesLens(t, url.Values{"lens": []string{"source"}}, ""),
		// One group open: the seven-column row table + the action bar + the pager.
		"my-group.html": renderMyRulesLens(t, url.Values{"lens": []string{"source"}}, "dom:rutracker.org"),
		// The admin page (B523): the same card model, with the owner pivot and the
		// switched-off view on — the widest toolbar the page can have.
		"admin.html": renderAdminLensSnapshot(t, url.Values{"lens": []string{"source"}}, ""),
		// One admin group open: the per-row switch + delete next to the owner column.
		"admin-group.html": renderAdminLensSnapshot(t, url.Values{"lens": []string{"source"}}, "dom:rutracker.org"),
	}

	for name, body := range pages {
		body = b521Localize(body)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(b521WrapPage(t, body)), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(body))
	}
}

// renderAdminLensSnapshot renders the admin CARD page (B523) with the switched-off
// view ON — the widest toolbar that page can have, which is the shape a phone has to
// survive. It reuses the B523 fixture and projection, so the snapshot measures the
// page the render tests assert.
func renderAdminLensSnapshot(t *testing.T, q url.Values, groupKey string) string {
	t.Helper()
	rules := b523AdminRules()
	lens := exit_rules.BuildRulesLensViewFor("/admin/exit-rules", q,
		adminRowProjection(rules), q.Get("lens"), groupKey, exit_rules.AdminRuleLensOrder())
	lens.Mode = true
	return renderAdminPage(t, rules, map[string]any{"AdminLens": lens, "ShowDisabled": true})
}
