// boot_monitors.go — B526 (2026-11-27): boot phases lifted out of main().
//
// main() was a 2 272-line function: the boot sequence read as one script, so nobody
// could see where it stuck (B269 added `startup: phase=` lines for exactly that
// reason). Each phase moves here VERBATIM, taking the names it already used, so a
// reader can follow main() as a list of steps. Keep the bodies unrewritten.
package main

import (
	"context"
	"log"

	"skygate/internal/config"
	"skygate/internal/db"
	adminsvc "skygate/internal/feature/admin"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/headscale_version"
	"skygate/internal/monitoring"
)

// startHeadscaleUpdateMonitor asks the RUNNING headscale what version it is (B297) and,
// when polling is enabled, wires the version monitor into the app and the bot.
func (b bootCtx) startHeadscaleUpdateMonitor() {
	hsVer := b.hs.DetectServerVersion(b.ctx)
	if hsVer.Found() {
		log.Printf("headscale-version: running headscale is %s (via %s)", hsVer.Version, hsVer.Via)
		// B495: the capability table, so the journal answers "can this daemon do X" without anybody
		// re-deriving it from a version number. TD-5/RR-5 (per-user DNS) is gated on ExtraRecords.
		log.Printf("headscale-capabilities: %s", headscale.CapabilitiesFor(hsVer.Version).Summary())
		if b.cfg.HeadscaleVersionPin != "" &&
			headscale_version.CompareSemver(hsVer.Version, b.cfg.HeadscaleVersionPin) != 0 {
			log.Printf("headscale-version: MISMATCH — SKYGATE_HEADSCALE_VERSION_PIN says %q but the daemon answered %s; the DETECTED version is used for the update comparison (fix or clear the pin)",
				b.cfg.HeadscaleVersionPin, hsVer.Version)
		}
	} else {
		log.Printf("headscale-version: could not detect the running headscale (%s) — falling back to SKYGATE_HEADSCALE_VERSION_PIN=%q",
			hsVer.Reason(), b.cfg.HeadscaleVersionPin)
	}
	if b.cfg.HeadscalePollInterval > 0 {
		hsMon := headscale_version.NewMonitor(b.db.DB, b.cfg.HeadscaleVersionPin, b.app.Notifier)
		hsMon.CheckEvery = b.cfg.HeadscalePollInterval
		// B297: re-probe on every tick (and once immediately in Start), so a
		// headscale upgrade is reflected without a skygate restart. The monitor
		// keeps the declaration for display and reports the mismatch.
		hsMon.DeclaredPin = b.cfg.HeadscaleVersionPin
		hsMon.VersionProbe = b.hs.VersionProbe
		hsMon.Start(b.ctx)
		b.app.HeadscaleUpdateMonitor = hsMon
		// 2026-07-20: v0.20.0 — also wire the
		// monitor to the bot so /headscale renders
		// the same status the /admin/headscale
		// page does. b.rn was hoisted out of the
		// Telegram block above so this call is
		// in scope.
		b.rn.SetHeadscaleUpdateMonitor(hsMon)
		// B297: alerts no longer REQUIRE the pin — the probe supplies the running
		// version, and the pin is only the fallback for a host whose headscale
		// cannot be asked. Say which of the two is in charge.
		if hsVer.Found() {
			log.Printf("📡 headscale-update-monitor: polling every %s, running headscale %s (detected via %s; SKYGATE_HEADSCALE_VERSION_PIN=%q is only the fallback)",
				b.cfg.HeadscalePollInterval, hsVer.Version, hsVer.Via, b.cfg.HeadscaleVersionPin)
		} else {
			log.Printf("📡 headscale-update-monitor: polling every %s, version UNDETECTED — falling back to SKYGATE_HEADSCALE_VERSION_PIN=%q (set it to enable alerts)",
				b.cfg.HeadscalePollInterval, b.cfg.HeadscaleVersionPin)
		}
	} else {
		log.Printf("📡 headscale-update-monitor: disabled (SKYGATE_HEADSCALE_POLL_INTERVAL=0). /admin/headscale still works as a manual look-up.")
	}
}

// startExitNodeMonitor builds and starts the exit-node health monitor and hands it to the
// admin service (/admin/exit-nodes reads its snapshot). Interval 0 keeps the monitor OFF on
// purpose: Start() re-defaults 0 to five minutes, which used to hold the SQLite write lock
// at exactly the moment the first page request arrived (v0.32.13).
func startExitNodeMonitor(ctx context.Context, cfg *config.Config, d *db.ResettableDB, app *handlers.App, adminSvc *adminsvc.Service) {
	exitMon := &monitoring.ExitNodeMonitor{
		DB:           d,
		HS:           app.HS,
		Notifier:     app.Notifier,
		CheckEvery:   cfg.ExitNodeCheckInterval,
		OfflineAfter: cfg.ExitNodeOfflineAfter,
		OnStartup:    cfg.ExitNodeOnStartup,
		// v0.14.1: when true, the monitor's per-tick path
		// also calls db.SyncNodesFromHeadscale so new
		// exit-nodes appear in /admin/exit-nodes and the
		// bot's /exit_nodes without an admin button click.
		// Off by default; the explicit
		// /admin/devices "Sync from headscale" button is
		// still the recommended path.
		AutoSync: cfg.ExitNodeAutoSync,
	}
	// 2026-07-31: v0.32.13 — gate exitMon.Start on
	// cfg.ExitNodeCheckInterval > 0. Pre-fix the monitor
	// was always started; its Start() does an immediate
	// pre-tick (cfg.ExitNodeOnStartup defaults to true)
	// that calls m.tick() which lists every node in
	// headscale and writes rows to exit_node_health,
	// holding the SQLite WAL write lock for several
	// seconds at the worst possible time (right when the
	// first /admin/exit-nodes request arrived). The "off"
	// / "0" sentinel for SKYGATE_EXIT_NODE_CHECK_INTERVAL
	// didn't actually disable anything because Start()
	// re-defaults CheckEvery to 5min when it's 0. The
	// fix: skip Start() entirely when the operator has
	// set ExitNodeCheckInterval to a non-positive value
	// (0 / off). The /admin/exit-nodes page still works
	// from the DB; the monitor is just disabled.
	if cfg.ExitNodeCheckInterval > 0 {
		exitMon.Start(ctx)
	} else {
		log.Printf("exit-node-monitor: SKYGATE_EXIT_NODE_CHECK_INTERVAL=0/off, skipping startup")
	}
	// refactor-v0.30 Phase B step 3b.3 (2026-07-29):
	// exit-nodes admin handlers moved to feature/admin;
	// the monitor is wired there now. The App field is
	// removed once admin_exit_nodes.go is deleted.
	adminSvc.ExitNodeMonitor = exitMon
}
