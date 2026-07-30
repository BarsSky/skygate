package admin

// 2026-07-30: refactor-v0.30 Phase B step 6f follow-up - ported
// from internal/handlers/handlers_my_telegram_test.go (the
// 4 /admin/telegram strict-mode tests that lived in the same
// file as the /my/telegram tests because they shared a testApp
// helper). After the refactor, the strict-mode tests belong in
// feature/admin (the handler lives in
// internal/feature/admin/telegram.go:AdminTelegramPost).
//
// What changed in the port:
//   - *App.AdminTelegramPost -> *Service.AdminTelegramPost
//   - auth.IssueJWT + session cookie -> X-Test-User / X-Test-UserID
//     headers (admin testBackend reads them, same as the
//     feature/my port)
//   - The original test 4 (no-op strict toggle should NOT fire
//     SendTelegram) uses a recording testNotifier. The
//     recording type was added to feature/admin/testutil.go
//     in the same commit (recordingTestNotifier).

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// csrfCookieForAdmin builds the CSRF cookie the /admin/telegram
// handler expects on POST. The value must match the "csrf"
// form field for the success path; a mismatch triggers
// err=csrf_invalid and an audit row for "telegram_csrf_fail".
func csrfCookieForAdmin(value string) *http.Cookie {
	return &http.Cookie{Name: "skygate_tg_csrf", Value: value, Path: "/admin/telegram", HttpOnly: true}
}

// TestHandleTelegramStrictEnables: an admin POSTs
// /admin/telegram with action=strict, enabled=1, confirm=yes.
// The handler:
//   1. Sets global_settings.telegram.strict_mode = "1"
//   2. Audits "telegram_strict_mode_changed"
//   3. Invalidates the probe cache (so the next /admin/telegram
//      load picks up the new value)
//   4. Redirects 303 to /admin/telegram?ok=...
func TestHandleTelegramStrictEnables(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	req := authedReqFor(t, "POST", "/admin/telegram",
		url.Values{
			"csrf":    {"testcsrf1"},
			"action":  {"strict"},
			"enabled": {"1"},
			"confirm": {"yes"},
		}, "skyadmin", true)
	req.AddCookie(csrfCookieForAdmin("testcsrf1"))
	w := httptest.NewRecorder()
	s.AdminTelegramPost(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	var v string
	if err := d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.strict_mode'`).Scan(&v); err != nil {
		t.Errorf("strict_mode query: %v", err)
	}
	if v != "1" {
		t.Errorf("strict_mode = %q, want 1", v)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_strict_mode_changed'`).Scan(&n); err != nil {
		t.Errorf("audit count query: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 audit row, got %d", n)
	}
}

// TestHandleTelegramStrictRequiresConfirm: an admin POSTs
// action=strict enabled=1 WITHOUT confirm=yes. The handler
// rejects with a flash error and does NOT change strict_mode.
// The "you must check the confirmation box" gate is the
// operator's safety net against accidentally flipping strict
// mode on (which would lock out every bot user until they
// re-bind).
func TestHandleTelegramStrictRequiresConfirm(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	req := authedReqFor(t, "POST", "/admin/telegram",
		url.Values{
			"csrf":    {"testcsrf1"},
			"action":  {"strict"},
			"enabled": {"1"},
			// confirm missing
		}, "skyadmin", true)
	req.AddCookie(csrfCookieForAdmin("testcsrf1"))
	w := httptest.NewRecorder()
	s.AdminTelegramPost(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want err=...", loc)
	}
	var v string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.strict_mode'`).Scan(&v)
	if v != "" && v != "0" {
		t.Errorf("strict_mode should be unchanged, got %q", v)
	}
}

// TestHandleTelegramStrictRequiresAdmin: a non-admin (alice)
// POSTs the same form. The handler returns 403 (forbidden) and
// does NOT change strict_mode. The "must be admin" gate is the
// security boundary - strict mode is an operator-level toggle
// and must never be reachable by a regular user.
func TestHandleTelegramStrictRequiresAdmin(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	req := authedReqFor(t, "POST", "/admin/telegram",
		url.Values{
			"csrf":    {"testcsrf1"},
			"action":  {"strict"},
			"enabled": {"1"},
			"confirm": {"yes"},
		}, "alice", false)
	req.AddCookie(csrfCookieForAdmin("testcsrf1"))
	w := httptest.NewRecorder()
	s.AdminTelegramPost(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	var v string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.strict_mode'`).Scan(&v)
	if v != "" && v != "0" {
		t.Errorf("strict_mode should be unchanged, got %q", v)
	}
}

// TestHandleTelegramStrictIsNoopWhenUnchanged: an admin POSTs
// action=strict enabled=1 when strict_mode is ALREADY 1. The
// handler:
//   1. Detects the no-op (old == want)
//   2. Does NOT write to global_settings
//   3. Does NOT audit
//   4. Does NOT fire SendTelegram (the regression guard - a
//      strict-mode toggle was accidentally dispatching a
//      "🔒 strict mode changed" message to the operator's
//      chat on every page reload that triggered the form,
//      before v0.12)
//
// The "no SendTelegram call" assertion is the important one:
// a no-op toggle should be 100% silent. We use a
// recordingTestNotifier (see testutil.go) to capture calls.
func TestHandleTelegramStrictIsNoopWhenUnchanged(t *testing.T) {
	rec := &recordingTestNotifier{}
	s := newTestService(t)
	s.Notifier = rec
	d := s.DB
	// Pre-set strict_mode=1.
	if _, err := d.Exec(`INSERT INTO global_settings(key, value) VALUES ('telegram.strict_mode', '1')`); err != nil {
		t.Fatalf("seed strict_mode: %v", err)
	}
	req := authedReqFor(t, "POST", "/admin/telegram",
		url.Values{
			"csrf":    {"testcsrf1"},
			"action":  {"strict"},
			"enabled": {"1"},
			"confirm": {"yes"},
		}, "skyadmin", true)
	req.AddCookie(csrfCookieForAdmin("testcsrf1"))
	w := httptest.NewRecorder()
	s.AdminTelegramPost(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	// No new audit row (no-op).
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_strict_mode_changed'`).Scan(&n); err != nil {
		t.Errorf("audit count query: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 audit rows for no-op toggle, got %d", n)
	}
	// Notifier recorded no SendTelegram calls.
	if len(rec.sendTelegramCalls) != 0 {
		t.Errorf("expected no Telegram messages for no-op toggle, got %d", len(rec.sendTelegramCalls))
	}
	if len(rec.sendTelegramToChatCalls) != 0 {
		t.Errorf("expected no Telegram-to-chat for no-op toggle, got %d", len(rec.sendTelegramToChatCalls))
	}
}
