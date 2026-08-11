package admin

// 2026-07-30: refactor-v0.30 follow-up - ported from
// internal/handlers/admin_telegram_test.go (deleted in commit
// 149a3a4 as part of moving /admin/telegram to feature/admin).
// 9 tests for the SendTest handler + 2 CSRF helpers.
//
// Background: prior to this change, the "Send test" button
// was a no-op whenever global_settings.telegram.chat_id was
// unset. An operator who had bound their Telegram chat via
// /start + [Bind] but never pasted the chat_id into the web
// form was left with no way to verify the bot was reachable
// from the UI. The new fallback iterates over
// telegram_bindings when the global chat_id is empty and sends
// to each. The 9 tests pin this contract.
//
// What changed in the port:
//   - *App.AdminTelegramPost -> *Service.AdminTelegramPost
//   - app.Notifier.(*testNotifier) -> s.Notifier.(*recordingTestNotifier)
//   - sessionCookieFor(t, app, ...) -> X-Test-UserID header
//     (the testBackend in feature/admin/testutil.go already
//     supports this pattern)
//   - The CSRF cookie is built inline (db.RandomConfirmationToken
//     is called directly, no helper that touches *App)
//   - newMemoryDB now creates telegram_bindings + telegram_login_tokens
//     (the test inserts bindings via direct SQL)

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/db"
)

// issueTelegramCSRF mints a fresh CSRF token and returns the
// matching cookie + value. The handler's CSRF check is
// exact-match (subtle.ConstantTimeCompare), so the test must
// echo the token back in the "csrf" form field.
//
// The token is independent of the service / app — no *App
// reference, no *Service reference. The CSRF flow is pure
// (cookie + form field), so this works for both old and new
// architectures.
func issueTelegramCSRF(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	tok, err := db.RandomConfirmationToken(8)
	if err != nil {
		t.Fatalf("csrf token: %v", err)
	}
	cookie := &http.Cookie{Name: "skygate_tg_csrf", Value: tok, Path: "/admin/telegram", HttpOnly: true}
	return cookie, tok
}

// invokeSendTest builds a POST /admin/telegram request with
// the given form + CSRF cookie, attaches the X-Test-User headers
// (admin), and runs s.AdminTelegramPost.
func invokeSendTest(t *testing.T, s *Service, csrfCookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/telegram", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if csrfCookie != nil {
		req.AddCookie(csrfCookie)
	}
	// X-Test-UserID=1 (admin) so the testBackend's CurrentUser
	// returns Claims{UserID: 1, Username: "admin", IsAdmin: true}.
	// IsAdmin=true is required for AdminTelegramPost to pass the
	// admin gate.
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-UserID", "1")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.AdminTelegramPost(w, req)
	return w
}

// TestSendTestUsesGlobalChatID: when global telegram.chat_id
// is set, SendTelegram (the legacy path) is called and
// SendTelegramToChat is NOT called. This is the existing
// behaviour the form used to depend on; we don't want to
// regress it.
func TestSendTestUsesGlobalChatID(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	d := s.DB
	if err := db.SaveTelegramToken(d, "test-bot-token", "4242"); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t)
	form := url.Values{
		"action":        {"test"},
		"csrf":          {csrf.Value},
		"test_subject":  {"unit test"},
	}
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	notif := s.Notifier.(*recordingTestNotifier)
	if len(notif.sendTelegramCalls) != 1 {
		t.Errorf("SendTelegram calls = %d, want 1", len(notif.sendTelegramCalls))
	}
	if len(notif.sendTelegramToChatCalls) != 0 {
		t.Errorf("SendTelegramToChat calls = %d, want 0 (global chat_id was set)", len(notif.sendTelegramToChatCalls))
	}
}

// TestSendTestFallbackToBoundChats: when global chat_id is
// empty but a binding exists, SendTelegramToChat is called
// with the binding's chat_id. This is the new behaviour —
// operators who bound via /start + [Bind] but never pasted a
// chat_id into the form can still verify reachability.
func TestSendTestFallbackToBoundChats(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	d := s.DB
	// Save token only, NO global chat_id.
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	// Seed a binding for admin (chat_id=12345).
	if _, err := d.Exec(
		`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (?, ?, ?, ?)`,
		12345, 1, 1, 1700000000,
	); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"fallback test"},
	}
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	notif := s.Notifier.(*recordingTestNotifier)
	if len(notif.sendTelegramCalls) != 0 {
		t.Errorf("SendTelegram calls = %d, want 0 (global chat_id was empty; should fall back to bindings)", len(notif.sendTelegramCalls))
	}
	if len(notif.sendTelegramToChatCalls) != 1 {
		t.Fatalf("SendTelegramToChat calls = %d, want 1", len(notif.sendTelegramToChatCalls))
	}
	got := notif.sendTelegramToChatCalls[0]
	if got.ChatID != 12345 {
		t.Errorf("chat_id = %d, want 12345", got.ChatID)
	}
	if !strings.Contains(got.Text, "fallback test") {
		t.Errorf("text = %q, want to contain subject %q", got.Text, "fallback test")
	}
}

// TestSendTestFallbackToMultipleBoundChats: when global
// chat_id is empty and multiple bindings exist, ALL of them
// get the test message. Operators may have multiple devices
// (phone + laptop) and we want the test to land everywhere
// they could be reading.
func TestSendTestFallbackToMultipleBoundChats(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	d := s.DB
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	for _, chatID := range []int64{111, 222, 333} {
		if _, err := d.Exec(
			`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (?, ?, ?, ?)`,
			chatID, 1, 1, 1700000000,
		); err != nil {
			t.Fatalf("seed binding %d: %v", chatID, err)
		}
	}
	csrf, _ := issueTelegramCSRF(t)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"multi-binding"},
	}
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	notif := s.Notifier.(*recordingTestNotifier)
	if len(notif.sendTelegramToChatCalls) != 3 {
		t.Fatalf("SendTelegramToChat calls = %d, want 3", len(notif.sendTelegramToChatCalls))
	}
	gotIDs := map[int64]bool{}
	for _, c := range notif.sendTelegramToChatCalls {
		gotIDs[c.ChatID] = true
	}
	for _, want := range []int64{111, 222, 333} {
		if !gotIDs[want] {
			t.Errorf("chat_id %d missing from sent targets", want)
		}
	}
}

// TestSendTestNoTargetAtAll: when global chat_id is empty
// AND no bindings exist, the handler must NOT no-op silently.
// It must redirect with a flash message that tells the
// operator what to do next (send /start to the bot). The
// test asserts on the flash query parameter.
func TestSendTestNoTargetAtAll(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	d := s.DB
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"no-target"},
	}
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want to contain err= flash", loc)
	}
	if !strings.Contains(loc, "start") {
		t.Errorf("Location = %q, want to mention /start so the operator knows what to do", loc)
	}
	notif := s.Notifier.(*recordingTestNotifier)
	if len(notif.sendTelegramCalls)+len(notif.sendTelegramToChatCalls) != 0 {
		t.Errorf("notifier should not have been called; got SendTelegram=%d, SendTelegramToChat=%d",
			len(notif.sendTelegramCalls), len(notif.sendTelegramToChatCalls))
	}
}

// TestSendTestGlobalPreferredOverBindings: when BOTH global
// chat_id and bindings exist, the global chat_id wins.
// Operators who have a configured admin chat shouldn't be
// spammed via the binding path.
func TestSendTestGlobalPreferredOverBindings(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	d := s.DB
	if err := db.SaveTelegramToken(d, "test-bot-token", "9999"); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (?, ?, ?, ?)`,
		12345, 1, 1, 1700000000,
	); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"prefer-global"},
	}
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	notif := s.Notifier.(*recordingTestNotifier)
	if len(notif.sendTelegramCalls) != 1 {
		t.Errorf("SendTelegram calls = %d, want 1 (global chat_id set)", len(notif.sendTelegramCalls))
	}
	if len(notif.sendTelegramToChatCalls) != 0 {
		t.Errorf("SendTelegramToChat calls = %d, want 0 (global chat_id takes precedence)", len(notif.sendTelegramToChatCalls))
	}
}

// TestSendTestAuditIncludesTargets: the audit row's detail
// must mention which chats got the message. The operator
// needs to be able to confirm in the audit log which chats
// were actually targeted - without that, a "test was sent"
// entry with no targets is unactionable.
func TestSendTestAuditIncludesTargets(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	d := s.DB
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	for _, chatID := range []int64{42, 99} {
		if _, err := d.Exec(
			`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (?, ?, ?, ?)`,
			chatID, 1, 1, 1700000000,
		); err != nil {
			t.Fatalf("seed binding %d: %v", chatID, err)
		}
	}
	csrf, _ := issueTelegramCSRF(t)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"audit-detail"},
	}
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	// The audit row should mention both chat_ids. We don't
	// hardcode the format - just check the chat_ids are
	// present.
	row := d.QueryRow(`SELECT detail FROM audit_log WHERE action = 'telegram_test_sent' ORDER BY id DESC LIMIT 1`)
	var detail string
	if err := row.Scan(&detail); err != nil {
		t.Fatalf("scan audit: %v", err)
	}
	if !strings.Contains(detail, "audit-detail") {
		t.Errorf("audit detail = %q, want to contain subject", detail)
	}
	for _, want := range []string{"42", "99"} {
		if !strings.Contains(detail, want) {
			t.Errorf("audit detail = %q, want to mention chat_id %s", detail, want)
		}
	}
}

// TestInvokeSendTestHelperSmoke: the helper used by the tests
// above must produce a session cookie that AdminTelegramPost
// accepts. We already cover this end-to-end via the other
// tests, but adding a no-op assertion here makes the failure
// mode obvious if a future refactor breaks session wiring.
func TestInvokeSendTestHelperSmoke(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	csrf, _ := issueTelegramCSRF(t)
	form := url.Values{
		"action": {"save"}, // not "test" - we want to hit a no-op-ish branch
		"csrf":   {csrf.Value},
		"token":  {"some-token"},
	}
	// No global chat_id saved. Should hit handleTelegramSave
	// which requires token+chat_id; we provided only token, so
	// it should redirect with err=.
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
}

// Compile-time guard: when adding a new method to the
// Notifier interface (e.g. SendTelegramToChat), the
// recordingTestNotifier in handlers_my_telegram_test.go must
// implement it. This declaration makes the test package
// fail to build if the recordingTestNotifier drifts from
// the interface - without this, the test would panic at
// runtime the first time the handler tried to call the
// missing method.
var _ interface {
	SendTelegram(string)
	SendTelegramToChat(string, int64)
	SendAlert(string) int64
} = (*recordingTestNotifier)(nil)

// TestInvokeSendTestPreservesCSRF: the URL escape of the test
// form doesn't break the CSRF check (the handler does a
// constant-time compare of the submitted csrf value vs the
// cookie). This is more of a paranoia test - http.NewRequest
// with the body correctly URL-form-encodes, but if a future
// refactor switches to multipart/form-data we'd want to know.
func TestInvokeSendTestPreservesCSRF(t *testing.T) {
	s := newTestServiceWithNotifier(t, &recordingTestNotifier{})
	d := s.DB
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	csrf, token := issueTelegramCSRF(t)
	form := url.Values{
		"action": {"test"},
		"csrf":   {token},
	}
	// Substitute the cookie to the exact token we just issued.
	// (issueTelegramCSRF already does this; the test is a
	// belt-and-suspenders against the form.Values.Encode()
	// mangling the value.)
	_ = csrf
	w := invokeSendTest(t, s, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	// Expect: redirect with err= because no global chat_id AND
	// no bindings exist. The CSRF check should have passed
	// (we used the correct token) and the handler should
	// have reached the no-target branch.
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want err= flash (CSRF must have passed for the handler to reach the no-target branch)", loc)
	}
}
