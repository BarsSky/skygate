// update_settings_test.go — regression tests for the v0.32.20
// UI-controlled auto-update toggle (PostAdminUpdateAutoToggle).
//
// The toggle persists in global_settings (key='auto_update_enabled')
// and is read on every render of /admin/update. The env var
// SKYGATE_AUTO_UPDATE_ENABLED is the DEFAULT at first start
// (when the row doesn't exist); the UI takes over after the
// first toggle.
package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"skygate/internal/db"
)

func TestPostAdminUpdateAutoToggle_EnablePersists(t *testing.T) {
	s := newTestService(t)

	// No row in global_settings yet — read returns the default (false).
	if got := db.GetGlobalSettingBool(s.DB, "auto_update_enabled", false); got {
		t.Fatalf("before toggle: GetGlobalSettingBool should default to false; got true")
	}

	form := url.Values{"enabled": {"1"}}
	r := authedReqFor(t, "POST", "/admin/update/auto-toggle", form, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminUpdateAutoToggle(w, r)

	// Expect 303 See Other (redirect to /admin/update?auto_toggled=enabled).
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (See Other); body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "/admin/update?auto_toggled=enabled" {
		t.Errorf("Location = %q, want /admin/update?auto_toggled=enabled", loc)
	}

	// global_settings should now have value=1.
	if got := db.GetGlobalSettingBool(s.DB, "auto_update_enabled", false); !got {
		t.Errorf("after toggle: GetGlobalSettingBool should be true; got false")
	}
}

func TestPostAdminUpdateAutoToggle_DisablePersists(t *testing.T) {
	s := newTestService(t)

	// Pre-set the row to "1" (enabled), then toggle off.
	if err := db.SetGlobalSettingBool(s.DB, "auto_update_enabled", true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := db.GetGlobalSettingBool(s.DB, "auto_update_enabled", false); !got {
		t.Fatalf("precondition: expected true after seed; got false")
	}

	form := url.Values{"enabled": {"0"}}
	r := authedReqFor(t, "POST", "/admin/update/auto-toggle", form, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminUpdateAutoToggle(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc != "/admin/update?auto_toggled=disabled" {
		t.Errorf("Location = %q, want /admin/update?auto_toggled=disabled", loc)
	}
	if got := db.GetGlobalSettingBool(s.DB, "auto_update_enabled", true); got {
		t.Errorf("after toggle off: expected false; got true")
	}
}

func TestPostAdminUpdateAutoToggle_NonAdminForbidden(t *testing.T) {
	s := newTestService(t)
	form := url.Values{"enabled": {"1"}}
	// X-Test-IsAdmin=0 (not admin)
	r := authedReqFor(t, "POST", "/admin/update/auto-toggle", form, "alice", false)
	w := httptest.NewRecorder()
	s.PostAdminUpdateAutoToggle(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-admin POST: status = %d, want 403", w.Code)
	}
	// DB should still be untouched (no row written).
	if got := db.GetGlobalSettingBool(s.DB, "auto_update_enabled", false); got {
		t.Errorf("non-admin POST: DB should remain at default (false); got true")
	}
}

func TestPostAdminUpdateAutoToggle_TogglePersistsAcrossReads(t *testing.T) {
	// End-to-end: toggle on, verify GetGlobalSettingBool reflects
	// it, toggle off, verify it reflects the new state.
	s := newTestService(t)
	for _, want := range []bool{true, false, true, false} {
		val := "0"
		if want {
			val = "1"
		}
		form := url.Values{"enabled": {val}}
		r := authedReqFor(t, "POST", "/admin/update/auto-toggle", form, "admin", true)
		w := httptest.NewRecorder()
		s.PostAdminUpdateAutoToggle(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("toggle to %s: status = %d, want 303", val, w.Code)
		}
		got := db.GetGlobalSettingBool(s.DB, "auto_update_enabled", !want)
		if got != want {
			t.Errorf("after toggle to %s: GetGlobalSettingBool = %v, want %v", val, got, want)
		}
	}
}
