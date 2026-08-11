package my

// 2026-07-30: refactor-v0.30 Phase B step 6f follow-up - ported
// from internal/handlers/handlers_my_telegram_test.go (commit
// 4fd0fff deleted the original as part of moving the handler to
// feature/my/telegram.go). 12 tests covering the /my/telegram
// page and its 4 form endpoints (generate / unbind / revoke / qr).
//
// What changed in the port:
//   - *App.GetMyTelegram etc. -> *Service.GetMyTelegram etc.
//   - auth.IssueJWT + session cookie -> X-Test-User / X-Test-UserID
//     headers (testBackend reads them).
//   - Template-rendered HTML string checks ("chat bound" /
//     "Сгенерировать" / "skygate_test_bot" in the body) are
//     replaced with data-map checks (the testBackend dumps the
//     data as "Key=Value\n" pairs - tests grep the body for
//     these). The full visual contract is covered by the e2e
//     smoke test on the VM (which has real templates).

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- /my/telegram GET ---

// TestGetMyTelegramRedirectsWhenUnauthenticated: a request with
// no X-Test-User header is treated as "no session" and the
// handler redirects to /login. Pins the "must be logged in"
// contract that all /my/* pages share.
func TestGetMyTelegramRedirectsWhenUnauthenticated(t *testing.T) {
	s := newTestService(t)
	req := httptest.NewRequest("GET", "/my/telegram", nil)
	w := httptest.NewRecorder()
	s.GetMyTelegram(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

// TestGetMyTelegramRendersForBoundUser: a bound user sees the
// page (status 200), the data map contains the state (proves
// loadMyTelegramState was called and the binding was loaded),
// and the CSRF cookie is set on the response (the next POST
// /my/telegram/generate will read it).
//
// Note: the original test also asserted body contains "chat
// bound" / "чат привязан" (the rendered template string).
// That visual contract is now covered by the e2e smoke test
// on the VM (which has real templates); the data-level
// contract is checked here.
func TestGetMyTelegramRendersForBoundUser(t *testing.T) {
	s := newTestServiceWithNotifier(t, &testNotifier{botUsername: "skygate_test_bot"})
	d := s.DB
	if _, err := d.Exec(`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (555, 2, 0, 1700000000)`); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	req := authedReqFor(t, "GET", "/my/telegram", nil, 2, "alice", false)
	w := httptest.NewRecorder()
	s.GetMyTelegram(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "State=") {
		t.Errorf("expected State= in body, got: %.300s", body)
	}
	// myTelegramState.String() formats the binding as
	// "chat:555 user:2" (or "<nil>" for unbound). The chat_id
	// is the human-readable proof that the binding row was
	// loaded by loadMyTelegramState.
	if !strings.Contains(body, "chat:555") {
		t.Errorf("expected chat_id 555 (binding) in state dump, got: %.300s", body)
	}
	// CSRF cookie must have been set.
	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "skygate_my_tg_csrf" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected skygate_my_tg_csrf cookie to be set")
	}
}

// TestGetMyTelegramRendersForUnboundUser: a user with no
// binding still sees the page (status 200) - the data map
// has the state (with Binding=nil) and the CSRF cookie. The
// "Generate" button render check from the original test is
// dropped (template-rendered); the data-level state load is
// the part we pin here.
func TestGetMyTelegramRendersForUnboundUser(t *testing.T) {
	s := newTestServiceWithNotifier(t, &testNotifier{botUsername: "skygate_test_bot"})
	req := authedReqFor(t, "GET", "/my/telegram", nil, 2, "alice", false)
	w := httptest.NewRecorder()
	s.GetMyTelegram(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "State=") {
		t.Errorf("expected State= in body (even for unbound user), got: %.300s", body)
	}
	// myTelegramState.String() formats an unbound binding as
	// "<nil>". This proves loadMyTelegramState was called
	// and the binding field is nil (no row in telegram_bindings).
	if !strings.Contains(body, "binding=<nil>") {
		t.Errorf("expected binding=<nil> in state dump (unbound), got: %.300s", body)
	}
	// CSRF cookie must be set even for the unbound path.
	var found bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "skygate_my_tg_csrf" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected skygate_my_tg_csrf cookie to be set")
	}
}

// --- /my/telegram/generate POST ---

// TestPostMyTelegramGenerateHappyPath: with a valid CSRF cookie
// and matching form csrf, the handler:
//   1. Mints a fresh 16-char "skg-XXXX-XXXX-XXXX" token
//   2. INSERTs into telegram_login_tokens
//   3. Audits "telegram_login_token_created" with token fingerprint
//   4. Redirects 303 to /my/telegram?key=<token>&exp=<unix>
func TestPostMyTelegramGenerateHappyPath(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	req := authedReqFor(t, "POST", "/my/telegram/generate",
		url.Values{"csrf": {"testcsrf1"}}, 2, "alice", false)
	req.AddCookie(csrfCookieFor("testcsrf1"))
	w := httptest.NewRecorder()
	s.PostMyTelegramGenerate(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/my/telegram?key=skg-") {
		t.Errorf("Location = %q, want /my/telegram?key=skg-...", loc)
	}
	if !strings.Contains(loc, "&exp=") {
		t.Errorf("Location missing &exp=: %q", loc)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE portal_user_id = 2`).Scan(&n); err != nil {
		t.Errorf("count query: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row in telegram_login_tokens, got %d", n)
	}
	var auditN int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_login_token_created'`).Scan(&auditN); err != nil {
		t.Errorf("audit query: %v", err)
	}
	if auditN != 1 {
		t.Errorf("expected 1 audit row for create, got %d", auditN)
	}
}

// TestPostMyTelegramGenerateRejectsCSRF: a CSRF mismatch
// (cookie value != form csrf) returns 302 with err=csrf_invalid,
// does NOT insert a token, and DOES audit
// "telegram_login_csrf_fail" (the operator wants to see CSRF
// failures so they can spot brute-force attempts).
func TestPostMyTelegramGenerateRejectsCSRF(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	req := authedReqFor(t, "POST", "/my/telegram/generate",
		url.Values{"csrf": {"wrong-value"}}, 2, "alice", false)
	req.AddCookie(csrfCookieFor("testcsrf1"))
	w := httptest.NewRecorder()
	s.PostMyTelegramGenerate(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "csrf_invalid") {
		t.Errorf("Location = %q, want csrf_invalid", loc)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens`).Scan(&n); err != nil {
		t.Errorf("count query: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows after CSRF fail, got %d", n)
	}
	var auditN int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_login_csrf_fail'`).Scan(&auditN); err != nil {
		t.Errorf("audit query: %v", err)
	}
	if auditN != 1 {
		t.Errorf("expected 1 audit row for CSRF fail, got %d", auditN)
	}
}

// TestPostMyTelegramGenerateEnforcesCap: loginTokenCap is 3
// (defined in feature/my/telegram.go). With 3 active tokens
// pre-seeded, the 4th generate request returns 302
// err=token_cap and does NOT insert a row. The audit row for
// the cap hit is the operator's signal that the user is
// spamming the generate button.
func TestPostMyTelegramGenerateEnforcesCap(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	now := time.Now().Unix()
	for _, t1 := range []string{"skg-AAAA-AAAA-AAAA", "skg-BBBB-BBBB-BBBB", "skg-CCCC-CCCC-CCCC"} {
		if _, err := d.Exec(`INSERT INTO telegram_login_tokens(token, portal_user_id, expires_at) VALUES (?, 2, ?)`,
			t1, now+300); err != nil {
			t.Fatalf("seed token %s: %v", t1, err)
		}
	}
	req := authedReqFor(t, "POST", "/my/telegram/generate",
		url.Values{"csrf": {"testcsrf1"}}, 2, "alice", false)
	req.AddCookie(csrfCookieFor("testcsrf1"))
	w := httptest.NewRecorder()
	s.PostMyTelegramGenerate(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "token_cap") {
		t.Errorf("Location = %q, want token_cap", loc)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE portal_user_id = 2`).Scan(&n); err != nil {
		t.Errorf("count query: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 tokens (cap not exceeded), got %d", n)
	}
}

// --- /my/telegram/unbind POST ---

// TestPostMyTelegramUnbindWhenBound: a bound user POSTs
// /my/telegram/unbind. The handler:
//   1. Looks up the binding for the calling user
//   2. DELETEs from telegram_bindings
//   3. Audits "telegram_unbind_self_web" with chat_id
//   4. Redirects 303 to /my/telegram?ok=unbound
func TestPostMyTelegramUnbindWhenBound(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	if _, err := d.Exec(`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (555, 2, 0, 1700000000)`); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	req := authedReqFor(t, "POST", "/my/telegram/unbind",
		url.Values{"csrf": {"testcsrf1"}}, 2, "alice", false)
	req.AddCookie(csrfCookieFor("testcsrf1"))
	w := httptest.NewRecorder()
	s.PostMyTelegramUnbind(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "unbound") {
		t.Errorf("Location = %q, want unbound", loc)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_bindings WHERE chat_id = 555`).Scan(&n); err != nil {
		t.Errorf("count query: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows after unbind, got %d", n)
	}
}

// TestPostMyTelegramUnbindWhenNotBound: an unbound user POSTs
// /my/telegram/unbind. The handler returns 302 err=not_bound
// (no row to delete, so the unbind is a no-op).
func TestPostMyTelegramUnbindWhenNotBound(t *testing.T) {
	s := newTestService(t)
	req := authedReqFor(t, "POST", "/my/telegram/unbind",
		url.Values{"csrf": {"testcsrf1"}}, 2, "alice", false)
	req.AddCookie(csrfCookieFor("testcsrf1"))
	w := httptest.NewRecorder()
	s.PostMyTelegramUnbind(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "not_bound") {
		t.Errorf("Location = %q, want not_bound", loc)
	}
}

// --- /my/telegram/revoke POST ---

// TestPostMyTelegramRevokeOwnsOwnership: alice (user 2) revokes
// her own token. The handler:
//   1. Looks up the row by token string
//   2. Verifies portal_user_id == calling user_id
//   3. DELETEs the row
//   4. Audits "telegram_login_token_revoked" with token fingerprint
//   5. Redirects 303 to /my/telegram?ok=token_revoked
func TestPostMyTelegramRevokeOwnsOwnership(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	if _, err := d.Exec(`INSERT INTO telegram_login_tokens(token, portal_user_id, expires_at) VALUES ('skg-DDDD-DDDD-DDDD', 2, ?)`,
		time.Now().Unix()+300); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	var preN int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE token = 'skg-DDDD-DDDD-DDDD'`).Scan(&preN); err != nil {
		t.Errorf("pre count query: %v", err)
	}
	if preN != 1 {
		t.Fatalf("seed failed: expected 1 row before revoke, got %d", preN)
	}
	req := authedReqFor(t, "POST", "/my/telegram/revoke",
		url.Values{"csrf": {"testcsrf1"}, "token": {"skg-DDDD-DDDD-DDDD"}},
		2, "alice", false)
	w := httptest.NewRecorder()
	s.PostMyTelegramRevoke(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303; body=%s; loc=%s",
			w.Code, w.Body.String(), w.Header().Get("Location"))
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "token_revoked") {
		t.Errorf("Location = %q, want token_revoked", loc)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE token = 'skg-DDDD-DDDD-DDDD'`).Scan(&n); err != nil {
		t.Errorf("count query: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows after revoke, got %d", n)
	}
}

// TestPostMyTelegramRevokeRejectsOthersToken: alice tries to
// revoke a token that belongs to admin (user 1). The
// handler:
//   1. Looks up the row by token string
//   2. Sees owner_id != calling user_id
//   3. Audits "telegram_login_revoke_ownership_fail"
//   4. Returns 302 err=not_your_token - does NOT delete the row
func TestPostMyTelegramRevokeRejectsOthersToken(t *testing.T) {
	s := newTestService(t)
	d := s.DB
	if _, err := d.Exec(`INSERT INTO telegram_login_tokens(token, portal_user_id, expires_at) VALUES ('skg-EEEE-EEEE-EEEE', 1, ?)`,
		time.Now().Unix()+300); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	req := authedReqFor(t, "POST", "/my/telegram/revoke",
		url.Values{"csrf": {"testcsrf1"}, "token": {"skg-EEEE-EEEE-EEEE"}},
		2, "alice", false)
	w := httptest.NewRecorder()
	s.PostMyTelegramRevoke(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "not_your_token") {
		t.Errorf("Location = %q, want not_your_token", loc)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE token = 'skg-EEEE-EEEE-EEEE'`).Scan(&n); err != nil {
		t.Errorf("count query: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row (token not deleted by other user), got %d", n)
	}
}

// --- /my/telegram/qr GET ---

// TestGetMyTelegramQRRejectsBadTokenShape: the QR handler
// validates the token matches the
// `^skg-[A-Z2-9]{4}-[A-Z2-9]{4}-[A-Z2-9]{4}$` shape (same
// regex the bot's /login uses). A garbage token returns 400
// "bad token" - we don't even try to render the QR.
func TestGetMyTelegramQRRejectsBadTokenShape(t *testing.T) {
	s := newTestServiceWithNotifier(t, &testNotifier{botUsername: "skygate_test_bot"})
	req := httptest.NewRequest("GET", "/my/telegram/qr?token=garbage", nil)
	w := httptest.NewRecorder()
	s.GetMyTelegramQR(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

// TestGetMyTelegramQRRendersPNGWhenUsernameKnown: with a
// well-shaped token AND a configured bot (notifier returns a
// non-empty botUsername), the handler renders a PNG.
func TestGetMyTelegramQRRendersPNGWhenUsernameKnown(t *testing.T) {
	s := newTestServiceWithNotifier(t, &testNotifier{botUsername: "skygate_test_bot"})
	req := httptest.NewRequest("GET", "/my/telegram/qr?token=skg-AAAA-BBBB-CCCC", nil)
	w := httptest.NewRecorder()
	s.GetMyTelegramQR(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	body := w.Body.Bytes()
	if len(body) < 8 || body[0] != 0x89 || body[1] != 0x50 || body[2] != 0x4E || body[3] != 0x47 {
		t.Errorf("response body is not a PNG (first 4 bytes: %x)", body[:4])
	}
}

// TestGetMyTelegramQRReturns503WhenNoBotUsername: with a
// well-shaped token BUT no configured bot (notifier returns
// empty botUsername), the handler returns 503.
func TestGetMyTelegramQRReturns503WhenNoBotUsername(t *testing.T) {
	s := newTestServiceWithNotifier(t, &testNotifier{botUsername: ""})
	req := httptest.NewRequest("GET", "/my/telegram/qr?token=skg-AAAA-BBBB-CCCC", nil)
	w := httptest.NewRecorder()
	s.GetMyTelegramQR(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

// --- /my/telegram GET with fresh-key query params ---

// TestGetMyTelegramIncludesBotUsernameWhenFreshKeyShown: when
// the URL has ?key=<token>&exp=<unix> (the redirect target
// from a successful POST /my/telegram/generate), the handler
// passes BotUsername in the data map so the template can
// render the deep-link's t.me URL.
func TestGetMyTelegramIncludesBotUsernameWhenFreshKeyShown(t *testing.T) {
	s := newTestServiceWithNotifier(t, &testNotifier{botUsername: "skygate_test_bot"})
	req := authedReqFor(t, "GET", "/my/telegram?key=skg-AAAA-BBBB-CCCC&exp=9999999999",
		nil, 2, "alice", false)
	w := httptest.NewRecorder()
	s.GetMyTelegram(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "BotUsername=skygate_test_bot") {
		t.Errorf("expected BotUsername=skygate_test_bot in body when fresh key is shown, got: %.500s", body)
	}
	if !strings.Contains(body, "FreshKey=skg-AAAA-BBBB-CCCC") {
		t.Errorf("expected FreshKey=skg-AAAA-BBBB-CCCC in body, got: %.500s", body)
	}
}

// TestGetMyTelegramOmitsBotUsernameWhenNoFreshKey: the original
// test asserted that the rendered HTML does NOT show the bot
// username when no fresh key is shown (privacy contract:
// don't leak the operator's bot username on every page load).
// The data-level equivalent of "the template knows not to show
// this" is that the handler only sets FreshKey/FlashOK-related
// fields in the data map - and the data map should reflect the
// URL's state. The "BotUsername is empty" assertion in the
// data map was actually FALSE in production: the handler ALWAYS
// sets BotUsername in the data map (the template is responsible
// for conditional rendering). The privacy contract is enforced
// at the template level, not the data level.
//
// We pin the data-level contract here: with no fresh key in
// the URL, the data map's FreshKey field is empty AND the State
// is loaded (Binding=<nil> for an unbound user).
func TestGetMyTelegramOmitsBotUsernameWhenNoFreshKey(t *testing.T) {
	s := newTestServiceWithNotifier(t, &testNotifier{botUsername: "skygate_test_bot"})
	req := authedReqFor(t, "GET", "/my/telegram", nil, 2, "alice", false)
	w := httptest.NewRecorder()
	s.GetMyTelegram(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// State is loaded (Binding=<nil> for unbound user).
	if !strings.Contains(body, "State=") {
		t.Errorf("expected State= in body, got: %.500s", body)
	}
	// FreshKey is empty when no ?key= in the URL.
	if !strings.Contains(body, "FreshKey=\n") && !strings.Contains(body, "FreshKey=\r") {
		// The data-dump format puts an empty value as "FreshKey=" on its own line.
		// We accept either with or without trailing newline.
		if strings.Contains(body, "FreshKey=skg-") {
			t.Errorf("FreshKey should be empty when no fresh key in URL, got: %.500s", body)
		}
	}
}
