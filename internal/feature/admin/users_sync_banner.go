// internal/feature/admin/users_sync_banner.go — v1.5.2 admin-user-sync
// T6: startup detection banner.
//
// When SKYGATE_ADMIN_USER (env) doesn't match the actual admin in
// portal_users, the operator sees a banner on /admin/users offering
// "Adopt as Admin" (T5 path) or "Rename to match" (T4 path). This
// closes the "drift detected by check_b_admin_user_sync.sh (T1) but
// no remediation inside the UI" gap.
//
// Two drift cases:
//   1. SKYGATE_ADMIN_USER is a name that EXISTS in headscale but
//      doesn't have a matching portal_users row → suggest "Adopt
//      as Admin" (creates portal row with is_admin=1 + hs_id).
//      Pre-fix the operator had to find the headscale orphan
//      manually and run InsertPortalUserAdoptAdmin by SQL.
//   2. SKYGATE_ADMIN_USER is a name that EXISTS in headscale AND
//      matches a portal user with is_admin=0 → suggest "Promote
//      to Admin" (single UPDATE).
//
// (We don't show a banner for the "no admin row at all" case —
// that's already the first-run banner's territory. We don't show
// a banner for the "rename to match" case — T4 already provides
// that as a button per row.)
//
// Architecture mirrors firstRunNeedsBanner (dashboard_banner.go):
//   - adminUserSyncBanner (integration helper) reads DB + headscale,
//     then delegates to adminUserSyncBannerFromFacts (pure).
//   - The pure function takes AdminSyncFacts (counts + names) and
//     returns the (show, mode, headscaleUserId) tuple. Unit tests
//     exercise the pure decision; integration is tested via the
//     end-to-end verify in T9.
//
// 2026-09-12: T6 of admin-user-sync.
package admin

import (
	"context"
	"log"
	"strconv"
)

// AdminSyncMode describes which remediation the banner offers.
// Each mode maps to a different form on /admin/users/{id} or
// /admin/users/HSOrphan/adopt.
type AdminSyncMode int

const (
	// AdminSyncNone means no drift detected — banner hidden.
	AdminSyncNone AdminSyncMode = iota
	// AdminSyncAdoptAsAdmin means there's a headscale user
	// named SKYGATE_ADMIN_USER without a matching portal row
	// → operator should adopt (T5 path: promote_to_admin=true).
	AdminSyncAdoptAsAdmin
	// AdminSyncPromoteToAdmin means there's a portal user with
	// the SKYGATE_ADMIN_USER name but is_admin=0 → operator
	// should promote.
	AdminSyncPromoteToAdmin
)

// String renders AdminSyncMode for audit logs and template
// conditionals. Kept lowercase + underscored to match the
// template syntax ({{if eq .AdminSyncMode "adopt_as_admin"}}).
func (m AdminSyncMode) String() string {
	switch m {
	case AdminSyncAdoptAsAdmin:
		return "adopt_as_admin"
	case AdminSyncPromoteToAdmin:
		return "promote_to_admin"
	default:
		return "none"
	}
}

// AdminSyncFacts is the pure-input shape for the banner decision.
// All fields are computed from the live DB + headscale probes by
// adminUserSyncBanner; tests pass a pre-built facts struct to
// adminUserSyncBannerFromFacts directly.
type AdminSyncFacts struct {
	// ExpectedAdminUsername is SKYGATE_ADMIN_USER from env.
	// Empty disables the banner (operator hasn't configured
	// the env var → nothing to reconcile against).
	ExpectedAdminUsername string

	// PortalAdminCount is the number of portal_users rows with
	// is_admin=1 AND headscale_user_id IS NOT NULL. The banner
	// triggers when this is 0 (no linked admin) or > 1 (multiple
	// linked admins) or the row's username != ExpectedAdminUsername.
	PortalAdminCount int
	// PortalAdminUsername is the username of the linked admin
	// row (the one with is_admin=1 AND headscale_user_id IS NOT
	// NULL). Empty when PortalAdminCount == 0 or > 1.
	PortalAdminUsername string
	// PortalAdminIsAdmin is true if the matching portal row
	// has is_admin=1 (the linked-admin check covers this; this
	// is the second-pass check for the case where the admin row
	// is linked but somehow is_admin flipped to 0).
	PortalAdminIsAdmin bool
	// PortalAdminID is the id of the linked admin row (the row
	// that owns the linked-admin check). Used by the T6.1
	// AdminSyncPromoteToAdmin banner to render the per-row
	// /admin/users/{id}/promote form action. 0 when the banner
	// isn't in promote mode.
	PortalAdminID int64

	// HeadscaleHasExpected is true when headscale has a user
	// with name == ExpectedAdminUsername. Used to choose
	// between AdoptAsAdmin (HS user exists → adopt) and a no-op
	// (HS user doesn't exist → operator must create the HS user
	// first or fix SKYGATE_ADMIN_USER).
	HeadscaleHasExpected bool
	// HeadscaleExpectedID is the headscale user id for the
	// matching user (when HeadscaleHasExpected is true). 0
	// otherwise. The banner's Adopt form embeds this as the
	// hs_id form field.
	HeadscaleExpectedID int64
}

// adminUserSyncBannerFromFacts is the pure decision logic.
// Returns (mode, facts). The facts are echoed back so callers
// can render the banner without re-querying.
func adminUserSyncBannerFromFacts(f AdminSyncFacts) (AdminSyncMode, AdminSyncFacts) {
	if f.ExpectedAdminUsername == "" {
		return AdminSyncNone, f
	}

	// Case A: portal has no linked admin row → drift.
	if f.PortalAdminCount == 0 {
		if f.HeadscaleHasExpected {
			// The headscale user exists; just create the portal row.
			return AdminSyncAdoptAsAdmin, f
		}
		// No portal row AND no headscale user with that name →
		// operator needs to fix SKYGATE_ADMIN_USER first.
		// We don't surface a banner (the "no headscale user" case
		// is confusing without a remediation path inside skygate).
		return AdminSyncNone, f
	}

	// Case B: portal has > 1 linked admin → drift (data corruption).
	// We don't surface a banner — the operator needs to clean up
	// duplicates manually via /admin/users DELETE.
	if f.PortalAdminCount > 1 {
		return AdminSyncNone, f
	}

	// Case C: portal has exactly 1 linked admin row, but the
	// username doesn't match SKYGATE_ADMIN_USER → drift.
	// Two sub-cases:
	//   - the portal admin row's username matches a headscale
	//     user (i.e. they need to be renamed) → T4 path,
	//     handled per-row, no banner.
	//   - the portal admin row's username doesn't match ANY
	//     headscale user → orphan admin row, needs cleanup,
	//     no banner (operator should DELETE the orphan row).
	if f.PortalAdminUsername != f.ExpectedAdminUsername {
		return AdminSyncNone, f
	}

	// Case D: portal has 1 linked admin row with the right
	// username, but is_admin somehow flipped to 0 → suggest
	// promote_to_admin.
	if !f.PortalAdminIsAdmin {
		return AdminSyncPromoteToAdmin, f
	}

	// Case E: everything matches → no drift.
	return AdminSyncNone, f
}

// adminUserSyncBanner is the integration helper. Reads the live
// DB + headscale, then delegates the decision to
// adminUserSyncBannerFromFacts. Failures (DB down, headscale
// down) hide the banner — never show an erroneous nag.
func (s *Service) adminUserSyncBanner(ctx context.Context, expectedAdmin string) (AdminSyncMode, AdminSyncFacts) {
	facts := AdminSyncFacts{
		ExpectedAdminUsername: expectedAdmin,
	}

	if expectedAdmin == "" {
		return adminUserSyncBannerFromFacts(facts)
	}
	if s == nil || s.DB == nil {
		return AdminSyncNone, facts
	}
	conn := s.DB.Current()
	if conn == nil {
		return AdminSyncNone, facts
	}

	// Query 1: the linked-admin row (count + username + id in one
	// round-trip). The WHERE filters are: is_admin=1 AND
	// headscale_user_id IS NOT NULL — the B-check (T1) contract.
	// is_admin is trivially 1 for every row that survives the
	// WHERE (the filter guarantees it), so we don't need a
	// third column — PortalAdminIsAdmin = true whenever the
	// count is 1.
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MIN(username), ''), COALESCE(MIN(id), 0)
		FROM portal_users
		WHERE is_admin = 1 AND headscale_user_id IS NOT NULL
	`).Scan(&facts.PortalAdminCount, &facts.PortalAdminUsername, &facts.PortalAdminID)
	if err != nil {
		log.Printf("[admin-sync-banner] portal admin count: %v (banner hidden)", err)
		return AdminSyncNone, facts
	}
	// Normalize: count==0 means there's no linked admin row.
	// count==1 means there's exactly one — the matching username
	// is in PortalAdminUsername and (by the WHERE filter) it
	// has is_admin=1.
	if facts.PortalAdminCount == 1 {
		facts.PortalAdminIsAdmin = true
	} else {
		facts.PortalAdminUsername = ""
		facts.PortalAdminID = 0
		facts.PortalAdminIsAdmin = false
	}

	// Query 2: headscale user with the expected name.
	hsClient := s.HSGlobalFn
	if hsClient == nil {
		return adminUserSyncBannerFromFacts(facts)
	}
	hsUsers, err := hsClient().ListUsers()
	if err != nil {
		log.Printf("[admin-sync-banner] headscale ListUsers: %v (banner hidden)", err)
		return AdminSyncNone, facts
	}
	for _, u := range hsUsers {
		if u.Name == expectedAdmin {
			facts.HeadscaleHasExpected = true
			facts.HeadscaleExpectedID, _ = strconv.ParseInt(u.ID, 10, 64)
			break
		}
	}

	// Query 3 (T6.1, 2026-09-13): Case D — the right username but
	// is_admin=0 case. The Query 1 filter (is_admin=1) deliberately
	// excludes this row, so PortalAdminCount stays 0 and Case A
	// would otherwise fire (adopt). We need to detect "right
	// username + linked HS but is_admin=0" so the banner can offer
	// a Promote remediation instead of an Adopt that would CREATE
	// a duplicate row.
	if facts.PortalAdminCount == 0 && facts.HeadscaleHasExpected {
		var demotedID int64
		err := conn.QueryRowContext(ctx, `
			SELECT id FROM portal_users
			WHERE username = $1 AND headscale_user_id IS NOT NULL AND is_admin = 0
			LIMIT 1
		`, expectedAdmin).Scan(&demotedID)
		if err == nil && demotedID > 0 {
			// Override the Case A facts so the decision function
			// routes us to AdminSyncPromoteToAdmin instead of
			// AdminSyncAdoptAsAdmin. We synthesise a "count==1,
			// username matches, is_admin=0" facts shape.
			facts.PortalAdminCount = 1
			facts.PortalAdminUsername = expectedAdmin
			facts.PortalAdminID = demotedID
			facts.PortalAdminIsAdmin = false
		}
		// err != nil (sql.ErrNoRows) is the expected "no demoted
		// row" case — leave facts at their Case A shape so the
		// banner offers Adopt.
	}

	return adminUserSyncBannerFromFacts(facts)
}
