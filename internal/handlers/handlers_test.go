package handlers

// handlers_test.go — regression tests for the App-level
// renderWithLayout helper, in particular the auto-injection
// of ControlURL (v0.18.1) and other fields that the
// fragment templates reference via {{.X}} without the
// handler having to pass them.
//
// History: 2026-07-20 v0.18.1 — user reported the
// admin/exit-nodes tutorial rendered with an empty
// `--login-server=` because the template referenced
// {{.ControlURL}} but AdminExitNodes never passed it.
// Same bug in user/preauth_result.html (4 references).
// Fix: renderWithLayout now auto-injects ControlURL
// from a.ControlURL on every page. Handlers can still
// override it via the data map (caller values win in
// the merge loop).
//
// This test pins the contract: any future handler that
// calls renderWithLayout must get ControlURL populated
// without having to remember to add it to the data map.
//
// refactor-v0.30 Phase B step 6f (2026-07-29): the
// newTestApp helper used to live in handlers_my_telegram_test.go
// (now moved to feature/my). This file has its own minimal
// version that wires only the deps the renderWithLayout
// test needs (DB + AdminService).

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"skygate/internal/auth"
	"skygate/internal/i18n"
)

// memDBCounter6f increments per-test so each in-memory
// SQLite DB is isolated (see internal/handlers/handlers_my_telegram_test.go
// for the long-form explanation of why shared-cache mode is
// used here). Counter name is step-6f-specific to avoid
// collisions if a future step adds a new memDBCounter.
var memDBCounter6f int64

func newMemoryDB6f(t *testing.T) *sql.DB {
	t.Helper()
	n := atomic.AddInt64(&memDBCounter6f, 1)
	dsn := "file:skygate-test-render-6f-" + itoa(n) + "?mode=memory&cache=shared"
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	// Minimal schema: just enough for App.DB to be non-nil.
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS portal_users (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create portal_users: %v", err)
	}
	return d
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// newTestApp builds a minimal App for the renderWithLayout
// regression tests. The renderWithLayout path doesn't touch
// the headscale client or the bot, so we only need a DB +
// an AdminService wire (the wire is required so feature/admin
// thin-wrappers don't 500 — renderWithLayout isn't one of
// those, but the constructor sets a no-op wire in the
// absence of one).
func newTestApp(t *testing.T, notifier interface{}) (*App, *sql.DB) {
	t.Helper()
	d := newMemoryDB6f(t)
	a := &App{
		DB:       d,
		I18n:     i18n.New(),
		Notifier: nil, // notifier arg is intentionally ignored; the test doesn't trigger telegram code paths
	}
	// renderWithLayout reads the templates off a.templates,
	// so the test sets that explicitly via makeSyntheticTemplates.
	// No AdminService wire — renderWithLayout doesn't route
	// through any of the feature/admin methods.
	return a, d
}

// testNotifier is a minimal Notifier stub that satisfies
// the Notifier interface. The renderWithLayout tests
// don't trigger any telegram code path, but the
// `notifier interface{}` argument to newTestApp keeps
// the original test signature alive (call sites pass
// &testNotifier{} to make the intent explicit).
type testNotifier struct{}

// SendTelegram is unused by renderWithLayout. Stub.
func (testNotifier) SendTelegram(_ int64, _ string) error { return nil }

// SendAlert is unused by renderWithLayout. Stub.
func (testNotifier) SendAlert(_ string) error { return nil }

// makeSyntheticTemplates builds a minimal *Templates
// with a layout + a "login.html" body fragment that
// references {{.ControlURL}}. We add a "login.html"
// define so the layout's {{template .BodyTemplate .}}
// call resolves to something. The body just prints
// ControlURL so we can assert the auto-injection.
//
// The layout's {{template "body" .}} is the dispatch
// hook that real templates use. For this synthetic
// test we skip the renderBody funcmap by also
// defining a "body" stub.
func makeSyntheticTemplates() *Templates {
	t := template.Must(template.New("root").Parse(
		`{{define "layout"}}<html><body>{{template "body" .}}|{{template "login.html" .}}</body></html>{{end}}` +
			`{{define "body"}}body-stub{{end}}` +
			`{{define "login.html"}}url={{.ControlURL}}{{end}}`))
	return &Templates{t: t}
}

// TestRenderWithLayout_AutoInjectsControlURL — sets
// a.ControlURL on the App, then calls renderWithLayout
// directly with a synthetic template. The body must
// contain the URL. Before the v0.18.1 fix, this body
// was empty (the field was never set).
//
// We call renderWithLayout directly (not via a real
// page handler) so the test is independent of /login
// vs /admin/exit-nodes routing. /login uses the
// simpler `render` helper (no layout), so it wouldn't
// exercise the auto-inject anyway. The /admin/exit-
// nodes page uses renderWithLayout, but it requires
// HSGlobal + exit_servers + a lot of setup. The
// direct call is the cleanest unit test.
func TestRenderWithLayout_AutoInjectsControlURL(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.ControlURL = "https://head.skynas.ru"
	a.templates = makeSyntheticTemplates()

	req := httptest.NewRequest("GET", "/anything", nil)
	w := httptest.NewRecorder()

	// Call renderWithLayout directly. The c (claims)
	// can be nil for this test — we just want to
	// verify the auto-injection. An empty c skips
	// the Username/IsAdmin fields but ControlURL
	// is set unconditionally.
	a.renderWithLayout(w, req, "login.html", nil, map[string]any{})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// The synthetic body is "url={{.ControlURL}}"
	// which renders to "url=https://head.skynas.ru".
	if !strings.Contains(body, "https://head.skynas.ru") {
		t.Errorf("expected ControlURL injected into body, got %q", body)
	}
}

// TestRenderWithLayout_CallerCannotOverrideControlURL —
// documents the current merge order: the auto-inject
// runs AFTER the data map merge, so a.ControlURL
// always wins. This is fine for v0.18.1 (no per-user
// ControlURL yet). If a future refactor adds per-user
// ControlURL (e.g. v0.12.0+ per-user headscale) the
// order must be reversed — and this test should be
// updated to reflect the new contract.
func TestRenderWithLayout_CallerCannotOverrideControlURL(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.ControlURL = "https://head.skynas.ru"
	a.templates = makeSyntheticTemplates()

	req := httptest.NewRequest("GET", "/anything", nil)
	w := httptest.NewRecorder()
	// Caller tries to override with a different URL
	// via the data map. The auto-inject runs AFTER
	// the merge, so a.ControlURL wins. This is the
	// current contract; if a future refactor changes
	// the order (to support per-user ControlURL),
	// update this test.
	a.renderWithLayout(w, req, "login.html", &auth.Claims{Username: "x", IsAdmin: false}, map[string]any{
		"ControlURL": "https://other.example.com",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	// Auto-injection wins.
	if !strings.Contains(body, "https://head.skynas.ru") {
		t.Errorf("expected a.ControlURL injected (default wins), got %q", body)
	}
	if strings.Contains(body, "https://other.example.com") {
		t.Errorf("did not expect a caller-override value, got %q", body)
	}
}
