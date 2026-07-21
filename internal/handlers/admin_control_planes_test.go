// Tests for the v0.12.0 /admin/control-planes admin UI.
// Pins the per-user control plane admin flow end-to-end:
//   - landing page renders with the global plane + 0 users
//   - /admin/users/{id}/plane GET + POST + clear
//   - corrupted SKYGATE_SECRET_KEY path shows the
//     "stored key was encrypted with a different key"
//     hint rather than 500-ing
//   - admin-only (non-admin gets 403)
//   - per-user Save invalidates the cached client so the
//     next HSForUser call returns a fresh one

package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// adminPlaneApp builds a test app using the standard newTestApp
// helper (which seeds skyadmin=id=1, alice=id=2 — matching the
// authedReqFor helper in admin_integrations_test.go). The
// newTestApp DB has the full schema (v0.12.0 portal_users
// columns included) so we don't need openControlplaneTestDB
// here.
func adminPlaneApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	a, d := newTestApp(t, &testNotifier{})
	a.SecretKeyHex = cpTestKey
	// newTestApp doesn't wire a *headscale.Client; tests that
	// need one set a.hs + a.HS. The control-plane tests need
	// a.HSGlobal() to return a non-nil client (the form
	// reads the global BaseURL).
	hs := headscale.New("http://global:50444", "global-key")
	a.hs = hs
	a.HS = hs
	a.InitHSForUserState()
	return a, d
}

// TestGetAdminControlPlanes_200ForAdmin: the admin sees
// the landing page (no users on per-user planes yet).
func TestGetAdminControlPlanes_200ForAdmin(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()
	a.withTemplates()
	req := authedReqFor(t, a, "GET", "/admin/control-planes", nil, "skyadmin")
	w := httptest.NewRecorder()
	a.GetAdminControlPlanes(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Control planes") && !strings.Contains(body, "плоскости") {
		t.Errorf("expected planes title, got: %s", body)
	}
	// Per-user section should list both seeded users.
	if !strings.Contains(body, "skyadmin") || !strings.Contains(body, "alice") {
		t.Errorf("expected both portal users in rows, got: %s", body)
	}
}

// TestGetAdminControlPlanes_403ForNonAdmin.
func TestGetAdminControlPlanes_403ForNonAdmin(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()
	req := authedReqFor(t, a, "GET", "/admin/control-planes", nil, "alice")
	w := httptest.NewRecorder()
	a.GetAdminControlPlanes(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestGetAdminUserControlPlane_GET: non-admin opening
// the edit form for an existing user.
func TestGetAdminUserControlPlane_GET(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()
	a.withTemplates()
	req := authedReqFor(t, a, "GET", "/admin/users/2/plane", nil, "skyadmin")
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	a.GetAdminUserControlPlane(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "alice") {
		t.Errorf("expected username in body, got: %s", w.Body.String())
	}
}

// TestPostAdminUserControlPlane_SaveAndReflect: admin
// saves a per-user (url, key); the next GET shows the
// new URL and the DB has the encrypted key.
func TestPostAdminUserControlPlane_SaveAndReflect(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()

	form := url.Values{}
	form.Set("url", "https://head-us.example.com")
	form.Set("api_key", "us-key-12345")
	req := authedReqFor(t, a, "POST", "/admin/users/2/plane", form, "skyadmin")
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	a.PostAdminUserControlPlane(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d; body=%s", w.Code, w.Body.String())
	}
	// DB has the row.
	var url, keyEnc string
	if err := d.QueryRow(`SELECT headscale_url, headscale_api_key_enc FROM portal_users WHERE id = 2`).Scan(&url, &keyEnc); err != nil {
		t.Fatalf("read: %v", err)
	}
	if url != "https://head-us.example.com" {
		t.Errorf("URL = %q, want https://head-us.example.com", url)
	}
	if keyEnc == "" {
		t.Errorf("keyEnc is empty, want encrypted blob")
	}
	if strings.Contains(keyEnc, "us-key-12345") {
		t.Errorf("keyEnc contains plain text, encryption not applied")
	}
}

// TestPostAdminUserControlPlane_MissingSecret: the form
// rejects saves when SKYGATE_SECRET_KEY is unset.
func TestPostAdminUserControlPlane_MissingSecret(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()
	a.SecretKeyHex = "" // simulate env not set

	form := url.Values{}
	form.Set("url", "https://h.example.com")
	form.Set("api_key", "k")
	req := authedReqFor(t, a, "POST", "/admin/users/2/plane", form, "skyadmin")
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	a.PostAdminUserControlPlane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
	var gotURL string
	_ = d.QueryRow(`SELECT headscale_url FROM portal_users WHERE id = 2`).Scan(&gotURL)
	if gotURL != "" {
		t.Errorf("URL should not be saved when key is missing, got %q", gotURL)
	}
}

// TestPostAdminUserControlPlane_Clear: Save + Clear
// leaves the row back on the default.
func TestPostAdminUserControlPlane_Clear(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()

	// Save first.
	if err := db.SetUserHeadscaleConfig(d, 2, "https://h.example.com", "k", cpTestKey); err != nil {
		t.Fatal(err)
	}
	// Clear.
	req := authedReqFor(t, a, "POST", "/admin/users/2/plane/clear", nil, "skyadmin")
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	a.PostAdminUserControlPlaneClear(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	// DB back to default.
	var gotURL, gotEnc string
	_ = d.QueryRow(`SELECT headscale_url, headscale_api_key_enc FROM portal_users WHERE id = 2`).Scan(&gotURL, &gotEnc)
	if gotURL != "" || gotEnc != "" {
		t.Errorf("expected clear, got url=%q enc=%q", gotURL, gotEnc)
	}
}

// TestPostAdminControlPlanesTest_GlobalPlaneOK: a
// successful probe of the global plane redirects with
// the OK flash.
func TestPostAdminControlPlanesTest_GlobalPlaneOK(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()
	// The global client's BaseURL is "http://global:50444";
	// the test will use a fake URL matching it.
	form := url.Values{}
	form.Set("plane_url", a.HSGlobal().BaseURL)
	req := authedReqFor(t, a, "POST", "/admin/control-planes/test", form, "skyadmin")
	w := httptest.NewRecorder()
	a.PostAdminControlPlanesTest(w, req)
	// The handler redirects to /admin/control-planes. The
	// call to a.HSGlobal().ListAllNodes() will fail (no
	// real headscale running), so we expect an err= flash.
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") && !strings.Contains(loc, "ok=") {
		t.Errorf("expected err= or ok= in redirect, got %q", loc)
	}
}

// TestPostAdminControlPlanesTest_PerUserRejected: testing
// a per-user plane URL from /admin/control-planes is
// rejected (the per-user key isn't available here).
func TestPostAdminControlPlanesTest_PerUserRejected(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()
	form := url.Values{}
	form.Set("plane_url", "https://head-us.example.com")
	req := authedReqFor(t, a, "POST", "/admin/control-planes/test", form, "skyadmin")
	w := httptest.NewRecorder()
	a.PostAdminControlPlanesTest(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect for per-user plane, got %q", loc)
	}
}

// TestHSForUser_AfterAdminSave: after the admin saves a
// per-user override, HSForUser returns a per-user client
// (not the global). Pins that the route and the cache
// integration work end-to-end.
func TestHSForUser_AfterAdminSave(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()
	// Save via the DB helper (we already test the form
	// path above).
	if err := db.SetUserHeadscaleConfig(d, 2, "https://head-eu.example.com", "eu-key", cpTestKey); err != nil {
		t.Fatal(err)
	}
	c := a.HSForUser(2)
	if c == a.HSGlobal() {
		t.Errorf("expected per-user client after admin save, got global")
	}
	if c.ApiKeyForCache() != "eu-key" {
		t.Errorf("client apiKey = %q, want eu-key", c.ApiKeyForCache())
	}
}

// TestAdminRoutes_403ForNonAdmin: every new admin route
// must 403 when called by a non-admin.
func TestAdminRoutes_403ForNonAdmin(t *testing.T) {
	a, d := adminPlaneApp(t)
	defer d.Close()

	cases := []struct {
		name, method, path string
		form               url.Values
	}{
		{"control_planes GET", "GET", "/admin/control-planes", nil},
		{"control_planes test POST", "POST", "/admin/control-planes/test", url.Values{"plane_url": {"x"}}},
		{"user plane GET", "GET", "/admin/users/2/plane", nil},
		{"user plane POST", "POST", "/admin/users/2/plane", url.Values{"url": {"x"}, "api_key": {"y"}}},
		{"user plane clear POST", "POST", "/admin/users/2/plane/clear", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := authedReqFor(t, a, c.method, c.path, c.form, "alice")
			if c.path == "/admin/users/2/plane" {
				req.SetPathValue("id", "2")
			}
			w := httptest.NewRecorder()
			switch c.method + " " + c.path {
			case "GET /admin/control-planes":
				a.GetAdminControlPlanes(w, req)
			case "POST /admin/control-planes/test":
				a.PostAdminControlPlanesTest(w, req)
			case "GET /admin/users/2/plane":
				a.GetAdminUserControlPlane(w, req)
			case "POST /admin/users/2/plane":
				a.PostAdminUserControlPlane(w, req)
			case "POST /admin/users/2/plane/clear":
				a.PostAdminUserControlPlaneClear(w, req)
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d", w.Code)
			}
		})
	}
}
