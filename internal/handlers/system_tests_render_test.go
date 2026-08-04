package handlers

// system_tests_render_test.go — regression test for the v0.33.1.2
// "LiveResults inside range" panic.
//
// What was wrong: /admin/system_tests.html had `{{if .LiveResults}}`
// inside a `{{range .Tests}}` block. Inside the range, `.` is the
// current iteration element (a SystemTestDef, which has Name /
// Category / Description / Run — no LiveResults). The page-level
// LiveResults field had to be referenced as `$.LiveResults`.
//
// Why it was hidden: before v0.33.1.1 the body template was named
// `body-admin-system-tests` (with a hyphen), but renderBody looks
// for `body-admin-system_tests` (with an underscore, derived from
// the filename "system_tests.html" via the renderBody convention in
// templates.go). The body was never found, so the page returned
// 200 + empty body. After v0.33.1.1 renamed the body to
// `body-admin-system_tests`, the body was found and the
// .LiveResults access inside the range panicked, surfacing as a
// 500 with `can't evaluate field LiveResults in type
// admin.SystemTestDef`.
//
// This test pins the v0.33.1.2 fix: change all three `.LiveResults`
// inside the `{{range .Tests}}` block to `$.LiveResults`.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"testing"
)

// stubSystemTest mimics admin.SystemTestDef (Name / Category /
// Description only — the test does NOT need Run or other fields).
// If the template accesses `.LiveResults` (or any other page-level
// field) on this struct, Execute returns
// `can't evaluate field LiveResults in type handlers.stubSystemTest`.
type stubSystemTest struct {
	Name        string
	Category    string
	Description string
}

// loadSystemTestsBody parses the system_tests.html body template
// with a minimal funcmap and returns it. Reused by both subtests.
func loadSystemTestsBody(t *testing.T) *template.Template {
	t.Helper()
	data, err := templatesFS.ReadFile("templates/admin/system_tests.html")
	if err != nil {
		t.Fatalf("read system_tests.html: %v", err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":    func(key string) string { return key },
		"tf":   func(key string, args ...any) string { return key },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeJSON": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
	}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse system_tests.html: %v", err)
	}
	return tpl
}

// TestSystemTestsRendersWithoutPanic — the v0.33.1.2 regression
// guard. Mimics the GET handler: page-level LiveResults and
// RecentRuns are nil. The buggy `{{if .LiveResults}}` inside
// `{{range .Tests}}` panics on the first iteration, failing the
// test. The fix (`$.LiveResults`) makes the inner check evaluate
// to nil → false → empty icon, no panic.
func TestSystemTestsRendersWithoutPanic(t *testing.T) {
	tpl := loadSystemTestsBody(t)
	data := map[string]any{
		"Tests": []stubSystemTest{
			{Name: "net.foo", Category: "network", Description: "test 1"},
			{Name: "db.bar", Category: "db", Description: "test 2"},
		},
		"RecentRuns":  nil,
		"LiveResults": nil,
		"LiveSummary": nil,
		"Page":        "admin/system_tests",
		"Title":       "Test Page",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-system_tests", data); err != nil {
		t.Fatalf("render body: %v\n\nv0.33.1.2 fix required: "+
			"change `.LiveResults` to `$.LiveResults` inside "+
			"the {{range .Tests}} block in "+
			"internal/handlers/templates/admin/system_tests.html "+
			"(3 occurrences at lines 56, 74, 83).", err)
	}
	if buf.Len() < 100 {
		t.Errorf("rendered body too small: %d bytes (want > 100) — "+
			"the template may have rendered only the form header "+
			"and bailed before the test grid", buf.Len())
	}
}

// TestSystemTestsRendersWithLiveResults — covers the POST path.
// After "Run all" is clicked, the handler passes LiveResults +
// LiveSummary to the page. The pre-fix panic happens regardless
// of whether LiveResults is nil or populated (the inner
// `{{if .LiveResults}}` runs for every row in the range), so
// this case also exercises the fix.
func TestSystemTestsRendersWithLiveResults(t *testing.T) {
	tpl := loadSystemTestsBody(t)
	type stubResult struct {
		Name     string
		Category string
		Status   string
		Output   string
		Duration string
	}
	data := map[string]any{
		"Tests": []stubSystemTest{
			{Name: "net.foo", Category: "network", Description: "test 1"},
		},
		"RecentRuns": nil,
		"LiveResults": []stubResult{
			{Name: "net.foo", Category: "network", Status: "pass", Output: "ok", Duration: "1ms"},
		},
		"LiveSummary": map[string]any{
			"Pass": 1, "Fail": 0, "Skip": 0, "Duration": "1ms",
		},
		"FlashSuccess": "pass=1 fail=0",
		"Page":         "admin/system_tests",
		"Title":        "Test Page",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-system_tests", data); err != nil {
		t.Fatalf("render with live results: %v", err)
	}
	if buf.Len() < 100 {
		t.Errorf("rendered body too small: %d bytes", buf.Len())
	}
}
