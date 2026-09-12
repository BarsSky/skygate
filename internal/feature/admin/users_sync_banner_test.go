// internal/feature/admin/users_sync_banner_test.go — unit tests for
// the pure adminUserSyncBannerFromFacts decision function.
//
// The integration helper (adminUserSyncBanner) is exercised by
// the T9 end-to-end verify. These tests pin the pure decision
// logic so a future refactor that flips the drift cases trips
// the test, not the operator.

package admin

import (
	"testing"
)

func TestAdminUserSyncBannerFromFacts_NoDrift_Expected(t *testing.T) {
	// ExpectedAdminUsername + 1 portal admin row matching it
	// + headscale has the user → no drift.
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "skyadmin",
		PortalAdminCount:      1,
		PortalAdminUsername:   "skyadmin",
		PortalAdminIsAdmin:    true,
		HeadscaleHasExpected:  true,
		HeadscaleExpectedID:   86,
	})
	if got != AdminSyncNone {
		t.Errorf("got %v, want AdminSyncNone (no drift)", got)
	}
}

func TestAdminUserSyncBannerFromFacts_NoDrift_EmptyExpected(t *testing.T) {
	// Empty SKYGATE_ADMIN_USER → banner hidden (operator
	// hasn't configured the env var → nothing to reconcile).
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "",
		PortalAdminCount:      1,
		PortalAdminUsername:   "skyadmin",
		PortalAdminIsAdmin:    true,
		HeadscaleHasExpected:  true,
		HeadscaleExpectedID:   86,
	})
	if got != AdminSyncNone {
		t.Errorf("got %v, want AdminSyncNone (empty SKYGATE_ADMIN_USER)", got)
	}
}

func TestAdminUserSyncBannerFromFacts_AdoptAsAdmin_NoPortalRow(t *testing.T) {
	// 0 portal admin rows + headscale has the expected user →
	// adopt_as_admin.
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "skyadmin",
		PortalAdminCount:      0,
		HeadscaleHasExpected:  true,
		HeadscaleExpectedID:   86,
	})
	if got != AdminSyncAdoptAsAdmin {
		t.Errorf("got %v, want AdminSyncAdoptAsAdmin", got)
	}
}

func TestAdminUserSyncBannerFromFacts_NoBanner_NoPortalNoHeadscale(t *testing.T) {
	// 0 portal admin rows AND no headscale user with that name →
	// operator needs to fix SKYGATE_ADMIN_USER first. We don't
	// surface a banner (the "no HS user" case has no
	// remediation path inside skygate).
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "skyadmin",
		PortalAdminCount:      0,
		HeadscaleHasExpected:  false,
	})
	if got != AdminSyncNone {
		t.Errorf("got %v, want AdminSyncNone (no portal row AND no headscale user)", got)
	}
}

func TestAdminUserSyncBannerFromFacts_NoBanner_MultiplePortalAdmins(t *testing.T) {
	// > 1 portal admin row → data corruption; operator must
	// clean up duplicates. Banner hidden.
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "skyadmin",
		PortalAdminCount:      2,
		PortalAdminUsername:   "skyadmin",
		PortalAdminIsAdmin:    true,
		HeadscaleHasExpected:  true,
		HeadscaleExpectedID:   86,
	})
	if got != AdminSyncNone {
		t.Errorf("got %v, want AdminSyncNone (multiple linked admins; operator must dedupe)", got)
	}
}

func TestAdminUserSyncBannerFromFacts_NoBanner_PortalAdminDifferentName(t *testing.T) {
	// Portal has 1 linked admin row, but the username is
	// different from SKYGATE_ADMIN_USER. The operator either
	// needs to rename (T4) or delete the orphan row — both
	// are per-row actions, not banner-worthy.
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "skyadmin",
		PortalAdminCount:      1,
		PortalAdminUsername:   "oldadmin",
		PortalAdminIsAdmin:    true,
		HeadscaleHasExpected:  true,
		HeadscaleExpectedID:   86,
	})
	if got != AdminSyncNone {
		t.Errorf("got %v, want AdminSyncNone (portal admin username != SKYGATE_ADMIN_USER; per-row remediation)", got)
	}
}

func TestAdminUserSyncBannerFromFacts_NoBanner_PortalAdminOrphan(t *testing.T) {
	// Portal has 1 linked admin row with the right username,
	// but the headscale user doesn't exist (orphaned portal
	// row). Operator must delete the portal row (B162/B169
	// per-row) — banner hidden.
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "skyadmin",
		PortalAdminCount:      1,
		PortalAdminUsername:   "skyadmin",
		PortalAdminIsAdmin:    true,
		HeadscaleHasExpected:  false,
	})
	if got != AdminSyncNone {
		t.Errorf("got %v, want AdminSyncNone (orphan portal row; per-row DELETE)", got)
	}
}

func TestAdminUserSyncBannerFromFacts_PromoteToAdmin(t *testing.T) {
	// Portal has 1 linked row with the expected username, but
	// is_admin somehow flipped to 0 → promote.
	got, _ := adminUserSyncBannerFromFacts(AdminSyncFacts{
		ExpectedAdminUsername: "skyadmin",
		PortalAdminCount:      1,
		PortalAdminUsername:   "skyadmin",
		PortalAdminIsAdmin:    false, // someone flipped is_admin to 0
		HeadscaleHasExpected:  true,
		HeadscaleExpectedID:   86,
	})
	if got != AdminSyncPromoteToAdmin {
		t.Errorf("got %v, want AdminSyncPromoteToAdmin", got)
	}
}

func TestAdminSyncMode_String(t *testing.T) {
	for _, tc := range []struct {
		m    AdminSyncMode
		want string
	}{
		{AdminSyncNone, "none"},
		{AdminSyncAdoptAsAdmin, "adopt_as_admin"},
		{AdminSyncPromoteToAdmin, "promote_to_admin"},
		{AdminSyncMode(999), "none"}, // unknown → none
	} {
		if got := tc.m.String(); got != tc.want {
			t.Errorf("AdminSyncMode(%d).String() = %q, want %q", tc.m, got, tc.want)
		}
	}
}
