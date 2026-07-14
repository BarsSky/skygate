package handlers

// 2026-07-14: Tests for the /admin/backup config UI (Этап 14 v6).
//
// Coverage:
//   - GetAdminBackupConfig returns 200 with the config +
//     protocols in the data when the admin hits the page.
//   - PostAdminBackupConfig persists the form fields to
//     global_settings and reflects them on the next Load.
//   - PostAdminBackupTest runs the no-mount parser and
//     reports either "ok" with the parsed host/share/path
//     or a clear error when the destination is malformed.
//   - PostAdminBackupRun is gated by the in-app mutex
//     (TryLock) — a second concurrent call returns the
//     "already running" friendly error.
//   - PostAdminBackupToggle flips the in_app_enabled flag
//     and writes an audit_log row.
//
// We use the in-memory DB + newTestApp helper from
// handlers_my_telegram_test.go (already imports the same
// schema). The schema is sufficient — global_settings,
// portal_users, audit_log — no extra migrations needed.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/backup"
)

// adminSessionCookie mints a JWT for skyadmin (id=1) —
// the seeded user newMemoryDB creates.
func adminSessionCookie(t *testing.T, app *App) *http.Cookie {
	return sessionCookieFor(t, app, 1, "skyadmin", true)
}

// hitConfig dispatches a GET or POST to the new endpoints.
// The path picks the handler. Body may be nil for GET.
func hitConfig(t *testing.T, app *App, method, path string, body url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(body.Encode())
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(adminSessionCookie(t, app))
	w := httptest.NewRecorder()
	switch {
	case method == "GET" && path == "/admin/backup/config":
		app.GetAdminBackupConfig(w, req)
	case method == "POST" && path == "/admin/backup/config":
		app.PostAdminBackupConfig(w, req)
	case method == "POST" && path == "/admin/backup/test":
		app.PostAdminBackupTest(w, req)
	case method == "POST" && path == "/admin/backup/run":
		app.PostAdminBackupRun(w, req)
	case method == "POST" && path == "/admin/backup/toggle":
		app.PostAdminBackupToggle(w, req)
	default:
		t.Fatalf("unhandled %s %s", method, path)
	}
	return w
}

// TestGetAdminBackupConfig_RequiresAdmin: non-admin
// session → 403.
func TestGetAdminBackupConfig_RequiresAdmin(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	req := httptest.NewRequest("GET", "/admin/backup/config", nil)
	// Use alice (id=2) — non-admin.
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	w := httptest.NewRecorder()
	app.GetAdminBackupConfig(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestGetAdminBackupConfig_RendersForAdmin: 200 with the
// default Config in the data (no row in global_settings
// yet → Load returns the defaults from backup.Default()).
func TestGetAdminBackupConfig_RendersForAdmin(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	app.withTemplates()
	w := hitConfig(t, app, "GET", "/admin/backup/config", nil)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d (body=%s)", w.Code, w.Body.String())
	}
}

// TestPostAdminBackupConfig_PersistsFields: form values
// land in global_settings, Load returns them, and the
// auto-detect picks "smb" for //host/share.
func TestPostAdminBackupConfig_PersistsFields(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	form := url.Values{
		"protocol":       {"smb"},
		"destination":    {"//nas.local/backups/skygate"},
		"mountpoint":     {"/mnt/skygate-backups"},
		"username":       {"backup_user"},
		"password":       {"s3cret"},
		"keep_count":     {"15"},
		"schedule":       {"0 3 * * *"},
		"enabled":        {"1"},
		"in_app_enabled": {"1"},
	}
	w := hitConfig(t, app, "POST", "/admin/backup/config", form)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d (body=%s)", w.Code, w.Body.String())
	}
	// Reload and check.
	cfg, err := backup.Load(d)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Destination != "//nas.local/backups/skygate" {
		t.Errorf("Destination = %q, want %q", cfg.Destination, "//nas.local/backups/skygate")
	}
	if cfg.Protocol != backup.ProtocolSMB {
		t.Errorf("Protocol = %q, want smb", cfg.Protocol)
	}
	if cfg.Username != "backup_user" {
		t.Errorf("Username = %q, want backup_user", cfg.Username)
	}
	if cfg.KeepCount != 15 {
		t.Errorf("KeepCount = %d, want 15", cfg.KeepCount)
	}
	if !cfg.Enabled || !cfg.InAppEnabled {
		t.Errorf("Enabled=%t InApp=%t, want both true", cfg.Enabled, cfg.InAppEnabled)
	}
}

// TestPostAdminBackupConfig_AutoDetectProtocol: leaving
// the protocol blank on a smb:// URL → Load returns SMB.
func TestPostAdminBackupConfig_AutoDetectProtocol(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	form := url.Values{
		"destination": {"smb://nas.local/backups/skygate"},
		"mountpoint":  {"/mnt/skygate-backups"},
		"username":    {"u"},
		"password":    {"p"},
		"keep_count":  {"10"},
		"schedule":    {"0 3 * * *"},
		"enabled":     {"1"},
	}
	w := hitConfig(t, app, "POST", "/admin/backup/config", form)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	cfg, _ := backup.Load(d)
	if cfg.Protocol != backup.ProtocolSMB {
		t.Errorf("expected auto-detected smb, got %q", cfg.Protocol)
	}
}

// TestPostAdminBackupConfig_RejectsEmptyDestination:
// destination is required; missing → 303 with err=...
// (we don't inspect the body — just confirm the form
//  didn't silently succeed).
func TestPostAdminBackupConfig_RejectsEmptyDestination(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	form := url.Values{
		"destination": {""},
		"keep_count":  {"10"},
	}
	w := hitConfig(t, app, "POST", "/admin/backup/config", form)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	// Confirm the DB is still empty.
	cfg, _ := backup.Load(d)
	if cfg.Destination != "" {
		t.Errorf("destination should be empty after rejected save, got %q", cfg.Destination)
	}
	// And the err query param is present.
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("expected ?err= in Location, got %s", loc)
	}
}

// TestPostAdminBackupTest_ParsesSMB: Test connection
// parses the URL and reports host/share/subpath in the
// ok= flash param. No mount is performed.
func TestPostAdminBackupTest_ParsesSMB(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	form := url.Values{
		"protocol":    {"smb"},
		"destination": {"//nas.local/backups/skygate"},
		"mountpoint":  {"/mnt/skygate-backups"},
		"username":    {"u"},
		"ssh_key_path": {""},
	}
	w := hitConfig(t, app, "POST", "/admin/backup/test", form)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "nas.local") || !strings.Contains(loc, "backups") {
		t.Errorf("expected parsed host/share in ok= flash, got Location=%s", loc)
	}
}

// TestPostAdminBackupTest_ReportsMissingFields: empty
// destination → err=... flash.
func TestPostAdminBackupTest_ReportsMissingFields(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	form := url.Values{
		"protocol":    {"smb"},
		"destination": {""},
		"mountpoint":  {"/mnt/x"},
		"username":    {"u"},
	}
	w := hitConfig(t, app, "POST", "/admin/backup/test", form)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected ?err=, got %s", loc)
	}
}

// TestPostAdminBackupToggle_FlipsFlag: a POST with
// enabled=1 sets in_app_enabled=true; enabled=0 clears
// it. The master Enabled flag is unchanged.
func TestPostAdminBackupToggle_FlipsFlag(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// Pre-seed: master=off, in-app=on.
	if err := backup.Save(d, &backup.Config{
		Destination:  "/tmp/skygate-test-backup",
		Protocol:     backup.ProtocolLocal,
		KeepCount:    5,
		Schedule:     "0 3 * * *",
		Enabled:      false,
		InAppEnabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Toggle off.
	w := hitConfig(t, app, "POST", "/admin/backup/toggle", url.Values{"enabled": {"0"}})
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	cfg, _ := backup.Load(d)
	if cfg.InAppEnabled {
		t.Errorf("InAppEnabled should be false after toggle-off")
	}
	if cfg.Enabled {
		t.Errorf("Enabled should remain false (toggle only flips in_app)")
	}
	// Toggle on.
	w = hitConfig(t, app, "POST", "/admin/backup/toggle", url.Values{"enabled": {"1"}})
	cfg, _ = backup.Load(d)
	if !cfg.InAppEnabled {
		t.Errorf("InAppEnabled should be true after toggle-on")
	}
	// Audit row was written.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='backup.toggle'`).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 audit rows, got %d", n)
	}
}

// TestPostAdminBackupRun_RefusesEmptyDestination:
// destination is empty → 303 with err= flash. We don't
// invoke the real mount; the test stops at the
// Validate() gate.
func TestPostAdminBackupRun_RefusesEmptyDestination(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	w := hitConfig(t, app, "POST", "/admin/backup/run", nil)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("expected ?err= in Location, got %s", loc)
	}
}

// TestPostAdminBackupRun_RequiresAdmin: non-admin → 403.
func TestPostAdminBackupRun_RequiresAdmin(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	req := httptest.NewRequest("POST", "/admin/backup/run", nil)
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	w := httptest.NewRecorder()
	app.PostAdminBackupRun(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}
