// The HTTP route table of the skygate binary.
//
// This was ~960 lines in the middle of main() — the largest single reason
// cmd/skygate/main.go could not be navigated (refactor Phase D, 2026-10-02).
// It is a PURE MOVE: every registration, comment and closure is byte-identical
// to what main() used to contain, and the function takes exactly the values the
// block reads.
//
// The eleven inputs were derived mechanically, not by reading: `var _ int = x`
// probes made the type checker name the type of every candidate (a middleware
// is `func(http.Handler) http.Handler`, `err` is `error`), and nine more
// candidates turned out not to be locals at all — `elector` is the imported
// PACKAGE, the rest were words in the comments. The block writes to none of
// them, so passing them by value preserves the original semantics exactly.
//
// Why it can be called with `err` still in scope and stay honest: the block
// only READS it (the `err :=` declarations inside are scoped to their own
// `if`/closure).

package main

import (
	"net/http"
	"skygate/internal/dbmigrate"
	"skygate/internal/deployrun"
	adminsvc "skygate/internal/feature/admin"
	authsvc "skygate/internal/feature/auth"
	clusterapi "skygate/internal/feature/cluster"
	exitrules "skygate/internal/feature/exit_rules"
	mysvc "skygate/internal/feature/my"
	"skygate/internal/handlers"
)

func registerRoutes(
	mux *http.ServeMux,
	app *handlers.App,
	adminSvc *adminsvc.Service,
	authSvc *authsvc.Service,
	mySvc *mysvc.Service,
	exitRulesSvc *exitrules.Service,
	clusterAPI *clusterapi.Service,
	deployrunSvc *deployrun.Service,
	authMW func(http.Handler) http.Handler,
	apiMW func(http.Handler) http.Handler,
	err error,
) {
	// 2026-07-29: refactor-v0.30 Phase B step 6d —
	// /dashboard is a user-facing page, lives in feature/my
	// next to the other /my/* routes. Registered here
	// (after mySvc construction) so the closure can reference
	// the mySvc variable.
	mux.Handle("GET /dashboard", authMW(http.HandlerFunc(mySvc.GetDashboard)))

	// B395 - the setup wizard's ACL acknowledgement: it snapshots the live policy before
	// the operator accepts that skygate will replace it (acknowledging is not applying).
	mux.Handle("POST /admin/setup/acl-ack", authMW(http.HandlerFunc(adminSvc.PostAdminSetupACLAck))) // refactor-v0.30 Phase B step 6e (2026-07-29):
	// /settings/theme moved to feature/my/settings.go
	// (mySvc.PostSettingsTheme). Same handler for GET
	// and POST (the picker is a GET form; the form
	// submission is a POST). Registered without authMW
	// so the unauth theme-preview path can still
	// redirect to /login?theme=...
	mux.HandleFunc("GET /settings/theme", mySvc.PostSettingsTheme)
	mux.HandleFunc("POST /settings/theme", mySvc.PostSettingsTheme)
	// 2026-07-29: refactor-v0.30 Phase B step 5a —
	// /my/exit-nodes, /my/preauth, /my/keys live in
	// feature/my now.
	mux.Handle("GET /my/exit-nodes", authMW(http.HandlerFunc(mySvc.GetExitNodes)))
	// 2026-07-24: v0.28.1 — per-user preferred exit-node.
	// Visible to all authenticated users (self-service).
	mux.Handle("POST /my/exit-nodes/preferred", authMW(http.HandlerFunc(mySvc.PostMyExitNodePreferred)))
	mux.Handle("POST /my/preauth", authMW(http.HandlerFunc(mySvc.PostMyPreauth)))
	mux.Handle("GET /my/keys", authMW(http.HandlerFunc(mySvc.GetMyKeys)))
	mux.Handle("POST /my/keys/{id}/expire", authMW(http.HandlerFunc(mySvc.PostMyKeyExpire)))
	// B160 (v1.5.0): per-device manual expiry
	// renewal. The preauth key is one-time (B155
	// reissue, B159 cleanup) so renewing it doesn't
	// help; the device's NODE EXPIRY is what keeps
	// the device authenticated. The auto-renewer
	// (internal/expirewatch) does this every 5min
	// for nodes within 7d, but the manual button is
	// useful for "renew now" + explicit visibility
	// (the audit log records every renewal). The
	// handler scope-checks the node to the current
	// user (cross-user renewals return 404).
	mux.Handle("POST /my/devices/{id}/renew", authMW(http.HandlerFunc(mySvc.PostMyDeviceRenew)))
	// B162 (v1.5.1): per-row device delete. The
	// handler calls headscale DeleteNode (which
	// InvalidatesCache) + cleans up node_owner_map
	// + device_exit_node_prefs + writes the
	// device_deleted audit row. Cross-user deletes
	// return 404; deletes for a node the snapshot
	// still references but headscale has already
	// purged return 410 Gone (mirrors the B160.1
	// pattern).
	mux.Handle("POST /my/devices/{id}/delete", authMW(http.HandlerFunc(mySvc.PostMyDeviceDelete)))
	// B-mod-reregister (2026-09-13): per-row re-register for nodes
	// in the synthetic "tagged-devices" sentinel user (id=2147455555).
	// Pre-B-mod-reregister these ghost nodes had no recovery path —
	// headscale 0.29.1 has no `nodes move` CLI, and /my/preauth
	// alone wouldn't help (the existing node is in tagged-devices,
	// not the user's namespace, so Backfill never attributes it).
	// The handler deletes the ghost + issues a fresh reusable 24h
	// preauth key bound to the current user + renders the result
	// page so the user reconnects with `tailscale up --authkey=<key>`
	// under the correct user. Backfill's Strategy A picks up the new
	// node on the next /my/devices load.
	mux.Handle("POST /my/devices/{id}/reregister", authMW(http.HandlerFunc(mySvc.PostMyDeviceReregister)))
	// B155 (v1.5.0): per-row preauth key reissue.
	// Mirrors B153's /my/token/{id}/renew pattern:
	// reissue button on /my/keys (POST, no JS).
	// The handler expires the old key + issues a
	// new one with the same TTL + renders the
	// preauth_result page so the user sees the
	// new raw key.
	mux.Handle("POST /my/keys/{id}/reissue", authMW(http.HandlerFunc(mySvc.PostMyKeyReissue)))
	// B159 (v1.5.0): bulk-cleanup endpoint.
	// POST /my/keys/cleanup (no id segment) deletes
	// every (used=0, expires_at>0, expires_at<=now)
	// preauth_keys row for the current user. Used
	// keys are NEVER deleted (audit history). The
	// handler redirects back to /my/keys?cleaned=N
	// with the count of removed rows.
	mux.Handle("POST /my/keys/cleanup", authMW(http.HandlerFunc(mySvc.PostMyKeysCleanup)))
	// B157 (v1.5.0): in-web notification inbox.
	// The bell icon in the layout sidebar calls
	// these POST endpoints. The user_id scoping
	// is enforced inside the handlers (MarkRead
	// / MarkAllRead both filter on user_id) so
	// a malicious id-probe returns 404.
	mux.Handle("POST /my/notifications/{id}/read", authMW(http.HandlerFunc(mySvc.PostMyNotificationRead)))
	mux.Handle("POST /my/notifications/read-all", authMW(http.HandlerFunc(mySvc.PostMyNotificationsReadAll)))
	// B157.1 (v1.5.0): full-page /my/notifications
	// view. The bell dropdown shows the unread
	// slice; this page shows EVERYTHING
	// (unread + read) with filter pills
	// (All / Unread) + pagination. Same
	// user_id scoping as the POST endpoints.
	mux.Handle("GET /my/notifications", authMW(http.HandlerFunc(mySvc.GetMyNotifications)))
	// 2026-07-29: refactor-v0.30 Phase B step 5b —
	// /my/devices moved to feature/my.
	mux.Handle("GET /my/devices", authMW(http.HandlerFunc(mySvc.GetMyDevices)))
	// 2026-07-29: refactor-v0.30 Phase B step 5c —
	// /my/meshes + 3 POST endpoints (create / join /
	// leave) moved to feature/my.
	mux.Handle("GET /my/meshes", authMW(http.HandlerFunc(mySvc.GetMyMeshes)))
	mux.Handle("POST /my/meshes/create", authMW(http.HandlerFunc(mySvc.PostMyMeshesCreate)))
	mux.Handle("POST /my/meshes/join", authMW(http.HandlerFunc(mySvc.PostMyMeshesJoin)))
	mux.Handle("POST /my/meshes/leave", authMW(http.HandlerFunc(mySvc.PostMyMeshesLeave)))
	// 2026-07-29: refactor-v0.30 Phase B step 5d —
	// per-device preferred exit-node (self-service
	// + admin override) moved to feature/my.
	mux.Handle("POST /my/devices/preferred-exit", authMW(http.HandlerFunc(mySvc.PostMyDevicePreferredExit)))
	mux.Handle("POST /admin/devices/preferred-exit", authMW(http.HandlerFunc(mySvc.PostAdminDevicePreferredExit)))
	// 2026-07-29: refactor-v0.30 Phase B step 5d —
	// /my/account/audit (CSV/JSON export) moved to
	// feature/my.
	mux.Handle("GET /my/account/audit", authMW(http.HandlerFunc(mySvc.GetMyAccountAuditExport)))

	// B528 step 3: the /admin/users family (users page + per-row writes, the per-user
	// control plane, the subnet-routing landing page) lives in routes_admin_users.go.
	registerAdminUsersRoutes(mux, authMW, adminSvc)
	// 2026-07-25: v0.28.4 — admin override for a
	// specific DEVICE's preferred exit-node. The
	// admin can pin any device to any exit-node
	// (e.g. set workstation-3 → relay-3 for the operator).
	// The form posts user_id + device_hostname +
	// tag; the handler stores the pref in
	// device_exit_node_prefs.
	// 2026-07-29: refactor-v0.30 Phase B step 5d — moved
	// to feature/my (route registered after mySvc
	// construction, see below).
	// mux.Handle("POST /admin/devices/preferred-exit", ...) — see below
	mux.Handle("GET /admin/devices", authMW(http.HandlerFunc(adminSvc.GetAdminDevices)))
	mux.Handle("POST /admin/nodes/{id}/tag", authMW(http.HandlerFunc(adminSvc.PostAdminNodeTag)))
	mux.Handle("POST /admin/nodes/{id}/untag", authMW(http.HandlerFunc(adminSvc.PostAdminNodeUntag)))
	// 2026-07-29: v0.31.x — per-device OS + device_type
	// manual override. The form (rendered in
	// admin/devices.html) POSTs {node_id, os, device_type}
	// here. "unknown" re-enables auto-detect on the next
	// /my/devices load.
	mux.Handle("POST /admin/devices/{id}/meta", authMW(http.HandlerFunc(adminSvc.PostAdminDeviceMeta)))
	// 2026-07-15: v0.14.0 — "Sync from headscale" button.
	// Re-populates node_owner_map from headscale's authoritative
	// view. The /exit_nodes bot command reads from node_owner_map;
	// if the operator tagged a relay directly in headscale (not
	// via skygate's PostAdminNodeTag), the bot reports "no nodes"
	// until this button is clicked. /sync_nodes bot command hits
	// the same DB helper.
	mux.Handle("POST /admin/devices/sync-from-headscale", authMW(http.HandlerFunc(adminSvc.PostAdminDevicesSyncFromHeadscale)))
	// B-mod-first-run-adoption T4-T5: bulk-claim handler. The
	// operator's escape hatch for "I imported headscale users
	// as portal users, now I need to attach their existing
	// nodes" — every node_owner_map row matching that headscale
	// user gets its tagged_by_user_id set to the portal user.
	// Closes the operator-side gap where nodes joined via
	// `headscale preauthkeys create` (not via /my/preauth)
	// don't get auto-attributed by nodeownership.Backfill
	// (Strategies A/C/D/E all need a preauth key or existing
	// tag to match).
	mux.Handle("POST /admin/devices/claim-all-for-user", authMW(http.HandlerFunc(adminSvc.PostAdminDevicesClaimAllForUser)))
	// 2026-08-09: v0.33.1.20 — "Force resync all tags" admin
	// action. Iterates every portal user and runs the
	// per-user backfill (the same helper /my/devices runs
	// on every page load), so the operator can apply the
	// per-device dev-tags to users who haven't loaded
	// /my/devices since their device joined the tailnet.
	// Also handles the rename detection (existing.hostname
	// != n.Hostname) that v0.33.1.20 added to
	// nodeownership.Backfill.
	mux.Handle("POST /admin/devices/force-backfill-tags", authMW(http.HandlerFunc(adminSvc.PostAdminDevicesForceBackfillTags)))
	// 2026-08-09: v0.33.1.20 — reassign a node to a
	// different portal user. Resolves orphan rows like the
	// v0.33.1.19 svyatoslava conflict by Upsert + UntagNode
	// (old dev-tag) + AddTag (new dev-tag). The ACL re-apply
	// is a separate manual step (the handler's redirect
	// message tells the operator).
	mux.Handle("POST /admin/devices/transfer", authMW(http.HandlerFunc(adminSvc.PostAdminDeviceTransfer)))
	// B257 (v1.5.8+, 2026-09-16): per-device adoption for the
	// "pre-existing headscale, skygate installed later" case.
	// Different from Transfer: the node has NO node_owner_map
	// row yet, so the handler INSERTs instead of UPDATEs and
	// doesn't try to UntagNode (no prior dev-tag to drop).
	// Backs the "Devices awaiting adoption" card on
	// /admin/devices (see internal/feature/admin/adopt_devices.go
	// for the scanner + handler).
	mux.Handle("POST /admin/devices/adopt", authMW(http.HandlerFunc(adminSvc.PostAdminDeviceAdopt)))
	// B169 (v1.5.2) — admin-side device deletion. B162
	// (v1.5.1) is the per-user delete on /my/devices;
	// this one is the admin-scoped delete on /admin/devices
	// for cleaning up orphan / duplicate / stuck devices.
	// Admin-only (handler does the c.IsAdmin check).
	mux.Handle("POST /admin/devices/{id}/delete", authMW(http.HandlerFunc(adminSvc.PostAdminDeviceDelete)))
	mux.Handle("GET /admin/audit", authMW(http.HandlerFunc(adminSvc.GetAdminAudit)))
	// 2026-07-16: v0.13.0 — ACL import/export. GET shows
	// the current policy in a downloadable file; POST
	// /admin/acls/import is the dry-run; POST
	// /admin/acls/import/apply actually pushes to every
	// plane. /admin/acls itself is unchanged (still the
	// read-only view).
	mux.Handle("GET /admin/acls", authMW(http.HandlerFunc(adminSvc.GetAdminACLs)))
	mux.Handle("GET /admin/acls/export", authMW(http.HandlerFunc(adminSvc.GetAdminACLsExport)))
	mux.Handle("GET /admin/acls/import", authMW(http.HandlerFunc(adminSvc.GetAdminACLsImport)))
	mux.Handle("POST /admin/acls/import", authMW(http.HandlerFunc(adminSvc.PostAdminACLsImport)))
	mux.Handle("POST /admin/acls/import/apply", authMW(http.HandlerFunc(adminSvc.PostAdminACLsImportApply)))
	// /admin/integrations is the landing page for every pluggable component; its
	// per-component forms live in their own registries (the /admin/derp* ones in
	// routes_admin_derp.go, /admin/headplane below).
	mux.Handle("GET /admin/integrations", authMW(http.HandlerFunc(adminSvc.GetAdminIntegrations)))
	// B528 step 4 (v1.5.273): the /admin/derp family lives in
	// routes_admin_derp.go — see that file for why.
	registerAdminDerpRoutes(mux, authMW, adminSvc)

	// v1.5.0 / B149 — /admin/ha (High Availability chain editor).
	// The page renders the cluster topology, failover policy,
	// HA nodes CRUD, DNS provider credentials, and force actions.
	// See internal/feature/admin/ha.go for the handler bodies
	// + doc comments and internal/ha/ for the underlying types.
	mux.Handle("GET /admin/ha", authMW(http.HandlerFunc(adminSvc.GetAdminHA)))

	// v1.5.0+ / B195 — /admin/database (DB management, Phase 1.1 read-only).
	// The page renders the live DSN (from env), the desired DSN
	// (from cluster_database), and a quick reachability probe.
	// See internal/feature/admin/database.go for the handler
	// + doc comments and docs/ha.md for
	// the full design (D3, D8).
	mux.Handle("GET /admin/database", authMW(http.HandlerFunc(adminSvc.GetAdminDatabase)))
	// 2026-09-28 — the cross-BACKEND conversion (SQLite ↔ PostgreSQL).
	// Distinct from the "migrate to a new host" workflow below, which
	// stays inside PostgreSQL. The handler opens the target through
	// db.OpenIsolated so a second connection cannot change the dialect
	// the running process branches on, and it renders the per-table
	// report on the same page instead of redirecting.
	mux.Handle("POST /admin/database/convert", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseConvert)))
	// v1.5.0+ / B197 — Phase 1.2: Test Connection + Edit DSN.
	// Test is non-persistent (just probes the DSN and
	// re-renders with the latency). Edit writes
	// cluster_database + audit_log. Both are no-ops on the
	// live skygate process until the Phase 3.1 watchdog
	// (skygate-watchdog) lands — until then the operator
	// must restart the container to apply.
	mux.Handle("POST /admin/database/test", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseTest)))
	mux.Handle("POST /admin/database/edit", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseEdit)))
	// B373 — "apply the defaults" for cluster_database.dsn_template: composes the
	// template from the DSN THIS process runs on (the database a standby must
	// mirror), with %s in the HOST position and no password. Reached from the hint
	// cards on /admin/database and /admin/cluster (the latter passes ?next=).
	mux.Handle("POST /admin/database/apply-default", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseApplyDefault)))
	// v1.5.0+ / B219 — Phase 3.3 PG failover (Patroni plumbing).
	// Triggers a Patroni /switchover on the configured
	// PatroniURL (default http://localhost:8008). The
	// candidate field names the target PG node to
	// promote; the leader field is optional (Patroni
	// picks the current leader from its own state if
	// empty). After Patroni completes the switchover,
	// the watchdog (B210) detects the new DSN from
	// etcd and hot-reloads the pgxpool — skygate keeps
	// running on the new primary without restart.
	mux.Handle("POST /admin/database/failover", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseFailover)))
	// v1.5.0+ / B220 — Phase 3.7 PG failover rollback
	// (operator-driven). Re-uses the B219 Patroni
	// /switchover plumbing but with the OLD primary
	// as the candidate (read from db.last_failover
	// global_setting, written by the B219 handler
	// after a successful failover). The fully
	// automatic "system detects the new primary is
	// unhealthy + triggers the rollback without
	// operator intervention" flow is deferred to a
	// follow-up B-block (it needs a stable
	// "is_healthy_for_N_seconds" check + a
	// "no flap" guard that this B220 doesn't ship).
	mux.Handle("POST /admin/database/failover/rollback", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseFailoverRollback)))

	// v1.5.0+ / B198 — Phase 1.4 DB migration workflow.
	// 6-step state machine (precheck, dump, restore, verify,
	// flip, cleanup) with SSE for live progress. See
	// internal/dbmigrate/ for the framework + steps.
	// Phase 1.4 limitation: dump + restore + cleanup are
	// stubbed; they need a second PG host (resource upgrade
	// on agent) and SSH to the source (svi) to actually run.
	// The flip step is real (it updates cluster_database +
	// the local .env so a skygate restart picks it up).
	migrateSvc := dbmigrate.NewService(app.DB.Current())
	_ = migrateSvc // Phase 1.4 framework is wired; routes below
	//                  delegate to adminSvc for rendering (so the
	//                  page uses the admin layout + nav).

	// v1.5.0+ / B202 — wire the auth-claims extractor so
	// the migrate handlers can do their admin check. Pre-B202
	// this was a pre-existing B198 bug: getClaims() always
	// returned nil → every admin-gated migrate endpoint
	// returned 403, even with a valid admin session. The
	// bridge translates auth.Claims to the local claims type
	// (dbmigrate has a tiny inline struct to avoid an
	// import cycle with the auth package).
	dbmigrate.SetCurrentClaims(func(r *http.Request) *dbmigrate.Claims {
		u := app.CurrentUser(r)
		if u == nil {
			return nil
		}
		return &dbmigrate.Claims{
			UserID:   u.UserID,
			Username: u.Username,
			IsAdmin:  u.IsAdmin,
		}
	})
	//                  The migrateSvc methods are still called by
	//                  the admin handler for data access (LoadRun,
	//                  etc.).
	mux.Handle("GET /admin/database/migrate", authMW(http.HandlerFunc(adminSvc.GetAdminDatabaseMigrate)))
	mux.Handle("POST /admin/database/migrate", authMW(http.HandlerFunc(migrateSvc.PostAdminDatabaseMigrate)))
	mux.Handle("GET /admin/database/migrate/{id}/stream", authMW(http.HandlerFunc(migrateSvc.GetAdminDatabaseMigrateStream)))
	mux.Handle("GET /admin/database/migrate/{id}", authMW(http.HandlerFunc(adminSvc.GetAdminDatabaseMigrateRun)))
	// B214 (Phase 1.4.4 / 1.4.5): cancel + rollback
	// endpoints for the in-flight / completed run
	// workflow. The cancel button is visible only when
	// the run is in-flight (per framework's IsRunLive
	// check); the rollback button is visible only when
	// the run is in a terminal non-rolled-back state
	// (success / failed / cancelled).
	mux.Handle("POST /admin/database/migrate/{id}/cancel", authMW(http.HandlerFunc(migrateSvc.PostAdminDatabaseMigrateCancel)))
	mux.Handle("POST /admin/database/migrate/{id}/rollback", authMW(http.HandlerFunc(migrateSvc.PostAdminDatabaseMigrateRollback)))

	// B482 — /admin/migrate: the HOST migration wizard (a different page from the
	// /admin/database/migrate workflow above, which migrates a database between two
	// PostgreSQL servers). This one answers nine questions about moving the whole
	// installation to another machine (SSH access, what travels, which database, the
	// new tailnet name, DNS, the old host), stores the answers as a host_migrations
	// row, generates the step list from internal/migrate.Plan into
	// host_migration_steps, and executes ONE stored step at a time through
	// internal/migrate.RunStep. Templates: admin/host_migration.html (the form +
	// history) and admin/host_migration_run.html (one run's plan). Handlers:
	// internal/feature/admin/migrate.go. Rationale: docs/host-migration.md.
	mux.Handle("GET /admin/migrate", authMW(http.HandlerFunc(adminSvc.GetAdminMigrate)))
	mux.Handle("POST /admin/migrate/save", authMW(http.HandlerFunc(adminSvc.PostAdminMigrateSave)))
	mux.Handle("GET /admin/migrate/{id}", authMW(http.HandlerFunc(adminSvc.GetAdminMigrateRun)))
	mux.Handle("POST /admin/migrate/{id}/plan", authMW(http.HandlerFunc(adminSvc.PostAdminMigratePlan)))
	mux.Handle("POST /admin/migrate/{id}/step", authMW(http.HandlerFunc(adminSvc.PostAdminMigrateStep)))
	mux.Handle("POST /admin/migrate/{id}/delete", authMW(http.HandlerFunc(adminSvc.PostAdminMigrateDelete)))

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

	// v1.5.0+ / B201 — cluster join + heartbeat API. No
	// authMW — the sgn1 token is the auth (the new node
	// doesn't have a skygate session cookie yet, and
	// the join bootstrap runs on a fresh machine). The
	// endpoints are JSON in / JSON out (not HTML pages).
	// Routes are added BEFORE authMW-gated routes so the
	// /api prefix is unambiguous to operators reading
	// the route table.
	mux.HandleFunc("POST /api/cluster/join", clusterAPI.PostAPIClusterJoin)
	mux.HandleFunc("POST /api/cluster/heartbeat", clusterAPI.PostAPIClusterHeartbeat)

	// v1.5.0 / B150 — /admin/deploy (cluster deploy +
	// failover dry-run). The page is the web mirror of
	// `skygate deploy {push,pull,sync,status}` and
	// `skygate ha {promote,demote,reclaim}` — same
	// internal/deploy primitives, different transport.
	// See internal/feature/admin/deploy.go for the
	// handler bodies + doc comments and
	// internal/deploy/ for the underlying verbs.
	mux.Handle("GET /admin/deploy", authMW(http.HandlerFunc(adminSvc.GetAdminDeploy)))
	mux.Handle("POST /admin/deploy/push", authMW(http.HandlerFunc(adminSvc.PostAdminDeployPush)))
	mux.Handle("POST /admin/deploy/test-failover", authMW(http.HandlerFunc(adminSvc.PostAdminDeployTestFailover)))
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

	// v1.5.0 / B194 — auto-deploy framework pages.
	//
	// /admin/deploys              — list of recent runs
	// /admin/deploys/new          — new-run form (GET only)
	// /admin/deploys/{id}         — single run + live SSE UI (GET)
	// /admin/deploys/{id}/stream  — SSE event stream (GET)
	// /admin/deploys (POST)       — start a new run
	//
	// The framework handles the deploy asynchronously
	// (PostAdminDeploys returns 303 to /admin/deploys/{id}
	// immediately; the framework.Run() runs in a goroutine
	// and the SSE stream pushes step transitions to the
	// open EventSource connection).
	mux.Handle("GET /admin/deploys", authMW(http.HandlerFunc(deployrunSvc.GetAdminDeploys)))
	mux.Handle("GET /admin/deploys/new", authMW(http.HandlerFunc(deployrunSvc.GetAdminDeploysNew)))
	mux.Handle("POST /admin/deploys", authMW(http.HandlerFunc(deployrunSvc.PostAdminDeploys)))
	mux.Handle("GET /admin/deploys/", authMW(http.HandlerFunc(deployrunSvc.GetAdminDeployRun)))
	// v1.5.0 / B148 — /admin/certificates (TLS cert management:
	// show current cert, upload new PEM pair, LE DNS-01 toggle).
	// See internal/feature/admin/certificates.go for the handler
	// bodies. The upload handler re-uses the certsync package's
	// ValidateCertKeyPair so the rules (x509 + matchedAny over
	// PKCS#1/PKCS#8/SEC1) stay in one place (B147 + B148 share
	// the same validation surface).
	mux.Handle("GET /admin/certificates", authMW(http.HandlerFunc(adminSvc.GetAdminCertificates)))
	mux.Handle("POST /admin/certificates/upload", authMW(http.HandlerFunc(adminSvc.PostAdminCertificateUpload)))
	mux.Handle("POST /admin/certificates/toggle-dns01", authMW(http.HandlerFunc(adminSvc.PostAdminCertificateToggleDNS01)))

	// B161.4 (v1.5.1) — /admin/oidc operator-facing
	// surface for the OIDC config. The page renders
	// the 5 endpoint URLs + a copy-paste headscale.conf
	// snippet + the current env-var values + a
	// "Test connection" button that runs a live
	// discovery+userinfo probe. The handler is
	// admin-only; the actual OIDC config lives in
	// the 4 env vars (read at boot). See
	// docs/oidc-headscale.md for the operator runbook.
	mux.Handle("GET /admin/oidc", authMW(http.HandlerFunc(adminSvc.GetAdminOIDC)))
	// B-oidc-setup (v0.75, 2026-09-21): the form action that
	// saves the oidc_settings DB row. Pre-fix the page was
	// read-only — operators had to SSH into the host and edit
	// /home/admin/skygate/.env (or restart the docker container)
	// to change any value. The form writes the four config
	// fields + an enabled toggle; restart skygate to apply.
	mux.Handle("POST /admin/oidc", authMW(http.HandlerFunc(adminSvc.PostAdminOIDC)))
	mux.Handle("POST /admin/oidc/test", authMW(http.HandlerFunc(adminSvc.PostAdminOIDCTest)))
	// B167 (v1.5.2) — /admin/oidc/sync operator-
	// facing page. The Apply button posts to
	// /admin/oidc/sync, which calls
	// deploy/oidc-sync.sh via internal/oidc/sync.go
	// (Go wrapper) and returns a JSON result.
	// Auto-detects docker / systemd / k8s / manual
	// mode (full Option C).
	mux.Handle("GET /admin/oidc/sync", authMW(http.HandlerFunc(adminSvc.GetAdminOIDCSync)))
	mux.Handle("POST /admin/oidc/sync", authMW(http.HandlerFunc(adminSvc.PostAdminOIDCSync)))
	// B313 (v1.5.78): apply the saved configuration to headscale from the panel — a
	// privileged request the installed skygate-oidc.path unit performs, so the
	// operator no longer copy-pastes a script (their complaint about `aro`).
	mux.Handle("POST /admin/oidc/apply-headscale", authMW(http.HandlerFunc(adminSvc.PostAdminOIDCApplyHeadscale)))
	mux.Handle("GET /admin/headplane", authMW(http.HandlerFunc(adminSvc.GetAdminHeadplane)))
	mux.Handle("POST /admin/headplane", authMW(http.HandlerFunc(adminSvc.PostAdminHeadplane)))
	mux.Handle("GET /admin/backup", authMW(http.HandlerFunc(adminSvc.GetAdminBackup)))
	mux.Handle("POST /admin/backup/save", authMW(http.HandlerFunc(adminSvc.PostAdminBackupSave)))
	mux.Handle("POST /admin/backup/restore", authMW(http.HandlerFunc(adminSvc.PostAdminBackupRestore)))
	mux.Handle("GET /admin/backup/download", authMW(http.HandlerFunc(adminSvc.GetAdminBackupDownload)))
	// 2026-08-12 v1.3.8 (BL-18): stream an S3 backup
	// directly to the operator's browser. Triggered
	// by the "Download from S3" button on
	// /admin/backup when LastArchive starts with
	// "s3://". Closes the gap where the operator
	// had to `aws s3 cp` (or `mc cp`) the file
	// down and then re-upload to
	// /admin/backup/restore.
	mux.Handle("GET /admin/backup/download-s3", authMW(http.HandlerFunc(adminSvc.GetAdminBackupDownloadS3)))
	// 2026-07-14: Этап 14 v6 — destination & schedule config.
	// /admin/backup itself serves the form; the four action
	// endpoints accept POSTs from the form buttons. No CSRF
	// (admin-only; the legacy /admin/backup/save also has
	// none).
	mux.Handle("GET /admin/backup/config", authMW(http.HandlerFunc(adminSvc.GetAdminBackupConfig)))
	mux.Handle("POST /admin/backup/config", authMW(http.HandlerFunc(adminSvc.PostAdminBackupConfig)))
	mux.Handle("POST /admin/backup/test", authMW(http.HandlerFunc(adminSvc.PostAdminBackupTest)))
	mux.Handle("POST /admin/backup/run", authMW(http.HandlerFunc(adminSvc.PostAdminBackupRun)))
	// 2026-08-18 (B142, v1.4.1): "Verify now" button on
	// /admin/backup. The pre-B142 page only had "Run now"
	// for backup creation; verify had to be triggered
	// by hand-running scripts/verify_backup.sh on the
	// VM. B142 adds the manual button so the operator
	// can verify a freshly-created backup without
	// waiting for the weekly schedule.
	mux.Handle("POST /admin/backup/verify-now", authMW(http.HandlerFunc(adminSvc.PostAdminBackupVerifyNow)))
	mux.Handle("POST /admin/backup/toggle", authMW(http.HandlerFunc(adminSvc.PostAdminBackupToggle)))
	mux.Handle("GET /admin/settings", authMW(http.HandlerFunc(adminSvc.GetAdminSettings)))
	mux.Handle("GET /admin/telegram", authMW(http.HandlerFunc(adminSvc.AdminTelegram)))
	mux.Handle("POST /admin/telegram", authMW(http.HandlerFunc(adminSvc.AdminTelegramPost)))
	// B255 (v1.5.8+, 2026-09-16): background polling on
	// /admin/telegram — the probe (api.telegram.org) + the
	// container tailscaled state used to block the page
	// render on first load (up to ~5s on cold-cache). The
	// page now renders immediately with a CSS spinner slot;
	// the JS in admin/telegram.html fetches these two GETs
	// on DOM-ready and replaces the slots in place.
	mux.Handle("GET /admin/telegram/probe-bg", authMW(http.HandlerFunc(adminSvc.AdminTelegramProbeBg)))
	mux.Handle("GET /admin/telegram/container-bg", authMW(http.HandlerFunc(adminSvc.AdminTelegramContainerBg)))
	// B253 (v1.5.6+, 2026-09-15): "Probe now" button on /admin/telegram
	// bypasses the cache + the stale-while-revalidate background
	// refresh. Operator hits this after fixing the network path
	// (re-enabled tailscaled in skygate container, rotated the
	// bot token, etc.) to see an immediate result instead of
	// waiting for the next async-refresh tick (5 min on failure,
	// 30s on success).
	//
	// 2026-09-18 (B253): the handler now exists — the "Probe now" button
	// used to answer 501 because the B253 UI half was never merged. It
	// bypasses the cache (and so the stale-while-revalidate path) and
	// redirects back with ?ok=/?err=; the refreshed probe badge is the
	// operator-visible result.
	mux.Handle("POST /admin/telegram/probe/now", authMW(http.HandlerFunc(adminSvc.PostAdminTelegramProbeNow)))
	// v1.5.2+ / B-mod-admin (2026-09-10): /admin/modules
	// list + /admin/modules/{name} detail + POST handlers
	// for install/start/stop/enable/disable/sub/{name}.
	// CSRF-protected; cookie skygate_modules_csrf is minted
	// on every GET (see AdminModulesList / AdminModuleDetail).
	mux.Handle("GET /admin/modules", authMW(http.HandlerFunc(adminSvc.AdminModulesList)))
	mux.Handle("GET /admin/modules/{name}", authMW(http.HandlerFunc(adminSvc.AdminModuleDetail)))
	// B-fix-modules-route (2026-09-11): the sub-feature toggle URL is
	// /admin/modules/{name}/sub/{subname} — TWO segments after {name}.
	// Go 1.22 mux {action} only matches a single segment, so the
	// pre-fix route silently fell through to the `/` catch-all handler
	// which 302'd to /dashboard. Use {action...} wildcard to capture
	// the multi-segment sub-feature path.
	mux.Handle("POST /admin/modules/{name}/{action...}", authMW(http.HandlerFunc(adminSvc.AdminModulePost)))
	mux.Handle("GET /admin/modules/csrf", authMW(http.HandlerFunc(adminSvc.AdminModulesListCSRF)))
	// v0.33.1.9: Tailscale web-UI management (status + auth key
	// paste + start/stop). Pairs with the /admin/telegram
	// egress-relay card (v0.33.1.8) — the user pastes a
	// preauth key here, starts Tailscale, then picks the
	// egress relay on /admin/telegram to make the bot work.
	mux.Handle("GET /admin/tailscale", authMW(http.HandlerFunc(adminSvc.GetAdminTailscale)))
	mux.Handle("POST /admin/tailscale", authMW(http.HandlerFunc(adminSvc.PostAdminTailscale)))
	// B323 (2026-09-25) — the SERVICE CONTROL page. The operator asked for the
	// "restart skygate to apply the new env" control to be a first-class, findable
	// block instead of a button hidden on /admin/tailscale: «явно нужно вынести
	// подобного рода функционал в настройки и в отдельный блок управления
	// состоянием skygate чтобы не искать где он есть сейчас». It detects the
	// install kind (docker/systemd/OpenRC/kubernetes/binary), shows the env file
	// the unit/compose actually reads, and offers restart + (docker only)
	// recreate. Same authMW admin gate as every neighbouring /admin/* route.
	mux.Handle("GET /admin/service", authMW(http.HandlerFunc(adminSvc.GetAdminService)))
	mux.Handle("POST /admin/service", authMW(http.HandlerFunc(adminSvc.PostAdminService)))
	// B320 — delete the OFFLINE node that squats the canonical tailnet name
	// `skygate-host` (a dead registration from an earlier machine key) and rename the
	// running client back to it. The guards live in the handler: offline, canonical
	// name, infra family, never the live node.
	mux.Handle("POST /admin/tailscale/reclaim-name", authMW(http.HandlerFunc(adminSvc.PostAdminTailscaleReclaimName)))
	mux.Handle("GET /my/tokens", authMW(http.HandlerFunc(authSvc.GetMyTokens)))
	mux.Handle("POST /my/token", authMW(http.HandlerFunc(authSvc.PostMyToken)))
	mux.Handle("POST /my/token/{id}/revoke", authMW(http.HandlerFunc(authSvc.PostMyTokenRevoke)))
	// B153 (v1.5.0): per-row token renewal. Default 30d
	// when the per-row button is clicked; the dedicated
	// ?renew=ID form posts a `ttl` field with a custom value.
	mux.Handle("POST /my/token/{id}/renew", authMW(http.HandlerFunc(authSvc.PostMyTokenRenew)))
	mux.Handle("GET /my/account", authMW(http.HandlerFunc(authSvc.GetMyAccount)))
	mux.Handle("POST /my/account/password", authMW(http.HandlerFunc(authSvc.PostMyAccountPassword)))
	// B136 (v1.3.20.6): per-user display prefs (font + size +
	// selection color). DB-persisted in portal_users, so the
	// operator's display follows them across devices and
	// survives cache clears (operator request on 2026-08-18).
	mux.Handle("POST /my/account/display", authMW(http.HandlerFunc(mySvc.PostMyAccountDisplay)))
	// v0.25.1: per-user audit log export (CSV or JSON).
	// Gated by the user's session cookie — they get only
	// their own audit trail. Useful for compliance
	// reporting without giving the user admin access to
	// /admin/audit.
	// 2026-07-29: refactor-v0.30 Phase B step 5d — moved
	// to feature/my (route registered after mySvc
	// construction, see below).
	// mux.Handle("GET /my/account/audit", ...) — see below
	// 2026-07-13: Этап 12 — self-service Telegram binding. Any
	// portal user (not just admin) can generate a one-time login
	// key here and paste it into the bot. The /my/telegram page
	// also lets a user unbind their own chat (mirror of the
	// bot's /unbind_self) and revoke unused keys.
	//
	// 2026-07-29: refactor-v0.30 Phase B step 6f —
	// /my/telegram moved to feature/my/telegram.go
	// (mySvc.GetMyTelegram + 4 POST siblings).
	mux.Handle("GET /my/telegram", authMW(http.HandlerFunc(mySvc.GetMyTelegram)))
	mux.Handle("POST /my/telegram/generate", authMW(http.HandlerFunc(mySvc.PostMyTelegramGenerate)))
	mux.Handle("POST /my/telegram/unbind", authMW(http.HandlerFunc(mySvc.PostMyTelegramUnbind)))
	mux.Handle("POST /my/telegram/revoke", authMW(http.HandlerFunc(mySvc.PostMyTelegramRevoke)))
	// 2026-07-13: Этап 13 — Bind-by-QR. The QR PNG is served from
	// the same /my/telegram path tree (cookie-authenticated like
	// the rest of the page) so anonymous users can't spam the
	// generator with arbitrary tokens.
	mux.Handle("GET /my/telegram/qr", authMW(http.HandlerFunc(mySvc.GetMyTelegramQR)))
	// B528 step 5 (v1.5.274): the /my/exit-rules family lives in
	// routes_my_exit_rules.go — see that file for why it needs both middlewares.
	registerMyExitRulesRoutes(mux, authMW, apiMW, exitRulesSvc)
	// B528 step 6 (v1.5.275): the /admin/exit-rules family lives in
	// routes_admin_exit_rules.go — see that file for the one user-facing route it keeps.
	registerAdminExitRulesRoutes(mux, authMW, exitRulesSvc)
	mux.Handle("POST /admin/settings", authMW(http.HandlerFunc(adminSvc.PostAdminSettings)))
	mux.Handle("GET /admin/exit-nodes", authMW(http.HandlerFunc(adminSvc.AdminExitNodes)))
	// B528 step 1 (v1.5.265): the /admin/headscale* group lives in
	// routes_headscale.go — see that file for why.
	registerHeadscaleRoutes(mux, authMW, adminSvc)
	// 2026-08-10: v0.33.1.40 B92 — Integration status board.
	// Renders the cached status of headscale/headplane/tailscale
	// (refreshed every 30s by the Availability Checker in
	// internal/feature/healthz/availability.go). Admin-only.
	mux.Handle("GET /admin/services", authMW(http.HandlerFunc(adminSvc.AdminServices)))
	// 2026-08-04: v0.33.0 — Admin Test Page. Run the
	// TestRegistry (network/db/headscale checks) and see
	// the result inline. History in system_tests_runs.
	mux.Handle("GET /admin/system_tests", authMW(http.HandlerFunc(adminSvc.GetAdminSystemTests)))
	mux.Handle("POST /admin/system_tests/run", authMW(http.HandlerFunc(adminSvc.PostAdminSystemTestsRun)))
	// B394 - the run is asynchronous and reports progress, so the page needs a
	// poller endpoint; ?test=<name> on the POST re-runs exactly one test.
	mux.Handle("GET /admin/system_tests/run.json", authMW(http.HandlerFunc(adminSvc.GetAdminSystemTestsRunJSON)))
	// B305 (v1.5.70) — the monitoring inbox: one list for every monitoring
	// signal skygate records (system tests, tag reconciliation, exit-node
	// health, database health, …) with severities, dedup, ack/resolve and the
	// Telegram push threshold. The operator asked for «поле с уведомлениями
	// куда будут приходить все сообщения разной важности с мониторинга».
	mux.Handle("GET /admin/monitor", authMW(http.HandlerFunc(adminSvc.GetAdminMonitor)))
	mux.Handle("POST /admin/monitor/{id}/ack", authMW(http.HandlerFunc(adminSvc.PostAdminMonitorAck)))
	mux.Handle("POST /admin/monitor/ack-all", authMW(http.HandlerFunc(adminSvc.PostAdminMonitorAckAll)))
	mux.Handle("POST /admin/monitor/settings", authMW(http.HandlerFunc(adminSvc.PostAdminMonitorSettings)))
	// 2026-08-06 v0.33.1.18 — DNS-autoupdater toggle (DB-backed).
	// Was previously wired to SKYGATE_AUTO_UPDATE_ENABLED (the
	// skygate self-update flag), which silently turned off
	// domain→/32 refresh for operators who disabled self-update
	// in .env. See internal/feature/admin/settings_dns_autoupdate.go.
	mux.Handle("POST /admin/system_tests/dns-autoupdate-toggle", authMW(http.HandlerFunc(adminSvc.PostAdminSystemTestsDNSAutoToggle)))
	// B308 (v1.5.73) — how often a domain rule may be re-resolved. Every domain
	// used to be re-resolved on every tick, which rewrote ±20 derived rows per
	// five minutes on the agent VM, kept the generated ACL in permanent motion and
	// made /admin/exit-nodes show the red stale-policy banner almost always.
	mux.Handle("POST /admin/system_tests/dns-interval", authMW(http.HandlerFunc(adminSvc.PostAdminSystemTestsDNSInterval)))
	// 2026-09-03: v1.5.2 (B231) — preferred-exit auto-
	// reconciler toggle. Mirrors the DNS-autoupdater
	// pattern: DB-backed with env-var default. The
	// /admin/system_tests page exposes the toggle;
	// the goroutine in RunPreferredExitReconciler
	// (handlers.go) reads it on every tick. See
	// internal/feature/admin/settings_pref_reconcile.go.
	mux.Handle("POST /admin/system_tests/preferred-reconcile-toggle", authMW(http.HandlerFunc(adminSvc.PostAdminSystemTestsPrefReconcileToggle)))
	// 2026-07-27: v0.29.0 — self-update page. Shows
	// current vs latest GitHub release + copy-pasteable
	// manual steps for the detected install kind. The
	// "Check now" button forces an immediate GitHub
	// poll (bypasses the 6h success / 15m failure cache).
	registerAdminUpdateRoutes(mux, authMW, adminSvc)
	// 2026-07-20: v0.21.0 — user-to-user invite
	// overview. Lists every invite_codes row
	// (grantor / grantee / status / expiry),
	// supports a "Revoke" action for active
	// rows. Admin-only — the bot /invites command
	// is the per-user "show me my own invites"
	// view.
	mux.Handle("GET /admin/invites", authMW(http.HandlerFunc(adminSvc.GetAdminInvites)))
	mux.Handle("POST /admin/invites/revoke", authMW(http.HandlerFunc(adminSvc.PostAdminInvitesRevoke)))
	// 2026-07-20: v0.22.0 — /admin/meshes. Read-only
	// admin overview of every mesh (active +
	// dissolved). The user-to-user mesh workflow
	// (create / join / leave) is bot-driven; the
	// admin page is for oversight, same UX choice
	// as /admin/invites.
	mux.Handle("GET /admin/meshes", authMW(http.HandlerFunc(adminSvc.GetAdminMeshes)))
	registerAdminExitNodesRoutes(mux, authMW, adminSvc)
}
