package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"skygate/internal/subnet"
)

// TestGetAdminSubnets_EmptyAndWithRows — v0.16.10.
// /admin/subnets renders the flat overview. Empty
// case shows the "how it works" hint; populated
// case shows the per-row table with status pills.
func TestGetAdminSubnets_EmptyAndWithRows(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	// Empty.
	req := authedReqFor(t, a, "GET", "/admin/subnets", nil, "skyadmin")
	w := httptest.NewRecorder()
	a.GetAdminSubnets(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("empty: expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "How it works") && !strings.Contains(body, "Как это работает") && !strings.Contains(body, "admin.subnets.how_it_works_title") {
		t.Errorf("expected 'how it works' hint, got body excerpt: %q",
			extractExcerpt(body, "admin.subnets"))
	}

	// Seed two users + two subnets (one active, one pending).
	uid1 := adminSubnetSeed(t, a, d, "alice-subnets")
	uid2 := adminSubnetSeed(t, a, d, "bob-subnets")
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
	req = authedReqFor(t, a, "GET", "/admin/subnets", nil, "skyadmin")
	w = httptest.NewRecorder()
	a.GetAdminSubnets(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("populated: expected 200, got %d", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, "alice-subnets") {
		t.Errorf("expected 'alice-subnets' in body, got excerpt: %q",
			extractExcerpt(body, "alice-subnets"))
	}
	if !strings.Contains(body, "bob-subnets") {
		t.Errorf("expected 'bob-subnets' in body, got excerpt: %q",
			extractExcerpt(body, "bob-subnets"))
	}
	if !strings.Contains(body, "10.0."+itoa(uid1)+".0/24") {
		t.Errorf("expected alice's CIDR in body, got excerpt: %q",
			extractExcerpt(body, "10.0."))
	}
	if !strings.Contains(body, "10.0."+itoa(uid2)+".0/24") {
		t.Errorf("expected bob's CIDR in body, got excerpt: %q",
			extractExcerpt(body, "10.0."))
	}
}

// TestGetAdminSubnets_StatusFilter — v0.16.10.
// ?status=active narrows the list to active rows
// only; ?status=pending to pending; "" shows all.
// Uses the same seeded fixture as the previous test.
func TestGetAdminSubnets_StatusFilter(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	uid1 := adminSubnetSeed(t, a, d, "alice-filter")
	uid2 := adminSubnetSeed(t, a, d, "bob-filter")
	if _, err := subnet.Create(d, uid1, "", "skygate-subnet-alice"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := subnet.Create(d, uid2, "", "skygate-subnet-bob"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := subnet.SetStatus(d, uid1, subnet.StatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	// ?status=active → only alice.
	req := authedReqFor(t, a, "GET", "/admin/subnets?status=active", nil, "skyadmin")
	w := httptest.NewRecorder()
	a.GetAdminSubnets(w, req)
	body := w.Body.String()
	if !strings.Contains(body, "alice-filter") {
		t.Errorf("active filter: expected alice-filter, got: %q", extractExcerpt(body, "alice-filter"))
	}
	if strings.Contains(body, "bob-filter") {
		t.Errorf("active filter: bob-filter should NOT appear, got: %q", extractExcerpt(body, "bob-filter"))
	}

	// ?status=pending → only bob.
	req = authedReqFor(t, a, "GET", "/admin/subnets?status=pending", nil, "skyadmin")
	w = httptest.NewRecorder()
	a.GetAdminSubnets(w, req)
	body = w.Body.String()
	if strings.Contains(body, "alice-filter") {
		t.Errorf("pending filter: alice should NOT appear, got: %q", extractExcerpt(body, "alice-filter"))
	}
	if !strings.Contains(body, "bob-filter") {
		t.Errorf("pending filter: expected bob-filter, got: %q", extractExcerpt(body, "bob-filter"))
	}

	// ?status=disabled → empty list (no disabled rows).
	req = authedReqFor(t, a, "GET", "/admin/subnets?status=disabled", nil, "skyadmin")
	w = httptest.NewRecorder()
	a.GetAdminSubnets(w, req)
	body = w.Body.String()
	if strings.Contains(body, "alice-filter") || strings.Contains(body, "bob-filter") {
		t.Errorf("disabled filter: no rows should appear, got: %q", extractExcerpt(body, "alice-filter"))
	}
}

// TestGetAdminSubnets_ForbiddenForNonAdmin — v0.16.10.
// A non-admin user gets 403.
func TestGetAdminSubnets_ForbiddenForNonAdmin(t *testing.T) {
	a, d := newTestApp(t, &testNotifier{})
	defer d.Close()
	a.withTemplates()

	req := authedReqFor(t, a, "GET", "/admin/subnets", nil, "alice")
	w := httptest.NewRecorder()
	a.GetAdminSubnets(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-admin, got %d", w.Code)
	}
}
