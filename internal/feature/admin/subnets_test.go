package admin

// 2026-07-30: refactor-v0.30 follow-up - ported from
// internal/handlers/admin_subnets_test.go (commit 504660d
// deleted the original as part of moving GetAdminSubnets to
// feature/admin/subnets.go). 3 tests for the /admin/subnets
// flat overview page.
//
// What changed in the port:
//   - *App.GetAdminSubnets -> *Service.GetAdminSubnets
//   - a.withTemplates() -> dropped (the testBackend is a
//     no-op data-dump; the visual contract is covered by the
//     e2e smoke test on the VM, which has real templates)
//   - Template-rendered HTML string checks ("How it works",
//     "alice-subnets", "10.0.X.0/24" in body) are replaced
//     with data-map checks (the testBackend dumps the data
//     as "Key=Value\n" pairs - tests grep the body for these).
//   - "How it works" / "i18n key" body checks are dropped
//     (template-rendered; covered by VM e2e smoke).
//   - adminSubnetSeed (old handlers_test.go helper) -> direct
//     call to seedPortalUser (the new testutil helper).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/subnet"
)

// TestGetAdminSubnets_EmptyAndWithRows — v0.16.10.
// /admin/subnets renders the flat overview. Empty
// case shows no rows in the data map; populated
// case shows the per-row list with usernames + CIDRs.
func TestGetAdminSubnets_EmptyAndWithRows(t *testing.T) {
	s := newTestService(t)
	d := s.DB

	// Empty.
	req := authedReqForURL(t, "GET", "/admin/subnets", "admin", true)
	w := httptest.NewRecorder()
	s.GetAdminSubnets(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("empty: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	// Empty list: no per-row data. The Counts["all"] is 0.
	if !strings.Contains(body, "Counts=") {
		t.Errorf("expected Counts= in body, got: %.300s", body)
	}
	if !strings.Contains(body, "all:0") {
		t.Errorf("expected all:0 in Counts map, got: %.300s", body)
	}

	// Seed two users + two subnets (one active, one pending).
	uid1 := seedPortalUser(t, d, "alice-subnets", false)
	uid2 := seedPortalUser(t, d, "bob-subnets", false)
	if _, err := subnet.Create(d, uid1, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	if _, err := subnet.Create(d, uid2, "", "skygate-subnet-bob"); err != nil {
		t.Fatalf("Create bob: %v", err)
	}
	// Mark uid1 as active.
	if err := subnet.SetStatus(d, uid1, subnet.StatusActive); err != nil {
		t.Fatalf("SetStatus active: %v", err)
	}

	// Populated.
	req = authedReqForURL(t, "GET", "/admin/subnets", "admin", true)
	w = httptest.NewRecorder()
	s.GetAdminSubnets(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("populated: expected 200, got %d", w.Code)
	}
	body = w.Body.String()
	// Rows= contains the per-row data. The username appears in
	// each overviewRow's Username field.
	if !strings.Contains(body, "alice-subnets") {
		t.Errorf("expected 'alice-subnets' in Rows, got: %.500s", body)
	}
	if !strings.Contains(body, "bob-subnets") {
		t.Errorf("expected 'bob-subnets' in Rows, got: %.500s", body)
	}
	// Counts: all=2, active=1, pending=1.
	if !strings.Contains(body, "all:2") {
		t.Errorf("expected all:2 in Counts (2 subnets seeded), got: %.500s", body)
	}
	if !strings.Contains(body, "active:1") {
		t.Errorf("expected active:1 in Counts, got: %.500s", body)
	}
	if !strings.Contains(body, "pending:1") {
		t.Errorf("expected pending:1 in Counts, got: %.500s", body)
	}
}

// TestGetAdminSubnets_StatusFilter — v0.16.10.
// ?status=active narrows the list to active rows
// only; ?status=pending to pending; "" shows all.
// Uses the same seeded fixture as the previous test.
func TestGetAdminSubnets_StatusFilter(t *testing.T) {
	s := newTestService(t)
	d := s.DB

	uid1 := seedPortalUser(t, d, "alice-filter", false)
	uid2 := seedPortalUser(t, d, "bob-filter", false)
	if _, err := subnet.Create(d, uid1, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := subnet.Create(d, uid2, "", "skygate-subnet-bob"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := subnet.SetStatus(d, uid1, subnet.StatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	// ?status=active -> only alice.
	req := authedReqForURL(t, "GET", "/admin/subnets?status=active", "admin", true)
	w := httptest.NewRecorder()
	s.GetAdminSubnets(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "alice-filter") {
		t.Errorf("active filter: expected alice-filter, got: %.500s", body)
	}
	if strings.Contains(body, "bob-filter") {
		t.Errorf("active filter: bob-filter should NOT appear, got: %.500s", body)
	}
	// Counts still reflect ALL subnets (the filter only affects Rows).
	if !strings.Contains(body, "all:2") {
		t.Errorf("active filter: Counts['all'] should be 2 (both subnets exist), got: %.500s", body)
	}

	// ?status=pending -> only bob.
	req = authedReqForURL(t, "GET", "/admin/subnets?status=pending", "admin", true)
	w = httptest.NewRecorder()
	s.GetAdminSubnets(w, req)
	body = w.Body.String()
	if strings.Contains(body, "alice-filter") {
		t.Errorf("pending filter: alice should NOT appear, got: %.500s", body)
	}
	if !strings.Contains(body, "bob-filter") {
		t.Errorf("pending filter: expected bob-filter, got: %.500s", body)
	}

	// ?status=disabled -> empty list (no disabled rows).
	req = authedReqForURL(t, "GET", "/admin/subnets?status=disabled", "admin", true)
	w = httptest.NewRecorder()
	s.GetAdminSubnets(w, req)
	body = w.Body.String()
	if strings.Contains(body, "alice-filter") || strings.Contains(body, "bob-filter") {
		t.Errorf("disabled filter: no rows should appear, got: %.500s", body)
	}
	// Counts still 2 (both are still active/pending, not disabled).
	if !strings.Contains(body, "all:2") {
		t.Errorf("disabled filter: Counts['all'] should still be 2, got: %.500s", body)
	}
}

// TestGetAdminSubnets_ForbiddenForNonAdmin — v0.16.10.
// A non-admin user gets 403.
func TestGetAdminSubnets_ForbiddenForNonAdmin(t *testing.T) {
	s := newTestService(t)
	req := authedReqForURL(t, "GET", "/admin/subnets", "alice", false)
	w := httptest.NewRecorder()
	s.GetAdminSubnets(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", w.Code)
	}
}
