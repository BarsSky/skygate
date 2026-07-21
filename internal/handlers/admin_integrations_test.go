// Tests for the v0.11.0 integration admin handlers. The flow
// mirrors the existing admin_backup_config_test.go / admin_telegram_test.go:
// build an App, call the handler method directly with a
// session cookie set on the request, assert on the response
// code and the DB state.

package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// authedReqFor builds an *http.Request with the right session
// cookie for the named user. The caller is responsible for
// calling the handler method (Get/Post) and inspecting the
// recorder.
//
// 2026-07-15: Этап 14 v14 (v0.11.0) — uses the existing
// sessionCookieFor helper from handlers_my_telegram_test.go.
// userID + isAdmin are looked up from the username
// (skyadmin = admin, alice = non-admin).
func authedReqFor(t *testing.T, app *App, method, path string, form url.Values, username string) *http.Request {
	t.Helper()
	var cookie *http.Cookie
	switch username {
	case "skyadmin":
		cookie = sessionCookieFor(t, app, 1, username, true)
	case "alice":
		cookie = sessionCookieFor(t, app, 2, username, false)
	default:
		t.Fatalf("unknown username %q (only skyadmin + alice are pre-seeded)", username)
	}
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.AddCookie(cookie)
	return req
}

// TestGetAdminIntegrations_403ForNonAdmin: the page is admin-only.
func TestGetAdminIntegrations_403ForNonAdmin(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	req := authedReqFor(t, app, "GET", "/admin/integrations", nil, "alice")
	w := httptest.NewRecorder()
	app.GetAdminIntegrations(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestGetAdminIntegrations_200ForAdmin: the admin sees the page.
func TestGetAdminIntegrations_200ForAdmin(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	req := authedReqFor(t, app, "GET", "/admin/integrations", nil, "skyadmin")
	w := httptest.NewRecorder()
	app.withTemplates()
	app.GetAdminIntegrations(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Интеграции") && !strings.Contains(body, "Integrations") {
		t.Errorf("expected integrations title, got body: %s", body)
	}
}

// TestPostAdminDerpConfig_PersistsAndReflects: admin saves a DERP
// list via the form; the DB row appears and the next GET renders
// the new state.
func TestPostAdminDerpConfig_PersistsAndReflects(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()

	form := url.Values{}
	form.Set("external_urls", "https://derp1.example.com, https://derp2.example.com")
	form.Set("bundled_enabled", "1")
	req := authedReqFor(t, app, "POST", "/admin/derp/config", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d; body=%s", w.Code, w.Body.String())
	}
	// DB has the values.
	var urls, bundled string
	if err := d.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&urls); err != nil {
		t.Fatalf("read derp.external_urls: %v", err)
	}
	if err := d.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.bundled_enabled'`).Scan(&bundled); err != nil {
		t.Fatalf("read derp.bundled_enabled: %v", err)
	}
	if !strings.Contains(urls, "derp1.example.com") || !strings.Contains(urls, "derp2.example.com") {
		t.Errorf("derp.external_urls = %q, want both derp1+derp2", urls)
	}
	if bundled != "1" {
		t.Errorf("derp.bundled_enabled = %q, want 1", bundled)
	}
}

// TestPostAdminDerpConfig_403ForNonAdmin: only admin can save.
func TestPostAdminDerpConfig_403ForNonAdmin(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	form := url.Values{}
	form.Set("external_urls", "https://evil.example.com")
	req := authedReqFor(t, app, "POST", "/admin/derp/config", form, "alice")
	w := httptest.NewRecorder()
	app.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	// DB must not have the URL.
	var got string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&got)
	if got != "" {
		t.Errorf("non-admin save should not write DB; got %q", got)
	}
}

// TestPostAdminDerpConfig_NewlineSeparated: the form textarea
// can also accept newline-separated URLs.
func TestPostAdminDerpConfig_NewlineSeparated(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	form := url.Values{}
	form.Set("external_urls", "https://derp1.example.com\nhttps://derp2.example.com\n")
	form.Set("bundled_enabled", "0")
	req := authedReqFor(t, app, "POST", "/admin/derp/config", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	var urls, bundled string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&urls)
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.bundled_enabled'`).Scan(&bundled)
	if !strings.Contains(urls, "derp1.example.com") || !strings.Contains(urls, "derp2.example.com") {
		t.Errorf("newline-separated URLs not saved: %q", urls)
	}
	if bundled != "0" {
		t.Errorf("bundled_enabled = %q, want 0", bundled)
	}
}

// TestPostAdminHeadplane_PersistsAndPreservesDerp: saving the
// Headplane form must not clobber the DERP config the admin
// just set on the same session.
func TestPostAdminHeadplane_PersistsAndPreservesDerp(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	// First, set DERP.
	form1 := url.Values{}
	form1.Set("external_urls", "https://derp-only.example.com")
	form1.Set("bundled_enabled", "0")
	req1 := authedReqFor(t, app, "POST", "/admin/derp/config", form1, "skyadmin")
	w1 := httptest.NewRecorder()
	app.PostAdminDerpConfig(w1, req1)
	if w1.Code != http.StatusSeeOther {
		t.Errorf("derp save: expected 303, got %d", w1.Code)
	}
	// Then, set Headplane mode=external.
	form2 := url.Values{}
	form2.Set("mode", "external")
	form2.Set("external_url", "https://headplane.example.com")
	req2 := authedReqFor(t, app, "POST", "/admin/headplane", form2, "skyadmin")
	w2 := httptest.NewRecorder()
	app.PostAdminHeadplane(w2, req2)
	if w2.Code != http.StatusSeeOther {
		t.Errorf("headplane save: expected 303, got %d", w2.Code)
	}
	// DERP must still be there.
	var derpURLs, hpMode, hpURL string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&derpURLs)
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'headplane.mode'`).Scan(&hpMode)
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'headplane.external_url'`).Scan(&hpURL)
	if derpURLs != "https://derp-only.example.com" {
		t.Errorf("DERP clobbered by headplane save: %q", derpURLs)
	}
	if hpMode != "external" {
		t.Errorf("headplane.mode = %q, want external", hpMode)
	}
	if hpURL != "https://headplane.example.com" {
		t.Errorf("headplane.external_url = %q, want https://headplane.example.com", hpURL)
	}
}

// TestPostAdminHeadplane_RejectsInvalidMode: mode other than
// bundled/external/off must be rejected.
func TestPostAdminHeadplane_RejectsInvalidMode(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	form := url.Values{}
	form.Set("mode", "magic")
	form.Set("external_url", "")
	req := authedReqFor(t, app, "POST", "/admin/headplane", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminHeadplane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
	var saved string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'headplane.mode'`).Scan(&saved)
	if saved == "magic" {
		t.Errorf("invalid mode was saved: %q", saved)
	}
}

// TestPostAdminHeadplane_RejectsExternalWithoutURL: mode=external
// requires an external URL.
func TestPostAdminHeadplane_RejectsExternalWithoutURL(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	form := url.Values{}
	form.Set("mode", "external")
	form.Set("external_url", "")
	req := authedReqFor(t, app, "POST", "/admin/headplane", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminHeadplane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
}

// TestPostAdminHeadplane_RejectsNonHTTPS: external URL must be
// HTTPS (the field is a public Headplane URL over TLS).
func TestPostAdminHeadplane_RejectsNonHTTPS(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	form := url.Values{}
	form.Set("mode", "external")
	form.Set("external_url", "http://insecure.example.com")
	req := authedReqFor(t, app, "POST", "/admin/headplane", form, "skyadmin")
	w := httptest.NewRecorder()
	app.PostAdminHeadplane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
}

// TestGetAdminIntegrations_RendersConfig: after the admin sets
// state, the landing page reflects it.
func TestGetAdminIntegrations_RendersConfig(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	// Pre-seed: DERP + Headplane external.
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('derp.external_urls', 'https://x.example.com')`)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('headplane.mode', 'external')`)
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('headplane.external_url', 'https://h.example.com')`)

	req := authedReqFor(t, app, "GET", "/admin/integrations", nil, "skyadmin")
	w := httptest.NewRecorder()
	app.withTemplates()
	app.GetAdminIntegrations(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "x.example.com") {
		t.Errorf("expected x.example.com in DERP list, got: %s", body)
	}
	if !strings.Contains(body, "h.example.com") {
		t.Errorf("expected h.example.com in Headplane list, got: %s", body)
	}
	if !strings.Contains(body, "Внешний") && !strings.Contains(body, "External") {
		t.Errorf("expected External status label, got: %s", body)
	}
}

// TestSplitAndTrimCSV is the form-side parser (mirrors
// db.splitCSV but handles newlines too).
func TestSplitAndTrimCSV(t *testing.T) {
	cases := []struct {
		name, in string
		want     []string
	}{
		{"empty", "", nil},
		{"single", "a", []string{"a"}},
		{"two commas", "a,b", []string{"a", "b"}},
		{"two newlines", "a\nb", []string{"a", "b"}},
		{"crlf", "a\r\nb", []string{"a", "b"}},
		{"whitespace", "  a  ,\n  b  ", []string{"a", "b"}},
		{"mixed", "a, b\nc", []string{"a", "b", "c"}},
		{"empty entries", "a,,b,", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitAndTrimCSV(c.in)
		if !equalStringSlices(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
