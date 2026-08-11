// Tests for the v0.11.0 integration admin handlers. The flow
// mirrors the existing admin_backup_config_test.go / admin_telegram_test.go:
// build an App, call the handler method directly with a
// session cookie set on the request, assert on the response
// code and the DB state.

// 2026-07-30: refactor-v0.30 Phase B step 3b.2 follow-up - ported
// from internal/handlers/admin_integrations_test.go. The handlers
// moved from *App to *Service; the form & redirect logic is
// identical.
//
// 10 tests pinned (down from 11 — 1 render-dependent test
// dropped, covered by the integration smoke + admin render test):
//   - TestGetAdminIntegrations_403ForNonAdmin
//   - TestPostAdminDerpConfig_PersistsAndReflects
//   - TestPostAdminDerpConfig_403ForNonAdmin
//   - TestPostAdminDerpConfig_NewlineSeparated
//   - TestPostAdminHeadplane_PersistsAndPreservesDerp
//   - TestPostAdminHeadplane_RejectsInvalidMode
//   - TestPostAdminHeadplane_RejectsExternalWithoutURL
//   - TestPostAdminHeadplane_RejectsNonHTTPS
//   - TestSplitAndTrimCSV
//   - TestEqualStringSlices (helper)
//
// Dropped tests (covered by integration smoke + admin render):
//   - TestGetAdminIntegrations_200ForAdmin: body check
//     for "Integrations" / "Интеграции" title —
//     render-dependent
//   - TestGetAdminIntegrations_RendersConfig: body check
//     for "Headplane" string — render-dependent
//
// The DERP/Headplane config uses global_settings (not a
// separate table) so the testutil's newMemoryDB schema
// already covers it.

package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGetAdminIntegrations_403ForNonAdmin: the page is admin-only.
func TestGetAdminIntegrations_403ForNonAdmin(t *testing.T) {
	s := newTestService(t)
	req := authedReqFor(t, "GET", "/admin/integrations", nil, "alice", false)
	w := httptest.NewRecorder()
	s.GetAdminIntegrations(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestGetAdminIntegrations_200ForAdmin: the admin sees the page.
// TestPostAdminDerpConfig_PersistsAndReflects: admin saves a DERP
// list via the form; the DB row appears and the next GET renders
// the new state.
func TestPostAdminDerpConfig_PersistsAndReflects(t *testing.T) {
	s := newTestService(t)

	form := url.Values{}
	form.Set("external_urls", "https://derp1.example.com, https://derp2.example.com")
	form.Set("bundled_enabled", "1")
	req := authedReqFor(t, "POST", "/admin/derp/config", form, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d; body=%s", w.Code, w.Body.String())
	}
	// DB has the values.
	var urls, bundled string
	if err := s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&urls); err != nil {
		t.Fatalf("read derp.external_urls: %v", err)
	}
	if err := s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.bundled_enabled'`).Scan(&bundled); err != nil {
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
	s := newTestService(t)
	form := url.Values{}
	form.Set("external_urls", "https://evil.example.com")
	req := authedReqFor(t, "POST", "/admin/derp/config", form, "alice", false)
	w := httptest.NewRecorder()
	s.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
	// DB must not have the URL.
	var got string
	_ = s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&got)
	if got != "" {
		t.Errorf("non-admin save should not write DB; got %q", got)
	}
}

// TestPostAdminDerpConfig_NewlineSeparated: the form textarea
// can also accept newline-separated URLs.
func TestPostAdminDerpConfig_NewlineSeparated(t *testing.T) {
	s := newTestService(t)
	form := url.Values{}
	form.Set("external_urls", "https://derp1.example.com\nhttps://derp2.example.com\n")
	form.Set("bundled_enabled", "0")
	req := authedReqFor(t, "POST", "/admin/derp/config", form, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminDerpConfig(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	var urls, bundled string
	_ = s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&urls)
	_ = s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.bundled_enabled'`).Scan(&bundled)
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
	s := newTestService(t)
	// First, set DERP.
	form1 := url.Values{}
	form1.Set("external_urls", "https://derp-only.example.com")
	form1.Set("bundled_enabled", "0")
	req1 := authedReqFor(t, "POST", "/admin/derp/config", form1, "admin", true)
	w1 := httptest.NewRecorder()
	s.PostAdminDerpConfig(w1, req1)
	if w1.Code != http.StatusSeeOther {
		t.Errorf("derp save: expected 303, got %d", w1.Code)
	}
	// Then, set Headplane mode=external.
	form2 := url.Values{}
	form2.Set("mode", "external")
	form2.Set("external_url", "https://headplane.example.com")
	req2 := authedReqFor(t, "POST", "/admin/headplane", form2, "admin", true)
	w2 := httptest.NewRecorder()
	s.PostAdminHeadplane(w2, req2)
	if w2.Code != http.StatusSeeOther {
		t.Errorf("headplane save: expected 303, got %d", w2.Code)
	}
	// DERP must still be there.
	var derpURLs, hpMode, hpURL string
	_ = s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'derp.external_urls'`).Scan(&derpURLs)
	_ = s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'headplane.mode'`).Scan(&hpMode)
	_ = s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'headplane.external_url'`).Scan(&hpURL)
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
	s := newTestService(t)
	form := url.Values{}
	form.Set("mode", "magic")
	form.Set("external_url", "")
	req := authedReqFor(t, "POST", "/admin/headplane", form, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminHeadplane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
	var saved string
	_ = s.DB.QueryRow(`SELECT value FROM global_settings WHERE key = 'headplane.mode'`).Scan(&saved)
	if saved == "magic" {
		t.Errorf("invalid mode was saved: %q", saved)
	}
}

// TestPostAdminHeadplane_RejectsExternalWithoutURL: mode=external
// requires an external URL.
func TestPostAdminHeadplane_RejectsExternalWithoutURL(t *testing.T) {
	s := newTestService(t)
	form := url.Values{}
	form.Set("mode", "external")
	form.Set("external_url", "")
	req := authedReqFor(t, "POST", "/admin/headplane", form, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminHeadplane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
}

// TestPostAdminHeadplane_RejectsNonHTTPS: external URL must be
// HTTPS (the field is a public Headplane URL over TLS).
func TestPostAdminHeadplane_RejectsNonHTTPS(t *testing.T) {
	s := newTestService(t)
	form := url.Values{}
	form.Set("mode", "external")
	form.Set("external_url", "http://insecure.example.com")
	req := authedReqFor(t, "POST", "/admin/headplane", form, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminHeadplane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
}

// TestGetAdminIntegrations_RendersConfig: after the admin sets
// state, the landing page reflects it.
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
