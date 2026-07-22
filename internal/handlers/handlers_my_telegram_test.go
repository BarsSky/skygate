// 2026-07-13: Этап 12-13 — tests for the /my/telegram and
// /admin/telegram endpoints. These handlers were the only
// features added in the last two commits without test
// coverage. The tests use httptest to drive the real handler
// chain with an in-memory SQLite DB, a mock Notifier, and
// session cookies minted via auth.IssueJWT.
//
// Why a separate file: handlers_test.go doesn't exist (the
// only handler test in this package is templates_test.go, a
// panic-free check for LoadTemplates). The new endpoints
// share helpers (mintLoginToken shape, currentUser, audit)
// that need realistic test data, so a focused file keeps
// the per-endpoint setup local.

package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"skygate/internal/auth"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// testNotifier is a minimal Notifier implementation that
// records SendTelegram / SendAlert calls and (optionally)
// returns a bot username. Used to exercise the QR handler's
// "bot username not yet discovered" path AND the real
// "render the QR" path in the same test binary.
type testNotifier struct {
	sendTelegramCalls        []string
	sendTelegramToChatCalls  []sendToChatCall
	sendAlertCalls           []string
	botUsername              string
}

type sendToChatCall struct {
	Text   string
	ChatID int64
}

func (n *testNotifier) SendTelegram(text string) {
	n.sendTelegramCalls = append(n.sendTelegramCalls, text)
}
func (n *testNotifier) SendTelegramToChat(text string, chatID int64) {
	n.sendTelegramToChatCalls = append(n.sendTelegramToChatCalls, sendToChatCall{Text: text, ChatID: chatID})
}
func (n *testNotifier) SendAlert(text string) int64 {
	n.sendAlertCalls = append(n.sendAlertCalls, text)
	return int64(len(n.sendAlertCalls))
}
func (n *testNotifier) BotUsernameCached() string { return n.botUsername }

// newTestApp builds an App with an in-memory DB, seeded
// portal_users, and the test Notifier. Sessions are signed
// with a fixed secret ("test-secret") so test code can mint
// JWTs with auth.IssueJWT.
func newTestApp(t *testing.T, notifier telegram.Notifier) (*App, *sql.DB) {
	t.Helper()
	d := newMemoryDB(t)
	app := &App{
		Version:   "v0.0-test",
		Notifier:  notifier,
		I18n:      i18n.New(),
		DB:        d,
		JWTSecret: "test-secret",
		// templates: set in the test that needs it via LoadTemplates().
		// We don't set it here because some tests (those that
		// fail-fast on CSRF, etc.) don't go through the
		// template path and would otherwise trigger a
		// panic-on-init (LoadTemplates panics on parse errors
		// — which is desirable in production but unfriendly in
		// tests that only exercise the validation branches).
	}
	return app, d
}

// withTemplates loads the embed.FS templates onto the App. Call
// this after newTestApp for any test that reaches the render
// path. We use a method on App (rather than a New() shortcut)
// because some tests want a bare App for negative-path checks
// (forbidden, CSRF fail) that short-circuit before the
// template engine runs.
func (a *App) withTemplates() *App {
	a.templates = LoadTemplates()
	return a
}

// newMemoryDB builds an in-memory SQLite DB with the schema
// /my/telegram and /admin/telegram touch. We hand-write the
// minimal schema here rather than calling db.Open() because
// Open() depends on a real file path.
//
// SQLite ":memory:" databases are PER-CONNECTION (each conn
// in the Go pool sees its own fresh DB). To avoid the
// "missing table" failure that bit us with the handler's
// DELETE landing on a different conn than the test's
// INSERT, we use SQLite's shared-cache mode via DSN:
// "file:memdb1?mode=memory&cache=shared" makes every conn
// in the pool share the same in-memory store. The "memdb1"
// name is per-test (a counter is appended) so tests in the
// same process don't see each other's tables.
var memDBCounter int64

func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	// Per-test unique cache name so tests don't share state.
	// (file:NAME?mode=memory&cache=shared is the SQLite URI
	// for a named in-memory DB; the name is what isolates
	// concurrent tests.)
	n := atomic.AddInt64(&memDBCounter, 1)
	dsn := fmt.Sprintf("file:skygate-test-%d?mode=memory&cache=shared", n)
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	stmts := []string{
		`CREATE TABLE portal_users (
			id INTEGER PRIMARY KEY,
			username TEXT NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0,
			password_hash TEXT NOT NULL DEFAULT '',
			theme TEXT NOT NULL DEFAULT 'linear',
			created_at INTEGER NOT NULL DEFAULT 0,
			headscale_user_id INTEGER,
			default_device_node_id TEXT NOT NULL DEFAULT '',
			default_exit_node_id TEXT NOT NULL DEFAULT '',
			-- 2026-07-15: v0.12.0 — per-user control plane.
			-- The DEFAULT '' keeps every existing test that
			-- relies on the "no override" path working without
			-- having to seed a value.
			headscale_url TEXT NOT NULL DEFAULT '',
			headscale_api_key_enc TEXT NOT NULL DEFAULT '',
			-- 2026-07-17: v0.16.0 — per-user subnets.
			subnet_cidr TEXT NOT NULL DEFAULT '',
			subnet_status TEXT NOT NULL DEFAULT 'none',
			subnet_router_node_id TEXT NOT NULL DEFAULT ''
		)`,
		// 2026-07-17: v0.16.0 — user_subnets table for per-user
		// personal subnets. The admin UI /admin/users/{id}/subnet
		// and the bot /mysubnet read this table.
		`CREATE TABLE user_subnets (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL UNIQUE,
			cidr TEXT NOT NULL UNIQUE,
			subnet_bits INTEGER NOT NULL DEFAULT 24,
			control_plane_url TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			router_node_id TEXT NOT NULL DEFAULT '',
			router_container_id TEXT NOT NULL DEFAULT '',
			router_hostname TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE telegram_bindings (
			chat_id INTEGER PRIMARY KEY,
			portal_user_id INTEGER NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0,
			bound_at INTEGER NOT NULL DEFAULT 0,
			bound_by_user_id INTEGER NOT NULL DEFAULT 0,
			lang TEXT NOT NULL DEFAULT 'en'
		)`,
		`CREATE TABLE telegram_login_tokens (
			token TEXT PRIMARY KEY,
			portal_user_id INTEGER NOT NULL,
			created_at INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL,
			used_at INTEGER NOT NULL DEFAULT 0,
			used_by_chat_id INTEGER NOT NULL DEFAULT 0,
			request_ip TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE global_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY,
			user_id INTEGER,
			username TEXT,
			action TEXT,
			detail TEXT DEFAULT '',
			created_at INTEGER DEFAULT 0
		)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	// Seed the two users the tests reference.
	if _, err := d.Exec(`INSERT INTO portal_users(id, username, is_admin) VALUES (1, 'skyadmin', 1)`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO portal_users(id, username, is_admin) VALUES (2, 'alice', 0)`); err != nil {
		t.Fatalf("seed alice: %v", err)
	}
	return d
}

// sessionCookieFor mints a JWT for the given user and returns
// the cookie value (no Cookie wrapping — the test sets it
// directly via req.AddCookie).
func sessionCookieFor(t *testing.T, app *App, userID int64, username string, isAdmin bool) *http.Cookie {
	t.Helper()
	tok, err := auth.IssueJWT(app.JWTSecret, userID, username, isAdmin, 1)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	return &http.Cookie{Name: "skygate_session", Value: tok, Path: "/"}
}

// csrfCookieFor mints a CSRF cookie matching the secret
// returned by db.RandomConfirmationToken. We don't import db
// here to avoid a wider surface; the tests just need a
// non-empty 8-char value.
func csrfCookieFor(path string) *http.Cookie {
	return &http.Cookie{Name: "skygate_my_tg_csrf", Value: "testcsrf1", Path: path, HttpOnly: true}
}

// do is a tiny helper: build a request, attach the session
// cookie, run the handler, return the response.
func do(t *testing.T, app *App, method, path string, cookies []*http.Cookie, body url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(body.Encode())
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	switch method {
	case "GET":
		app.GetMyTelegram(w, req)
	case "POST":
		// Dispatch on path so one helper covers generate/unbind/revoke/qr.
		switch {
		case strings.HasSuffix(path, "/generate"):
			app.PostMyTelegramGenerate(w, req)
		case strings.HasSuffix(path, "/unbind"):
			app.PostMyTelegramUnbind(w, req)
		case strings.HasSuffix(path, "/revoke"):
			app.PostMyTelegramRevoke(w, req)
		default:
			t.Fatalf("unknown POST path %q", path)
		}
	default:
		t.Fatalf("unsupported method %q", method)
	}
	return w
}

// --- /my/telegram tests ---

func TestGetMyTelegramRedirectsWhenUnauthenticated(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	req := httptest.NewRequest("GET", "/my/telegram", nil)
	w := httptest.NewRecorder()
	app.GetMyTelegram(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestGetMyTelegramRendersForBoundUser(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{botUsername: "skygate_test_bot"})
	app.withTemplates()
	// Seed a binding for alice.
	_, _ = d.Exec(`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (555, 2, 0, 1700000000)`)
	w := do(t, app, "GET", "/my/telegram", []*http.Cookie{sessionCookieFor(t, app, 2, "alice", false)}, nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "chat bound") && !strings.Contains(body, "чат привязан") {
		t.Errorf("expected 'chat bound' or 'чат привязан' in body, got: %.300s", body)
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

func TestGetMyTelegramRendersForUnboundUser(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{botUsername: "skygate_test_bot"})
	app.withTemplates()
	w := do(t, app, "GET", "/my/telegram", []*http.Cookie{sessionCookieFor(t, app, 2, "alice", false)}, nil)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	// Should NOT show "chat bound" / "чат привязан".
	if strings.Contains(body, "chat bound") || strings.Contains(body, "чат привязан") {
		t.Errorf("expected unbound state, got: %.300s", body)
	}
	// Should show the strict-mode hint OR the off-hint, plus the
	// "Generate" button.
	if !strings.Contains(body, "Generate") && !strings.Contains(body, "Сгенерировать") {
		t.Errorf("expected Generate button in body, got: %.300s", body)
	}
}

func TestPostMyTelegramGenerateHappyPath(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	w := do(t, app, "POST", "/my/telegram/generate",
		[]*http.Cookie{
			sessionCookieFor(t, app, 2, "alice", false),
			csrfCookieFor("/my/telegram"),
		},
		url.Values{"csrf": {"testcsrf1"}},
	)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	// Redirect should carry ?key=...&exp=...
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/my/telegram?key=skg-") {
		t.Errorf("Location = %q, want /my/telegram?key=skg-...", loc)
	}
	if !strings.Contains(loc, "&exp=") {
		t.Errorf("Location missing &exp=: %q", loc)
	}
	// DB row should exist.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE portal_user_id = 2`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 row in telegram_login_tokens, got %d", n)
	}
	// Audit row should exist.
	var auditN int
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_login_token_created'`).Scan(&auditN)
	if auditN != 1 {
		t.Errorf("expected 1 audit row for create, got %d", auditN)
	}
}

func TestPostMyTelegramGenerateRejectsCSRF(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	w := do(t, app, "POST", "/my/telegram/generate",
		[]*http.Cookie{
			sessionCookieFor(t, app, 2, "alice", false),
			csrfCookieFor("/my/telegram"),
		},
		url.Values{"csrf": {"wrong-value"}},
	)
	// err=... redirects use http.StatusFound (302), not SeeOther.
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "csrf_invalid") {
		t.Errorf("Location = %q, want csrf_invalid", loc)
	}
	// No DB row.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 rows after CSRF fail, got %d", n)
	}
	// Audit row SHOULD exist (CSRF fail is audited so an operator
	// can spot brute-force attempts).
	var auditN int
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_login_csrf_fail'`).Scan(&auditN)
	if auditN != 1 {
		t.Errorf("expected 1 audit row for CSRF fail, got %d", auditN)
	}
}

func TestPostMyTelegramGenerateEnforcesCap(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// Pre-seed 3 active tokens for alice. The cap is loginTokenCap
	// (defined as 3 in handlers_my_telegram.go). expires_at must
	// be in the future (CountActive filters out expired rows).
	// We specify only the columns we care about; DEFAULTs fill
	// in created_at/used_at/used_by_chat_id/request_ip.
	now := time.Now().Unix()
	for _, t1 := range []string{"skg-AAAA-AAAA-AAAA", "skg-BBBB-BBBB-BBBB", "skg-CCCC-CCCC-CCCC"} {
		_, _ = d.Exec(`INSERT INTO telegram_login_tokens(token, portal_user_id, expires_at) VALUES ($1, 2, $2)`,
			t1, now+300)
	}
	w := do(t, app, "POST", "/my/telegram/generate",
		[]*http.Cookie{
			sessionCookieFor(t, app, 2, "alice", false),
			csrfCookieFor("/my/telegram"),
		},
		url.Values{"csrf": {"testcsrf1"}},
	)
	// Token-cap hit is an error redirect → 302 (Found).
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "token_cap") {
		t.Errorf("Location = %q, want token_cap", loc)
	}
	// No 4th row.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE portal_user_id = 2`).Scan(&n)
	if n != 3 {
		t.Errorf("expected 3 tokens (cap not exceeded), got %d", n)
	}
}

func TestPostMyTelegramUnbindWhenBound(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	_, _ = d.Exec(`INSERT INTO telegram_bindings(chat_id, portal_user_id, is_admin, bound_at) VALUES (555, 2, 0, 1700000000)`)
	w := do(t, app, "POST", "/my/telegram/unbind",
		[]*http.Cookie{
			sessionCookieFor(t, app, 2, "alice", false),
			csrfCookieFor("/my/telegram"),
		},
		url.Values{"csrf": {"testcsrf1"}},
	)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "unbound") {
		t.Errorf("Location = %q, want unbound", loc)
	}
	// Row should be gone.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_bindings WHERE chat_id = 555`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 rows after unbind, got %d", n)
	}
}

func TestPostMyTelegramUnbindWhenNotBound(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{})
	w := do(t, app, "POST", "/my/telegram/unbind",
		[]*http.Cookie{
			sessionCookieFor(t, app, 2, "alice", false),
			csrfCookieFor("/my/telegram"),
		},
		url.Values{"csrf": {"testcsrf1"}},
	)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "not_bound") {
		t.Errorf("Location = %q, want not_bound", loc)
	}
}

func TestPostMyTelegramRevokeOwnsOwnership(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// expires_at must be in the future for revoke to make sense
	// (we don't gate on expiry, but using time.Now keeps the
	// test robust if a future refactor adds that gate).
	_, _ = d.Exec(`INSERT INTO telegram_login_tokens(token, portal_user_id, expires_at) VALUES ('skg-DDDD-DDDD-DDDD', 2, $1)`, time.Now().Unix()+300)
	// Sanity: did the seed insert?
	var preN int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE token = 'skg-DDDD-DDDD-DDDD'`).Scan(&preN)
	if preN != 1 {
		t.Fatalf("seed failed: expected 1 row before revoke, got %d", preN)
	}
	form := url.Values{"csrf": {"testcsrf1"}, "token": {"skg-DDDD-DDDD-DDDD"}}
	req := httptest.NewRequest("POST", "/my/telegram/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	w := httptest.NewRecorder()
	app.PostMyTelegramRevoke(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303; body=%s; loc=%s", w.Code, w.Body.String(), w.Header().Get("Location"))
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "token_revoked") {
		t.Errorf("Location = %q, want token_revoked", loc)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE token = 'skg-DDDD-DDDD-DDDD'`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 rows after revoke, got %d", n)
	}
}

func TestPostMyTelegramRevokeRejectsOthersToken(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// Token belongs to user 1 (skyadmin), not user 2 (alice).
	_, _ = d.Exec(`INSERT INTO telegram_login_tokens(token, portal_user_id, expires_at) VALUES ('skg-EEEE-EEEE-EEEE', 1, $1)`, time.Now().Unix()+300)
	w := do(t, app, "POST", "/my/telegram/revoke",
		[]*http.Cookie{
			sessionCookieFor(t, app, 2, "alice", false),
			csrfCookieFor("/my/telegram"),
		},
		url.Values{"csrf": {"testcsrf1"}, "token": {"skg-EEEE-EEEE-EEEE"}},
	)
	if w.Code != http.StatusFound {
		t.Errorf("status = %d, want 302; body=%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "not_your_token") {
		t.Errorf("Location = %q, want not_your_token", loc)
	}
	// Row should still exist.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM telegram_login_tokens WHERE token = 'skg-EEEE-EEEE-EEEE'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 row (token not deleted by other user), got %d", n)
	}
}

func TestGetMyTelegramQRRejectsBadTokenShape(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{botUsername: "skygate_test_bot"})
	req := httptest.NewRequest("GET", "/my/telegram/qr?token=garbage", nil)
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	w := httptest.NewRecorder()
	app.GetMyTelegramQR(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestGetMyTelegramQRRendersPNGWhenUsernameKnown(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{botUsername: "skygate_test_bot"})
	req := httptest.NewRequest("GET", "/my/telegram/qr?token=skg-AAAA-BBBB-CCCC", nil)
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	w := httptest.NewRecorder()
	app.GetMyTelegramQR(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	// PNG magic bytes: 89 50 4E 47 0D 0A 1A 0A
	body := w.Body.Bytes()
	if len(body) < 8 || body[0] != 0x89 || body[1] != 0x50 || body[2] != 0x4E || body[3] != 0x47 {
		t.Errorf("response body is not a PNG (first 4 bytes: %x)", body[:4])
	}
}

func TestGetMyTelegramQRReturns503WhenNoBotUsername(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{botUsername: ""})
	req := httptest.NewRequest("GET", "/my/telegram/qr?token=skg-AAAA-BBBB-CCCC", nil)
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	w := httptest.NewRecorder()
	app.GetMyTelegramQR(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

// --- /admin/telegram tests: handleTelegramStrict ---

func TestHandleTelegramStrictEnables(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// Pre-seed the CSRF cookie.
	csrf := &http.Cookie{Name: "skygate_tg_csrf", Value: "testcsrf1", Path: "/admin/telegram"}
	// Build the form.
	form := url.Values{
		"csrf":    {"testcsrf1"},
		"action":  {"strict"},
		"enabled": {"1"},
		"confirm": {"yes"},
	}
	req := httptest.NewRequest("POST", "/admin/telegram", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookieFor(t, app, 1, "skyadmin", true))
	req.AddCookie(csrf)
	w := httptest.NewRecorder()
	app.AdminTelegramPost(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	// global_settings should now have strict_mode=1.
	var v string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.strict_mode'`).Scan(&v)
	if v != "1" {
		t.Errorf("strict_mode = %q, want 1", v)
	}
	// Audit row.
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_strict_mode_changed'`).Scan(&n)
	if n != 1 {
		t.Errorf("expected 1 audit row, got %d", n)
	}
}

func TestHandleTelegramStrictRequiresConfirm(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	csrf := &http.Cookie{Name: "skygate_tg_csrf", Value: "testcsrf1", Path: "/admin/telegram"}
	form := url.Values{
		"csrf":    {"testcsrf1"},
		"action":  {"strict"},
		"enabled": {"1"},
		// confirm missing
	}
	req := httptest.NewRequest("POST", "/admin/telegram", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookieFor(t, app, 1, "skyadmin", true))
	req.AddCookie(csrf)
	w := httptest.NewRecorder()
	app.AdminTelegramPost(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want err=...", loc)
	}
	// No change.
	var v string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.strict_mode'`).Scan(&v)
	if v != "" && v != "0" {
		t.Errorf("strict_mode should be unchanged, got %q", v)
	}
}

func TestHandleTelegramStrictRequiresAdmin(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	csrf := &http.Cookie{Name: "skygate_tg_csrf", Value: "testcsrf1", Path: "/admin/telegram"}
	form := url.Values{
		"csrf":    {"testcsrf1"},
		"action":  {"strict"},
		"enabled": {"1"},
		"confirm": {"yes"},
	}
	req := httptest.NewRequest("POST", "/admin/telegram", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Alice (non-admin) is making the request.
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	req.AddCookie(csrf)
	w := httptest.NewRecorder()
	app.AdminTelegramPost(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	// No change.
	var v string
	_ = d.QueryRow(`SELECT value FROM global_settings WHERE key = 'telegram.strict_mode'`).Scan(&v)
	if v != "" && v != "0" {
		t.Errorf("strict_mode should be unchanged, got %q", v)
	}
}

func TestHandleTelegramStrictIsNoopWhenUnchanged(t *testing.T) {
	app, d := newTestApp(t, &testNotifier{})
	// Pre-set strict_mode=1.
	_, _ = d.Exec(`INSERT INTO global_settings(key, value) VALUES ('telegram.strict_mode', '1')`)
	csrf := &http.Cookie{Name: "skygate_tg_csrf", Value: "testcsrf1", Path: "/admin/telegram"}
	form := url.Values{
		"csrf":    {"testcsrf1"},
		"action":  {"strict"},
		"enabled": {"1"}, // already enabled
		"confirm": {"yes"},
	}
	req := httptest.NewRequest("POST", "/admin/telegram", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(sessionCookieFor(t, app, 1, "skyadmin", true))
	req.AddCookie(csrf)
	w := httptest.NewRecorder()
	app.AdminTelegramPost(w, req)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want 303", w.Code)
	}
	// No new audit row (no-op).
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'telegram_strict_mode_changed'`).Scan(&n)
	if n != 0 {
		t.Errorf("expected 0 audit rows for no-op toggle, got %d", n)
	}
	// sanity: confirm Notifier recorded no SendTelegram calls.
	if notifier, ok := app.Notifier.(*testNotifier); ok {
		if len(notifier.sendTelegramCalls) != 0 {
			t.Errorf("expected no Telegram messages for no-op toggle, got %d", len(notifier.sendTelegramCalls))
		}
	}
}

// --- Render sanity: the page includes BotUsername when a fresh key is shown ---

// The bot username is only embedded in the freshly-minted-key
// card (the deep link href, the page title). The unbound-state
// page WITHOUT a fresh key shouldn't show it (it would leak
// the operator's bot username to anyone with a valid session
// even if they're not currently binding).
func TestGetMyTelegramIncludesBotUsernameWhenFreshKeyShown(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{botUsername: "skygate_test_bot"})
	app.withTemplates()
	// Pass ?key=<token>&exp=<unix> in the query to simulate the
	// "freshly minted key" view.
	req := httptest.NewRequest("GET", "/my/telegram?key=skg-AAAA-BBBB-CCCC&exp=9999999999", nil)
	req.AddCookie(sessionCookieFor(t, app, 2, "alice", false))
	w := httptest.NewRecorder()
	app.GetMyTelegram(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "skygate_test_bot") {
		t.Errorf("expected bot username in body when fresh key is shown, got: %.500s", body)
	}
}

// And the inverse: no fresh key = no bot username in the body
// (otherwise we'd embed operator info in every page load).
func TestGetMyTelegramOmitsBotUsernameWhenNoFreshKey(t *testing.T) {
	app, _ := newTestApp(t, &testNotifier{botUsername: "skygate_test_bot"})
	app.withTemplates()
	w := do(t, app, "GET", "/my/telegram", []*http.Cookie{sessionCookieFor(t, app, 2, "alice", false)}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "skygate_test_bot") {
		t.Errorf("bot username should not be in body when no fresh key, got: %.500s", body)
	}
}

// --- helpers below are unused right now; kept as a tiny
// utility for future tests in this file. ---

// fetchJSON is a thin JSON-GET helper; reserved for handlers
// that return JSON. Currently none of the Этап 12-13
// endpoints do, so this is unused today but documents the
// pattern for future tests.
func fetchJSON(t *testing.T, app *App, method, path string, cookies []*http.Cookie, body []byte) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	switch method {
	case "GET":
		app.GetMyTelegram(w, req)
	default:
		t.Fatalf("fetchJSON: unsupported method %q", method)
	}
	return w.Code, w.Body.Bytes()
}

// jsonMust is a tiny assert helper; reserved.
func jsonMust(t *testing.T, b []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("json: %v body=%s", err, string(b))
	}
}

// fmtPlaceHolder keeps the import list happy if future tests
// need fmt without breaking the unused-import check.
var _ = fmt.Sprintf
