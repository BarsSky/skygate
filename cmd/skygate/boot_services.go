// boot_services.go — B526 (2026-11-27): the background services and the late wiring.
//
// main() was a 2 272-line function; the phases lifted so far are `boot_monitors.go`
// (three monitors + bootCtx) and this file (the services started after the route table
// and the wiring that runs after the HTTP server goroutine).
//
// WHY THE DEFERS COULD MOVE THIS TIME. B372 documents the paying lesson: a `defer`
// inside a helper runs when the HELPER returns, so lifting `defer wd.Stop()` verbatim
// would stop the watchdog microseconds after starting it — a silent behaviour change no
// grep would catch. The block therefore takes a *bootLifecycle: it registers each
// stopper, main() defers ONE Stop(), and the order (reverse of the start order) and the
// once-only contract are unit-tested in boot_lifecycle_test.go.
//
// The bodies are VERBATIM moves. The parameter list is exactly the names the block
// already used, so no statement was rewritten to fit (`grep` for a moved call and you
// will find the same call text as before, with the same arguments).
package main

import (
	"context"
	"log"
	"net/http"
	"skygate/internal/backup"
	"skygate/internal/certsync"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/elector"
	"skygate/internal/expirewatch"
	adminsvc "skygate/internal/feature/admin"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/nodeownership"
	"skygate/internal/release"
	"skygate/internal/sidecar"
	telegram "skygate/internal/telegram"
	"skygate/internal/watchdog"
	"time"
)

// startBackgroundServices starts the long-running background services that own a
// stopper. Every one of them used to be `defer X.Stop()` inside main().
func startBackgroundServices(
	ctx context.Context,
	cfg *config.Config,
	d *db.ResettableDB,
	app *handlers.App,
	rn *telegram.RealNotifier,
	hs *headscale.Client,
	adminSvc *adminsvc.Service,
	lc *bootLifecycle,
) {

	// 2026-07-17: v0.16.7 — per-user subnet sidecar
	// auto-approver. Hoisted before the RealNotifier block
	// so we can hand the same manager to the bot via
	// rn.SetSidecar() below.
	sidecarMgr := sidecar.New(d.DB, app.HSForUserFn, log.Default(), cfg.SidecarSyncPeriod)
	app.Sidecar = sidecarMgr
	// sidecarMgr.Run blocks on a ticker loop; launch it in a
	// goroutine so the main flow can continue to start the
	// HTTP server + Telegram notifier. v0.16.7
	// regression-prevention: the first v0.16.7 deploy had
	// this as a direct call, which blocked main() before
	// the HTTP listener could bind, so the process was
	// up but unreachable (the sidecar goroutine was the
	// only thing still running).
	// 2026-07-31: v0.32.13 — gate on cfg.SidecarSyncPeriod
	// (env SKYGATE_SIDECAR_SYNC_PERIOD, default 30s;
	// set to 0 / off to disable). Pre-fix the goroutine
	// launched unconditionally and its initial SyncOnce
	// at startup did a full headscale ListAllNodes + per-
	// node route approval, holding the WAL write lock
	// for several seconds at a critical point in startup.
	if cfg.SidecarSyncPeriod > 0 {
		go sidecarMgr.Run(ctx)
	} else {
		log.Printf("sidecar: SKYGATE_SIDECAR_SYNC_PERIOD=0, skipping startup goroutine")
	}

	// v1.5.0+ / B203 — skygate-watchdog for cluster_database
	// hot-reload. Every 5s the watchdog reads the
	// cluster_database row and, if the desired DSN differs
	// from the current pool's DSN, opens a new pool,
	// pings it, and atomically swaps it into the
	// ResettableDB. The operator edits the DSN via
	// /admin/database/edit (B197) and the change takes
	// effect within ~5s with no service interruption.
	//
	// D8 (per cluster-management.md §0.2): cluster_database
	// wins on conflict with .env. The watchdog enforces
	// this by always reading cluster_database; if the row
	// is empty (no override), the env-DSN pool stays.
	wdCfg := watchdog.DefaultConfig()
	// B225.2 (Phase 4.4 follow-up): the
	// watchdog's Notifier is wired from
	// app.Notifier. When the operator has
	// configured a Telegram bot, the watchdog
	// pushes a "PG health DEGRADED" alert on
	// 3 consecutive failed reads of the
	// cluster_database table (= 15s of
	// unreachable DB with the default 5s
	// Interval), and a "PG health recovered"
	// alert on the next successful read. When
	// no bot is configured, the
	// NoopNotifierSink drops alerts silently
	// — the watchdog's "dbmigrate-watchdog"
	// log line on the same event is the
	// durable record.
	wdCfg.Notifier = schedulerNotifierSink(app.Notifier)
	wd := watchdog.NewDBSwap(
		wdCfg,
		d, // d is *db.ResettableDB, satisfies watchdog.DBMigrator
		func(ctx context.Context) (*watchdog.ClusterDatabaseRow, error) {
			row, err := db.GetClusterDatabase(d.DB, "skygate-staging")
			if err != nil {
				return nil, err
			}
			if row == nil {
				return nil, nil
			}
			return &watchdog.ClusterDatabaseRow{
				ID:         row.ID,
				CurrentDSN: row.CurrentDSN,
				DBName:     row.DBName,
				Username:   row.Username,
				SSLMode:    row.SSLMode,
			}, nil
		},
	)
	wd.Start()
	lc.Add("dbmigrate-watchdog", wd.Stop)
	log.Printf("dbmigrate-watchdog: started (interval=%s, ping-timeout=%s)", watchdog.DefaultConfig().Interval, watchdog.DefaultConfig().PingTimeout)

	// v1.5.0+ / B204 — HA elector. Reads cluster_node
	// every 5s, transitions stale nodes to 'failed', and
	// logs auto-failover recommendations to cluster_audit
	// when a skygate primary is failed AND a skygate-standby
	// is ready. The actual promote is admin-gated (B205).
	// The elector receives `d` (the *ResettableDB) as its
	// DBSource; on every tick it calls d.Current() to get
	// the current *sql.DB. This is critical: when B203's
	// watchdog hot-reloads the pool via Reset(), the
	// elector's next tick transparently follows the swap
	// (it would otherwise keep reading from a closed
	// pool). The fixed-source adapter (NewElectorWithDB)
	// is for unit tests + one-off scripts only.
	el := elector.NewElector(elector.DefaultConfig(), d)
	el.Start()
	lc.Add("ha-elector", el.Stop)
	log.Printf("ha-elector: started (interval=%s, heartbeat=%s, cluster=%s)",
		elector.DefaultConfig().Interval,
		elector.DefaultConfig().HeartbeatInterval,
		elector.DefaultConfig().ClusterID)

	// 2026-07-21: v0.23.3 — node-expiry watcher.
	// Background goroutine that walks every non-tagged
	// node in headscale every cfg.ExpireWatchInterval
	// (default 5m) and extends any node whose expiry is
	// missing or within cfg.ExpireWatchThreshold
	// (default 7d) out to cfg.ExpireWatchRenewal
	// (default 30d). Works around a Tailscale 1.98.x
	// client behaviour where RegisterRequest.Expiry is
	// only 2-4 seconds in the future and headscale
	// 0.29.x applies that verbatim — see
	// internal/expirewatch/manager.go for the full
	// background. Tagged nodes (tag:exit-node,
	// tag:public, tag:subnet-router, tag:client) are
	// skipped because headscale's state.go explicitly
	// guards `if !node.IsTagged()` around the
	// regReq.Expiry branch.
	//
	// Set SKYGATE_EXPIREWATCH_ENABLED=false (or
	// SKYGATE_EXPIREWATCH_INTERVAL=off/0) to disable.
	// When disabled, the goroutine returns from Run
	// immediately and no list/extend calls are made.
	expireWatchMgr := expirewatch.New(d.DB, hs, log.Default(), cfg.ExpireWatchInterval)
	expireWatchMgr.Threshold = cfg.ExpireWatchThreshold
	expireWatchMgr.Renewal = cfg.ExpireWatchRenewal
	expireWatchMgr.SetAppendAudit(db.AppendAuditLog)
	app.ExpireWatch = expireWatchMgr
	// Same goroutine-launch pattern as sidecarMgr.Run:
	// direct call would block main() before the HTTP
	// listener binds. v0.16.7 caught this for sidecar;
	// same regression here.
	// 2026-07-31: v0.32.13 — gate on cfg.ExpireWatchEnabled.
	// Pre-fix only the Interval check happened inside Run();
	// setting ExpireWatchEnabled=false didn't actually
	// disable the goroutine — it still ran the initial
	// SyncOnce which lists every node in headscale and
	// potentially extends expirations, holding the WAL
	// write lock for a few seconds. On the live VM with
	// concurrent admin traffic that initial SyncOnce
	// coincided with the first /admin/exit-nodes request
	// and wedged the DB.
	if cfg.ExpireWatchEnabled {
		go expireWatchMgr.Run(ctx)
	} else {
		log.Printf("expirewatch: SKYGATE_EXPIREWATCH_ENABLED=false, skipping startup goroutine")
	}

	// 2026-07-11: Telegram bot — always arm the RealNotifier so a
	// hot-swap (admin saving a token at runtime) takes effect without
	// restart. RealNotifier.SendTelegram no-ops when Configured()==false,
	// and Run() sleeps-and-rechecks every 5s when the DB has no token.
	// No more "boot-time gate" on app.Notifier — it's always non-nil.
	//
	// The block was anonymous ({}) in v0.16.x and earlier — it
	// served no scoping purpose. v0.20.0 makes it a top-level
	// var so the headscale-update-monitor wiring (later in this
	// function) can call rn.SetHeadscaleUpdateMonitor(hsMon).
	// 2026-07-11: Phase 3 (/quota) needs per-user rule limits
	// to render "user X used N of M" rather than just N. Set
	// once at boot; the BotEnv snapshot is per-message so a
	// future reload still works without restart.
	rn.SetLimits(cfg.UserMaxRules, cfg.MaxRulesPerDevice)
	// 2026-07-11: Phase 4 (/version) needs the build label
	// (the same one app.Version holds for the dashboard).
	rn.SetVersion(app.Version)
	// 2026-07-13: Этап 11 part 1 — wire the headscale
	// client so /add_device can issue real preauth keys
	// from the bot. Reuse the same *headscale.Client that
	// the web handlers use (hs was constructed at line 77)
	// so both surfaces share one source of truth.
	rn.SetHS(hs)
	// 2026-07-17: v0.16.7 — wire the sidecar
	// manager (created above) so /mysubnet provision
	// can issue per-user preauth keys in chat. The
	// manager's own Run() goroutine is the auto-
	// approver for tag:subnet-router nodes; this
	// just hands the manager to the bot's env.
	rn.SetSidecar(sidecarMgr)
	// 2026-07-20: v0.20.0 — headscale-update-monitor.
	// Wired below (after the monitor's struct is
	// created) so the variable is in scope; the
	// SetHeadscaleUpdateMonitor call lives outside
	// this block where `hsMon` is reachable.
	// 2026-07-16: v0.12.1 — per-user headscale-client
	// routing. The closure binds app so the bot calls
	// the same App.HSForUser the web handlers use
	// (which reads portal_users.headscale_url +
	// headscale_api_key_enc and falls through to the
	// global default when no override is set). Single-
	// plane deploys still work — App.HSForUser returns
	// app.HS when there's no per-user row.
	rn.SetHSForUser(app.HSForUserFn)
	// 2026-07-16: v0.13.0 — per-user plane-URL routing
	// (parallel to SetHSForUser). Returns the
	// headscale_url the user is on so the bot can
	// scope acl.GenerateACLForPlane to the right
	// identities. Returns "" for users on the global
	// default plane, which preserves v0.12.0
	// behaviour.
	rn.SetPlaneURLForUser(app.PlaneURLForUser)
	// 2026-07-13: Этап 11 part 2b — per-device and total
	// rule caps for /add_rule. Mirrors the web form's
	// PostMyExitRule checks. Zero = no cap (same convention
	// as SetLimits above).
	rn.SetRuleCaps(cfg.MaxRulesPerDevice, cfg.MaxTotalRules)
	// B465: on a READ-ONLY COPY the real notifier is never installed, so every app.Notifier
	// consumer keeps the no-op from handlers.New (and the bot loop below is skipped). The
	// reasoning and the live measurement live in readonly_b465.go — main.go only shrinks.
	readOnlyCopy := installNotifierUnlessReadOnly(app, rn)
	// 2026-08-10: v0.33.1.38 — Notifier order bug fix.
	// adminSvc was constructed at line 413 (way before
	// rn was even created), so adminSvc.Notifier captured
	// the initial app.Notifier value (NoopNotifier{}
	// from handlers.New). After this app.Notifier = rn
	// the admin handlers (including the /admin/telegram
	// "Send test" handler) still saw the stale
	// NoopNotifier and returned "Бот не сконфигурирован —
	// Notifier в no-op режиме" even though the bot WAS
	// configured. Re-bind here so the admin handlers
	// pick up the RealNotifier. Other services
	// (releaseMon, exitMon, hsMon) are constructed
	// below this point, so they pick up the new value
	// automatically.
	adminSvc.Notifier = app.Notifier
	// 2026-07-13: split the startup message by what's
	// actually configured. The polling gate in Run()
	// uses Configured() which is now token-only, so the
	// bot can start receiving /login as soon as the
	// admin saves the token (chat_id is needed only
	// for outgoing notifications, not for receiving
	// commands).
	//
	// B465: the message moved into logTelegramStartup (readonly_b465.go) because on a
	// read-only copy it promised a loop this process does not start. It is also where the
	// B372 size ceiling wants it: main.go only ever shrinks.
	logTelegramStartup(d.DB, readOnlyCopy)
	// B465: on a read-only copy the bot loop is not started at all (a copy that polls
	// getUpdates answers the operator's commands from the WRONG installation). The reasoning
	// and the live measurement live in readonly_b465.go.
	if !readOnlyCopy {
		go rn.Run(ctx)
	}
	// 2026-07-15: Этап 14 v13 — register the per-language
	// command menu. Best-effort: a Telegram-side failure
	// is logged inside SetMyCommandsAll and the bot
	// keeps running without a menu. The user can still
	// type commands from memory; the menu is a
	// convenience, not a gate.
	if !readOnlyCopy {
		go func() {
			if err := rn.SetMyCommandsAll(context.Background(), telegram.DefaultMyCommandsSpec); err != nil {
				log.Printf("🤖 setMyCommandsAll: %v", err)
			}
		}()
	}
}

// wireLateServices runs the wiring that happens AFTER the HTTP server goroutine is
// running: certificate sync, the certificate publisher/issuer, and the optional
// background schedulers. It owns no stopper (every started loop lives for the process).
func wireLateServices(
	ctx context.Context,
	cfg *config.Config,
	d *db.ResettableDB,
	app *handlers.App,
	adminSvc *adminsvc.Service,
	mux *http.ServeMux,
	bc bootCtx,
) {

	// B465 follow-up: the DNS auto-updater and the preferred-exit reconciler used to be
	// started right here — ABOVE wireOptionalSchedulers, so that function's read-only early
	// return did not cover them (measured: `autoupdater: starting` and
	// `preferred-reconciler: starting (live=true)` on a copy that had just logged «every
	// background scheduler is SKIPPED»). Both blocks moved into wireEarlySchedulers
	// (main_schedulers.go), whose first statement is the read-only gate; the rationale and
	// the measurement live there.
	wireEarlySchedulers(ctx, app, cfg)

	// 2026-08-09: v0.33.1.25 (B77) — node-discovery
	// autoupdater. Runs nodeownership.Backfill against
	// every portal user on a timer, so new devices
	// registered in headscale get their
	// `tag:dev-<user>-<device>` applied automatically
	// (without it, the per-device ACL rule's src=tag
	// doesn't match and the device has no internet
	// access). 2026-08-09 operator report: Issue 2
	// (new device registration didn't auto-assign the
	// tag + grant). Default 5m, 0/off disables.
	//
	// B227: wire the TagAlertSink so failed AddTag
	// calls surface to (a) skygate_tag_autoupdate_failures_total
	// Prometheus counter, (b) audit_log `tag.autoupdate_failed`
	// row, (c) rate-limited Telegram alert. Without
	// this, a stuck ACL reject (e.g. skygate-host-1-1
	// with `tag:infra-skygate-host-1-1` that headscale
	// keeps refusing) is invisible to the operator
	// except by tailing skygate stderr.
	if cfg.NodeDiscoveryInterval > 0 {
		if hs := app.HSGlobalFn(); hs != nil {
			alertSink := nodeownership.NewTagAlertSink(app.Notifier, d)
			go nodeownership.AutoBackfill(ctx, d, hs, alertSink, cfg.NodeDiscoveryInterval)
		} else {
			log.Printf("node-discovery: HSGlobalFn() returned nil, skipping startup goroutine (defensive guard)")
		}
	} else {
		log.Printf("node-discovery: SKYGATE_NODE_DISCOVERY_INTERVAL=%v, skipping startup goroutine (set to a positive duration to re-enable)", cfg.NodeDiscoveryInterval)
	}

	// In-app backup scheduler: the config loader must be wired FIRST so Unmount (called by
	// RunBackup on its way out) re-reads the mountpoint; LoadWithKey (B404) decrypts.
	//
	// B465: NOT on a read-only copy. The scheduler reads its schedule from the DATABASE
	// (backup.LoadWithKey), so it follows the COPIED settings, not this host's — live case:
	// the restored copy booted with `backup-scheduler: started (enabled=false, …)` only
	// because the copied row said disabled. With it enabled there, the copy would dump its
	// own database and upload it into the primary's S3 prefix. The configuration gate cannot
	// help here (the decision lives in the DB), so the guard is at the start site.
	if config.ReadOnlyCopy() {
		log.Printf("🛑 READ-ONLY COPY: backup-scheduler NOT started (its schedule comes from the COPIED database, so it would dump this copy's DB into the operator's backup bucket)")
	} else {
		backup.SetConfigLoader(func() (*backup.Config, error) {
			return backup.LoadWithKey(d.DB, cfg.SecretKeyHex)
		})
		backupSched := &backup.Scheduler{
			DB:       d,
			Notifier: schedulerNotifierSink(app.Notifier),
		}
		logBackupSchedulerStartup(d.DB, cfg.SecretKeyHex) // B446: says enabled/schedule/last_run at boot
		backupSched.Start(ctx)
	}

	// 2026-07-14: Этап 14 v8 — release-monitor goroutine.
	// Polls GitHub Releases once an hour and emits a
	// Notifier.SendAlert when a newer version is available.
	// Independent of system cron / external tooling — the
	// bot carries the message to admin and the operator
	// decides when to upgrade (see AGENTS.md "Updating").
	//
	// 2026-08-05 v0.33.1.10: Owner / Repo are now wired
	// from cfg (defaults to "BarsSky"/"skygate" — the
	// operator's actual GitHub repo; the previous
	// "skygate-operator/skygate" hardcode http.StatusNotFound'd).
	releaseMon := &release.Monitor{ // HTTP nil: Start installs the same 10s-timeout client (B425)
		Current:    version,
		Notified:   make(map[string]bool),
		Notifier:   app.Notifier,
		CheckEvery: 1 * time.Hour,
		DB:         d.DB, // B425: prompt first check + the announced tag survives restarts
		Owner:      cfg.GitHubOwner,
		Repo:       cfg.GitHubRepo,
	}
	releaseMon.Start(ctx)
	// 2026-07-15: v0.14.0 — expose the monitor on App so
	// the /dashboard banner can read its snapshot on every
	// page render. nil-safe: handlers guard with
	// `if a.ReleaseMonitor != nil`.
	app.ReleaseMonitor = releaseMon

	// 2026-07-15: v0.13.0 — exit-node health monitor.
	// Background goroutine that polls headscale every
	// cfg.ExitNodeCheckInterval (default 5 min), updates the
	// exit_node_health snapshot, and dispatches calm-mode
	// alerts (online↔offline transitions) via the
	// Notifier. The "Run health check now" button on
	// /admin/exit-nodes and the /exit_nodes_health bot
	// command both read the same DB rows the monitor
	// writes. cfg.ExitNodeCheckInterval = 0 disables the
	// monitor (the deploy-time check
	// scripts/check_exit_nodes.py still runs).
	startExitNodeMonitor(ctx, cfg, d, app, adminSvc)

	// 2026-07-20: v0.20.0 — headscale-update-monitor.
	// Polls the GitHub Releases API for
	// juanfont/headscale, writes each unique tag to
	// the headscale_releases table, and dispatches a
	// Telegram alert when a newer-than-pinned
	// version is found. The /admin/headscale page
	// and the bot /headscale command read the
	// monitor's Snapshot(); the /admin/exit-nodes
	// page reads UpdateAvailable + BreakingAvailable
	// to render a banner. cfg.HeadscalePollInterval
	// = 0 disables the goroutine (the page + bot
	// still work from the cache).
	// 2026-09-23 (B297): ASK the running headscale what version it is, instead of
	// trusting SKYGATE_HEADSCALE_VERSION_PIN. The pin is a declaration a human
	// typed once (config.go says outright that it is not auto-detected), and the
	// two live hosts already disagree: `aro` runs 0.29.0, the agent VM runs
	// 0.29.3. 0.29.x is not uniform in the API surface skygate depends on —
	// approve_routes left REST at 0.29.1, the REST expire path broke at 0.29.2,
	// and 0.29.2 is the version that rejects wildcards in `tagOwners` — so which
	// capability rung a host takes was previously unknowable from the portal.
	// The probe is a ladder (API → root endpoint → CLI) and never fails the boot:
	// it either reports a version or the rungs it tried. The socket is already
	// bound (B269), so a slow headscale cannot delay availability.
	bc.startHeadscaleUpdateMonitor()

	// 2026-08-18 (B130): background scheduler for
	// time-of-day auto-update. Reads the schedule from
	// global_settings (with env-var fallbacks) and
	// triggers the update orchestrator when (a) schedule
	// is enabled, (b) the configured HH:MM is due, (c) GitHub
	// has a newer release, and (d) no update is already in
	// flight. The /admin/update page (B129) shows the schedule + last-run state.
	//
	// 2026-10-10 (B375): the goroutine is ARMED UNCONDITIONALLY — the DATABASE
	// decides whether it acts, because this block used to sit behind
	// `if cfg.UpdateScheduleEnabled` (the ENV value) while global_settings is the
	// authoritative toggle, so a schedule the operator enabled in the panel had
	// no scheduler at all. See main_update_scheduler.go for the measured defect,
	// the boot-log contract and why the wire-up lives in its own function (B372's
	// ceiling on this file).
	wireUpdateScheduler(ctx, d, app, cfg)

	// 2026-08-18 (B142, v1.4.1): in-app backup-verify
	// scheduler. Mirrors the B130 update-scheduler wire-up
	// above. When enabled, runs scripts/verify_backup.sh
	// on the configured cron schedule and sends a
	// Telegram alert on failure (the pre-B142 system-cron
	// path wrote the status to global_settings but didn't
	// notify). The script path is the standard deploy
	// location ($cfg.RepoPath/scripts/verify_backup.sh).
	// Disabled by default — operators opt in via
	// SKYGATE_BACKUP_VERIFY_IN_APP_ENABLED=true (or
	// /admin/backup page toggle once B142 ships).
	if cfg.BackupVerifyInAppEnabled {
		verifyScriptPath := ""
		skygateBinPath := ""
		if cfg.RepoPath != "" {
			verifyScriptPath = cfg.RepoPath + "/scripts/verify_backup.sh"
			skygateBinPath = cfg.RepoPath
		}
		backup.StartVerifyScheduler(ctx, backup.VerifySchedulerDeps{
			DB:             d.DB,
			Notifier:       schedulerNotifierSink(app.Notifier),
			ScriptPath:     verifyScriptPath,
			SkygateBinPath: skygateBinPath,
		})
		log.Printf("🔍 backup-verify-scheduler: enabled (env-var default schedule=%q; /admin/backup page can override)", cfg.BackupVerifySchedule)
	} else {
		log.Printf("🔍 backup-verify-scheduler: disabled (SKYGATE_BACKUP_VERIFY_IN_APP_ENABLED=false; /admin/backup page can enable). NOTHING else is assumed: verify then runs ONLY if you wired scripts/verify_backup.sh into the host cron, and the panel cannot see that — backup.last_verify_* stays empty until it runs (B446: the pre-B446 banner claimed system cron «continues to run» on an install that had no such entry at all).")
	}

	// 2026-08-19: v1.5.0 (B147) — in-app certsync.
	// Polls the S3 deploy bucket's `certs/` prefix
	// every 30s, pulls newer certs if the local SHA
	// doesn't match, writes to LocalDir, then
	// triggers the Caddy reload callback. Independent
	// of the provider rate limit (no DNS-side work —
	// just S3 reads + local file writes). Disabled
	// by default (operator opts in via
	// SKYGATE_CERTSYNC_ENABLED=true).
	if cfg.CertSyncEnabled {
		// B371 (2026-10-09): the S3 settings come from the DB
		// the panel writes (global_settings via backup.Load),
		// with the SKYGATE_S3_* env vars as the fallback —
		// B147 read the env vars ALONE, so a deployment whose
		// S3 is configured on /admin/backup handed certsync an
		// empty endpoint and an empty region and minio-go got
		// the nonsense host "s3..amazonaws.com", retried every
		// 30 s forever, and the panel's own backup worked.
		// A config that cannot work is refused by name below
		// instead of being started. The bucket stays the
		// certsync-specific cfg.CertSyncBucket (default
		// "skygate-backups"); the key prefix `certs/` is
		// hardcoded in the scheduler.
		backupCfg, cfgErr := certSyncS3Config(d.DB, cfg)
		if cfgErr != nil {
			log.Printf("🔐 certsync: disabled — %v (certsync will not run; fix the setting and restart)", cfgErr)
		} else if s3Client, s3Err := backup.NewS3ClientForConfig(backupCfg); s3Err != nil {
			log.Printf("🔐 certsync: WARN could not build S3 client: %v (certsync disabled)", s3Err)
		} else {
			certsyncAdapter, err := certsync.NewMinioS3Client(s3Client)
			if err != nil {
				log.Printf("🔐 certsync: WARN could not build S3 adapter: %v (certsync disabled)", err)
			} else {
				_, err := certsync.Start(ctx, certsync.CertSyncDeps{
					DB:          d.DB,
					LocalDir:    cfg.CertSyncLocalDir,
					S3Client:    certsyncAdapter,
					S3Bucket:    cfg.CertSyncBucket,
					Interval:    cfg.CertSyncInterval,
					Notifier:    schedulerNotifierSink(app.Notifier),
					CaddyReload: nil, // future: wire to `docker exec skygate-caddy caddy reload`
				})
				if err != nil {
					log.Printf("🔐 certsync: WARN start failed: %v (certsync disabled)", err)
				} else {
					log.Printf("🔐 certsync: enabled (interval=%s, bucket=%s, endpoint=%s, region=%s, local_dir=%s, caddy_reload=not_configured)", cfg.CertSyncInterval, backupCfg.S3Bucket, certSyncS3EndpointLog(backupCfg), backupCfg.S3Region, cfg.CertSyncLocalDir)
				}
			}
		}
	} else {
		log.Printf("🔐 certsync: disabled (SKYGATE_CERTSYNC_ENABLED=false). Pre-B147 system-cron cert-renew.sh continues to run.")
	}

	// B467: give /admin/certificates a real publisher. The page's one action called a callback
	// that was never assigned, so it validated the pair, wrote nothing and said "Cert uploaded";
	// the wiring (local pair + .prev + optional S3 with a bumped .version + proxy reload) lives
	// in cert_publish_b467.go with the reasoning. Deliberately OUTSIDE the certsync block above:
	// a native install without S3 must still be able to install a certificate.
	wireCertPublish(adminSvc, d, cfg)
	// B467: and the ability to OBTAIN one (Let's Encrypt over HTTP-01). The public challenge
	// route is registered on the same mux as everything else, so it works without a second
	// listener; wireCertIssue installs the issuance callback.
	registerCertChallengeRoute(mux)
	wireCertIssue(adminSvc, cfg)
	// B467: POST /admin/certificates/issue — the button. Kept out of the route table in this
	// file because main.go only ever shrinks (B372).
	registerCertIssueRoute(mux, adminSvc)

	// B372 (2026-10-09): the optional background schedulers (smoke-mesh cleanup,
	// Tailscale discovery, token auto-rotate, preauth-key notify, HA chain) live
	// in main_schedulers.go. They are the tail of the boot sequence and were the
	// safest block to lift out of this function: goroutines only, no state the
	// shutdown path below needs, and — unlike the watchdog/elector blocks above —
	// no `defer`, so nothing stops earlier than it used to.
	wireOptionalSchedulers(ctx, d, app, adminSvc, cfg)

}
