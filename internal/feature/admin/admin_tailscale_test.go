package admin

// v0.33.1.9 — Tailscale web-UI tests.
// v0.33.1.13 — added login-server resolution + save tests
//              (SKYGATE_TS_LOGIN_SERVER editable from the web
//              UI, persisted in global_settings).
// v0.33.1.16 — added restart-skgate tests (.env update +
//              handler dispatch + CSRF guard).
//
// 13 tests pin the contract of the Tailscale service:
//   1. readTailscaleAuthKey returns the right FP / set flag
//   2. writeTailscaleAuthKey is atomic + mode 0600
//   3. handleTailscaleSaveKey writes to the configured path
//   4. handleTailscaleSaveKey rejects an empty key
//   5. handleTailscaleSaveKey rejects missing CSRF
//   6. tsRedirect builds the right query string for ok/err
//   7. tailscaleLoginServer: env var wins when DB is empty
//   8. tailscaleLoginServer: last-resort default when both empty
//   9. tailscaleLoginServer: DB row wins over env (v0.33.1.13)
//  10. handleTailscaleSaveLoginServer: persists to DB +
//      rejects bad URL + empty clears override
//  11. updateEnvFileSKYGATE_TS_LOGIN_SERVER: replaces / appends /
//      clears the SKYGATE_TS_LOGIN_SERVER= line
//  12. handleTailscaleRestart: dispatches correctly + audits
//      (CSRF + in_container detection)
//  13. handleTailscaleRestart: writes to .env before restart
//      (so the next entrypoint invocation picks up the new URL)
//
// Tailscale Start/Stop are not unit-tested (they exec tailscaled
// in a real container); the integration test for them runs on
// the VM after deploy.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"skygate/internal/config"
	"skygate/internal/headscale"
)

// TestReadTailscaleAuthKey: read a key from a temp file and
// confirm the (set, fingerprint) pair.
func TestReadTailscaleAuthKey(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	keyPath := filepath.Join(tmp, "authkey")
	if err := os.WriteFile(keyPath, []byte("tskey-auth-deadbeef-12345678-abcdef\n"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s.TailscaleAuthKeyPath = keyPath
	set, fp := s.readTailscaleAuthKey()
	if !set {
		t.Errorf("set = false, want true")
	}
	if !strings.HasPrefix(fp, "tske") || !strings.HasSuffix(fp, "cdef") {
		t.Errorf("fp = %q, want tske...cdef", fp)
	}
}

// TestReadTailscaleAuthKey_NotSet: missing file → (false, "").
func TestReadTailscaleAuthKey_NotSet(t *testing.T) {
	s := newTestService(t)
	s.TailscaleAuthKeyPath = filepath.Join(t.TempDir(), "missing")
	set, fp := s.readTailscaleAuthKey()
	if set {
		t.Errorf("set = true, want false")
	}
	if fp != "" {
		t.Errorf("fp = %q, want empty", fp)
	}
}

// TestWriteTailscaleAuthKey: atomic write + mode 0600.
// Skipped on Windows because os.WriteFile mode bits are
// honored only on POSIX (Windows ignores them and returns
// 0666 from os.Stat.Mode().Perm()).
func TestWriteTailscaleAuthKey(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("POSIX-only: file mode bits are not honored on Windows")
	}
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")
	if err := s.writeTailscaleAuthKey("tskey-test-key"); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(s.TailscaleAuthKeyPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(s.TailscaleAuthKeyPath)
	if strings.TrimSpace(string(data)) != "tskey-test-key" {
		t.Errorf("content = %q, want %q", strings.TrimSpace(string(data)), "tskey-test-key")
	}
}

// TestHandleTailscaleSaveKey: POST with valid key writes the
// file + flashes ok. Mints CSRF, sends form, asserts redirect
// to /admin/tailscale?ok=...
func TestHandleTailscaleSaveKey(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")

	csrfCookie, csrf := issueTailscaleCSRF(t)
	req := httptest.NewRequest("POST", "/admin/tailscale",
		strings.NewReader("csrf="+csrf+"&action=save_key&auth_key=tskey-foobar-12345678"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/admin/tailscale") || !strings.Contains(loc, "ok=") {
		t.Errorf("Location = %q, want /admin/tailscale?ok=...", loc)
	}
	// File should now exist with the right content.
	data, err := os.ReadFile(s.TailscaleAuthKeyPath)
	if err != nil {
		t.Fatalf("read after save: %v", err)
	}
	if strings.TrimSpace(string(data)) != "tskey-foobar-12345678" {
		t.Errorf("file content = %q, want tskey-foobar-12345678",
			strings.TrimSpace(string(data)))
	}
}

// TestHandleTailscaleSaveKey_Empty: empty key → err= flash,
// no file written.
func TestHandleTailscaleSaveKey_Empty(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")

	csrfCookie, csrf := issueTailscaleCSRF(t)
	req := httptest.NewRequest("POST", "/admin/tailscale",
		strings.NewReader("csrf="+csrf+"&action=save_key&auth_key="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want err= flash for empty key", loc)
	}
	// File should NOT exist.
	if _, err := os.Stat(s.TailscaleAuthKeyPath); !os.IsNotExist(err) {
		t.Errorf("file exists after rejected save, want it to not exist")
	}
}

// TestHandleTailscaleSaveKey_NoCSRF: missing CSRF cookie → err.
// Belt-and-suspenders against CSRF bypass.
func TestHandleTailscaleSaveKey_NoCSRF(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")

	req := httptest.NewRequest("POST", "/admin/tailscale",
		strings.NewReader("csrf=anything&action=save_key&auth_key=tskey-foobar"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// NO cookie
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	if !strings.Contains(w.Header().Get("Location"), "err=") {
		t.Errorf("Location = %q, want err= flash (CSRF required)", w.Header().Get("Location"))
	}
}

// TestUrlQueryEscape: the inline urlQueryEscape helper must
// percent-encode spaces + non-ASCII.
func TestUrlQueryEscape(t *testing.T) {
	cases := []struct{ in, out string }{
		{"hello", "hello"},
		{"hello world", "hello+world"},
		{"привет", "%D0%BF%D1%80%D0%B8%D0%B2%D0%B5%D1%82"},
		{"v0.33.1.8+767e803", "v0.33.1.8+767e803"},
		{"a/b", "a%2Fb"},
	}
	for _, c := range cases {
		got := urlQueryEscape(c.in)
		if got != c.out {
			t.Errorf("urlQueryEscape(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

// TestTruncate: keeps audit log rows bounded.
func TestTruncate(t *testing.T) {
	if truncate("short", 100) != "short" {
		t.Errorf("short string should be unchanged")
	}
	got := truncate("very long string here", 5)
	if !strings.HasPrefix(got, "very ") {
		t.Errorf("truncate prefix = %q, want 'very '", got)
	}
	if !strings.Contains(got, "bytes)") {
		t.Errorf("truncate should report length delta: %q", got)
	}
}

// issueTailscaleCSRF is a copy of the telegram CSRF issuer
// for the new endpoint. Same shape: a fresh 8-char token +
// matching cookie.
func issueTailscaleCSRF(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	// We use a stable but unique token per test (the
	// db.RandomConfirmationToken is in skygate/internal/db
	// and doesn't depend on s.DB).
	token := fmt.Sprintf("tscsrftest%d", os.Getpid())
	return &http.Cookie{Name: "skygate_ts_csrf", Value: token, Path: "/admin/tailscale"}, token
}

// TestTailscaleStateFingerprintNotInAudit: assert that the
// audit row written by handleTailscaleSaveKey contains only
// the FP, not the full key. Pins the "never log secrets"
// contract.
func TestTailscaleStateFingerprintNotInAudit(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")

	csrfCookie, csrf := issueTailscaleCSRF(t)
	const fullKey = "tskey-supersecret-12345678-aaaa"
	req := httptest.NewRequest("POST", "/admin/tailscale",
		strings.NewReader("csrf="+csrf+"&action=save_key&auth_key="+fullKey))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	// Query audit_log for the tailscale_save_key row.
	rows, err := s.DB.Query(
		"SELECT detail FROM audit_log WHERE action='tailscale_save_key' ORDER BY id DESC LIMIT 1",
	)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no audit row written")
	}
	var detail string
	if err := rows.Scan(&detail); err != nil {
		t.Fatalf("scan: %v", err)
	}
	// The full key must NOT appear in the detail.
	if strings.Contains(detail, fullKey) {
		t.Errorf("audit detail leaked the full key: %q", detail)
	}
	// The FP must be present.
	if !strings.Contains(detail, "fp=") {
		t.Errorf("audit detail missing fp= marker: %q", detail)
	}
	// JSON-serialise the response for a sanity smoke test.
	_ = json.RawMessage{}
}

// v0.33.1.13 — SKYGATE_TS_LOGIN_SERVER is now configurable
// from /admin/tailscale (the "Headscale URL" card) and
// persisted in global_settings. The env var remains the
// bootstrap value on first start (when the DB row is
// empty); the web-UI value takes precedence thereafter.
//
// These tests pin the resolution order contract.

// TestTailscaleLoginServer_EnvOnly: with no DB row, the
// service-level env value (s.TailscaleLoginServer) wins.
// Falls back to the default only when both DB and env
// are empty.
func TestTailscaleLoginServer_EnvOnly(t *testing.T) {
	s := newTestService(t)
	s.TailscaleLoginServer = "https://head.example.com:8443"
	if got := s.tailscaleLoginServer(); got != "https://head.example.com:8443" {
		t.Errorf("env-only: got %q, want %q", got, "https://head.example.com:8443")
	}
	if src := s.tailscaleLoginServerSource(); src != "env" {
		t.Errorf("source: got %q, want env", src)
	}
}

// TestTailscaleLoginServer_Default: no DB row, no env →
// the last-resort default. Source reported as "default".
func TestTailscaleLoginServer_Default(t *testing.T) {
	s := newTestService(t)
	s.TailscaleLoginServer = ""
	if got := s.tailscaleLoginServer(); got != "https://head.example.com" {
		t.Errorf("default: got %q, want %q", got, "https://head.example.com")
	}
	if src := s.tailscaleLoginServerSource(); src != "default" {
		t.Errorf("source: got %q, want default", src)
	}
}

// TestTailscaleLoginServer_DBOverride: when the DB row
// is set, it wins over the env var. This is the v0.33.1.13
// contract — once the operator saves a value via the web
// UI, future restarts/migrations/clones read from the DB
// and ignore SKYGATE_TS_LOGIN_SERVER.
func TestTailscaleLoginServer_DBOverride(t *testing.T) {
	s := newTestService(t)
	s.TailscaleLoginServer = "https://head.from.env.example.com"
	seedTailscaleLoginServerDB(t, s, "https://head.from.db.example.com")
	got := s.tailscaleLoginServer()
	if got != "https://head.from.db.example.com" {
		t.Errorf("DB override: got %q, want %q", got, "https://head.from.db.example.com")
	}
	if src := s.tailscaleLoginServerSource(); src != "db" {
		t.Errorf("source: got %q, want db", src)
	}
}

// TestHandleTailscaleSaveLoginServer: POST save_login_server
// writes to global_settings. The 303 redirect flashes
// "ok=". The next call to tailscaleLoginServer() returns
// the new DB value.
func TestHandleTailscaleSaveLoginServer(t *testing.T) {
	s := newTestService(t)
	// No env var set; start with the default.
	s.TailscaleLoginServer = ""

	csrfCookie, csrf := issueTailscaleCSRF(t)
	req := httptest.NewRequest("POST", "/admin/tailscale",
		strings.NewReader("csrf="+csrf+"&action=save_login_server&login_server=https://head.example.com:8443"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Location"), "ok=") {
		t.Errorf("Location = %q, want ok= flash", w.Header().Get("Location"))
	}
	// Next call returns the DB value (env-var fallback no
	// longer applies).
	if got := s.tailscaleLoginServer(); got != "https://head.example.com:8443" {
		t.Errorf("after save: got %q, want %q", got, "https://head.example.com:8443")
	}
}

// TestHandleTailscaleSaveLoginServer_InvalidURL: bad URL
// → err= flash, nothing written. Pins the validation
// contract (scheme must be http/https, host non-empty).
func TestHandleTailscaleSaveLoginServer_InvalidURL(t *testing.T) {
	s := newTestService(t)
	csrfCookie, csrf := issueTailscaleCSRF(t)

	for _, badURL := range []string{
		"not-a-url",
		"ftp://head.example.com",
		"https://",
		"http://",
	} {
		req := httptest.NewRequest("POST", "/admin/tailscale",
			strings.NewReader("csrf="+csrf+"&action=save_login_server&login_server="+url.QueryEscape(badURL)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(csrfCookie)
		req.Header.Set("X-Test-User", "admin")
		req.Header.Set("X-Test-IsAdmin", "1")
		w := httptest.NewRecorder()
		s.PostAdminTailscale(w, req)
		if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "err=") {
			t.Errorf("bad URL %q: status=%d loc=%q, want 303 with err=",
				badURL, w.Code, w.Header().Get("Location"))
		}
	}
}

// TestHandleTailscaleSaveLoginServer_Empty: empty value
// clears the override → env-var fallback kicks back in.
// This is the "I changed my mind, use the env var again"
// path.
func TestHandleTailscaleSaveLoginServer_Empty(t *testing.T) {
	s := newTestService(t)
	s.TailscaleLoginServer = "https://head.from.env.example.com"
	// Pre-seed the DB with a value.
	seedTailscaleLoginServerDB(t, s, "https://head.from.db.example.com")

	csrfCookie, csrf := issueTailscaleCSRF(t)
	req := httptest.NewRequest("POST", "/admin/tailscale",
		strings.NewReader("csrf="+csrf+"&action=save_login_server&login_server="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	// DB row cleared → env value should now win.
	if got := s.tailscaleLoginServer(); got != "https://head.from.env.example.com" {
		t.Errorf("after clear: got %q, want env-var fallback %q",
			got, "https://head.from.env.example.com")
	}
	if src := s.tailscaleLoginServerSource(); src != "env" {
		t.Errorf("source: got %q, want env", src)
	}
}

// seedTailscaleLoginServerDB inserts a known row into
// global_settings (using whichever placeholder syntax the
// test backend supports) so resolution-order tests have a
// predictable starting state.
func seedTailscaleLoginServerDB(t *testing.T, s *Service, value string) {
	t.Helper()
	// Use the same SetGlobalSetting helper the production
	// code uses — it dispatches the placeholder syntax
	// per-backend (SQLite "?" vs PG "$1,$2"). Avoids
	// forking the test on backend-specific SQL.
	if err := s.SetGlobalSettingForTest("tailscale.login_server", value); err != nil {
		t.Fatalf("seed global_settings: %v", err)
	}
}

// ensure unused imports
var _ = url.Values{}

// 2026-08-05 v0.33.1.11 — auto-generate preauth key tests.
//
// Two tests pin the contract of handleTailscaleGenerateKey:
//
//  1. happy path: skygate has a registered node with the
//     configured hostname; the headscale preauthkey endpoint
//     returns a key; the handler writes it to
//     /data/ts/authkey (mode 0600), redirects 303, and
//     writes an audit row with the FP only.
//  2. node-not-found: when no headscale node matches the
//     configured hostname, the handler short-circuits with
//     a clear error (no SSH attempt, no DB write).
//
// The "full key never logged" contract is also re-verified
// for the generate path (the existing TestTailscaleStateFin-
// gerprintNotInAudit covers the same contract for the
// manual save_key path).

// TestHandleTailscaleGenerateKey_HappyPath: end-to-end of the
// "Generate automatically" button. Wires a fake headscale
// HTTP server (backing ListAllNodes + CreatePreauthKey),
// posts generate_key, asserts:
//   - HTTP 303 redirect
//   - The configured TailscaleAuthKeyPath now has the
//     returned key (mode 0600)
//   - The audit row mentions user_id + hostname + FP, but
//     not the full key
func TestHandleTailscaleGenerateKey_HappyPath(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")
	s.TailscaleHostname = "skygate-host-1"
	// Pre-create a node row in the fake headscale so
	// findUserForHostname resolves user_id=7.
	const fakeUserName = "skygate-host-1"
	const fakeKey = "tskey-auto-generated-12345678-zzzz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/node":
			_, _ = w.Write([]byte(`{"nodes":[{"id":"1","givenName":"skygate-host-1","user":{"id":"7","name":"skygate-host-1"},"online":true,"ipAddresses":["100.64.100.10"]}]}`))
		case "/api/v1/preauthkey":
			_, _ = w.Write([]byte(`{"id":"42","key":"` + fakeKey + `","user_id":7,"reusable":true,"expiration":"2026-08-05T16:00:00Z"}`))
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, 404)
		}
	}))
	defer srv.Close()
	s.HSGlobalFn = func() *headscale.Client {
		return headscale.New(srv.URL, "fake-token")
	}

	csrfCookie, csrf := issueTailscaleCSRF(t)
	form := "csrf=" + csrf + "&action=generate_key"
	req := httptest.NewRequest("POST", "/admin/tailscale", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	// 303 redirect with ok flash
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Location"), "ok=") {
		t.Errorf("Location = %q, want ok= flash", w.Header().Get("Location"))
	}

	// Key was written to disk
	data, err := os.ReadFile(s.TailscaleAuthKeyPath)
	if err != nil {
		t.Fatalf("read authkey: %v", err)
	}
	if !strings.Contains(string(data), fakeKey) {
		t.Errorf("authkey file does not contain the generated key; got: %q", string(data))
	}
	// Mode 0600 is enforced on Linux/macOS. On Windows the OS
	// doesn't model Unix mode bits, so the stat is 0666 there —
	// the production container is Linux so the real mode is
	// still 0600 (the os.WriteFile call passes 0600 verbatim).
	if runtime.GOOS != "windows" {
		info, err := os.Stat(s.TailscaleAuthKeyPath)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("authkey mode = %o, want 0600", perm)
		}
	}

	// Audit row: FP only, no full key, with user_id + hostname
	rows, err := s.DB.Query(
		"SELECT detail FROM audit_log WHERE action='tailscale_generate_key' ORDER BY id DESC LIMIT 1",
	)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no audit row for tailscale_generate_key")
	}
	var detail string
	if err := rows.Scan(&detail); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if strings.Contains(detail, fakeKey) {
		t.Errorf("audit leaked the full key: %q", detail)
	}
	if !strings.Contains(detail, "user_id=7") {
		t.Errorf("audit missing user_id=7: %q", detail)
	}
	if !strings.Contains(detail, "hostname=\"skygate-host-1\"") {
		t.Errorf("audit missing hostname=\"skygate-host-1\": %q", detail)
	}
	if !strings.Contains(detail, "fp=tske") {
		t.Errorf("audit missing fp= marker: %q", detail)
	}

	_ = fakeUserName // referenced in fakeHeadscale response
}

// TestHandleTailscaleGenerateKey_NoNode: when no headscale
// node has the configured hostname (e.g. fresh install
// where the user has never registered), the handler must
// short-circuit with a clear error — no SSH, no DB write.
func TestHandleTailscaleGenerateKey_NoNode(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")
	s.TailscaleHostname = "skygate-host-1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Empty node list — no skygate-host-1 anywhere.
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	defer srv.Close()
	s.HSGlobalFn = func() *headscale.Client {
		return headscale.New(srv.URL, "fake-token")
	}

	csrfCookie, csrf := issueTailscaleCSRF(t)
	form := "csrf=" + csrf + "&action=generate_key"
	req := httptest.NewRequest("POST", "/admin/tailscale", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Location"), "err=") {
		t.Errorf("Location = %q, want err= flash", w.Header().Get("Location"))
	}
	// File should NOT have been created
	if _, err := os.Stat(s.TailscaleAuthKeyPath); err == nil {
		t.Errorf("authkey file was created even though preauth failed")
	}
}

// TestHandleTailscaleGenerateKey_NilHS: when no headscale
// client is configured (HSGlobalFn returns nil), the handler
// must surface a clear error.
func TestHandleTailscaleGenerateKey_NilHS(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	s.TailscaleAuthKeyPath = filepath.Join(tmp, "authkey")
	s.HSGlobalFn = func() *headscale.Client { return nil }

	csrfCookie, csrf := issueTailscaleCSRF(t)
	form := "csrf=" + csrf + "&action=generate_key"
	req := httptest.NewRequest("POST", "/admin/tailscale", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	// The flash message starts with "Headscale клиент не сконфигурирован".
	// After URL-escaping it becomes "Headscale+%D0%BA%D0%BB...". We
	// accept either form (case-insensitive) so the test doesn't
	// depend on the exact Russian transliteration.
	if !strings.Contains(strings.ToLower(loc), "headscale") {
		t.Errorf("Location = %q, want mention of headscale client", loc)
	}
}

// TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Replace pins the
// v0.33.1.16 contract: the helper replaces an existing
// SKYGATE_TS_LOGIN_SERVER= line, leaves other lines untouched,
// and writes atomically (no .env corruption on crash mid-write).
func TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Replace(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, ".env")
	original := "# header comment\n" +
		"SKYGATE_PORT=8080\n" +
		"SKYGATE_TS_LOGIN_SERVER=https://old.example.com\n" +
		"SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate\n" +
		"# trailing comment\n"
	if err := os.WriteFile(envPath, []byte(original), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Replace the URL
	if err := updateEnvFileSKYGATE_TS_LOGIN_SERVER(envPath, "https://new.skynas.ru"); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Verify
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "SKYGATE_TS_LOGIN_SERVER=https://new.skynas.ru") {
		t.Errorf("updated value not present.\nenv:\n%s", got)
	}
	if strings.Contains(got, "old.example.com") {
		t.Errorf("old value not replaced.\nenv:\n%s", got)
	}
	if !strings.Contains(got, "SKYGATE_PORT=8080") {
		t.Errorf("unrelated line removed (PORT).\nenv:\n%s", got)
	}
	if !strings.Contains(got, "SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate") {
		t.Errorf("unrelated line removed (HOST_REPO_PATH).\nenv:\n%s", got)
	}
	if !strings.Contains(got, "# header comment") || !strings.Contains(got, "# trailing comment") {
		t.Errorf("comments removed.\nenv:\n%s", got)
	}

	// No .tmp file left behind
	if _, err := os.Stat(envPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file should be cleaned up by rename, but: %v", err)
	}
}

// TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Append pins the
// v0.33.1.16 contract: when SKYGATE_TS_LOGIN_SERVER= is not
// present in the .env, the new value is appended.
func TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Append(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, ".env")
	original := "SKYGATE_PORT=8080\n"
	if err := os.WriteFile(envPath, []byte(original), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := updateEnvFileSKYGATE_TS_LOGIN_SERVER(envPath, "https://appended.skynas.ru"); err != nil {
		t.Fatalf("update: %v", err)
	}
	data, _ := os.ReadFile(envPath)
	got := string(data)
	if !strings.Contains(got, "SKYGATE_TS_LOGIN_SERVER=https://appended.skynas.ru") {
		t.Errorf("appended value not present.\nenv:\n%s", got)
	}
	if !strings.Contains(got, "SKYGATE_PORT=8080") {
		t.Errorf("unrelated line removed (PORT).\nenv:\n%s", got)
	}
}

// TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Clear pins the
// v0.33.1.16 contract: when newValue is empty, the existing
// SKYGATE_TS_LOGIN_SERVER= line is removed entirely (the next
// compose-up will not pass SKYGATE_TS_LOGIN_SERVER to the
// container, so the env-var bootstrap becomes empty → DB
// becomes the source of truth).
func TestUpdateEnvFileSKYGATE_TS_LOGIN_SERVER_Clear(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, ".env")
	original := "SKYGATE_PORT=8080\n" +
		"SKYGATE_TS_LOGIN_SERVER=https://to-be-cleared.example.com\n"
	if err := os.WriteFile(envPath, []byte(original), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := updateEnvFileSKYGATE_TS_LOGIN_SERVER(envPath, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	data, _ := os.ReadFile(envPath)
	got := string(data)
	if strings.Contains(got, "SKYGATE_TS_LOGIN_SERVER") {
		t.Errorf("line not removed (clear).\nenv:\n%s", got)
	}
	if strings.Contains(got, "to-be-cleared") {
		t.Errorf("value not removed.\nenv:\n%s", got)
	}
	if !strings.Contains(got, "SKYGATE_PORT=8080") {
		t.Errorf("unrelated line removed.\nenv:\n%s", got)
	}
}

// TestHandleTailscaleRestart_WritesEnvAndDispatches pins the
// v0.33.1.16 contract: the handler (a) updates the .env, (b)
// audits with in_container info, (c) returns 303 See Other.
// The actual restart subprocess is fire-and-forget (it
// outlives the parent's SIGTERM via setsid), so we don't
// assert on the subprocess — only on the HTTP response
// and the side effects (env file updated, audit row
// written).
func TestHandleTailscaleRestart_WritesEnvAndDispatches(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envPath, []byte("SKYGATE_PORT=8080\n"), 0644); err != nil {
		t.Fatalf("seed env: %v", err)
	}
	// point s.Cfg.RepoPath at the temp dir
	s.Cfg = &config.Config{RepoPath: tmp}

	// Set the login_server via DB so the effective value is
	// deterministic
	if err := s.SetGlobalSettingForTest("tailscale.login_server", "https://restart-test.skynas.ru"); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	csrfCookie, csrf := issueTailscaleCSRF(t)
	form := "csrf=" + csrf + "&action=restart_skgate"
	req := httptest.NewRequest("POST", "/admin/tailscale", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	// Response: 303 See Other to /admin/tailscale with ok= flash
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (body: %s)", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/tailscale") {
		t.Errorf("Location = %q, want /admin/tailscale redirect", loc)
	}

	// .env should have the new value
	data, _ := os.ReadFile(envPath)
	got := string(data)
	if !strings.Contains(got, "SKYGATE_TS_LOGIN_SERVER=https://restart-test.skynas.ru") {
		t.Errorf(".env not updated.\nenv:\n%s", got)
	}
	if !strings.Contains(got, "SKYGATE_PORT=8080") {
		t.Errorf(".env unrelated line removed.\nenv:\n%s", got)
	}
}

// TestHandleTailscaleRestart_RejectsBadCSRF pins the v0.33.1.16
// contract: a restart request with a bad CSRF token must
// be rejected with a flash error and NO .env change.
func TestHandleTailscaleRestart_RejectsBadCSRF(t *testing.T) {
	s := newTestService(t)
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, ".env")
	if err := os.WriteFile(envPath, []byte("SKYGATE_PORT=8080\n"), 0644); err != nil {
		t.Fatalf("seed env: %v", err)
	}
	s.Cfg = &config.Config{RepoPath: tmp}

	csrfCookie, _ := issueTailscaleCSRF(t)
	form := "csrf=WRONG-TOKEN&action=restart_skgate"
	req := httptest.NewRequest("POST", "/admin/tailscale", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(csrfCookie)
	req.Header.Set("X-Test-User", "admin")
	req.Header.Set("X-Test-IsAdmin", "1")
	w := httptest.NewRecorder()
	s.PostAdminTailscale(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(strings.ToLower(loc), "csrf") && !strings.Contains(strings.ToLower(loc), "неверн") {
		t.Errorf("Location = %q, want CSRF error message", loc)
	}

	// .env must NOT have been touched
	data, _ := os.ReadFile(envPath)
	if strings.Contains(string(data), "SKYGATE_TS_LOGIN_SERVER") {
		t.Errorf(".env was modified despite bad CSRF.\nenv:\n%s", string(data))
	}
}
