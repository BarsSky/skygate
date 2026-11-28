// boot_headscale.go — B526 step 10 (2026-11-27): the headscale-client phase leaves main().
//
// 137 lines that answer "is the headscale client usable, is the policy readable, are the
// infra/telegram/ownership prerequisites in place" — and end by building the handlers.App
// every later phase needs. The body is a VERBATIM move (the only edit is the final
// `app := handlers.New(...)` becoming `app = handlers.New(...)`, because app is now a
// named result). It has no defer, so the function-scope rule of B526 C3 is satisfied by
// construction rather than by a lifecycle registration.
package main

import (
	"context"
	"log"
	"os"

	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/derpcfg"
	adminsvc "skygate/internal/feature/admin"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
)

// wireHeadscaleClient builds the headscale client, runs the boot-time audits and
// prerequisites, and returns the client together with the handlers.App built from it.
func wireHeadscaleClient(cfg *config.Config, d *db.ResettableDB) (*headscale.Client, *handlers.App) {

	hs := headscale.New(cfg.HeadscaleURL, cfg.HeadscaleKey)
	if err := ensureHeadscaleUser(d.DB, hs, cfg.BootstrapAdminUser); err != nil {
		log.Printf("warn: ensure headscale user: %v", err)
	}
	// B272.2: with `policy.mode: file` everything tag-related depends on the
	// policy file being readable by headscale and writable by skygate. When it
	// is not, headscale's policy API answers 500 and NO tag can ever be
	// permitted — a state that used to surface only as a nested 500 body in the
	// autoupdater log. Report it at boot with the exact fix commands; the same
	// audit is rendered on /admin/derp at request time.
	if audit := headscale.AuditHeadscalePolicy(); !audit.OK() {
		log.Printf("⚠️  headscale policy: %s (%s)", audit.Detail, audit.Status)
		for _, fix := range audit.Fixes {
			log.Printf("    fix: %s", fix)
		}
	}

	// B237.18 (closes TD-10) — headscale_user_id
	// reconciliation cron. 1h interval (configurable
	// via SKYGATE_RECONCILE_HEADSCALE_USERS_INTERVAL;
	// 0 = use the package default). Detects 4
	// outcomes per portal_users row: ok (link still
	// valid), linked (was NULL/0, found by username
	// in headscale → updated), relinked (had a stale
	// ID, found by username → updated), orphan
	// (had an ID, neither the ID nor the username
	// exists in headscale → audit row, no auto-delete).
	// Disabled by setting
	// SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED=false
	// (air-gapped installs where headscale is
	// unreachable).
	//
	// Why AFTER ensureHeadscaleUser: the reconciliation
	// reads from headscale, so the headscale client
	// must exist. The local `hs` variable holds the
	// shared client; we pass it directly to the cron.
	//
	// Why we don't just use app.HSGlobalFn(): the cron
	// runs in a background goroutine that survives
	// across the (rare) v0.12.0+ per-user headscale
	// swap. app.HSGlobalFn() is the right hook for
	// request-scoped code (it always returns the
	// CURRENT headscale), but the cron captures a
	// reference at startup. For the 1h interval this
	// is fine — if the operator swaps headscale, the
	// next reconcile cycle uses the new config via a
	// skygate restart. If we wanted true hot-reload,
	// we'd wrap the cron in a similar closure as the
	// derphealth probe (which reads from a config
	// struct per tick). That's a future B-block.
	if cfg.ReconcileHeadscaleUsers {
		if err := headscale.StartReconcileCron(
			context.Background(),
			d.DB,
			hs,
			cfg.ReconcileHeadscaleUsersInterval,
		); err != nil {
			log.Printf("reconcile cron: %v (continuing without background reconciliation)", err)
		}
	} else {
		log.Printf("reconcile cron: disabled (SKYGATE_RECONCILE_HEADSCALE_USERS_ENABLED=false)")
	}

	// 2026-08-10: v0.33.1.41 — Issue 4 technical user.
	// Provision the 'infra' headscale user and link to the
	// portal_users row that V054 created. Idempotent (V054
	// is a no-op on re-runs; this function is a no-op when
	// the link is already set).
	if err := ensureInfraUser(d.DB, hs); err != nil {
		log.Printf("warn: ensure infra user: %v", err)
	}

	// 2026-09-15 (B-bug-fix): verify that infrastructure nodes
	// (skygate-host-* + tag:exit-node + tag:dev-infra-*) are
	// actually owned by the 'infra' headscale user. Live case
	// 2026-09-15 (agent VM 192.168.13.69): skygate-host-1-1 was
	// on `tagged-devices` (id=11) while the `infra` user
	// (id=85) had zero nodes — the ACL grant
	// `infra → autogroup:internet, tag:exit-*` was dead. This
	// check surfaces the drift at boot (stderr) so the operator
	// sees it without needing to run a manual audit. The check
	// is READ-ONLY; the actual migration (delete + re-register
	// with --user infra) requires operator action via
	// /admin/devices because it's destructive.
	if _, err := adminsvc.SanityCheckInfraUserOwners(hs); err != nil {
		log.Printf("warn: infra-sanity: %v (continuing — drift will show on /admin/devices)", err)
	}

	// 2026-09-19 (B265): detect the "skygate runs ON an exit node"
	// topology. Two consequences the operator must know before
	// configuring egress:
	//   1. never select a co-located exit node as this machine's own
	//      exit node (or as telegram.egress_node_id) — a broken relay
	//      then takes the management plane down with it;
	//   2. the Telegram Bot API probe on /admin/telegram originates on
	//      THIS host (the skygate container uses the host's network
	//      stack), so an egress/split-routing policy this host cannot
	//      traverse makes the probe fail even though every other
	//      device on the tailnet reaches api.telegram.org. That path
	//      has to be configured from a client, not from here.
	// Read-only; logs a WARN line per overlap.
	selfTSHostname := os.Getenv("SKYGATE_TS_HOSTNAME")
	if selfTSHostname == "" {
		selfTSHostname = "skygate-host"
	}
	adminsvc.SanityCheckExitNodeColocation(hs, selfTSHostname, nil, derpcfg.DialHost(d.DB))

	// Bootstrap Telegram credentials: copy from .env to DB once on
	// startup if no DB record exists. After that, the admin page at
	// /admin/telegram is the source of truth.
	if err := bootstrapTelegramFromEnv(d.DB); err != nil {
		log.Printf("warn: bootstrap telegram: %v", err)
	}

	// Backfill node_owner_map: any headscale node with tag:public whose
	// original owner we don't know is attributed to the bootstrap admin.
	if err := backfillNodeOwners(d.DB, hs, cfg.BootstrapAdminUser); err != nil {
		log.Printf("warn: backfill node owners: %v", err)
	}

	// B-mod-first-run-adoption T7: first-run auto-sync. When the
	// operator sets SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true AND
	// node_owner_map is empty AND at least one portal_user exists,
	// sync all headscale nodes into node_owner_map + auto-detect
	// exit-servers (T6). The operator's "deploy skygate as a
	// sidecar to an existing headscale" flow goes from manual
	// "click Sync from headscale" to fully automatic on first
	// boot. The flag is opt-in (default false) so existing
	// deployments don't change behavior on upgrade.
	if cfg.ImportExistingOnFirstRun {
		if err := runFirstRunAutoSync(context.Background(), d.DB, hs); err != nil {
			log.Printf("warn: first-run auto-sync: %v (operator can run 'Sync from headscale' manually on /admin/devices)", err)
		}
	}

	app := handlers.New(d, hs, cfg.HeadscaleKey, cfg.JWTSecret, cfg.ControlURL, cfg.SSHKeyPath, cfg.SessionHours, cfg)

	return hs, app
}
