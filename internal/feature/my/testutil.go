package my

// testutil.go — shared test helpers for the feature/my tests.
//
// Pattern mirrors internal/feature/admin/testutil.go (2026-07-30).
// The helpers are needed by:
//
//   - telegram_test.go     (12 tests for /my/telegram, /my/telegram/{generate,unbind,revoke,qr})
//   - (future) audit_test.go, exit_nodes_test.go, devices_test.go
//
// Each feature/* test file calls newTestService and then drives
// the route directly via s.<Handler>(w, r) (the same pattern as
// feature/admin/*_test.go).
//
// The testBackend reads X-Test-User / X-Test-UserID / X-Test-IsAdmin
// headers (set by authedReqFor) instead of parsing a real JWT
// cookie. This keeps tests self-contained — no JWTSecret field on
// *Service, no IssueJWT calls. The original
// handlers_my_telegram_test.go (deleted in refactor-v0.30 Phase B
// step 6f, 4fd0fff) used real JWTs via auth.IssueJWT(app.JWTSecret);
// the X-Test-User pattern is what the post-refactor test suite
// uses consistently (see feature/admin/testutil.go for the
// pattern's origin).

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"skygate/internal/auth"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// memDBCounter isolates per-test in-memory DBs so concurrent tests
// in the same `go test` process don't share tables.
var memDBCounter int64

// newMemoryDB opens a unique in-memory SQLite DB with the schema
// the /my/* and /admin/telegram handlers touch. Just enough — no
// full migration chain.
//
// Tables:
//   - portal_users          (with v0.12.0 per-user control plane columns)
//   - telegram_bindings     (for /my/telegram binding state)
//   - telegram_login_tokens (for /my/telegram/generate + revoke)
//   - global_settings       (for telegram strict mode + ttl)
//   - audit_log             (for Audit() dual-write)
//
// Note: user_subnets is NOT created here — the /my/* handlers that
// touch it (devices.go, exit_nodes.go) have their own test fixtures
// if/when they get unit tests.
func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	n := atomic.AddInt64(&memDBCounter, 1)
	dsn := "file:skygate-test-my-" + strconv.FormatInt(n, 10) + "?mode=memory&cache=shared"
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
			headscale_url TEXT NOT NULL DEFAULT '',
			headscale_api_key_enc TEXT NOT NULL DEFAULT '',
			subnet_cidr TEXT NOT NULL DEFAULT '',
			subnet_status TEXT NOT NULL DEFAULT 'none',
			subnet_router_node_id TEXT NOT NULL DEFAULT ''
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
			updated_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		// v0.33.1.14 — added for the per-device preferred-exit
		// regression tests. The handler is a 4-line bridge:
		// callerOwnsDevice (reads node_owner_map) → SetDeviceExitNodePref
		// (writes device_exit_node_prefs) → ACL re-apply.
		`CREATE TABLE node_owner_map (
			node_id TEXT PRIMARY KEY,
			hostname TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			headscale_user_id INTEGER NOT NULL DEFAULT 0,
			tag TEXT NOT NULL DEFAULT '',
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE device_exit_node_prefs (
			user_id INTEGER NOT NULL,
			device_hostname TEXT NOT NULL,
			exit_node_tag TEXT NOT NULL,
			via_enabled INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			set_by_user_id INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (user_id, device_hostname)
		)`,
		`CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER DEFAULT 0,
			username TEXT DEFAULT '',
			action TEXT NOT NULL,
			detail TEXT DEFAULT '',
			ip_address TEXT DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("schema %q: %v", q, err)
		}
	}
	return d
}

// newTestService builds a *Service with a non-nil DB + a
// minimal Backend that satisfies the Service.Backend interface
// for tests, plus a recording testNotifier wired in for the
// telegram handler tests.
//
// The Backend's CurrentUser reads X-Test-User / X-Test-UserID /
// X-Test-IsAdmin headers (set by authedReqFor). No real JWT
// minting needed.
//
// Also installs i18n.GlobalCatalog = c so the package-level
// i18n.Tf() calls in handlers (e.g. for audit log details
// that include translated strings) work correctly. t.Cleanup
// restores the previous value.
func newTestService(t *testing.T) *Service {
	t.Helper()
	return newTestServiceWithNotifier(t, &testNotifier{})
}

// newTestServiceWithNotifier is the variant where the caller
// supplies a custom notifier (e.g. to set botUsername). Most
// tests should use newTestService.
func newTestServiceWithNotifier(t *testing.T, n telegram.Notifier) *Service {
	t.Helper()
	d := newMemoryDB(t)
	t.Cleanup(func() { d.Close() })
	b := newTestBackend(d)
	c := i18n.New()
	prev := i18n.GlobalCatalog
	i18n.GlobalCatalog = c
	t.Cleanup(func() { i18n.GlobalCatalog = prev })
	return &Service{
		Backend:  b,
		DB:       d,
		Notifier: n,
		I18n:     c,
		// HS field default: nil. The /my/telegram handlers
		// don't touch headscale directly (the bind flow goes
		// through the bot, not the portal). Tests that need
		// a real headscale client (devices_test.go once it
		// lands) should set s.HS = <client> after the
		// constructor.
	}
}

// testBackend is the no-op Backend implementation used by the
// feature/my tests. Mirrors the pattern in
// internal/feature/admin/testutil.go (testBackend there). It
// satisfies the Backend interface (see service.go) without
// depending on a real *App.
//
// CurrentUser reads X-Test-* headers (set by authedReqFor).
// RenderWithLayout dumps the data map as "<key>=<value>\n"
// pairs so tests can substring-search the body. Audit() writes
// to the in-memory audit slice AND to the DB audit_log table
// (so tests can query via SQL — some original telegram tests
// asserted on COUNT(*) FROM audit_log).
type testBackend struct {
	db        *sql.DB
	auditRows []testAuditRow
}

type testAuditRow struct {
	UserID   int64
	Username string
	Action   string
	Detail   string
	When     time.Time
}

func newTestBackend(d *sql.DB) *testBackend {
	return &testBackend{db: d}
}

func (b *testBackend) CurrentUser(r *http.Request) *auth.Claims {
	username := r.Header.Get("X-Test-User")
	if username == "" {
		return nil
	}
	userIDStr := r.Header.Get("X-Test-UserID")
	userID, _ := strconv.ParseInt(userIDStr, 10, 64)
	isAdmin := r.Header.Get("X-Test-IsAdmin") == "1"
	return &auth.Claims{
		Username: username,
		UserID:   userID,
		IsAdmin:  isAdmin,
	}
}

func (b *testBackend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {
	// No-op: tests that call Render directly inspect the data map
	// via the data-dump pattern below (same as RenderWithLayout).
	w.WriteHeader(http.StatusOK)
}

func (b *testBackend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
	// Test renderer: dump every data-map value as "<key>=<value>\n"
	// into the body so tests can substring-search the body. The
	// production template machinery is bypassed; tests don't care
	// about the visual layout, only that the handler wired the
	// data correctly. This is the same pattern as the admin
	// testBackend (see feature/admin/testutil.go:stringifyForTest).
	w.WriteHeader(http.StatusOK)
	for k, v := range data {
		_, _ = w.Write([]byte(k + "=" + stringifyForTest(v) + "\n"))
	}
}

func (b *testBackend) Audit(userID int64, username, action, detail string) {
	b.auditRows = append(b.auditRows, testAuditRow{
		UserID: userID, Username: username,
		Action: action, Detail: detail, When: time.Now(),
	})
	if b.db != nil {
		_, _ = b.db.Exec(
			`INSERT INTO audit_log(user_id, username, action, detail) VALUES (?, ?, ?, ?)`,
			userID, username, action, detail,
		)
	}
}

// HSGlobalFn / HSForUserFn: the /my/telegram handlers don't touch
// headscale (the bind flow is bot-mediated, not portal-mediated),
// so the testBackend returns nil for both. Tests that exercise
// other /my/* handlers that DO need a real headscale client
// (devices.go, exit_nodes.go once they get unit tests) should
// override these via a backend wrapper that captures a real
// *headscale.Client.
func (b *testBackend) HSGlobalFn() *headscale.Client        { return nil }
func (b *testBackend) HSForUserFn(_ int64) *headscale.Client { return nil }

// stringifyForTest produces a stable text representation of an
// arbitrary data-map value. Maps and slices are flattened;
// everything else uses fmt.Sprintf("%v"). Mirrors the
// helper of the same name in feature/admin/testutil.go.
//
// The reflection fallback (in the default branch) is what
// makes the bound-vs-unbound test work: a *db.TelegramBinding
// pointer in myTelegramState.Binding is rendered as
// "&{555 2 0 1700000000 0 en}" (the dereferenced struct),
// not as "0xc000123456" (the raw pointer). Tests grep the
// body for the chat_id (e.g. "555") to confirm the binding
// was loaded.
func stringifyForTest(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []string:
		return strings.Join(x, ",")
	case map[string]any:
		parts := make([]string, 0, len(x))
		for k, vv := range x {
			parts = append(parts, k+"="+stringifyForTest(vv))
		}
		return strings.Join(parts, "; ")
	default:
		if s, ok := v.(interface{ String() string }); ok {
			return s.String()
		}
		// Fall through to reflection-based dereference for
		// pointer-to-struct (so *db.TelegramBinding renders
		// as "&{555 ...}" rather than "0xc000123456"). For
		// plain structs, fmt.Sprintf("%v", x) already uses
		// the struct's fields.
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Ptr && !rv.IsNil() {
			return fmt.Sprintf("&%v", rv.Elem().Interface())
		}
		return fmt.Sprintf("%v", x)
	}
}

// authedReqFor builds a request that looks like the caller
// (userID, username, isAdmin) hit the given path. In production
// this comes from the JWT cookie; in tests we use headers and
// the testBackend's CurrentUser shim.
//
// The optional `body` is url.Values form data (nil for GETs).
// For GETs we use httptest.NewRequest("GET", path, nil); for
// POSTs we set Content-Type: application/x-www-form-urlencoded.
func authedReqFor(t *testing.T, method, path string, body url.Values, userID int64, username string, isAdmin bool) *http.Request {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if username != "" {
		r.Header.Set("X-Test-User", username)
		r.Header.Set("X-Test-UserID", strconv.FormatInt(userID, 10))
	}
	if isAdmin {
		r.Header.Set("X-Test-IsAdmin", "1")
	}
	return r
}

// csrfCookieFor builds the CSRF cookie the /my/telegram handler
// expects on POST. The value is whatever the test wants the
// handler to read; pass the same value as the "csrf" form field
// for the success path, or a different value for the CSRF-reject
// path.
func csrfCookieFor(value string) *http.Cookie {
	return &http.Cookie{Name: "skygate_my_tg_csrf", Value: value, Path: "/my/telegram", HttpOnly: true}
}

// seedPortalUser inserts a portal_users row and returns the
// new id. Tests that need a user to bind a chat to / generate
// a token for call this first.
func seedPortalUser(t *testing.T, d *sql.DB, username string, isAdmin bool) int64 {
	t.Helper()
	adminVal := 0
	if isAdmin {
		adminVal = 1
	}
	res, err := d.Exec(
		`INSERT INTO portal_users(username, password_hash, is_admin) VALUES (?, '', ?)`,
		username, adminVal,
	)
	if err != nil {
		t.Fatalf("seed portal_user %s: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// testNotifier is a recording Notifier. The /my/telegram tests
// use it to assert that the QR handler picks up the botUsername
// from the cached field, and the /admin/telegram strict-mode
// tests use it to assert that a no-op toggle does NOT send a
// Telegram message (a regression guard — v0.12 accidentally
// dispatched a "🔒 strict mode changed" message on every
// toggle; the no-op path must stay silent).
//
// Goroutine-safe via the embedded mutex; tests don't actually
// call SendTelegram concurrently, but a future test might and
// the cost is trivial.
type testNotifier struct {
	mu sync.Mutex

	botUsername string

	sendTelegramCalls       []string
	sendTelegramToChatCalls []sendToChatCall
	sendAlertCalls          []string
}

type sendToChatCall struct {
	Text   string
	ChatID int64
}

func (n *testNotifier) SendTelegram(text string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sendTelegramCalls = append(n.sendTelegramCalls, text)
}

func (n *testNotifier) SendTelegramToChat(text string, chatID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sendTelegramToChatCalls = append(n.sendTelegramToChatCalls, sendToChatCall{Text: text, ChatID: chatID})
}

func (n *testNotifier) SendAlert(text string) int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sendAlertCalls = append(n.sendAlertCalls, text)
	return int64(len(n.sendAlertCalls))
}

func (n *testNotifier) BotUsernameCached() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.botUsername
}

// Compile-time check: testNotifier satisfies telegram.Notifier.
var _ telegram.Notifier = (*testNotifier)(nil)
