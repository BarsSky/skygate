// routes_admin_derp.go — B528 step 4 (2026-11-27): the /admin/derp family.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. This family moves here VERBATIM — 19 registrations: the DERP status page, its
// cert-sync / metrics / dashboard controls, the runtime config form and the relay CRUD,
// including /admin/derp/relays/derpmap.json, the one endpoint served WITHOUT authMW
// because headscale fetches it from inside the docker network. The parameter list is
// exactly what the moved lines use, and the compiler derives it: (mux, authMW, adminSvc).
package main

import (
	"net/http"

	adminsvc "skygate/internal/feature/admin"
)

// registerAdminDerpRoutes registers the /admin/derp family: the status page, its
// cert-sync / metrics / dashboard controls, the config form and the relay CRUD.
func registerAdminDerpRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, adminSvc *adminsvc.Service) {
	mux.Handle("GET /admin/derp", authMW(http.HandlerFunc(adminSvc.GetAdminDERP)))
	// 2026-09-15: v1.5.6+ (B251) — bundled derper cert auto-renewal.
	// The "Cert auto-renewal" section on /admin/derp surfaces the
	// derp_cert_sync rows; the "Sync now" button POSTs here so
	// the operator doesn't have to wait for the daily cron tick.
	//
	// 2026-09-18 (B252.1): the handler now exists — the "Sync now" button
	// used to answer 501 because the UI half of B252 was never merged (the
	// stub below was deliberate). It syncs one hostname or every enabled
	// row and redirects with ?ok=/?err= (a plain form POST must never get a
	// JSON body back — the B180 raw-JSON regression).
	mux.Handle("POST /admin/derp/cert-sync/run", authMW(http.HandlerFunc(adminSvc.PostAdminDerpCertSyncRun)))
	// B315 — the metrics endpoint (derper's /debug/vars is loopback/tailnet only,
	// so the container reads the metrics through the operator's loopback bridge;
	// see internal/derpmetricsproxy). One form, three actions: save / clear /
	// test-without-saving. Registered on /admin/derp because that is the page that
	// shows the warning this control answers.
	mux.Handle("POST /admin/derp/metrics-endpoint", authMW(http.HandlerFunc(adminSvc.PostAdminDerpMetricsEndpoint)))
	// 2026-07-15: v0.11.0 — runtime-editable DERP config. It used to be described
	// together with /admin/integrations (the LANDING page, which stays in routes.go);
	// the save handler persists to global_settings, and this shape is the deprecated
	// one — /admin/derp/relays below is the per-row surface the operator asked for.
	mux.Handle("GET /admin/derp/config", authMW(http.HandlerFunc(adminSvc.GetAdminDerpConfig)))
	mux.Handle("POST /admin/derp/config", authMW(http.HandlerFunc(adminSvc.PostAdminDerpConfig)))
	// 2026-08-13: v1.3.17 — DERP relay CRUD (per-row
	// add/edit/delete/toggle/test). The /admin/derp/config
	// page above is the v0.11.0 deprecated form; the new
	// /admin/derp/relays is the per-row management surface
	// the operator asked for. AutoMigrateDerpRelays runs
	// on every GET to bridge any legacy global_settings
	// rows into the new table.
	mux.Handle("GET /admin/derp/relays", authMW(http.HandlerFunc(adminSvc.GetAdminDerpRelays)))
	mux.Handle("POST /admin/derp/relays/add", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysAdd)))
	mux.Handle("POST /admin/derp/relays/edit", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysEdit)))
	mux.Handle("POST /admin/derp/relays/delete", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysDelete)))
	mux.Handle("POST /admin/derp/relays/toggle", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysToggle)))
	mux.Handle("POST /admin/derp/relays/test", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysTest)))
	// B296 — the relay probe address ("where can THIS container reach the
	// relay"). Stored in global_settings and resolved per probe by
	// internal/derpcfg (DB > .env SKYGATE_DERP_PROBE_HOST > none), so saving it
	// applies immediately and the container never has to be recreated.
	mux.Handle("POST /admin/derp/relays/probe-host", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysProbeHost)))
	// B164 (v1.5.1) — DERP relay init on a new host.
	// The page renders the form; the POST handler
	// shells out to bash deploy/derp-init.sh on
	// the SSH target (operator-supplied), which
	// installs derper, configures systemd, and
	// returns the relay metadata. The handler
	// then inserts a derp_relays row so the new
	// relay is registered in the live policy.
	// See internal/feature/admin/derp_init.go
	// for the handler bodies + doc comments.
	mux.Handle("GET /admin/derp/relays/init", authMW(http.HandlerFunc(adminSvc.GetAdminDerpRelaysInit)))
	mux.Handle("POST /admin/derp/relays/init", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysInit)))
	// B237 (v1.5.2+) — DERP map endpoint that headscale
	// fetches via its `derp.urls` config. Returns the
	// Tailscale-shaped derpmap.json of the operator's own
	// + bundled DERP rows. No auth — the URL is documented
	// only for the headscale config (lives on the same
	// docker network, CORS allows headscale from any
	// origin). The combined response is merged with the
	// public Tailscale derpmap by headscale's
	// `derp.Map.Updater` per update_frequency (24h default).
	mux.Handle("GET /admin/derp/relays/derpmap.json", http.HandlerFunc(adminSvc.GetAdminDerpRelaysDerpmap))
	// B237 (v1.5.2+) — "Apply derpmap URL to headscale"
	// one-click button. Rewrites headscale's config.yaml
	// `derp.urls` block to include the skygate derpmap.json
	// URL, then `docker restart headscale` (skygate has
	// /var/run/docker.sock mounted per docker-compose.yml).
	// Admin-only + CSRF-protected.
	mux.Handle("POST /admin/derp/relays/apply-headscale", authMW(http.HandlerFunc(adminSvc.PostAdminDerpRelaysApplyHeadscale)))

	mux.Handle("GET /admin/derp/refresh", authMW(http.HandlerFunc(adminSvc.GetAdminDERPRefresh)))
	// B189 (v1.5.2) — DERP Health Dashboard.
	mux.Handle("GET /admin/derp/dashboard", authMW(http.HandlerFunc(adminSvc.GetAdminDerpDashboard)))
	mux.Handle("POST /admin/derp/dashboard/refresh", authMW(http.HandlerFunc(adminSvc.PostAdminDerpDashboardRefresh)))
}
