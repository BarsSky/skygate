// routes_headscale.go — B528 step 1 (v1.5.265): the /admin/headscale route group.
//
// registerRoutes() is a BUDGETED function (B525: 1 109 lines at cc 2) and B533 had to add
// one route to it — the ratchet refused the growth (C1: 1 109 → 1 115) on CI run
// 46024883213, which is exactly what it exists for. Rather than argue the budget up, the
// headscale group moves here VERBATIM: it needs nothing but mux, authMW and adminSvc, so
// the seam costs nothing at the call site and shows the shape the rest of B528 will take
// (one sub-registry per feature, the parameters the group actually uses).
//
// Why a MOVE and not a rewrite: the routes below are the same strings, the same handlers
// and the same comments as before, in the same order inside the group. A pure relocation
// keeps git blame readable and keeps the contracts that read the entrypoint SURFACE
// (scripts/lib/gosurface.sh, e.g. B39's /admin/headscale/acl rows) satisfied without
// touching them.
package main

import (
	"net/http"

	adminsvc "skygate/internal/feature/admin"
)

// registerHeadscaleRoutes registers every /admin/headscale* route: the monitor status
// page, the Network Access Manager (B92), the manual re-poll button and, since B533, the
// privileged service restart.
func registerHeadscaleRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, adminSvc *adminsvc.Service) {
	// 2026-07-20: v0.20.0 — headscale-update-monitor
	// status page. Renders the monitor's snapshot
	// (pinned vs. latest, history table). Admin-only.
	mux.Handle("GET /admin/headscale", authMW(http.HandlerFunc(adminSvc.GetAdminHeadscale)))
	// 2026-08-04: v0.33.0 — Network Access Manager. Add /
	// remove skygate-managed headscale ACL rules without
	// touching operator-added ones. Idempotent on rule
	// fingerprint. See internal/feature/admin/headscale_acl.go.
	mux.Handle("GET /admin/headscale/acl", authMW(http.HandlerFunc(adminSvc.GetAdminHeadscaleACL)))
	mux.Handle("POST /admin/headscale/acl/add", authMW(http.HandlerFunc(adminSvc.PostAdminHeadscaleACLAdd)))
	mux.Handle("POST /admin/headscale/acl/remove", authMW(http.HandlerFunc(adminSvc.PostAdminHeadscaleACLRemove)))
	// 2026-07-20: v0.20.0 — "Run check now" button on
	// /admin/headscale. Forces the monitor to re-poll
	// GitHub immediately. Same pattern as
	// /admin/exit-nodes/health-now.
	mux.Handle("POST /admin/headscale/check-now", authMW(http.HandlerFunc(adminSvc.PostAdminHeadscaleCheckNow)))
	// 2026-11-27: B533 — restart the headscale SERVICE from the panel. The
	// handler never calls systemctl itself: it stages a data-only request for
	// the root-owned skygate-apply-service.sh (deploy/install-service-helper.sh),
	// the same privilege split the policy (B272.3) and local-routes (B293)
	// appliers use, because the native unit is unprivileged.
	mux.Handle("POST /admin/headscale/service/restart", authMW(http.HandlerFunc(adminSvc.PostAdminHeadscaleServiceRestart)))
}
