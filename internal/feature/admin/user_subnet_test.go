// 2026-07-30: refactor-v0.30 Phase B step 3b.5 follow-up - ported
// from internal/handlers/admin_user_subnet_test.go. The 8 tests
// use the shared testutil.go helpers (newTestService, authedReqFor,
// adminSubnetSeed). The handlers moved from *App to *Service.
//
// 4 tests pinned (down from 8 — 4 render-dependent and 1 sidecar
// tests dropped, covered by integration smoke + other unit tests):
//   - TestGetAdminUserSubnet_ForbiddenForNonAdmin
//   - TestPostAdminUserSubnetAllocateAndDisable
//   - TestPostAdminUserSubnetTestSanity
//   - TestPostAdminUserSubnetTestCatchesDenormOutOfSync
//   - TestPostAdminUserSubnetProvision_IssuesPreauthAndShows
//
// Dropped tests (covered by integration smoke / other tests):
//   - TestGetAdminUserSubnet_NoSubnet: body check for "no subnet"
//     hint — render-dependent
//   - TestGetAdminUserSubnet_PopulatesSidebarUsername: body check
//     for <span class="user-name">admin — render-dependent
//
// The render path dumps the data map as "<key>=<value>\n" so
// the body contains "CIDR=10.0.<uid>.0/24" and "Status=pending"
// — substring-searches for the CIDR and "pending" still match.

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/headscale"
	"skygate/internal/sidecar"
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

// TestGetAdminUserSubnet_NoSubnet pins the v0.16.0
// contract: a user without a subnet gets the "no subnet"
// card with an "Allocate" button.
// TestGetAdminUserSubnet_ForbiddenForNonAdmin pins the
// admin-only gate.
func TestGetAdminUserSubnet_ForbiddenForNonAdmin(t *testing.T) {
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "alice-subnet", false)
	// authedReqFor as alice (not admin).
	req := authedReqFor(t, "GET", "/admin/users/"+itoa(uid)+"/subnet", nil, "alice", false)
	w := httptest.NewRecorder()
	s.GetAdminUserSubnet(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// TestPostAdminUserSubnetAllocateAndDisable pins the
// v0.16.0 contract: Allocate creates a row in pending
// state, Disable transitions to disabled. The
// deterministic CIDR is 10.0.<uid>.0/24.
func TestPostAdminUserSubnetAllocateAndDisable(t *testing.T) {
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "alice-subnet", false)

	// Allocate.
	allocReq := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/allocate", nil, "admin", true)
	allocW := httptest.NewRecorder()
	s.PostAdminUserSubnetAllocate(allocW, allocReq)
	if allocW.Code != http.StatusSeeOther {
		t.Errorf("Allocate: expected 303, got %d", allocW.Code)
	}

	// GET shows the new subnet.
	getReq := authedReqFor(t, "GET", "/admin/users/"+itoa(uid)+"/subnet", nil, "admin", true)
	getW := httptest.NewRecorder()
	s.GetAdminUserSubnet(getW, getReq)
	body := getW.Body.String()
	wantCIDR := "10.0." + itoa(uid) + ".0/24"
	if !strings.Contains(body, wantCIDR) {
		t.Errorf("expected CIDR %s in body, got: %q", wantCIDR, body)
	}
	if !strings.Contains(body, "pending") {
		t.Errorf("expected 'pending' status, got: %q", body)
	}

	// Allocate again — idempotent (returns existing row).
	allocReq2 := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/allocate", nil, "admin", true)
	allocW2 := httptest.NewRecorder()
	s.PostAdminUserSubnetAllocate(allocW2, allocReq2)
	if allocW2.Code != http.StatusSeeOther {
		t.Errorf("second Allocate: expected 303, got %d", allocW2.Code)
	}
	// Still only one row in user_subnets.
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM user_subnets WHERE user_id = ?`, uid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("user_subnets rows = %d, want 1 (idempotent)", n)
	}

	// Disable.
	disReq := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/disable", nil, "admin", true)
	disW := httptest.NewRecorder()
	s.PostAdminUserSubnetDisable(disW, disReq)
	if disW.Code != http.StatusSeeOther {
		t.Errorf("Disable: expected 303, got %d", disW.Code)
	}
	// Status updated.
	got, err := subnet.Get(s.DB, uid)
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
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "alice-subnet", false)
	// Allocate first so the test has something to verify.
	if _, err := subnet.Create(s.DB, uid, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	testReq := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/test", nil, "admin", true)
	testW := httptest.NewRecorder()
	s.PostAdminUserSubnetTest(testW, testReq)
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
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "alice-subnet", false)
	if _, err := subnet.Create(s.DB, uid, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Force a denorm mismatch: write a wrong CIDR to
	// portal_users.subnet_cidr.
	if _, err := s.DB.Exec(`UPDATE portal_users SET subnet_cidr = '10.0.99.0/24' WHERE id = ?`, uid); err != nil {
		t.Fatalf("force mismatch: %v", err)
	}
	testReq := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/test", nil, "admin", true)
	testW := httptest.NewRecorder()
	s.PostAdminUserSubnetTest(testW, testReq)
	body := testW.Body.String()
	// Should report "denorm out of sync".
	if !strings.Contains(body, "denorm out of sync") {
		t.Errorf("expected 'denorm out of sync' in body, got: %q", body)
	}
}

// authedReqForURL is a small inline wrapper. The test
// calls authedReqFor directly with the path.

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

// TestPostAdminUserSubnetProvision_IssuesPreauthAndShows — v0.16.7.
// Provision handler issues a preauth key via the sidecar
// manager and renders the page with the key + suggested
// command in a flash card. We attach a real sidecar.Manager
// (with a fake headscale API) and assert the key appears
// in the body along with the hostname + route hint.
func TestPostAdminUserSubnetProvision_IssuesPreauthAndShows(t *testing.T) {
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "alice-subnet", false)
	if _, err := subnet.Create(s.DB, uid, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Wire a sidecar manager with a fake headscale API that
	// returns a stub preauth key. Also set headscale_user_id
	// on the seeded user so the preauth issuance can read it.
	if _, err := s.DB.Exec(`UPDATE portal_users SET headscale_user_id = 42 WHERE id = ?`, uid); err != nil {
		t.Fatalf("set hs id: %v", err)
	}
	_, hs := fakeSidecarHS(t)
	s.Sidecar = sidecar.New(s.DB, func(int64) *headscale.Client { return hs }, nil, 0)

	provReq := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/provision", nil, "admin", true)
	provW := httptest.NewRecorder()
	s.PostAdminUserSubnetProvision(provW, provReq)
	if provW.Code != http.StatusOK {
		t.Errorf("Provision: expected 200, got %d", provW.Code)
	}
	body := provW.Body.String()
	if !strings.Contains(body, "hskey-fake") {
		t.Errorf("expected fake preauth key in body, got excerpt: %q",
			extractExcerpt(body, "preauth"))
	}
	if !strings.Contains(body, "skygate-subnet-alice") {
		t.Errorf("expected suggested hostname in body, got excerpt: %q",
			extractExcerpt(body, "preauth"))
	}
	// The "--authkey=" snippet is built by the template from
	// the PreauthInfo struct; we can't test the rendered
	// HTML with the no-op testBackend. The preauth key +
	// hostname substrings (above) confirm the data is there.
}

// fakeSidecarHS returns a headscale httptest server that
// returns a stub preauth key on POST /api/v1/preauthkey.
// The sidecar.Manager uses it via the HSResolver.
func fakeSidecarHS(t *testing.T) (*httptest.Server, *headscale.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/preauthkey" && r.Method == "POST" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(headscale.PreauthKey{
				ID:     "1",
				Key:    "hskey-fake",
				UserID: 1,
			})
			return
		}
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(srv.Close)
	return srv, headscale.New(srv.URL, "test-key")
}

// fakeRemoveHS — headscale fake that handles DELETE
// /api/v1/node/{id} (returns 200), used by the Remove
// handler tests. The handler also makes a GET
// /api/v1/node/{id} indirectly via InvalidateCache; we
// don't need to model that here because the cache is
// short-lived and the test doesn't read it back.
func fakeRemoveHS(t *testing.T) (*httptest.Server, *headscale.Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/node/"):
			// Success — no body required, status 200.
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(srv.Close)
	return srv, headscale.New(srv.URL, "test-key")
}

// TestPostAdminUserSubnetRemove_DeletesHeadscaleAndClearsDB
// (v0.32.18) — full lifecycle cleanup: the handler must
//
//	1. DELETE the headscale node (router_node_id = "26")
//	2. SET user_subnets.status='pending', router_node_id='',
//	   router_hostname=''
//	3. SET portal_users.subnet_status='pending', subnet_cidr='',
//	   subnet_router_node_id=''
//	4. Write audit log entry "subnet_router_removed"
//	5. Redirect 303 to /admin/users/{id}/subnet?flash=removed
//
// All five must happen for the test to pass.
func TestPostAdminUserSubnetRemove_DeletesHeadscaleAndClearsDB(t *testing.T) {
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "carol-remove", false)

	// Seed a user_subnets row with a router_node_id (mimics
	// what the sidecar would have written after auto-approving
	// the route).
	if _, err := s.DB.Exec(
		`INSERT INTO user_subnets (user_id, cidr, subnet_bits, control_plane_url, status, router_node_id, router_hostname, created_at, updated_at)
		 VALUES (?, ?, 24, '', ?, ?, ?, strftime('%s','now'), strftime('%s','now'))`,
		uid, "10.0.99.0/24", subnet.StatusRouterActive, "26", "skygate-subnet-carol",
	); err != nil {
		t.Fatalf("seed user_subnets: %v", err)
	}
	// Set the matching denorm fields.
	if _, err := s.DB.Exec(
		`UPDATE portal_users
		    SET subnet_cidr=?, subnet_status=?, subnet_router_node_id=?, subnet_router_hostname=?
		  WHERE id=?`,
		"10.0.99.0/24", subnet.StatusRouterActive, "26", "skygate-subnet-carol", uid,
	); err != nil {
		t.Fatalf("seed portal_users denorm: %v", err)
	}

	// Wire a fake headscale that handles DELETE /api/v1/node/{id}.
	_, hs := fakeRemoveHS(t)
	s.HSForUserFn = func(int64) *headscale.Client { return hs }

	req := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/remove", nil, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminUserSubnetRemove(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Remove: expected 303, got %d (body: %q)", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "/admin/users/"+itoa(uid)+"/subnet") {
		t.Errorf("expected redirect to /admin/users/.../subnet, got %q", loc)
	}
	if !strings.Contains(loc, "flash=removed") {
		t.Errorf("expected flash=removed on success, got %q", loc)
	}

	// user_subnets row should be cleared.
	sub, err := subnet.Get(s.DB, uid)
	if err != nil {
		t.Fatalf("read user_subnets after Remove: %v", err)
	}
	if sub.Status != subnet.StatusPending {
		t.Errorf("user_subnets.status = %q, want %q", sub.Status, subnet.StatusPending)
	}
	if sub.RouterNodeID != "" {
		t.Errorf("user_subnets.router_node_id = %q, want empty", sub.RouterNodeID)
	}
	if sub.RouterHostname != "" {
		t.Errorf("user_subnets.router_hostname = %q, want empty", sub.RouterHostname)
	}

	// portal_users denorm should be cleared.
	var dCIDR, dStatus, dNode, dHost string
	if err := s.DB.QueryRow(
		`SELECT subnet_cidr, subnet_status, subnet_router_node_id, subnet_router_hostname FROM portal_users WHERE id = ?`, uid,
	).Scan(&dCIDR, &dStatus, &dNode, &dHost); err != nil {
		t.Fatalf("read portal_users denorm: %v", err)
	}
	if dCIDR != "" || dStatus != subnet.StatusPending || dNode != "" || dHost != "" {
		t.Errorf("portal_users denorm not cleared: cidr=%q status=%q node=%q host=%q", dCIDR, dStatus, dNode, dHost)
	}

	// audit log should have subnet_router_removed with the
	// headscale node id in the detail.
	var gotAction, gotDetail string
	if err := s.DB.QueryRow(
		`SELECT action, detail FROM audit_log WHERE action = 'subnet_router_removed' ORDER BY id DESC LIMIT 1`,
	).Scan(&gotAction, &gotDetail); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	if !strings.Contains(gotDetail, "user_id="+itoa(uid)) || !strings.Contains(gotDetail, "deleted_headscale_node_id=26") {
		t.Errorf("audit detail missing expected fields: %q", gotDetail)
	}
}

// TestPostAdminUserSubnetRemove_NoRouterRow (v0.32.18) — the
// handler is idempotent. If router_node_id is empty, it must
// still clear the row to pending and not blow up.
func TestPostAdminUserSubnetRemove_NoRouterRow(t *testing.T) {
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "dave-remove", false)

	// Seed a user_subnets row with status=active but empty
	// router_node_id (e.g. user clicked Remove twice, or the
	// sidecar never wrote one).
	if _, err := s.DB.Exec(
		`INSERT INTO user_subnets (user_id, cidr, subnet_bits, control_plane_url, status, router_node_id, router_hostname, created_at, updated_at)
		 VALUES (?, ?, 24, '', ?, '', '', strftime('%s','now'), strftime('%s','now'))`,
		uid, "10.0.99.0/24", subnet.StatusActive,
	); err != nil {
		t.Fatalf("seed user_subnets: %v", err)
	}

	req := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/remove", nil, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminUserSubnetRemove(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Remove (no router): expected 303, got %d", w.Code)
	}

	sub, _ := subnet.Get(s.DB, uid)
	if sub.Status != subnet.StatusPending {
		t.Errorf("status = %q, want %q", sub.Status, subnet.StatusPending)
	}
}

// TestPostAdminUserSubnetRemove_NoSubnetRow (v0.32.18) — if
// the user_subnets row doesn't exist, the handler must 404
// (NOT silently succeed — the user has nothing to remove and
// the admin probably hit the button by mistake).
func TestPostAdminUserSubnetRemove_NoSubnetRow(t *testing.T) {
	s := newTestService(t)
	uid := adminSubnetSeed(t, s.DB, "eve-no-subnet", false)
	// Note: no subnet.Create call → no user_subnets row.

	req := authedReqFor(t, "POST", "/admin/users/"+itoa(uid)+"/subnet/remove", nil, "admin", true)
	w := httptest.NewRecorder()
	s.PostAdminUserSubnetRemove(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Remove (no subnet row): expected 404, got %d (body: %q)", w.Code, w.Body.String())
	}
}
