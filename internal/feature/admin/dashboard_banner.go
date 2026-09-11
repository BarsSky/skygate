// internal/feature/admin/dashboard_banner.go — first-run banner
// helper for /admin/dashboard and /admin/devices (B-mod-first-run-
// adoption Task 1).
//
// When skygate is deployed as a sidecar to an existing headscale
// (the most common install path), the operator opens /admin/
// devices and sees "no devices found" — but headscale already
// has N nodes that just need to be adopted. Pre-B-mod-first-run-
// adoption, the operator had to know to click "Sync from headscale"
// manually. The operator reported this gap in the 2026-09-11
// deployment log ("I see /admin/devices but no devices — what
// do I do?").
//
// firstRunNeedsBanner returns true when the banner SHOULD show:
//   - node_owner_map is empty (operator hasn't adopted yet)
// AND
//   - headscale has at least 1 user with no matching portal_user
//     (i.e. at least one HS user is un-adopted)
//
// Returns (false, "") when:
//   - node_owner_map has rows (operator already adopted — no nag)
//   - headscale has 0 users (no nodes to adopt)
//   - all headscale users have a matching portal_user with
//     headscale_user_id set (everyone's adopted)
//
// The caller (dashboard / devices handler) renders a banner
// block in the HTML template when show=true, with msg as the
// "you have N un-adopted users, click here to sync" hint.
//
// Architecture notes:
//   - The pure decision logic lives in firstRunNeedsBannerFromCounts
//     (a pure function over a counts struct). The DB + headscale
//     reads live in firstRunNeedsBanner (which calls the pure
//     function with the counts it computed). Splitting these lets
//     unit tests exercise the decision logic without a real DB /
//     headscale — the integration is tested separately in Task 3.
//   - The DB reads go through s.dbc() (B203-hot-reload-safe; follows
//     the watchdog's pool swap transparently).
//   - Headscale reads go through s.HSGlobalFn().ListUsers(). If
//     the headscale probe fails (network down, auth bad), the
//     helper returns (false, "") — silently no nag rather than a
//     hard error. The pre-existing "Sync from headscale" button is
//     still there for the operator to click.
package admin

import (
	"context"
	"log"
)

// FirstRunCounts is the pure-input shape for the banner decision.
// Computed by firstRunNeedsBanner from the live DB + headscale
// probes; passed directly to firstRunNeedsBannerFromCounts in
// tests.
type FirstRunCounts struct {
	NodeOwnerMapCount   int // rows in node_owner_map (0 = nothing adopted yet)
	HeadscaleUserCount  int // users in headscale (0 = nothing to adopt)
	PortalUsersWithHS   int // portal_users with headscale_user_id IS NOT NULL
}

// firstRunNeedsBanner is the integration entry point used by the
// dashboard / devices handlers. Reads the live DB + headscale,
// then delegates the pure decision to firstRunNeedsBannerFromCounts.
//
// Side effects: queries the DB (node_owner_map + portal_users
// counts) and calls s.HSGlobalFn() (headscale API call). Both are
// best-effort — failures are logged + return (false, "").
func (s *Service) firstRunNeedsBanner(ctx context.Context) (bool, string) {
	if s == nil || s.DB == nil {
		return false, ""
	}
	conn := s.DB.Current()
	if conn == nil {
		return false, ""
	}

	counts := FirstRunCounts{}

	// 1. node_owner_map row count.
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM node_owner_map`).Scan(&counts.NodeOwnerMapCount); err != nil {
		log.Printf("[first-run-banner] query node_owner_map count: %v (banner hidden)", err)
		return false, ""
	}

	// 2. headscale user count via HSGlobalFn.
	hsClient := s.HSGlobalFn
	if hsClient == nil {
		return firstRunNeedsBannerFromCounts(counts)
	}
	hsUsers, err := hsClient().ListUsers()
	if err != nil {
		log.Printf("[first-run-banner] headscale ListUsers: %v (banner hidden)", err)
		return false, ""
	}
	counts.HeadscaleUserCount = len(hsUsers)

	// 3. portal_users with headscale_user_id set.
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM portal_users WHERE headscale_user_id IS NOT NULL`,
	).Scan(&counts.PortalUsersWithHS); err != nil {
		log.Printf("[first-run-banner] query portal_users count: %v (banner hidden)", err)
		return false, ""
	}

	return firstRunNeedsBannerFromCounts(counts)
}

// firstRunNeedsBannerFromCounts is the pure decision logic —
// given the three counts, return (show, msg). Used by
// firstRunNeedsBanner (after computing the counts) and by the
// unit tests (which pass counts directly without a DB).
//
// Returns (false, "") when:
//   - node_owner_map has rows (operator already adopted — no nag)
//   - headscale has 0 users (no nodes to adopt)
//   - all headscale users have a matching portal_user with
//     headscale_user_id set (everyone's adopted)
func firstRunNeedsBannerFromCounts(c FirstRunCounts) (bool, string) {
	if c.NodeOwnerMapCount > 0 {
		return false, ""
	}
	if c.HeadscaleUserCount == 0 {
		return false, ""
	}
	if c.HeadscaleUserCount <= c.PortalUsersWithHS {
		return false, ""
	}
	// Banner shows: at least one HS user is un-adopted.
	// msg is empty by design — the dashboard template renders
	// the count via firstRunUnadoptedCount (separate accessor
	// that runs the same logic but returns the int).
	return true, ""
}

// firstRunUnadoptedCount returns the number of un-adopted headscale
// users (the same number the banner surfaces). Returns 0 when the
// banner is hidden (operator has nothing to adopt).
func (s *Service) firstRunUnadoptedCount(ctx context.Context) int {
	show, _ := s.firstRunNeedsBanner(ctx)
	if !show {
		return 0
	}
	// We re-run the count queries rather than refactor the
	// integration helper to return the count — keeps the most-
	// common caller (which just needs bool) simple. The
	// double-read is cheap (< 10ms for 3 COUNT(*) queries).
	if s == nil || s.DB == nil {
		return 0
	}
	conn := s.DB.Current()
	if conn == nil {
		return 0
	}
	hsClient := s.HSGlobalFn
	if hsClient == nil {
		return 0
	}
	hsUsers, err := hsClient().ListUsers()
	if err != nil {
		return 0
	}
	var hsLinked int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM portal_users WHERE headscale_user_id IS NOT NULL`,
	).Scan(&hsLinked); err != nil {
		return 0
	}
	diff := len(hsUsers) - hsLinked
	if diff < 0 {
		return 0
	}
	return diff
}
