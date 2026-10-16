package handlers

// layout_bell_b392_test.go — B392 (2026-10-16).
//
// A `gt` in html/template FAILS THE WHOLE RENDER when its argument is missing:
//
//	render: template: layout.html:276:15: executing "layout" at <gt .UnreadMore 0>:
//	error calling gt: invalid type for comparison
//
// The notification bell lives in the LAYOUT, so this is not a page-local mistake — a
// user with nothing unread got a page with no body at all. The contract could not see
// it (it greps the template text) and neither could the db/page unit tests; the local
// end-to-end run found it, and this test is the regression guard: render the REAL
// layout with the data the handler guarantees and require the body to reach the end.
//
// The invariant it pins is therefore about the HANDLER as much as the template: every
// key the layout compares with gt/lt/eq must be present in the map even when its value
// is zero. The pairs are:
//
//	.UnreadCount / .UnreadMore          (the bell)
//	.OpenAlerts                         (the admin inbox badge)
//
// The test renders both branches: a user with nothing unread (the case that broke) and
// an admin whose alert count query returned nothing.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"
	"testing"

	"skygate/internal/notifications"
)

// loadLayout builds the production layout with the real embedded templates. The funcmap
// mirrors the one LoadTemplates registers — a template that calls a helper this map lacks
// fails HERE instead of on the page, which is the same signal the other render tests give.
// The catalogue lookups return the key: the subject is template EXECUTION, not translation.
func loadLayout(t *testing.T) *template.Template {
	t.Helper()
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":        func(key string) string { return key },
		"tf":       func(key string, args ...any) string { return key },
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeJSON": func(s string) template.JS {
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
		"tolower":        strings.ToLower,
		"add":            func(a, b int) int { return a + b },
		"sub":            func(a, b int) int { return a - b },
		"divceil":        func(a, b int) int { return (a + b - 1) / b },
		"dividefloat":    func(a, b float64) float64 { return a / b },
		"usageLevel":     func(count, max int) string { return "success" },
		"datetimeformat": func(unix int64) string { return "2026-01-01 00:00" },
		"bytesfmt":       func(n int64) string { return "0 B" },
		// renderBody is the layout's body slot; production swaps in the real
		// implementation after parsing the bodies. Here it renders nothing, because
		// this test is about the layout surviving its own comparisons.
		"renderBody": func(name string, data any) (template.HTML, error) { return "", nil },
	}).ParseFS(templatesFS, "templates/layout.html", "templates/dashboard.html")
	if err != nil {
		t.Fatalf("parse layout: %v", err)
	}
	return tpl
}

// baseLayoutData is what renderWithLayout always provides, minus the two counts under
// test — the fields the layout touches for every user.
func baseLayoutData() map[string]any {
	return map[string]any{
		"Lang":                "ru",
		"Page":                "dashboard",
		"Username":            "admin",
		"IsAdmin":             false,
		"Theme":               "linear",
		"ThemeLabel":          "Linear",
		"DisplayFont":         "inter",
		"DisplayScale":        100,
		"DisplaySelBg":        "accent",
		"Version":             "v0.0-test",
		"ControlURL":          "https://head.example.com",
		"UnreadNotifications": []notifications.View{},
		"TailnetMetrics":      nil,
		// The layout's body slot (renderWithLayout always sets these two).
		"BodyTemplate": "body-dashboard",
		"Title":        "Dashboard",
	}
}

// TestLayoutBell_RendersWithNothingUnread_B392 — the exact failure: no unread rows, so
// the handler must still have supplied UnreadMore (as 0). Without it the render stops
// inside the bell and the page body never appears.
func TestLayoutBell_RendersWithNothingUnread_B392(t *testing.T) {
	tpl := loadLayout(t)
	data := baseLayoutData()
	data["UnreadCount"] = 0
	data["UnreadMore"] = 0

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("layout render failed with nothing unread — a missing `gt` argument breaks the whole page: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `id="notif-bell"`) {
		t.Error("the bell is missing from the rendered layout")
	}
	if !strings.Contains(out, "notif.empty") {
		t.Error("the empty-state line is missing when there is nothing unread")
	}
	// The failure this guards against truncated the layout, so the assertion is that the
	// render REACHED THE END. (renderBody is stubbed here, so the body itself is empty by
	// construction — the point is that the layout survived its own comparisons.)
	if !strings.HasSuffix(strings.TrimSpace(out), "</html>") {
		t.Errorf("the layout did not render to the end (a bell error truncates the page); tail: %q", tailOf(out, 200))
	}
}

// TestLayoutBell_RendersWithUnreadRows_B392 — the normal path, plus the "…and N more"
// line that only appears when the badge exceeds the rows the dropdown carries.
func TestLayoutBell_RendersWithUnreadRows_B392(t *testing.T) {
	tpl := loadLayout(t)
	data := baseLayoutData()
	data["UnreadCount"] = 300
	data["UnreadMore"] = 250
	data["UnreadNotifications"] = []notifications.View{
		{TimeAgo: "5 мин назад", Icon: "fa-key"},
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("layout render failed with unread rows: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"notif-badge", "notif-time", "notif.more_unread", "notif.view_all"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered bell is missing %q", want)
		}
	}
	if !strings.Contains(out, `href="/my/notifications"`) {
		t.Error("the bell does not link to the full list")
	}
}

// TestLayoutAdminBadge_RendersWithZeroAlerts_B392 — the same class for the admin inbox
// badge: a non-admin never gets OpenAlerts set, and an admin gets it only when the
// count query succeeded, so the template must tolerate 0.
func TestLayoutAdminBadge_RendersWithZeroAlerts_B392(t *testing.T) {
	tpl := loadLayout(t)
	for _, isAdmin := range []bool{false, true} {
		data := baseLayoutData()
		data["IsAdmin"] = isAdmin
		data["UnreadCount"] = 0
		data["UnreadMore"] = 0
		data["OpenAlerts"] = 0

		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
			t.Fatalf("layout render failed (isAdmin=%v, OpenAlerts=0): %v", isAdmin, err)
		}
		out := buf.String()
		if isAdmin && !strings.Contains(out, "/admin/monitor") {
			t.Error("the admin sidebar lost the monitoring inbox link")
		}
		if isAdmin && strings.Contains(out, "notif-badge\">0") {
			t.Error("a zero alert count must not render a badge")
		}
	}
}

// TestLayoutNavHasNotificationsEntry_B392 — the page existed since B157.1 with no way to
// reach it. Both the entry and its highlight key are pinned here.
func TestLayoutNavHasNotificationsEntry_B392(t *testing.T) {
	tpl := loadLayout(t)
	data := baseLayoutData()
	data["Page"] = "my/notifications"
	data["UnreadCount"] = 3
	data["UnreadMore"] = 0
	data["OpenAlerts"] = 0

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		t.Fatalf("layout render failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `href="/my/notifications"`) {
		t.Error("no sidebar entry for /my/notifications")
	}
	if !strings.Contains(out, `class="active"`) {
		t.Error("the notifications entry is not highlighted when .Page is my/notifications (pageFromName must map it)")
	}
}

// tailOf returns the last n characters, for a failure message that shows WHERE the
// render stopped.
func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
