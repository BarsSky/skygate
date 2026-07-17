package handlers

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/subnet"
)

// admin_user_subnet_test.go — end-to-end test for the
// /admin/users/{id}/subnet admin page (v0.16.0).
//
// 2026-07-17: v0.16.0 — pins the v0.16.0 contract:
//   - GET /admin/users/{id}/subnet shows "no subnet"
//     when the user has no row, then "Allocate" creates
//     one
//   - POST /admin/users/{id}/subnet/allocate is
//     idempotent (a second call returns the existing row)
//   - GET after Allocate shows the CIDR + status
//   - POST /admin/users/{id}/subnet/disable transitions
//     the row to status=disabled
//   - POST /admin/users/{id}/subnet/test runs a sanity
//     check that catches "denorm out of sync"
//
// We don't go through the full HTTP route registration
// (that's the smoke test's job); we call the handler
// methods directly with a constructed request.

func adminSubnetSeed(t *testing.T, a *App, d *sql.DB, username string) int64 {
	t.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users(username, password_hash, is_admin) VALUES (?, '', 0)`,
		username,
	)
	if err != nil {
		t.Fatalf("seed portal_user: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// TestGetAdminUserSubnet_NoSubnet pins the v0.16.0
// contract: a user without a subnet gets the "no subnet"
// card with an "Allocate" button.
func TestGetAdminUserSubnet_NoSubnet(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()
	uid := adminSubnetSeed(t, a, d, "alice-subnet")

	// Set the route so the URL parser works correctly.
	// (newTestApp doesn't register routes; we use the
	// bare path /admin/users/{id}/subnet.)
	req := authedReqFor(t, a, "GET", "/admin/users/2/subnet", nil, "skyadmin")
	_ = req
	// Build the request with a /subnet suffix URL.
	req = authedReqForURL(t, a, "GET", "/admin/users/"+itoa(uid)+"/subnet", "skyadmin")
	w := httptest.NewRecorder()
	a.GetAdminUserSubnet(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	// The "no subnet" hint comes from bot/catalog. The
	// admin page uses the same key, so we check the
	// bot catalog text (which is the source of truth).
	// The test default lang is "en" but the test app
	// may have the i18n defaults set to "ru" (the
	// operator's default), so we accept either.
	if !strings.Contains(body, "no subnet") && !strings.Contains(body, "Не выделена") && !strings.Contains(body, "Personal subnet") {
		t.Errorf("expected 'no subnet' / 'Personal subnet' hint, got body: %q", body)
	}
}

// TestGetAdminUserSubnet_ForbiddenForNonAdmin pins the
// admin-only gate.
func TestGetAdminUserSubnet_ForbiddenForNonAdmin(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()
	uid := adminSubnetSeed(t, a, d, "alice-subnet")
	// authedReqFor as alice (not admin).
	req := authedReqForURL(t, a, "GET", "/admin/users/"+itoa(uid)+"/subnet", "alice")
	w := httptest.NewRecorder()
	a.GetAdminUserSubnet(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestPostAdminUserSubnetAllocateAndDisable pins the
// v0.16.0 contract: Allocate creates a row in pending
// state, Disable transitions to disabled. The
// deterministic CIDR is 10.0.<uid>.0/24.
func TestPostAdminUserSubnetAllocateAndDisable(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()
	uid := adminSubnetSeed(t, a, d, "alice-subnet")

	// Allocate.
	allocReq := authedReqForURL(t, a, "POST", "/admin/users/"+itoa(uid)+"/subnet/allocate", "skyadmin")
	allocW := httptest.NewRecorder()
	a.PostAdminUserSubnetAllocate(allocW, allocReq)
	if allocW.Code != http.StatusSeeOther {
		t.Errorf("Allocate: expected 303, got %d", allocW.Code)
	}

	// GET shows the new subnet.
	getReq := authedReqForURL(t, a, "GET", "/admin/users/"+itoa(uid)+"/subnet", "skyadmin")
	getW := httptest.NewRecorder()
	a.GetAdminUserSubnet(getW, getReq)
	body := getW.Body.String()
	wantCIDR := "10.0." + itoa(uid) + ".0/24"
	if !strings.Contains(body, wantCIDR) {
		t.Errorf("expected CIDR %s in body, got: %q", wantCIDR, body)
	}
	if !strings.Contains(body, "pending") {
		t.Errorf("expected 'pending' status, got: %q", body)
	}

	// Allocate again — idempotent (returns existing row).
	allocReq2 := authedReqForURL(t, a, "POST", "/admin/users/"+itoa(uid)+"/subnet/allocate", "skyadmin")
	allocW2 := httptest.NewRecorder()
	a.PostAdminUserSubnetAllocate(allocW2, allocReq2)
	if allocW2.Code != http.StatusSeeOther {
		t.Errorf("second Allocate: expected 303, got %d", allocW2.Code)
	}
	// Still only one row in user_subnets.
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM user_subnets WHERE user_id = ?`, uid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("user_subnets rows = %d, want 1 (idempotent)", n)
	}

	// Disable.
	disReq := authedReqForURL(t, a, "POST", "/admin/users/"+itoa(uid)+"/subnet/disable", "skyadmin")
	disW := httptest.NewRecorder()
	a.PostAdminUserSubnetDisable(disW, disReq)
	if disW.Code != http.StatusSeeOther {
		t.Errorf("Disable: expected 303, got %d", disW.Code)
	}
	// Status updated.
	got, err := subnet.Get(d, uid)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != subnet.StatusDisabled {
		t.Errorf("Status = %q, want %q", got.Status, subnet.StatusDisabled)
	}
}

// TestPostAdminUserSubnetTestSanity pins the
// v0.16.0 contract: the "Test" button reports the
// row state + denorm-in-sync check.
func TestPostAdminUserSubnetTestSanity(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()
	uid := adminSubnetSeed(t, a, d, "alice-subnet")
	// Allocate first so the test has something to verify.
	if _, err := subnet.Create(d, uid, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	testReq := authedReqForURL(t, a, "POST", "/admin/users/"+itoa(uid)+"/subnet/test", "skyadmin")
	testW := httptest.NewRecorder()
	a.PostAdminUserSubnetTest(testW, testReq)
	if testW.Code != http.StatusOK {
		t.Errorf("Test: expected 200, got %d", testW.Code)
	}
	body := testW.Body.String()
	// The sanity check renders the result lines. The
	// happy path includes "user_subnets row found" and
	// the denorm match confirmations.
	if !strings.Contains(body, "user_subnets row found") {
		t.Errorf("expected 'user_subnets row found' in test output, got: %q", body)
	}
	if !strings.Contains(body, "denorm") {
		t.Errorf("expected 'denorm' check in test output, got: %q", body)
	}
}

// TestPostAdminUserSubnetTestCatchesDenormOutOfSync
// pins the v0.16.0 contract: the Test button catches
// "denorm out of sync" bugs by comparing the
// user_subnets row with the portal_users denorm
// columns. We force a mismatch by writing to one
// but not the other, then expect the test to report
// the discrepancy.
func TestPostAdminUserSubnetTestCatchesDenormOutOfSync(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()
	uid := adminSubnetSeed(t, a, d, "alice-subnet")
	if _, err := subnet.Create(d, uid, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Force a denorm mismatch: write a wrong CIDR to
	// portal_users.subnet_cidr.
	if _, err := d.Exec(`UPDATE portal_users SET subnet_cidr = '10.0.99.0/24' WHERE id = ?`, uid); err != nil {
		t.Fatalf("force mismatch: %v", err)
	}
	testReq := authedReqForURL(t, a, "POST", "/admin/users/"+itoa(uid)+"/subnet/test", "skyadmin")
	testW := httptest.NewRecorder()
	a.PostAdminUserSubnetTest(testW, testReq)
	body := testW.Body.String()
	// Should report "denorm out of sync".
	if !strings.Contains(body, "denorm out of sync") {
		t.Errorf("expected 'denorm out of sync' in body, got: %q", body)
	}
}

// authedReqForURL is a variant of authedReqFor that
// accepts a free-form URL (the original uses fixed
// paths). Added for the admin subnet tests because
// /admin/users/{id}/subnet has a variable {id}.
func authedReqForURL(t *testing.T, a *App, method, url, username string) *http.Request {
	t.Helper()
	return authedReqFor(t, a, method, url, nil, username)
}

// itoa converts int64 to decimal string. Used to build
// dynamic URLs /admin/users/{id}/subnet in the tests.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	// Reverse.
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

// Ensure the unused import warning doesn't fire if a
// test is later removed. (db.DBTX was a previous
// type alias; keep the import for the test helpers
// that use *sql.DB.)

// TestGetAdminUserSubnet_PopulatesSidebarUsername — regression
// guard for v0.16.8. The renderUserSubnetPage helper used to
// pass c=nil to renderWithLayout, which meant the sidebar
// `<span class="user-name">` rendered empty and the admin nav
// links weren't shown (IsAdmin was unset). The operator
// reported "стили слетели" because the empty sidebar looked
// like a layout/CSS failure. Fix: pass the real c (from
// currentUser) through to renderWithLayout.
func TestGetAdminUserSubnet_PopulatesSidebarUsername(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()
	uid := adminSubnetSeed(t, a, d, "alice-subnet")

	req := authedReqForURL(t, a, "GET", "/admin/users/"+itoa(uid)+"/subnet", "skyadmin")
	w := httptest.NewRecorder()
	a.GetAdminUserSubnet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `class="user-name">skyadmin`) {
		t.Errorf("sidebar username empty (c=nil regression) — expected 'skyadmin' inside <span class=\"user-name\">, body excerpt: %q",
			extractExcerpt(body, `class="user-name"`))
	}
	// IsAdmin=true → admin nav links present. The /admin/users link is
	// always shown when IsAdmin=true (no {{if eq .Page ...}} class on it).
	if !strings.Contains(body, `href="/admin/users"`) {
		t.Errorf("admin nav link missing — IsAdmin flag not propagated to layout")
	}
}

// extractExcerpt returns a 200-char window around the first
// occurrence of needle in haystack, for diagnostic output.
func extractExcerpt(haystack, needle string) string {
	i := strings.Index(haystack, needle)
	if i < 0 {
		return "<needle not found>"
	}
	start := i - 50
	if start < 0 {
		start = 0
	}
	end := i + len(needle) + 150
	if end > len(haystack) {
		end = len(haystack)
	}
	return haystack[start:end]
}
var _ = db.User{}.Username
