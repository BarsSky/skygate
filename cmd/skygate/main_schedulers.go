// Optional background schedulers for the skygate binary (B372).
//
// Extracted from the tail of main() during the 2026-10-09 refactor of
// cmd/skygate/main.go, which had grown past 2500 lines of one linear function.
// This block is the SAFEST part of that sequence to move because it is the very
// end of the boot path: it starts only goroutines, it declares no state the
// shutdown path below needs, and — unlike the watchdog/elector blocks earlier in
// main() — it contains no `defer`, so moving it cannot change when anything is
// stopped. Everything it needs is passed in, and nothing is passed back out.
//
// Each scheduler keeps its own `SKYGATE_*_ENABLED` opt-in gate and its own
// enabled/disabled log line, so the operator-visible behaviour of the boot is
// unchanged by the move.

package main

import (
	"context"
	"log"
	"os"
	"time"

	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/dns"
	adminsvc "skygate/internal/feature/admin"
	"skygate/internal/ha"
	"skygate/internal/handlers"
	"skygate/internal/keynotify"
	"skygate/internal/mesh"
	"skygate/internal/tokenrotate"
)

// wireOptionalSchedulers starts the opt-in background schedulers at the end of
// the boot sequence: the smoke-mesh cleanup (B143), the Tailscale auto-discovery
// poller (B223), the personal-token auto-rotate (B154), the preauth-key expiry
// notifier (B156) and the HA chain/elector (B145). Each one logs whether it is
// enabled, so the journal still answers "what is running?" the way it did when
// this code lived inside main().
func wireOptionalSchedulers(ctx context.Context, d *db.ResettableDB, app *handlers.App, adminSvc *adminsvc.Service, cfg *config.Config) {
	// 2026-08-18 (B143, v1.4.3): in-app smoke-mesh
	// cleanup scheduler. Mirrors the B142
	// backup-verify-scheduler wire-up above. When
	// enabled, runs mesh.DeleteSmokeMeshes on the
	// configured cron schedule (default 5 AM daily,
	// after the 3 AM backup + 4 AM verify) and sends
	// a Telegram alert when the cleanup actually
	// deletes rows. The pre-B143 manual workaround
	// (operator-side SQL DELETE on the 30 cruft rows
	// that accumulated between v0.33.1.36 and now)
	// is no longer needed. Disabled by default —
	// operators opt in via
	// SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED=true
	// (or /admin/system_tests page toggle once TD-8
	// ships).
	if cfg.CleanupSmokeMeshInAppEnabled {
		mesh.StartCleanupScheduler(ctx, mesh.CleanupSchedulerDeps{
			DB:       d.DB,
			Notifier: schedulerNotifierSink(app.Notifier),
		})
		log.Printf("🧹 cleanup-scheduler: enabled (env-var default schedule=%q; /admin/system_tests page can override)", cfg.CleanupSmokeMeshSchedule)
	} else {
		log.Printf("🧹 cleanup-scheduler: disabled (SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED=false; /admin/system_tests page can enable). Pre-B143 manual SQL DELETE workaround is still the only other option.")
	}

	// 2026-09-03: v1.5.0+ / B223 (Phase 4.3) —
	// Tailscale auto-discovery poller. Runs every
	// 5 minutes (overridable via
	// SKYGATE_DISCOVERY_INTERVAL_SEC). For each
	// tick, runs cluster.DiscoverNewNodes (which
	// shells out to `tailscale status --json`) +
	// inserts cluster_node rows in state=pending
	// for any new peer. The admin then sees the
	// pending rows on /admin/cluster and clicks
	// the existing B217 "Approve" button to
	// transition them to state=ready. The HTTP
	// handler at /admin/cluster/discover runs the
	// same function on demand (so the operator
	// doesn't have to wait up to 5 min for a
	// "just-added-a-new-node" discovery).
	//
	// Errors are silent (the next tick retries);
	// we log to stderr so the operator can see
	// "discovery failed" in `docker logs`. The
	// /admin/cluster page also surfaces the last
	// `cluster.discovery.error` audit row.
	discoveryInterval := envOrDefaultDuration("SKYGATE_DISCOVERY_INTERVAL_SEC", 5*time.Minute, time.Second)
	if discoveryInterval > 0 {
		go runDiscoveryTicker(ctx, d.DB, adminSvc.DiscoveryTag, discoveryInterval, schedulerNotifierSink(app.Notifier))
		log.Printf("🔎 discovery-ticker: enabled (interval=%s, tag_filter=%q)", discoveryInterval, adminSvc.DiscoveryTag)
	} else {
		log.Printf("🔎 discovery-ticker: disabled (SKYGATE_DISCOVERY_INTERVAL_SEC=0). Operator can still trigger via /admin/cluster/discover.")
	}

	// 2026-08-20: v1.5.0 (B154) — in-app auto-rotate
	// scheduler for personal API tokens with
	// auto_rotate=1. When enabled, runs a daily cron
	// (default 03:00) that extends the expiry of any
	// token within 7 days of expiry to (now + 30d).
	// The token's hash DOES NOT change — the existing
	// token keeps working. Sends a Telegram alert with
	// the per-token label list when the extension
	// actually fires.
	//
	// Disabled by default (operator opt-in via
	// SKYGATE_TOKEN_AUTO_ROTATE_ENABLED=true). The
	// /my/tokens page (post-B154.1) will expose a
	// runtime toggle via global_settings["tokens.
	// auto_rotate_enabled"]. Same wire-up pattern as
	// the B130/B142/B143 schedulers above.
	if cfg.TokenAutoRotateEnabled {
		tokenrotate.Start(ctx, tokenrotate.SchedulerDeps{
			DB:       d.DB,
			Notifier: schedulerNotifierSink(app.Notifier),
		})
		log.Printf("🔄 auto-rotate-scheduler: enabled (env-var default schedule=%q; /my/tokens page can override)", cfg.TokenAutoRotateSchedule)
	} else {
		log.Printf("🔄 auto-rotate-scheduler: disabled (SKYGATE_TOKEN_AUTO_ROTATE_ENABLED=false; /my/tokens page can enable). Pre-B154 tokens with auto_rotate=1 just expire silently — operator has to manually re-create them.")
	}

	// 2026-08-20: v1.5.0 (B156) — in-app preauth key
	// expiration notification scheduler. Scans
	// preauth_keys daily (default 9 AM), sends a
	// localized Telegram message to the user
	// when their unused, not-yet-expired key is
	// within 14 days of expiry, with the reissue
	// instructions ("go to /my/keys → click
	// Reissue"). Differs from B154 (auto-rotate)
	// in two ways: (1) per-user chat (not
	// operator chat), (2) notify only, no
	// automatic action.
	//
	// Disabled by default (operator opt-in via
	// SKYGATE_KEY_NOTIFY_ENABLED=true). The
	// future /admin/settings page (post-B156.1)
	// will expose a runtime toggle via
	// global_settings["keys.notify_enabled"].
	if cfg.KeyNotifyEnabled {
		keynotify.Start(ctx, keynotify.SchedulerDeps{
			DB:       d.DB,
			Notifier: schedulerUserNotifierSink(app.Notifier),
		})
		log.Printf("🔑 key-notify-scheduler: enabled (env-var default schedule=%q; /admin/settings page can override)", cfg.KeyNotifySchedule)
	} else {
		log.Printf("🔑 key-notify-scheduler: disabled (SKYGATE_KEY_NOTIFY_ENABLED=false; /admin/settings page can enable). Pre-B156 users only saw the warning on the /my/keys page when they happened to log in.")
	}

	// 2026-08-18: v1.5.0 (B145) — HA chain + elector + DNS
	// provider wire-up. Disabled by default
	// (SKYGATE_HA_ENABLED=false) so the boot path is a
	// no-op on existing installs until the operator
	// has finished /admin/ha configuration. When
	// enabled, the elector goroutine runs every
	// cfg.HAHeartbeatInterval (default 5s) and
	// reconciles the chain in `global_settings.ha_chain`
	// based on local Patroni state + remote heartbeats.
	//
	// The DNS provider (cfg.DNSProvider) is constructed
	// via dns.BuildProvider. At v1.5.0 (B145) only
	// "external" is implemented; "cloudflare" / "route53" /
	// "rfc2136" return ErrUnknownProvider. The elector
	// currently doesn't auto-update DNS (that's Phase 3 /
	// B147 — the certsync + DNS update combined path); the
	// Phase 1 wire-up is intentionally limited to chain
	// reconciliation + transition audit-log entries.
	//
	// HASelfRoleOverride maps to Elector.SelfRoleOverride.
	// "auto" (default) → trust Patroni. "active" / "standby"
	// → force the role regardless of Patroni state. The
	// empty string falls through to "auto" (defensive).
	haProvider, haErr := dns.BuildProvider(cfg.DNSProvider, dns.BuildDeps{
		DB:        d.DB,
		SecretKey: cfg.SecretKeyHex,
	})
	if haErr != nil {
		log.Printf("ha: DNS provider build failed: %v (HA chain will still work, just no DNS update on failover)", haErr)
	}
	_ = haProvider // used in B147 (certsync + DNS update); kept here so the build validates the construction.
	if cfg.HAEnabled {
		elector := ha.NewElector(d.DB)
		elector.SelfHostname = os.Getenv("SKYGATE_HA_SELF_HOSTNAME")
		if elector.SelfHostname == "" {
			if h, err := os.Hostname(); err == nil {
				elector.SelfHostname = h
			}
		}
		elector.HeartbeatInterval = cfg.HAHeartbeatInterval
		elector.MissedThreshold = cfg.HAMissedThreshold
		if cfg.HASelfRoleOverride != "" && cfg.HASelfRoleOverride != config.HARoleAuto {
			elector.SelfRoleOverride = string(cfg.HASelfRoleOverride)
		}
		// Wire the existing telegram notifier into the
		// elector's transition callback via a thin
		// adapter. The adapter implements the ha.Notifier
		// interface (which has NotifyRoleChange, NOT
		// SendAlert — the update.NotifierSink interface
		// isn't reusable here without a wrapping method).
		elector.Notifier = haNotifierAdapter{n: app.Notifier}
		// We don't auto-update DNS from the elector yet
		// (that's B147). Log the provider for visibility.
		log.Printf("ha: HA enabled (self=%s, tick=%s, threshold=%d, role_override=%q, dns=%q)",
			elector.SelfHostname, elector.HeartbeatInterval, elector.MissedThreshold,
			elector.SelfRoleOverride, haProviderName(cfg.DNSProvider))
		go elector.Run(ctx)
	} else {
		log.Printf("ha: HA disabled (SKYGATE_HA_ENABLED=false; /admin/ha page or env-var will enable once the chain is configured)")
	}

	// 2026-07-17: v0.16.7 — per-user subnet sidecar
	// auto-approver moved earlier so the RealNotifier
	// can pick up the same manager via SetSidecar().
	// (See "Telegram bot" block above.")
}
