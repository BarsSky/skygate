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
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/headscale_version"
	"skygate/internal/telegram"
)

// startHeadscaleUpdateMonitor asks the RUNNING headscale what version it is (B297) and,
// when polling is enabled, wires the version monitor into the app and the bot.
func startHeadscaleUpdateMonitor(ctx context.Context, cfg *config.Config, hs *headscale.Client, app *handlers.App, d *db.ResettableDB, rn *telegram.RealNotifier) {
	hsVer := hs.DetectServerVersion(ctx)
	if hsVer.Found() {
		log.Printf("headscale-version: running headscale is %s (via %s)", hsVer.Version, hsVer.Via)
		// B495: the capability table, so the journal answers "can this daemon do X" without anybody
		// re-deriving it from a version number. TD-5/RR-5 (per-user DNS) is gated on ExtraRecords.
		log.Printf("headscale-capabilities: %s", headscale.CapabilitiesFor(hsVer.Version).Summary())
		if cfg.HeadscaleVersionPin != "" &&
			headscale_version.CompareSemver(hsVer.Version, cfg.HeadscaleVersionPin) != 0 {
			log.Printf("headscale-version: MISMATCH — SKYGATE_HEADSCALE_VERSION_PIN says %q but the daemon answered %s; the DETECTED version is used for the update comparison (fix or clear the pin)",
				cfg.HeadscaleVersionPin, hsVer.Version)
		}
	} else {
		log.Printf("headscale-version: could not detect the running headscale (%s) — falling back to SKYGATE_HEADSCALE_VERSION_PIN=%q",
			hsVer.Reason(), cfg.HeadscaleVersionPin)
	}
	if cfg.HeadscalePollInterval > 0 {
		hsMon := headscale_version.NewMonitor(d.DB, cfg.HeadscaleVersionPin, app.Notifier)
		hsMon.CheckEvery = cfg.HeadscalePollInterval
		// B297: re-probe on every tick (and once immediately in Start), so a
		// headscale upgrade is reflected without a skygate restart. The monitor
		// keeps the declaration for display and reports the mismatch.
		hsMon.DeclaredPin = cfg.HeadscaleVersionPin
		hsMon.VersionProbe = hs.VersionProbe
		hsMon.Start(ctx)
		app.HeadscaleUpdateMonitor = hsMon
		// 2026-07-20: v0.20.0 — also wire the
		// monitor to the bot so /headscale renders
		// the same status the /admin/headscale
		// page does. rn was hoisted out of the
		// Telegram block above so this call is
		// in scope.
		rn.SetHeadscaleUpdateMonitor(hsMon)
		// B297: alerts no longer REQUIRE the pin — the probe supplies the running
		// version, and the pin is only the fallback for a host whose headscale
		// cannot be asked. Say which of the two is in charge.
		if hsVer.Found() {
			log.Printf("📡 headscale-update-monitor: polling every %s, running headscale %s (detected via %s; SKYGATE_HEADSCALE_VERSION_PIN=%q is only the fallback)",
				cfg.HeadscalePollInterval, hsVer.Version, hsVer.Via, cfg.HeadscaleVersionPin)
		} else {
			log.Printf("📡 headscale-update-monitor: polling every %s, version UNDETECTED — falling back to SKYGATE_HEADSCALE_VERSION_PIN=%q (set it to enable alerts)",
				cfg.HeadscalePollInterval, cfg.HeadscaleVersionPin)
		}
	} else {
		log.Printf("📡 headscale-update-monitor: disabled (SKYGATE_HEADSCALE_POLL_INTERVAL=0). /admin/headscale still works as a manual look-up.")
	}
}
