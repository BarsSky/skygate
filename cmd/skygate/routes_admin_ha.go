// routes_admin_ha.go — B528 step 8 (2026-11-27): the /admin/ha family.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. This family moves here VERBATIM — 12 registrations: the High Availability chain
// editor page (B149) and its eleven writes (chain edit, the auto-reclaim toggle, node add /
// remove, force promote / demote, reclaim, the Phase 3.4 cluster failover, the DNS
// credential pair and the B472 read-only DNS-records view).
//
// The page and its writes arrived in two different runs of routes.go — the page next to
// the other page banners, the writes after /admin/deploy — and they belong together here:
// every write exists to serve the page, and the DNS-records action was added precisely so
// an operator does not have to leave it for a shell on the host that is about to stop.
// The parameter list is the usual three, derived by the compiler: (mux, authMW, adminSvc).
package main

import (
	"net/http"

	adminsvc "skygate/internal/feature/admin"
)

// registerAdminHARoutes registers the /admin/ha family: the chain editor page and the ten
// writes behind it.
func registerAdminHARoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, adminSvc *adminsvc.Service) {
	// v1.5.0 / B149 — /admin/ha (High Availability chain editor).
	// The page renders the cluster topology, failover policy,
	// HA nodes CRUD, DNS provider credentials, and force actions.
	// See internal/feature/admin/ha.go for the handler bodies
	// + doc comments and internal/ha/ for the underlying types.
	mux.Handle("GET /admin/ha", authMW(http.HandlerFunc(adminSvc.GetAdminHA)))

	mux.Handle("POST /admin/ha/chain/edit", authMW(http.HandlerFunc(adminSvc.PostAdminHAChainEdit)))
	mux.Handle("POST /admin/ha/auto-reclaim-toggle", authMW(http.HandlerFunc(adminSvc.PostAdminHAAutoReclaimToggle)))
	mux.Handle("POST /admin/ha/node/add", authMW(http.HandlerFunc(adminSvc.PostAdminHAAddNode)))
	mux.Handle("POST /admin/ha/node/remove", authMW(http.HandlerFunc(adminSvc.PostAdminHARemoveNode)))
	mux.Handle("POST /admin/ha/force-promote", authMW(http.HandlerFunc(adminSvc.PostAdminHAForcePromote)))
	mux.Handle("POST /admin/ha/force-demote", authMW(http.HandlerFunc(adminSvc.PostAdminHAForceDemote)))
	mux.Handle("POST /admin/ha/reclaim", authMW(http.HandlerFunc(adminSvc.PostAdminHAReclaim)))
	// v1.5.0+ / Phase 3.4 — skygate-cluster node failover
	// (operator-driven counterpart to the B204 elector's
	// automatic failover_recommend).
	mux.Handle("POST /admin/ha/cluster/failover", authMW(http.HandlerFunc(adminSvc.PostAdminHAClusterFailover)))
	mux.Handle("POST /admin/ha/dns/save", authMW(http.HandlerFunc(adminSvc.PostAdminHADNSCredsSave)))
	mux.Handle("POST /admin/ha/dns/test", authMW(http.HandlerFunc(adminSvc.PostAdminHADNSCredsTest)))
	// B472 (2026-11-14): the panel twin of the CLI's `dns-records` — read-only, so the
	// operator can see the current A record from the page instead of a shell on the host that
	// is about to be stopped.
	mux.Handle("POST /admin/ha/dns/records", authMW(http.HandlerFunc(adminSvc.PostAdminHADNSRecords)))
}
