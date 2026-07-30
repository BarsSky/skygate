package admin

// testutil.go — shared test helpers for the feature/admin tests.
//
// Moved from internal/handlers/handlers_test.go and admin_user_subnet_test.go
// as part of refactor-v0.30 (2026-07-30). The helpers are needed
// by:
//   - admin/subnets_test.go (the v0.16.10 tests)
//   - admin/user_subnet_test.go (the v0.16.6 tests)
//   - admin/exit_nodes_test.go (the v0.18.1 tests)
//   - admin/backup_config_test.go
//   - admin/control_planes_test.go
//
// Each feature/* test file calls newTestService + withTemplates
// (a 2-liner: newTestService + s.Backend.SetTemplates(makeSyntheticTemplates()))
// and then uses the Service's handler methods + authedReqFor to
// exercise the route.

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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

// newMemoryDB opens a unique in-memory SQLite DB. Just enough
// schema for the admin/handlers to run (no full migration chain).
func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	n := atomic.AddInt64(&memDBCounter, 1)
	dsn := "file:skygate-test-admin-" + strconv.FormatInt(n, 10) + "?mode=memory&cache=shared"
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	// Minimal schema: just enough for the admin/handlers to query.
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS portal_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
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
		`CREATE TABLE IF NOT EXISTS user_subnets (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL UNIQUE,
			cidr TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			router_node_id TEXT NOT NULL DEFAULT '',
			router_container_id TEXT NOT NULL DEFAULT '',
			router_hostname TEXT NOT NULL DEFAULT '',
			last_seen_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0,
			control_plane_url TEXT NOT NULL DEFAULT '',
			sidecar_node_id TEXT NOT NULL DEFAULT '',
			subnet_bits INTEGER NOT NULL DEFAULT 24
		)`,
		`CREATE TABLE IF NOT EXISTS node_owner_map (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			hostname TEXT NOT NULL,
			tag TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			node_id INTEGER NOT NULL DEFAULT 0,
			tagged_by_user_id INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS global_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
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
			t.Fatalf("schema: %v", err)
		}
	}
	return d
}

// newTestService builds a *Service with a non-nil DB + a
// minimal Backend that satisfies the Service.Backend interface
// for tests. The test then calls s.<Handler>(w, r) to exercise
// the route directly (without going through net/http).
//
// The Backend is wired with the package-level *App state via
// a small shim (see newTestBackend below). For most admin tests
// this is enough — the handler reads s.DB, calls s.Backend.Render
// or s.Backend.RenderWithLayout, and writes to the response.
// The shim is a no-op renderer that doesn't depend on real
// templates.
//
// Also installs s.I18n as the i18n.GlobalCatalog for the
// duration of the test (handlers use the package-level
// i18n.Tf/T which fall back to GlobalCatalog). t.Cleanup
// restores the previous value.
func newTestService(t *testing.T) *Service {
	t.Helper()
	d := newMemoryDB(t)
	t.Cleanup(func() { d.Close() })
	b := newTestBackend(d)
	c := i18n.New()
	prev := i18n.GlobalCatalog
	i18n.GlobalCatalog = c
	t.Cleanup(func() { i18n.GlobalCatalog = prev })
	return &Service{
		Backend: b,
		DB:      d,
		I18n:    c,
		// HSGlobalFn default: returns nil. Tests that need a
		// real headscale client (control_planes, exit_nodes)
		// should call s.HSGlobalFn = func() *headscale.Client
		// { return <client> } after the constructor.
		HSGlobalFn:  func() *headscale.Client { return nil },
		HSForUserFn: func(userID int64) *headscale.Client { return nil },
	}
}

// testBackend is the no-op Backend implementation used by the
// feature/admin tests. It satisfies the Backend interface
// (see internal/handlers/handlers_export.go for the public
// wrappers) without depending on a real *App.
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
	// Read X-Test-User / X-Test-IsAdmin headers (set by authedReqFor)
	username := r.Header.Get("X-Test-User")
	if username == "" {
		return nil
	}
	isAdmin := r.Header.Get("X-Test-IsAdmin") == "1"
	return &auth.Claims{Username: username, IsAdmin: isAdmin}
}

func (b *testBackend) Render(w http.ResponseWriter, r *http.Request, name string, data any) {
	// No-op: tests that call Render directly inspect the data map
	// via s.Backend.SetRenderData() before the call.
	w.WriteHeader(http.StatusOK)
}

func (b *testBackend) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
	// Test renderer: dump every data-map value as "<key>=<value>\n"
	// into the body so tests can substring-search the body. The
	// production template machinery is bypassed; tests don't care
	// about the visual layout, only that the handler wired the
	// data correctly. For richer tests, override the renderer
	// via a custom testBackend.
	w.WriteHeader(http.StatusOK)
	for k, v := range data {
		_, _ = w.Write([]byte(k + "=" + stringifyForTest(v) + "\n"))
	}
}

// stringifyForTest produces a stable text representation of an
// arbitrary data-map value. Maps and slices are flattened to
// "<key>=<value>" pairs; everything else uses fmt.Sprintf("%v").
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
		// Best-effort: handle fmt.Stringer + reflect-driven fallback.
		if s, ok := v.(interface{ String() string }); ok {
			return s.String()
		}
		return fmt.Sprintf("%v", x)
	}
}

func (b *testBackend) Audit(userID int64, username, action, detail string) {
	b.auditRows = append(b.auditRows, testAuditRow{
		UserID: userID, Username: username,
		Action: action, Detail: detail, When: time.Now(),
	})
	// Also write to the DB so tests can query audit_log via SQL.
	if b.db != nil {
		_, _ = b.db.Exec(
			`INSERT INTO audit_log(user_id, username, action, detail) VALUES (?, ?, ?, ?)`,
			userID, username, action, detail,
		)
	}
}

func (b *testBackend) Config() interface{} { return nil }

// authedReqFor builds a request that looks like the caller
// `username` (with admin if `isAdmin`) hit the given path.
// In production this comes from the JWT cookie; in tests we
// use headers + the testBackend's CurrentUser shim.
func authedReqFor(t *testing.T, method, path string, form url.Values, username string, isAdmin bool) *http.Request {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	var r *http.Request
	if method == "GET" {
		r = httptest.NewRequest("GET", path, nil)
	} else {
		r = httptest.NewRequest(method, path, body)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	r.Header.Set("X-Test-User", username)
	if isAdmin {
		r.Header.Set("X-Test-IsAdmin", "1")
	}
	return r
}

// authedReqForURL is the URL-variant (path-as-URL for handlers
// that parse r.URL.Path).
func authedReqForURL(t *testing.T, method, path, username string, isAdmin bool) *http.Request {
	return authedReqFor(t, method, path, nil, username, isAdmin)
}

// adminSubnetSeed inserts a portal_users row with the given
// username and returns the new id. Tests that need a user
// to allocate a subnet to call this first.
func adminSubnetSeed(t *testing.T, d *sql.DB, username string, isAdmin bool) int64 {
	t.Helper()
	return seedPortalUser(t, d, username, isAdmin)
}

// seedPortalUser is the lower-level helper that adminSubnetSeed
// wraps. The control_planes tests need a portal_users row to
// set per-user headscale config on.
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

// itoa is a tiny int64 → string helper. Used by the tests to
// compose CIDR strings like "10.0.<uid>.0/24".
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// extractExcerpt returns a 60-char window around the first
// occurrence of `needle` in `haystack` (or "" if not found).
// Used by the test assertions to make the failure messages
// readable when the body is large.
func extractExcerpt(haystack, needle string) string {
	const window = 60
	idx := strings.Index(haystack, needle)
	if idx < 0 {
		return ""
	}
	start := idx - window
	if start < 0 {
		start = 0
	}
	end := idx + len(needle) + window
	if end > len(haystack) {
		end = len(haystack)
	}
	return haystack[start:end]
}

// testNotifier is a minimal telegram.Notifier stub. The
// service.Notifier field uses this in tests that need
// the field set.
type testNotifier struct{}

func (testNotifier) SendTelegram(_ string)                {}
func (testNotifier) SendTelegramToChat(_ string, _ int64) {}
func (testNotifier) SendAlert(_ string) int64             { return 0 }
func (testNotifier) BotUsernameCached() string            { return "" }

// Compile-time check: testNotifier satisfies telegram.Notifier.
var _ telegram.Notifier = testNotifier{}

// silence unused imports on builds that don't pull every helper
var _ = context.Background
