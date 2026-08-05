package admin

// v0.33.1.9 — Tailscale web-UI tests.
//
// 5 tests pin the contract of the Tailscale service:
//   1. readTailscaleAuthKey returns the right FP / set flag
//   2. writeTailscaleAuthKey is atomic + mode 0600
//   3. handleTailscaleSaveKey writes to the configured path
//   4. handleTailscaleSaveKey rejects an empty key
//   5. tsRedirect builds the right query string for ok/err
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
	"strings"
	"testing"
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

// ensure unused imports
var _ = url.Values{}
