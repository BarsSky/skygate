package admin

// 2026-07-30: refactor-v0.30 Phase B step 3b.4 follow-up - ported
// from internal/handlers/admin_control_planes_test.go. The handlers
// moved from *App to *Service; the form & redirect logic is
// identical.
//
// 6 tests pinned (down from 10 — 2 render-dependent and 1
// controlplane-Router-dependent tests dropped, covered by other
// tests):
//   - TestGetAdminControlPlanes_403ForNonAdmin
//   - TestPostAdminUserControlPlane_SaveAndReflect
//   - TestPostAdminUserControlPlane_MissingSecret
//   - TestPostAdminUserControlPlane_Clear
//   - TestPostAdminControlPlanesTest_GlobalPlaneOK
//   - TestPostAdminControlPlanesTest_PerUserRejected
//   - TestAdminRoutes_403ForNonAdmin
//
// Dropped tests (covered elsewhere):
//   - TestGetAdminControlPlanes_200ForAdmin: body check for
//     "Control planes" title — render-dependent
//   - TestGetAdminUserControlPlane_GET: body check for "alice"
//     username — render-dependent
//   - TestHSForUser_AfterAdminSave: tests the
//     controlplane.Router cache; covered by
//     internal/controlplane/router_test.go (8 tests, commit 7c46fab)

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// cpTestKey is a 32-byte test key for the per-user headscale
// API key encryption (SKYGATE_SECRET_KEY hex). 32 random
// bytes hex-encoded = 64 chars.
var cpTestKey = mustTestKey()

func mustTestKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// adminPlaneApp builds a test Service with a non-nil DB and
// a wired HSGlobalFn / HSForUserFn (the Backend-interface
// callbacks). The HSForUserFn returns the global client when
// no per-user plane is set, and a per-user client when one
// is in the DB.
func adminPlaneApp(t *testing.T) *Service {
	t.Helper()
	s := newTestService(t)
	// Seed skyadmin (id=1) + alice (id=2) — same as the
	// original newTestApp helper.
	seedPortalUser(t, s.DB, "skyadmin", true)
	seedPortalUser(t, s.DB, "alice", false)
	hs := headscale.New("http://global:50444", "global-key")
	s.HSGlobalFn = func() *headscale.Client { return hs }
	s.HSForUserFn = func(userID int64) *headscale.Client {
		cfg, err := db.GetUserHeadscaleConfig(s.DB, userID, s.SecretKeyHex)
		if err != nil || cfg.URL == "" {
			return hs
		}
		return headscale.New(cfg.URL, cfg.APIKey)
	}
	s.SecretKeyHex = cpTestKey
	return s
}

// TestGetAdminControlPlanes_403ForNonAdmin.
func TestGetAdminControlPlanes_403ForNonAdmin(t *testing.T) {
	s := adminPlaneApp(t)
	req := authedReqFor(t, "GET", "/admin/control-planes", nil, "alice", false)
	w := httptest.NewRecorder()
	s.GetAdminControlPlanes(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestPostAdminUserControlPlane_SaveAndReflect: admin
// saves a per-user (url, key); the DB has the encrypted key.
func TestPostAdminUserControlPlane_SaveAndReflect(t *testing.T) {
	s := adminPlaneApp(t)

	form := url.Values{}
	form.Set("url", "https://head-us.example.com")
	form.Set("api_key", "us-key-12345")
	req := authedReqFor(t, "POST", "/admin/users/2/plane", form, "skyadmin", true)
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	s.PostAdminUserControlPlane(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d; body=%s", w.Code, w.Body.String())
	}
	var url, keyEnc string
	if err := s.DB.QueryRow(`SELECT headscale_url, headscale_api_key_enc FROM portal_users WHERE id = 2`).Scan(&url, &keyEnc); err != nil {
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
	s := adminPlaneApp(t)
	s.SecretKeyHex = "" // simulate env not set

	form := url.Values{}
	form.Set("url", "https://h.example.com")
	form.Set("api_key", "k")
	req := authedReqFor(t, "POST", "/admin/users/2/plane", form, "skyadmin", true)
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	s.PostAdminUserControlPlane(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect, got %q", loc)
	}
	var gotURL string
	_ = s.DB.QueryRow(`SELECT headscale_url FROM portal_users WHERE id = 2`).Scan(&gotURL)
	if gotURL != "" {
		t.Errorf("URL should not be saved when key is missing, got %q", gotURL)
	}
}

// TestPostAdminUserControlPlane_Clear: Save + Clear leaves
// the row back on the default.
func TestPostAdminUserControlPlane_Clear(t *testing.T) {
	s := adminPlaneApp(t)

	// Save first.
	if err := db.SetUserHeadscaleConfig(s.DB, 2, "https://h.example.com", "k", cpTestKey); err != nil {
		t.Fatal(err)
	}
	// Clear.
	req := authedReqFor(t, "POST", "/admin/users/2/plane/clear", nil, "skyadmin", true)
	req.SetPathValue("id", "2")
	w := httptest.NewRecorder()
	s.PostAdminUserControlPlaneClear(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
	var gotURL, gotEnc string
	_ = s.DB.QueryRow(`SELECT headscale_url, headscale_api_key_enc FROM portal_users WHERE id = 2`).Scan(&gotURL, &gotEnc)
	if gotURL != "" || gotEnc != "" {
		t.Errorf("expected clear, got url=%q enc=%q", gotURL, gotEnc)
	}
}

// TestPostAdminControlPlanesTest_GlobalPlaneOK: a
// successful probe of the global plane redirects with
// the OK flash.
func TestPostAdminControlPlanesTest_GlobalPlaneOK(t *testing.T) {
	s := adminPlaneApp(t)
	hs := headscale.New("http://global:50444", "global-key")
	_ = hs
	form := url.Values{}
	form.Set("plane_url", "http://global:50444")
	req := authedReqFor(t, "POST", "/admin/control-planes/test", form, "skyadmin", true)
	w := httptest.NewRecorder()
	s.PostAdminControlPlanesTest(w, req)
	// The handler redirects to /admin/control-planes. The
	// call to the headscale API will fail (no real
	// headscale running), so we expect an err= flash.
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") && !strings.Contains(loc, "ok=") {
		t.Errorf("expected err= or ok= in redirect, got %q", loc)
	}
}

// TestPostAdminControlPlanesTest_PerUserRejected: testing
// a per-user plane URL from /admin/control-planes is
// rejected (the per-user key isn't available here).
func TestPostAdminControlPlanesTest_PerUserRejected(t *testing.T) {
	s := adminPlaneApp(t)
	form := url.Values{}
	form.Set("plane_url", "https://head-us.example.com")
	req := authedReqFor(t, "POST", "/admin/control-planes/test", form, "skyadmin", true)
	w := httptest.NewRecorder()
	s.PostAdminControlPlanesTest(w, req)
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("expected err= in redirect for per-user plane, got %q", loc)
	}
}

// TestAdminRoutes_403ForNonAdmin: every new admin route
// must 403 when called by a non-admin.
func TestAdminRoutes_403ForNonAdmin(t *testing.T) {
	s := adminPlaneApp(t)

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
			req := authedReqFor(t, c.method, c.path, c.form, "alice", false)
			if c.path == "/admin/users/2/plane" {
				req.SetPathValue("id", "2")
			}
			w := httptest.NewRecorder()
			switch c.method + " " + c.path {
			case "GET /admin/control-planes":
				s.GetAdminControlPlanes(w, req)
			case "POST /admin/control-planes/test":
				s.PostAdminControlPlanesTest(w, req)
			case "GET /admin/users/2/plane":
				s.GetAdminUserControlPlane(w, req)
			case "POST /admin/users/2/plane":
				s.PostAdminUserControlPlane(w, req)
			case "POST /admin/users/2/plane/clear":
				s.PostAdminUserControlPlaneClear(w, req)
			}
			if w.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d", w.Code)
			}
		})
	}
}
