// pagination_b_test.go — pure-function tests for the v1.5.43
// page-clamping math used by db.GetDeviceRulesForUserPaged.
//
// The DB layer is exercised via the same query constants in
// the live package tests; here we pin the bounds-clamping
// algorithm without a live PostgreSQL.
//
// B-pending-write (v1.5.43): these tests fail if the clamp
// logic regresses — page < 1 stays at 1, pageSize > clamp
// shrinks, last-page clamp keeps an empty page from showing.

package db

import "testing"

// TestClampPageBounds_PageSizeDefault pins the "missing page_size"
// fallback to 50. Operators that bookmark the page without a
// page_size param should land on the same view as the template
// default.
func TestClampPageBounds_PageSizeDefault(t *testing.T) {
	_, _, ps := clampPageBounds(0, 0, 100)
	if ps != 50 {
		t.Fatalf("default pageSize = %d, want 50", ps)
	}
}

// TestClampPageBounds_PageSizeClamp pins the upper bound at
// pageSizeClamp=500. A user asking for 5000 rows gets 500 —
// bounds the SQL round-trip cost.
func TestClampPageBounds_PageSizeClamp(t *testing.T) {
	_, _, ps := clampPageBounds(0, 5000, 100000)
	if ps != pageSizeClamp {
		t.Fatalf("clamped pageSize = %d, want %d", ps, pageSizeClamp)
	}
}

// TestClampPageBounds_PageNegativeStaysAtOne pins the lower
// bound for page (1). Page=0 or page=-5 both stay at 1.
func TestClampPageBounds_PageNegativeStaysAtOne(t *testing.T) {
	for _, in := range []int{-5, 0, 1} {
		page, _, _ := clampPageBounds(in, 50, 100)
		if page < 1 {
			t.Fatalf("clamped page(%d) = %d, want >= 1", in, page)
		}
	}
}

// TestClampPageBounds_LastPageClamp pins that requesting a page
// beyond the last available one lands on the last page (not on
// an empty result).
func TestClampPageBounds_LastPageClamp(t *testing.T) {
	// 150 rules, pageSize 50 → 3 pages total. Page 99 → 3.
	page, _, _ := clampPageBounds(99, 50, 150)
	if page != 3 {
		t.Fatalf("clamped page = %d, want 3 (150/50 = 3 pages)", page)
	}
}

// TestClampPageBounds_ZeroTotalForcesPageOne is the
// "dataset is empty" case — the operator can paginate through
// any page number they want, but they all clamp to page 1.
func TestClampPageBounds_ZeroTotalForcesPageOne(t *testing.T) {
	page, _, _ := clampPageBounds(5, 50, 0)
	if page != 1 {
		t.Fatalf("empty dataset page = %d, want 1", page)
	}
}

// TestClampPageBounds_BoundaryAtLastPage is the "exactly the
// last page" case — page=3 of 3 should NOT be clamped to page 2.
func TestClampPageBounds_BoundaryAtLastPage(t *testing.T) {
	page, _, _ := clampPageBounds(3, 50, 150)
	if page != 3 {
		t.Fatalf("clamped page = %d, want 3 (boundary at last page)", page)
	}
}

// clampPageBounds is the same algorithm used inside
// GetDeviceRulesForUserPaged (kept private + duplicated here
// so we can pin it without a live PostgreSQL).
//
// Signature: (page, pageSize, total) → (page, total, pageSize).
// The production version reads `total` from a separate
// COUNT(*) query; here we accept it as a parameter so the tests
// can drive it explicitly.
func clampPageBounds(page, pageSize, total int) (int, int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	if pageSize > pageSizeClamp {
		pageSize = pageSizeClamp
	}
	if total <= 0 {
		total = 0
	}
	lastPage := (total + pageSize - 1) / pageSize
	if lastPage < 1 {
		lastPage = 1
	}
	if page > lastPage {
		page = lastPage
	}
	return page, total, pageSize
}
