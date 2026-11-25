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
// is set (so it never runs silently in CI), and writes four self-contained HTML
// files there. `scripts/mobile_overflow_check_b521.sh` then loads each one in a
// headless browser at 390x844 and reads the overflow it reports.
package handlers

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// TestB521_ExportMobileSnapshots writes the four page states a phone can be looking
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
		// The group list (the default page since B517).
		"my-groups.html": renderMyRulesLens(t, url.Values{"lens": []string{"source"}}, ""),
		// One group open: the seven-column row table + the action bar + the pager.
		"my-group.html": renderMyRulesLens(t, url.Values{"lens": []string{"source"}}, "dom:rutracker.org"),
		// The admin page: the same table, its own toolbar and the switched-off view.
		"admin.html": renderAdminPage(t, nil, map[string]any{"ShowDisabled": true}),
	}

	for name, body := range pages {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(b521WrapPage(t, body)), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(body))
	}
}
