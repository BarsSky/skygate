// routes_admin_users.go — B528 step 3 (2026-11-27): the /admin/users family.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. This group moves here 26 routes VERBATIM — the users page and its per-row
// actions (23), the per-user headscale control plane (2) and the subnet-routing landing
// page the users page links to (1). The parameter list is exactly what the moved lines
// use, and the compiler derives it: (mux, authMW, adminSvc), the same seam the earlier
// groups needed.
package main

import (
	"net/http"

	adminsvc "skygate/internal/feature/admin"
)

// registerAdminUsersRoutes registers the /admin/users family: the portal-user page and
// its per-row writes, the per-user control plane, and the subnet-routing landing page.
func registerAdminUsersRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, adminSvc *adminsvc.Service) {
	// Admin
	mux.Handle("GET /admin/users", authMW(http.HandlerFunc(adminSvc.GetAdminUsers)))
	mux.Handle("POST /admin/users", authMW(http.HandlerFunc(adminSvc.PostAdminUser)))
	mux.Handle("POST /admin/users/{id}/delete", authMW(http.HandlerFunc(adminSvc.PostAdminDeleteUser)))
	mux.Handle("POST /admin/users/{id}/reset-password", authMW(http.HandlerFunc(adminSvc.PostAdminUserResetPassword)))
	// v1.5.2 admin-user-sync (option c) T4: "Rename" button on
	// /admin/users/{id}. Pre-T4 the operator had to SSH into the
	// VM, run `docker exec headscale headscale users rename -i
	// <id> <new>`, UPDATE portal_users.username by hand, and
	// restart skygate so cached state refreshed — a 3-step +
	// 2-command gap every time SKYGATE_ADMIN_USER drifted from
	// the headscale admin user. T4 wraps that into a single
	// button per row: PostAdminUserRename calls
	// HSGlobalFn().RenameUser (T3) + db.UpdatePortalUsername,
	// emits an audit row, and surfaces headscale-side errors as
	// friendly ?err= flashes (duplicate-name conflict gets a
	// distinct message so the operator can clean up the dup
	// before retrying).
	mux.Handle("POST /admin/users/{id}/rename", authMW(http.HandlerFunc(adminSvc.PostAdminUserRename)))
	// v1.4.0 B141: "Adopt as skygate user" button on the
	// /admin/users HSOrphans list. The pre-B141 admin UI only
	// DISPLAYED the orphans list — to adopt one the operator had
	// to run a manual SQL INSERT into portal_users with the
	// headscale_user_id, plus a separate API call to set the
	// password. B141 wraps that into a single button per row.
	// The handler (PostAdminHSOrphanAdopt) uses ON CONFLICT DO
	// NOTHING so concurrent clicks on the same orphan are safe
	// (the second click gets a friendly "already adopted" flash
	// instead of an error).
	mux.Handle("POST /admin/users/HSOrphan/adopt", authMW(http.HandlerFunc(adminSvc.PostAdminHSOrphanAdopt)))
	// 2026-09-13: v1.5.2 admin-user-sync T6.1 — Promote button on
	// the AdminSyncPromoteToAdmin drift banner (portal row has the
	// right username + linked HS but is_admin somehow flipped to 0).
	// Single UPDATE that flips is_admin 0 → 1 + writes an
	// 'admin_promote' audit row. Idempotent: a re-click on an
	// already-admin row short-circuits with already_admin=<username>
	// flash (no DB change, no audit row).
	mux.Handle("POST /admin/users/{id}/promote", authMW(http.HandlerFunc(adminSvc.PostAdminUserPromote)))
	// 2026-09-19: v0.72 (B264) — Demote button next to Promote in the
	// per-row action menu on /admin/users. Same admin-only + authMW
	// chain as the sibling writes. Revokes is_admin for another portal
	// user; refuses the primary (bootstrap/root) row, self-demotion and
	// the last remaining admin (see PostAdminUserDemote). Audit action
	// 'admin_demote'.
	mux.Handle("POST /admin/users/{id}/demote", authMW(http.HandlerFunc(adminSvc.PostAdminUserDemote)))
	// 2026-07-15: v0.12.0 — per-user headscale control plane
	// (multi-tailnet). /admin/control-planes is the landing;
	// /admin/users/{id}/plane is the per-user edit form.
	mux.Handle("GET /admin/control-planes", authMW(http.HandlerFunc(adminSvc.GetAdminControlPlanes)))
	mux.Handle("POST /admin/control-planes/test", authMW(http.HandlerFunc(adminSvc.PostAdminControlPlanesTest)))
	mux.Handle("GET /admin/users/{id}/plane", authMW(http.HandlerFunc(adminSvc.GetAdminUserControlPlane)))
	mux.Handle("POST /admin/users/{id}/plane", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlane)))
	mux.Handle("POST /admin/users/{id}/plane/clear", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlaneClear)))
	// 2026-07-21: v0.23.0 Phase 1 — one-click provisioning of a
	// per-user headscale container. The Provision action runs the
	// bootstrap script (creates container + user + API key, returns
	// JSON) and persists the result to portal_users. The
	// Decommission action reverses it: tears down the container and
	// clears the DB row (data on disk is preserved for recovery).
	mux.Handle("POST /admin/users/{id}/plane/provision", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlaneProvision)))
	mux.Handle("POST /admin/users/{id}/plane/decommission", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlaneDecommission)))
	// 2026-07-17: v0.16.0 — per-user subnets admin page.
	// GET shows the user's subnet status; POSTs allocate
	// / disable / run a sanity check.
	mux.Handle("GET /admin/users/{id}/subnet", authMW(http.HandlerFunc(adminSvc.GetAdminUserSubnet)))
	mux.Handle("POST /admin/users/{id}/subnet/allocate", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetAllocate)))
	mux.Handle("POST /admin/users/{id}/subnet/disable", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetDisable)))
	// 2026-08-03: v0.32.18 — full subnet-router lifecycle
	// (Provision creates, Remove destroys). Disables the
	// headscale node and clears all related state in one
	// atomic flow; the older Disable button is the softer
	// "opt out without losing the row" option.
	mux.Handle("POST /admin/users/{id}/subnet/remove", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetRemove)))
	mux.Handle("POST /admin/users/{id}/subnet/test", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetTest)))
	mux.Handle("POST /admin/users/{id}/subnet/provision", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetProvision)))
	// v0.24.2: download a self-contained tar.gz bundle
	// (setup.sh + README.md + commands.txt with the
	// preauth key + CIDR.txt) for the user to scp to
	// their router host and untar. Issues a fresh
	// preauth on each call (same as the "Issue preauth
	// key" button).
	mux.Handle("GET /admin/users/{id}/subnet/download", authMW(http.HandlerFunc(adminSvc.GetAdminUserSubnetDownload)))
	mux.Handle("POST /admin/users/{id}/subnet/share", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetShare)))
	mux.Handle("POST /admin/users/{id}/subnet/revoke", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetRevoke)))
	// 2026-07-24: v0.28.1 — admin override for the
	// user's preferred exit-node (operator-driven
	// exit-node assignment).
	mux.Handle("POST /admin/users/{id}/subnet/preferred-exit", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetPreferredExit)))
	// The subnet-routing landing page the per-user subnet tab links to — it lists
	// every user's router, so it belongs to this family, not to /admin/devices.
	mux.Handle("GET /admin/subnets", authMW(http.HandlerFunc(adminSvc.GetAdminSubnets)))
}
