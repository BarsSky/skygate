// internal/feature/admin/dashboard_banner_test.go — RED tests for
// B-mod-first-run-adoption Task 1.
//
// Tests exercise the PURE decision logic in
// firstRunNeedsBannerFromCounts(FirstRunCounts). The integration
// helper (firstRunNeedsBanner) reads live DB + headscale and
// delegates to this pure function; the integration is tested in
// Task 3 (the auto-sync path) with a real :memory: DB.
package admin

import (
	"testing"
)

// TestFirstRunBannerFromCounts_HiddenWhenNodeOwnerMapPopulated —
// operator has already adopted at least one node; banner hidden.
func TestFirstRunBannerFromCounts_HiddenWhenNodeOwnerMapPopulated(t *testing.T) {
	show, msg := firstRunNeedsBannerFromCounts(FirstRunCounts{
		NodeOwnerMapCount:  1, // >=1 row → adopted
		HeadscaleUserCount: 2,
		PortalUsersWithHS:  1,
	})
	if show {
		t.Errorf("expected banner hidden when node_owner_map populated; got show=%v msg=%q", show, msg)
	}
	if msg != "" {
		t.Errorf("expected empty msg when banner hidden; got %q", msg)
	}
}

// TestFirstRunBannerFromCounts_ShownWhenEmptyAndUsersExist —
// the classic "un-adopted nodes" state.
func TestFirstRunBannerFromCounts_ShownWhenEmptyAndUsersExist(t *testing.T) {
	show, msg := firstRunNeedsBannerFromCounts(FirstRunCounts{
		NodeOwnerMapCount:  0, // nothing adopted
		HeadscaleUserCount: 2, // 2 HS users
		PortalUsersWithHS:  1, // 1 already linked → 1 un-adopted
	})
	if !show {
		t.Errorf("expected banner shown (HSUsers=2 > linked=1); got show=%v msg=%q", show, msg)
	}
	if msg != "" {
		t.Errorf("expected empty msg (banner UI reads count from template); got %q", msg)
	}
}

// TestFirstRunBannerFromCounts_HiddenWhenNoHeadscaleUsers — no
// headscale users = nothing to adopt.
func TestFirstRunBannerFromCounts_HiddenWhenNoHeadscaleUsers(t *testing.T) {
	show, _ := firstRunNeedsBannerFromCounts(FirstRunCounts{
		NodeOwnerMapCount:  0,
		HeadscaleUserCount: 0,
		PortalUsersWithHS:  0,
	})
	if show {
		t.Errorf("expected banner hidden when headscale has 0 users; got show=true")
	}
}

// TestFirstRunBannerFromCounts_ShownWhenAdminHasMoreHeadscaleUsers
// — operator has 1 portal user (admin) but headscale has 2
// (admin + daniil). node_owner_map empty, portalUserWithHS=0
// because admin's row hasn't been linked yet either.
func TestFirstRunBannerFromCounts_ShownWhenAdminHasMoreHeadscaleUsers(t *testing.T) {
	show, msg := firstRunNeedsBannerFromCounts(FirstRunCounts{
		NodeOwnerMapCount:  0,
		HeadscaleUserCount: 2,
		PortalUsersWithHS:  0, // 0 linked → 2 un-adopted
	})
	if !show {
		t.Errorf("expected banner shown (HSUsers=2 > linked=0); got show=%v msg=%q", show, msg)
	}
}

// TestFirstRunBannerFromCounts_HiddenWhenAllHeadscaleUsersAdopted
// — 1 HS user, 1 portal_user linked → all adopted, banner
// hidden even though node_owner_map is empty (link exists in
// portal_users but the nodes haven't been claimed yet; this is
// a valid intermediate state — the operator did the link step
// but hasn't done the per-node claim step).
func TestFirstRunBannerFromCounts_HiddenWhenAllHeadscaleUsersAdopted(t *testing.T) {
	show, _ := firstRunNeedsBannerFromCounts(FirstRunCounts{
		NodeOwnerMapCount:  0,
		HeadscaleUserCount: 1,
		PortalUsersWithHS:  1,
	})
	if show {
		t.Errorf("expected banner hidden when all HS users linked (HSUsers=1 == linked=1); got show=true")
	}
}

// TestFirstRunBannerFromCounts_HiddenWhenAllHeadscaleUsersLinked
// — boundary case: HSUsers == linked but not strictly >.
func TestFirstRunBannerFromCounts_HiddenWhenHSUsersEqualsLinked(t *testing.T) {
	show, _ := firstRunNeedsBannerFromCounts(FirstRunCounts{
		NodeOwnerMapCount:  0,
		HeadscaleUserCount: 3,
		PortalUsersWithHS:  3, // equal → all adopted
	})
	if show {
		t.Errorf("expected banner hidden when HSUsers==linked; got show=true")
	}
}

// TestFirstRunBannerFromCounts_LargeUnadopted — sanity check
// with a large number (operator with 50 headscale users, only
// 2 linked).
func TestFirstRunBannerFromCounts_LargeUnadopted(t *testing.T) {
	show, _ := firstRunNeedsBannerFromCounts(FirstRunCounts{
		NodeOwnerMapCount:  0,
		HeadscaleUserCount: 50,
		PortalUsersWithHS:  2,
	})
	if !show {
		t.Errorf("expected banner shown for 48 un-adopted users")
	}
}
