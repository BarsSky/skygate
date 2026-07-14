package handlers

// 2026-07-14: Tests for the /admin/telegram "Send test" handler,
// specifically the new fallback that sends to bound chats when
// the global telegram.chat_id is empty.
//
// Background: prior to this change, the "Send test" button was
// a no-op whenever global_settings.telegram.chat_id was unset
// (the SendTelegram helper required both token AND chat_id). An
// operator who had bound their Telegram chat via /start + [Bind]
// (writing a row to telegram_bindings) but never pasted the
// chat_id into the web form was left with no way to verify the
// bot was reachable from the UI — the form button looked
// enabled, the click registered in audit_log, and no message
// arrived. The new fallback iterates over telegram_bindings
// when the global chat_id is empty and sends to each.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// adminCtx returns an *auth.Claims for the seeded skyadmin
// user (id=1, is_admin=true). The test helpers seed that
// user in newMemoryDB; this is the identity the handler
// requires.
func adminCtx(app *App) *auth.Claims {
	return &auth.Claims{UserID: 1, Username: "skyadmin", IsAdmin: true}
}

// issueTelegramCSRF mints a fresh CSRF token and returns
// the cookie + the matching form value the test must echo
// back. The handler's CSRF check is exact-match (subtle.
// ConstantTimeCompare), so we always go through the helper
// instead of inlining a hard-coded "testcsrf" string.
func issueTelegramCSRF(t *testing.T, app *App) (*http.Cookie, string) {
	t.Helper()
	tok, err := db.RandomConfirmationToken(8)
	if err != nil {
		t.Fatalf("csrf token: %v", err)
	}
	cookie := &http.Cookie{Name: "skygate_tg_csrf", Value: tok, Path: "/admin/telegram", HttpOnly: true}
	return cookie, tok
}

// invokeSendTest builds a POST /admin/telegram request with
// action=test, the given form values, the CSRF cookie, and
// the session cookie for skyadmin; runs it through
// AdminTelegramPost; returns the recorder so the test can
// assert on the response.
func invokeSendTest(t *testing.T, app *App, csrfCookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/telegram", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if csrfCookie != nil {
		req.AddCookie(csrfCookie)
	}
	req.AddCookie(sessionCookieFor(t, app, 1, "skyadmin", true))
	w := httptest.NewRecorder()
	app.AdminTelegramPost(w, req)
	return w
}

// TestSendTestUsesGlobalChatID: when global telegram.chat_id
// is set, SendTelegram (the legacy path) is called and
// SendTelegramToChat is NOT called. This is the existing
// behaviour the form used to depend on; we don't want to
// regress it.
func TestSendTestUsesGlobalChatID(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// Save token + global chat_id.
	if err := db.SaveTelegramToken(d, "test-bot-token", "4242"); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t, app)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"unit test"},
	}
	w := invokeSendTest(t, app, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	notif := app.Notifier.(*testNotifier)
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
// operators who bound via /start + [Bind] but never pasted
// a chat_id into the form can still verify reachability.
func TestSendTestFallbackToBoundChats(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// Save token only, NO global chat_id.
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	// Seed a binding for skyadmin (chat_id=12345).
	if _, err := d.Exec(
		`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (?, ?, ?, ?)`,
		12345, 1, 1, 1700000000,
	); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t, app)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"fallback test"},
	}
	w := invokeSendTest(t, app, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	notif := app.Notifier.(*testNotifier)
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
	app, d := newTestApp(t, &testNotifier{})
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
	csrf, _ := issueTelegramCSRF(t, app)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"multi-binding"},
	}
	w := invokeSendTest(t, app, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	notif := app.Notifier.(*testNotifier)
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
	app, d := newTestApp(t, &testNotifier{})
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t, app)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"no-target"},
	}
	w := invokeSendTest(t, app, csrf, form)
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
	notif := app.Notifier.(*testNotifier)
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
	app, d := newTestApp(t, &testNotifier{})
	if err := db.SaveTelegramToken(d, "test-bot-token", "9999"); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	if _, err := d.Exec(
		`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (?, ?, ?, ?)`,
		12345, 1, 1, 1700000000,
	); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	csrf, _ := issueTelegramCSRF(t, app)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"prefer-global"},
	}
	w := invokeSendTest(t, app, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	notif := app.Notifier.(*testNotifier)
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
// were actually targeted — without that, a "test was sent"
// entry with no targets is unactionable.
func TestSendTestAuditIncludesTargets(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
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
	csrf, _ := issueTelegramCSRF(t, app)
	form := url.Values{
		"action":       {"test"},
		"csrf":         {csrf.Value},
		"test_subject": {"audit-detail"},
	}
	w := invokeSendTest(t, app, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	// The audit row should mention both chat_ids. We don't
	// hardcode the format — just check the chat_ids are
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

// Sanity: the helper used by the tests above must produce
// a session cookie that AdminTelegramPost accepts. We
// already cover this end-to-end via the other tests, but
// adding a no-op assertion here makes the failure mode
// obvious if a future refactor breaks session wiring.
func TestInvokeSendTestHelperSmoke(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	csrf, _ := issueTelegramCSRF(t, app)
	form := url.Values{
		"action": {"save"}, // not "test" — we want to hit a no-op-ish branch
		"csrf":   {csrf.Value},
		"token":  {"some-token"},
	}
	// No global chat_id saved. Should hit handleTelegramSave
	// which requires token+chat_id; we provided only token, so
	// it should redirect with err=.
	w := invokeSendTest(t, app, csrf, form)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
}

// Compile-time guard: when adding a new method to the
// Notifier interface (e.g. SendTelegramToChat), the
// testNotifier in handlers_my_telegram_test.go must implement
// it. This declaration makes the test package fail to build
// if the testNotifier drifts from the interface — without
// this, the test would panic at runtime the first time the
// handler tried to call the missing method.
var _ interface {
	SendTelegram(string)
	SendTelegramToChat(string, int64)
	SendAlert(string) int64
} = (*testNotifier)(nil)

// Sanity check the random suffix on the cache name doesn't
// collide between tests; if this fails, the test isolation
// is broken and a parallel run would corrupt each other's
// state. Catches a future change to memDBCounter that drops
// the atomic increment.
func TestMemDBCounterIsPerTest(t *testing.T) {
	d1 := newMemoryDB(t)
	d2 := newMemoryDB(t)
	if d1 == d2 {
		t.Fatal("newMemoryDB returned the same handle twice; per-test DSN broken")
	}
	// And: each DB has its own table set (we can write a
	// row in d1 and it should NOT be visible in d2).
	if _, err := d1.Exec(`INSERT INTO portal_users(id, username, is_admin) VALUES (99, 'counter_test_d1', 0)`); err != nil {
		t.Fatalf("insert d1: %v", err)
	}
	var got string
	err := d2.QueryRow(`SELECT username FROM portal_users WHERE id = 99`).Scan(&got)
	if err == nil {
		t.Errorf("d2 saw d1's row %q; isolation broken", got)
	}
}

// Ensure the URL escape of the test form doesn't break the
// CSRF check (the handler does a constant-time compare of
// the submitted csrf value vs the cookie). This is more of
// a paranoia test — http.NewRequest with the body correctly
// URL-form-encodes — but if a future refactor switches to
// multipart/form-data we'd want to know.
func TestInvokeSendTestPreservesCSRF(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	if err := db.SaveTelegramToken(d, "test-bot-token", ""); err != nil {
		t.Fatalf("SaveTelegramToken: %v", err)
	}
	csrf, token := issueTelegramCSRF(t, app)
	form := url.Values{
		"action": {"test"},
		"csrf":   {token},
	}
	// Substitute the cookie to the exact token we just issued.
	// (issueTelegramCSRF already does this; the test is a
	// belt-and-suspenders against the form.Values.Encode()
	// mangling the value.)
	_ = csrf
	w := invokeSendTest(t, app, csrf, form)
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
	// Use fmt to keep the import set honest in case the
	// rest of the file gets refactored.
	_ = fmt.Sprintf("csrf=%s", token)
}
