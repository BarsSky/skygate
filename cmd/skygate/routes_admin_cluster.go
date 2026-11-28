// routes_admin_cluster.go — B528 step 7 (2026-11-27): the /admin/cluster family.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. This family moves here VERBATIM — 12 registrations: the topology page, the node
// lifecycle writes (add / remove / approve / drain / rejoin / drain-remove), the invite
// pair (generate / revoke), the B342 onboard action, the B222 rolling-upgrade orchestrator
// and the B223 discovery tick.
//
// Each block keeps the comment that explains WHY it exists — the rolling-upgrade
// orchestrator's self-upgrade guard and the onboard token are not obvious from the route
// string, and a registry without its reasons is a list of paths. The parameter list is the
// usual three, derived by the compiler: (mux, authMW, adminSvc).
package main

import (
	"net/http"

	adminsvc "skygate/internal/feature/admin"
)

// registerAdminClusterRoutes registers the /admin/cluster family: the page, the node and
// invite lifecycle, onboarding, the rolling-upgrade orchestrator and discovery.
func registerAdminClusterRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, adminSvc *adminsvc.Service) {
	// v1.5.0+ / B199 — /admin/cluster (cluster topology view,
	// Phase 2.1 read-only). The page renders the cluster +
	// cluster_node + cluster_database + cluster_invite +
	// cluster_audit state. Phase 2.2 (B200) adds the action
	// surface (add/remove nodes, generate/revoke invites).
	// See internal/feature/admin/cluster.go for the handlers
	// and docs/ha.md §2 for the plan.
	mux.Handle("GET /admin/cluster", authMW(http.HandlerFunc(adminSvc.GetAdminCluster)))
	// v1.5.0+ / B200 — Phase 2.2 action surface.
	// 4 POST handlers behind authMW:
	//   /admin/cluster/node/add      — append a new cluster_node row
	//   /admin/cluster/node/remove   — delete by hostname
	//   /admin/cluster/invite/generate — create signed sgn1 token
	//   /admin/cluster/invite/revoke   — mark invite status=revoked
	mux.Handle("POST /admin/cluster/node/add", authMW(http.HandlerFunc(adminSvc.PostAdminClusterNodeAdd)))
	mux.Handle("POST /admin/cluster/node/remove", authMW(http.HandlerFunc(adminSvc.PostAdminClusterNodeRemove)))
	// B217: Phase 2.2 — Approve / Drain / Drain+Remove.
	// Approve is the explicit-approval gate (Phase 2.2.3):
	// transitions state=pending → state=ready. Drain is
	// the "mark as draining but keep the row" action
	// (Phase 2.2.4 step 1). Drain+Remove is the safe
	// "drain + leave + cleanup" combo (Phase 2.2.4 steps
	// 1+2 in one transaction).
	mux.Handle("POST /admin/cluster/node/approve", authMW(http.HandlerFunc(adminSvc.PostAdminClusterNodeApprove)))
	mux.Handle("POST /admin/cluster/node/drain", authMW(http.HandlerFunc(adminSvc.PostAdminClusterNodeDrain)))
	// B402 — the operator's own "Mark Ready": RejoinNode used to be reachable only from
	// the rolling-upgrade orchestrator, so a row the elector had marked failed stayed
	// failed even while its heartbeats kept arriving.
	mux.Handle("POST /admin/cluster/node/rejoin", authMW(http.HandlerFunc(adminSvc.PostAdminClusterNodeRejoin)))
	mux.Handle("POST /admin/cluster/node/drain-remove", authMW(http.HandlerFunc(adminSvc.PostAdminClusterNodeDrainRemove)))
	mux.Handle("POST /admin/cluster/invite/generate", authMW(http.HandlerFunc(adminSvc.PostAdminClusterInviteGenerate)))
	mux.Handle("POST /admin/cluster/invite/revoke", authMW(http.HandlerFunc(adminSvc.PostAdminClusterInviteRevoke)))

	// B342 (RR-15 option a) — onboard a SECOND host from the panel alone: one
	// action mints the invite + the tailnet preauth key and parks them behind
	// an opaque one-time token; GET /admin/cluster?boot=<token> then renders
	// the ready-to-run block. Without this the operator assembled four steps
	// by hand in three places (docs/ha.md §2.4).
	mux.Handle("POST /admin/cluster/onboard", authMW(http.HandlerFunc(adminSvc.PostAdminClusterOnboard)))

	// v1.5.0+ / B222 (Phase 4.2) — rolling upgrade
	// orchestrator. POST /admin/cluster/upgrade with
	// target=<hostname> upgrades that one node;
	// target=all iterates every ready+failed node
	// in cluster_node (skipping the self row).
	// See internal/cluster/upgrade.go for the
	// state machine + the self-upgrade guard.
	mux.Handle("POST /admin/cluster/upgrade", authMW(http.HandlerFunc(adminSvc.PostAdminClusterUpgrade)))

	// v1.5.0+ / B223 (Phase 4.3) — Tailscale
	// auto-discovery. POST /admin/cluster/discover
	// runs the discovery tick immediately (the
	// background ticker in main.go runs the same
	// function every 5 min). See
	// internal/cluster/discovery.go for the parsing
	// + de-duplication logic.
	mux.Handle("POST /admin/cluster/discover", authMW(http.HandlerFunc(adminSvc.PostAdminClusterDiscover)))
}
