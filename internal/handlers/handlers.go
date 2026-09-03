package handlers

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/controlplane"
	"skygate/internal/db"
	"skygate/internal/expirewatch"
	"skygate/internal/feature/exit_rules"
	"skygate/internal/headscale"
	"skygate/internal/headscale_version"
	"skygate/internal/httputil"
	"skygate/internal/i18n"
	"skygate/internal/notifications"
	"skygate/internal/ratelimit"
	"skygate/internal/release"
	"skygate/internal/sidecar"
	"skygate/internal/telegram"
)

func init() { i18n.SetGlobal(i18n.New()) }







type App struct {
	Version string
	// v0.26.0 — process-wide liveness/readiness fields.
	// Set once at boot, read by the /healthz and /readyz
	// handlers. Carried in the App so the handlers can
	// render them without reaching into the global
	// state.
	InstanceID  string        // SKYGATE_INSTANCE_ID env, "unconfigured" if empty
	BuildVersion string        // "v0.26.0" + commit SHA (set by main.go at boot)
	StartedAt   time.Time     // wall-clock when main() returned from setup
	RateLimiter *ratelimit.Limiter
	Notifier    telegram.Notifier
	I18n         *i18n.Catalog
	// DB is the *db.ResettableDB wrapper (NOT a captured
	// *sql.DB). The B203 watchdog hot-reloads the pool via
	// db.ResettableDB.Reset(); the wrapper's promoted
	// methods (Query / Exec / etc.) read the embedded
	// *sql.DB at call time, so a captured *App.DB
	// (a *db.ResettableDB) automatically follows every
	// swap. Pre-B224 App.DB was a *sql.DB captured at
	// handlers.New() — the watchdog swap closed the OLD
	// pool and every background service that held a.DB
	// got "sql: database is closed" forever.
	DB           *db.ResettableDB
	hs           *headscale.Client
	HS           *headscale.Client
	HeadscaleKey string
	JWTSecret    string
	// B161.1 (v1.5.0): OIDC provider config. The
	// OIDC service reads these to mount the
	// discovery + JWKS endpoints and (in B161.2+)
	// to validate headscale's token requests.
	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCKeyDir       string
	// B161.2: comma-separated allowlist of redirect URIs.
	OIDCRedirectURIs string
	// ControlURL is the public-facing URL of the headscale control plane,
	// shown to users in preauth instructions so they can configure
	// Tailscale with a custom coordination server. Typically
	// https://head.example.com; falls back to a hardcoded default if the
	// SKYGATE_CONTROL_URL env var is empty at startup.
	ControlURL   string
	SessionHours int
	DerpBaseURL  string // base URL of the local custom DERP server
	SSHKeyPath   string // SSH key for exit node route sync
	Cfg         *config.Config // 2026-07-07: issue #12 — limits & stagger sync
	// 2026-07-15: v0.10.12 — public URL of an existing Headplane
	// instance (HEADPLANE_EXTERNAL_URL). When set, the admin
	// ACL page links to this URL instead of the bundled
	// sidecar. Empty = use the bundled sidecar at
	// https://${ControlURL-host}:50445/admin/.
	HeadplaneExternalURL string
	SecretKeyHex string
	// refactor-v0.30 Phase D3 (2026-07-29): the
	// per-user control plane routing (HSForUser,
	// HSGlobal, PlaneURLForUser, InvalidateHSCache)
	// moved to internal/controlplane/router.go. The
	// App still owns a *Router for the per-request
	// routing; the cache + mutex moved into the
	// Router type itself.
	Router *controlplane.Router

	// 2026-07-15: v0.14.0 — release-monitor reference. The
	// /dashboard banner reads ReleaseMonitor.Snapshot() to
	// surface "newer version available" without waiting for
	// the operator to read the Telegram alert. Set by
	// cmd/skygate/main.go after the monitor's Start()
	// returns. nil if the monitor is disabled (the
	// operator ran skygate with SKYGATE_RELEASE_MONITOR=off
	// — not yet a real env var, but the test suite
	// disables it via this nil field).
	ReleaseMonitor *release.Monitor
	// HeadscaleUpdateMonitor (v0.20.0) tracks new
	// headscale releases. The /admin/headscale page reads
	// its Snapshot(); the /admin/exit-nodes page reads
	// UpdateAvailable + BreakingAvailable to render a
	// banner; the bot /headscale command reads the same
	// fields. nil-safe: handlers guard with
	// `if a.HeadscaleUpdateMonitor != nil`.
	HeadscaleUpdateMonitor *headscale_version.Monitor
	// Sidecar is the per-user subnet auto-approver (v0.16.7).
	// The admin /admin/users/{id}/subnet page uses it to issue
	// preauth keys; the bot /mysubnet command uses it for the
	// same; the background goroutine started in cmd/skygate/main.go
	// calls Run() which periodically approves routes + flips
	// status active/disabled based on headscale state.
	Sidecar *sidecar.Manager

	// 2026-07-21: v0.23.3 — node-expiry watcher. The
	// background goroutine started in cmd/skygate/main.go
	// calls Run() which periodically (every
	// cfg.ExpireWatchInterval, default 5m) walks every
	// non-tagged node in headscale and extends any whose
	// Expiry is within cfg.ExpireWatchThreshold
	// (default 7d) out to cfg.ExpireWatchRenewal
	// (default 30d). Works around the Tailscale 1.98.x
	// client behaviour of sending a 2-4-second Expiry
	// in RegisterRequest — see
	// internal/expirewatch/manager.go for the full
	// background. nil if the watcher is disabled
	// (SKYGATE_EXPIREWATCH_ENABLED=false).
	ExpireWatch *expirewatch.Manager

	// 2026-07-15: v0.12.0.2 — Telegram probe result cache.
	// The probe does a real GET to api.telegram.org with a
	// 5s timeout. On the production VM that host is
	// unreachable (RF block + no relay advertised for the
	// resolved IPs), so every page load took the full 5s
	// timeout. The cache holds the most recent result for
	// telegramProbeTTL so the page renders instantly on
	// subsequent loads. Invalidated by the save/rotate/
	// disable/strict handlers so the operator sees the
	// fresh result after they take an action.
	//
	// refactor-v0.30 Phase B step 3b.1a (2026-07-29): the
	// telegram probe cache state (telegramProbeMu,
	// telegramProbeResult, telegramProbeAt,
	// telegramProbeTokenFP) was moved to the Service in
	// internal/feature/admin/. The cache is owned by the
	// feature that uses it now.

	templates *Templates

	// adminSvc is set by main.go after New(). It owns the
	// admin routes that were moved to internal/feature/admin/
	// in refactor-v0.30 Phase B step 3. The thin wrapper
	// methods below (AdminTelegram, AdminTelegramPost) keep
	// the existing /admin/telegram routes working without
	// touching every test caller. New code should call
	// adminSvc directly via the route registration in
	// cmd/skygate/main.go.
	adminSvc adminSvcHandle

	// refactor-v0.30 Phase B step 4 (2026-07-29): the
	// exit_rules feature service. Set by main.go after
	// New() and after exitRulesSvc is constructed. The
	// *App.SyncAdvertisedRoutes and *App.RunDomainAutoUpdater
	// wrappers below route through this field so the
	// /admin/exit-nodes/sync button (called via app.SyncAdvertisedRoutes)
	// and the boot-time autoupdater goroutine
	// (go app.RunDomainAutoUpdater(ctx, ...)) work without
	// duplicating the implementation in handlers/.
	exitRulesSvc exitRulesRunner
}

// adminSvcHandle is an interface so handlers.go doesn't import
// internal/feature/admin (which would create a cyclic dep —
// feature/admin imports handlers via Backend). The concrete
// *adminsvc.Service satisfies it. Set via SetAdminService in
// main.go.
//
// Only the methods that existing tests still call through
// *App live here. New routes go directly to adminSvc in
// main.go.
type adminSvcHandle interface {
	AdminTelegram(w http.ResponseWriter, r *http.Request)
	AdminTelegramPost(w http.ResponseWriter, r *http.Request)
}

// exitRulesRunner is the surface the legacy *App needs from
// the exit_rules feature service. The concrete
// *exitrules.Service satisfies it. Set via SetExitRulesService
// in main.go.
//
// refactor-v0.30 Phase B step 4 (2026-07-29): previously
// these methods lived directly on *App (in exit_rules_sync.go).
// The Service now owns the implementation; *App keeps
// thin wrappers so admin/exit_nodes/sync + the boot-time
// autoupdater goroutine can call into them without holding
// a *Service reference.
type exitRulesRunner interface {
	SyncAdvertisedRoutes() map[string]string
	DomainAutoUpdater() (added, removed int, err error)
	StaggeredSync()
	GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error)
	// 2026-09-03: v1.5.2 (B229) — preferred-exit
	// auto-reconciler. Wraps exit_rules.Service.ReconcileDeviceExitNodePrefs
	// for the boot-time goroutine in RunPreferredExitReconciler.
	ReconcileDeviceExitNodePrefs(ctx context.Context, n exit_rules.ReconcilerNotifier) ([]exit_rules.ReconcilerChange, error)
	// 2026-09-03: v1.5.2 (B231) — preferred-exit
	// hostname-rename migrator. Same surface shape as
	// ReconcileDeviceExitNodePrefs (returns a list of
	// changes for the caller's log + alerter).
	MigrateRenamedDevicePrefs(ctx context.Context, n exit_rules.ReconcilerNotifier) ([]exit_rules.RenameMigration, error)
}

// SetAdminService wires the admin feature service into the
// legacy *App so the thin wrapper methods (and any future
// test-time helpers) can route through it. Called once
// from main.go after New() and after adminSvc is constructed.
func (a *App) SetAdminService(s adminSvcHandle) {
	a.adminSvc = s
}

// cfgOrEmptyStr is a tiny nil-safe accessor for the
// optional config.Config pointer that New() receives.
// When cfg is nil (e.g. unit tests that construct App
// directly), the field stays as "" so the OIDC routes
// return 503 "provider disabled" instead of crashing
// on a nil-deref. B161.1.
func cfgOrEmptyStr(cfg *config.Config, getter func(*config.Config) string) string {
	if cfg == nil {
		return ""
	}
	return getter(cfg)
}

// SetExitRulesService wires the exit_rules feature service
// into the legacy *App so the SyncAdvertisedRoutes +
// RunDomainAutoUpdater wrappers can route through it.
// Called once from main.go after New() and after
// exitRulesSvc is constructed.
func (a *App) SetExitRulesService(s exitRulesRunner) {
	a.exitRulesSvc = s
}

// SyncAdvertisedRoutes is the legacy *App entry point used
// by the /admin/exit-nodes/sync button and the bot. With
// Phase B step 4 it became a thin wrapper that routes
// through the exit_rules feature service.
func (a *App) SyncAdvertisedRoutes() map[string]string {
	if a.exitRulesSvc != nil {
		return a.exitRulesSvc.SyncAdvertisedRoutes()
	}
	return map[string]string{"error": "exit_rules service not wired"}
}

// GenerateRouteSetupScript is the legacy *App entry point
// used by /my/exit-rules (the ?script= download). With
// Phase B step 4c it became a thin wrapper that routes
// through the exit_rules feature service. The form_my.go
// handler still lives in handlers/ (it moves to feature/
// exit_rules/ in step 4e) so the wrapper is preserved
// until then.
func (a *App) GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error) {
	if a.exitRulesSvc != nil {
		return a.exitRulesSvc.GenerateRouteSetupScript(userID, deviceID, os, restore)
	}
	return "", fmt.Errorf("exit_rules service not wired")
}

// RunDomainAutoUpdater is the boot-time goroutine entry
// point. main.go calls `go app.RunDomainAutoUpdater(ctx,
// cfg.DNSAutoCheck)` once at startup; the wrapper routes
// through the exit_rules feature service.
func (a *App) RunDomainAutoUpdater(ctx context.Context, interval time.Duration) {
	if a.exitRulesSvc == nil {
		return
	}
	if interval <= 0 {
		return
	}
	log.Printf("autoupdater: starting (interval=%s)", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	// 2026-08-06 v0.33.1.18 — read the DB-backed toggle on every
	// tick so the /admin/system_tests UI takes effect without a
	// skygate restart. The env var SKYGATE_DNS_AUTOUPDATE_ENABLED
	// is the default only when the global_settings row doesn't
	// exist (e.g. fresh start). After the first UI toggle the
	// DB value wins.
	checkEnabled := func() bool {
		// 2026-08-06 v0.33.1.18 — read the DB-backed toggle.
		// GetGlobalSetting returns the third arg as the default
		// when the row doesn't exist (first start). We pass ""
		// so missing row → "". Then we map the string to a bool
		// (only "1"/"true" count as enabled). Any other value
		// (including "" after a fresh DB write, or "0"/"false"
		// after the operator disabled it) means the autoupdater
		// is off. The empty-row case here means "operator never
		// touched the UI, defer to the start-up gate in main.go"
		// — and since we ARE running, the start-up gate decided
		// we should be running, so default-true is the right
		// fallback.
		row, err := db.GetGlobalSetting(a.DB.Current(), "dns_autoupdate_enabled", "")
		if err != nil {
			log.Printf("autoupdater: read global_settings: %v (assuming enabled)", err)
			return true
		}
		if row == "" {
			return true // no UI override yet — start-up gate decided
		}
		return row == "1" || row == "true"
	}
	// Run once immediately, then on tick.
	if !checkEnabled() {
		log.Printf("autoupdater: dns_autoupdate_enabled=false, skipping initial run")
	} else {
		added, removed, err := a.exitRulesSvc.DomainAutoUpdater()
		if err != nil {
			log.Printf("autoupdater: initial: %v", err)
		} else if added > 0 || removed > 0 {
			log.Printf("autoupdater: initial: added=%d removed=%d", added, removed)
			a.exitRulesSvc.StaggeredSync() // 2026-07-07: issue #12 — staggered
		}
	}
	for {
		select {
		case <-ctx.Done():
			log.Printf("autoupdater: stopping")
			return
		case <-t.C:
			if !checkEnabled() {
				continue // toggle flipped off — skip this tick
			}
			added, removed, err := a.exitRulesSvc.DomainAutoUpdater()
			if err != nil {
				log.Printf("autoupdater: %v", err)
				continue
			}
			if added > 0 || removed > 0 {
				log.Printf("autoupdater: added=%d removed=%d, syncing exit-nodes", added, removed)
				a.exitRulesSvc.StaggeredSync() // 2026-07-07: issue #12
			}
		}
	}
}

// RunPreferredExitReconciler — v1.5.2 (B229, B231) —
// boot-time goroutine that calls
// exitRulesSvc.ReconcileDeviceExitNodePrefs +
// MigrateRenamedDevicePrefs on a ticker. Mirrors
// RunDomainAutoUpdater (initial run at boot, then on
// tick). Two flags control behaviour:
//   - SKYGATE_PREFERRED_RECONCILER_LIVE: live-mode
//     (writes) vs dry-run (logs only). Read on every
//     tick so the operator can flip the switch without
//     a redeploy. Safety belt — never exposed via UI.
//   - global_settings.preferred_reconcile_enabled (DB)
//     + SKYGATE_PREFERRED_RECONCILE_ENABLED (env): on/
//     off toggle. The DB row wins once written; env
//     var is the default at first start (when no row
//     exists). The /admin/system_tests page writes
//     the DB row.
//
// Every change (create / update from main
// reconciler; rename / orphan-candidate from rename
// migrator) is recorded in audit_log with the
// appropriate action. A rate-limited Telegram alert
// fires for each new (hostname, reason) bucket.
//
// 2026-09-03: v1.5.2 (B229).
// 2026-09-03: v1.5.2 (B231) — add the per-tick enabled
//   toggle + the rename migrator sub-tick.
func (a *App) RunPreferredExitReconciler(ctx context.Context, notifier interface {
	SendAlert(text string) int64
}, interval time.Duration) {
	if a.exitRulesSvc == nil {
		return
	}
	if interval <= 0 {
		return
	}
	live := exit_rules.PreferredExitReconcilerLive()
	log.Printf("preferred-reconciler: starting (interval=%s, live=%v)", interval, live)
	t := time.NewTicker(interval)
	defer t.Stop()
	// checkEnabled mirrors the dns_autoupdate_enabled
	// pattern (see RunDomainAutoUpdater above): read
	// the env var, then the DB row, then default to
	// "on" (matches the start-up gate's default-true
	// fallback). The DB row wins once written.
	checkEnabled := func() bool {
		// Env override: if the operator wants to
		// force-off via env without UI, this is the
		// belt. We treat any of "0", "false", "no",
		// "off" as off, and "1", "true", "yes", or
		// unset (defer to DB) as on.
		if v := strings.ToLower(strings.TrimSpace(os.Getenv("SKYGATE_PREFERRED_RECONCILE_ENABLED"))); v != "" {
			return v == "1" || v == "true" || v == "yes"
		}
		// DB override: the /admin/system_tests
		// toggle writes "preferred_reconcile_enabled"
		// here. Empty row → defer to default-on
		// (consistent with RunDomainAutoUpdater).
		row, err := db.GetGlobalSetting(a.DB.Current(), "preferred_reconcile_enabled", "")
		if err != nil {
			log.Printf("preferred-reconciler: read global_settings: %v (assuming enabled)", err)
			return true
		}
		if row == "" {
			return true // no UI override yet — start-up gate decided
		}
		return row == "1" || row == "true"
	}
	// firstEnabledState tracks the previous-tick
	// enabled state so we can fire a one-shot
	// Telegram alert when the operator toggles the
	// reconciler OFF. Mirrors the
	// "B227 tag-autoupdate disabled" alert pattern.
	firstEnabledState := checkEnabled()
	// Run once immediately at boot so the operator
	// sees the first dry-run log line on startup
	// (gives them a chance to review before flipping
	// SKYGATE_PREFERRED_RECONCILER_LIVE).
	runOnce := func(stage string) {
		enabled := checkEnabled()
		live := exit_rules.PreferredExitReconcilerLive()
		// One-shot alert on disable (operator flipped
		// OFF after it was ON). We only emit on the
		// transition; subsequent ticks while OFF are
		// silent.
		if firstEnabledState && !enabled {
			notifier.SendAlert("❌ preferred-exit auto-reconcile (B229/B231) DISABLED — new device_rules will not be auto-pinned until re-enabled. Re-enable: /admin/system_tests")
			firstEnabledState = false
		}
		if !enabled {
			log.Printf("preferred-reconciler: %s: disabled (skip — re-enable via /admin/system_tests or SKYGATE_PREFERRED_RECONCILE_ENABLED)", stage)
			return
		}
		firstEnabledState = true
		// Step 1: main reconciler (auto-create pref
		// from device_rules + refresh stale tag).
		changes, err := a.exitRulesSvc.ReconcileDeviceExitNodePrefs(ctx, notifier)
		if err != nil {
			log.Printf("preferred-reconciler: %s reconcile: %v", stage, err)
		} else {
			logLiveSummary(stage+":reconcile", changes, live)
		}
		// Step 2: rename migrator (B231) — orphan
		// detection + rename auto-migrate. Inherits
		// the live flag from the main reconciler
		// (it's a write, requires live mode).
		migChanges, migErr := a.exitRulesSvc.MigrateRenamedDevicePrefs(ctx, notifier)
		if migErr != nil {
			log.Printf("preferred-reconciler: %s rename-migrate: %v", stage, migErr)
		} else {
			logMigrateSummary(stage+":rename", migChanges, live)
		}
	}
	runOnce("initial")
	for {
		select {
		case <-ctx.Done():
			log.Printf("preferred-reconciler: stopping")
			return
		case <-t.C:
			runOnce("tick")
		}
	}
}

// logLiveSummary — helper for RunPreferredExitReconciler.
// Renders a one-line "N applied, M skipped" summary so
// the operator can see at a glance whether B229 made
// changes on this tick.
//
// 2026-09-03: v1.5.2 (B229).
func logLiveSummary(stage string, changes []exit_rules.ReconcilerChange, live bool) {
	if len(changes) == 0 {
		return
	}
	created, updated, skipped := 0, 0, 0
	for _, c := range changes {
		switch c.Action {
		case "create":
			created++
		case "update":
			updated++
		case "skip":
			skipped++
		}
	}
	mode := "DRY-RUN"
	if live {
		mode = "LIVE"
	}
	log.Printf("preferred-reconciler: %s summary [%s]: created=%d updated=%d skipped=%d", stage, mode, created, updated, skipped)
}

// logMigrateSummary — v1.5.2 (B231) — counterpart to
// logLiveSummary for the B230 rename migrator. Renders
// a one-line "renamed N orphan-candidate M" summary.
//
// 2026-09-03: v1.5.2 (B231).
func logMigrateSummary(stage string, changes []exit_rules.RenameMigration, live bool) {
	if len(changes) == 0 {
		return
	}
	migrated, orphans, ambiguous := 0, 0, 0
	for _, c := range changes {
		switch c.Classification {
		case exit_rules.ClassificationRename:
			migrated++
		case exit_rules.ClassificationOrphan:
			orphans++
		case exit_rules.ClassificationAmbiguous:
			ambiguous++
		}
	}
	mode := "DRY-RUN"
	if live {
		mode = "LIVE"
	}
	log.Printf("preferred-reconciler: %s summary [%s]: renamed=%d orphan-candidates=%d ambiguous=%d", stage, mode, migrated, orphans, ambiguous)
}

// AdminTelegram is a thin wrapper preserved for the existing
// test surface (handlers_my_telegram_test.go calls
// app.AdminTelegram directly). Production routes go through
// adminSvc via the registration in cmd/skygate/main.go.
func (a *App) AdminTelegram(w http.ResponseWriter, r *http.Request) {
	if a.adminSvc != nil {
		a.adminSvc.AdminTelegram(w, r)
		return
	}
	http.Error(w, "admin service not wired", http.StatusInternalServerError)
}

// AdminTelegramPost is the POST counterpart of AdminTelegram.
func (a *App) AdminTelegramPost(w http.ResponseWriter, r *http.Request) {
	if a.adminSvc != nil {
		a.adminSvc.AdminTelegramPost(w, r)
		return
	}
	http.Error(w, "admin service not wired", http.StatusInternalServerError)
}

func New(d *db.ResettableDB, hs *headscale.Client, headscaleKey, secret, controlURL, sshKeyPath string, sessionH int, cfg *config.Config) *App {
	derpURL := "http://derp.example.com:8766"
	if cfg != nil && cfg.DerpBaseURL != "" {
		derpURL = cfg.DerpBaseURL
	}
	a := &App{
		DB:           d,
		hs:           hs,
		HS:           hs,
		HeadscaleKey: headscaleKey,
		JWTSecret:    secret,
		ControlURL:   controlURL,
		SessionHours: sessionH,
		DerpBaseURL:  derpURL,
		// B161.1: OIDC provider config is plumbed
		// through New() so the auth/oidc service
		// can read it. If cfg is nil the OIDC
		// routes still mount but return 503
		// (provider disabled) — this lets the
		// binary ship without forcing operators
		// to set env vars immediately.
		OIDCIssuerURL:    cfgOrEmptyStr(cfg, func(c *config.Config) string { return c.OIDCIssuerURL }),
		OIDCClientID:     cfgOrEmptyStr(cfg, func(c *config.Config) string { return c.OIDCClientID }),
		OIDCClientSecret: cfgOrEmptyStr(cfg, func(c *config.Config) string { return c.OIDCClientSecret }),
		OIDCKeyDir:       cfgOrEmptyStr(cfg, func(c *config.Config) string { return c.OIDCKeyDir }),
		OIDCRedirectURIs: cfgOrEmptyStr(cfg, func(c *config.Config) string { return c.OIDCRedirectURIs }),
		// 2026-08-09 v0.33.1.31 B83: sshKeyPath is the
		// in-container path of the SSH private key used by
		// the per-row `exit_servers.ssh_key_path` fallback
		// (e.g. /admin/telegram egress handler reads
		// `s.SSHKeyPath` directly, NOT `s.Cfg.SSHKeyPath`,
		// so without this assignment the field stayed
		// zero-valued and the per-row `ssh_key_path` empty
		// case triggered "no ssh_key_path provided" from
		// headscale.SetAdvertisedRoutes). The Cfg field
		// ALSO holds the same value (it's the config-layer
		// copy); the /admin/exit-nodes/sync path uses
		// Cfg.SSHKeyPath, but the /admin/telegram path uses
		// App.SSHKeyPath, so both need to be populated.
		// v0.33.1 (the original sshKeyPath refactor) added
		// the field to the App struct and the parameter to
		// New() but missed the assignment here — pre-B83
		// the field was always empty. Live-verified via
		// /admin/telegram "Set as egress relay" against
		// emilia on the live VM (the 2026-08-09 operator
		// report that triggered the fix).
		SSHKeyPath:   sshKeyPath,
		templates:    LoadTemplates(),
		Notifier:    telegram.NoopNotifier{},
		I18n:         i18n.New(),
		Cfg:          cfg,
		// v0.26.0 — process-wide liveness/readiness fields.
		// InstanceID comes from SKYGATE_INSTANCE_ID env
		// (so multi-VM operators can tell which instance
		// answered a probe). BuildVersion is set later
		// by main.go (after the build hash is known).
		// StartedAt is set below.
		InstanceID: getenvOr("SKYGATE_INSTANCE_ID", "unconfigured"),
		StartedAt:  time.Now().UTC(),
		// refactor-v0.30 Phase D3 (2026-07-29): the
		// per-user control plane routing is now a
		// separate *controlplane.Router owned by the
		// App. The old hsCache + hsCacheMu fields +
		// the *App.HSForUser / HSGlobal / PlaneURLForUser
		// / InvalidateHSCache / InitHSForUserState
		// methods have all moved to that type. The
		// App keeps thin delegate methods (see
		// app_controlplane.go) so existing callers
		// (handlers_export.go, app_controlplane_test.go)
		// keep working.
		Router: controlplane.New(d.Current(), secret, hs),
	}
	a.Router.Init()
	return a
}

// getenvOr is a tiny helper for New()'s inline env
// lookups. Not exported — the production env-reader is
// config.Load, but the New() function is the wrong place
// to depend on a fully-loaded config (it would create a
// circular init at boot). The env var we want is so
// simple (a single identifier) that a one-liner is fine.
func getenvOr(key, fallback string) string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v
}

// render executes a template directly (no layout). Used for self-contained pages.
func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	// 2026-07-11: publish per-request lang to the funcmap before ExecuteTemplate.
	// The funcmap `t` / `tf` helpers read i18n.GlobalLang atomically.
	lang := a.I18n.LangFromRequest(r)
	i18n.SetLang(lang)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderWithLayout wraps a fragment template in the layout. data is merged into
// the wrapper, so handlers can add per-page fields (Nodes, Users, Entries, ...).
// IsAdmin and Page are auto-derived from c (the JWT claims) so admin nav stays visible.
func (a *App) renderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
	// 2026-07-11: i18n. Detect lang and publish it to the funcmap. The funcmap
	// helpers `t` / `tf` in templates.go read i18n.GlobalLang atomically, so
	// concurrent requests each see their own language without a data race.
	lang := a.I18n.LangFromRequest(r)
	i18n.SetLang(lang)
	data["Lang"] = lang
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data["Page"] = pageFromName(name)
	if c != nil {
		data["Username"] = c.Username
		data["IsAdmin"] = c.IsAdmin
	}
	// Theme: prefer explicit theme in data, else derive from logged-in user, else linear.
	theme := db.ThemeLinear
	if c != nil {
		theme = db.GetUserTheme(a.DB.Current(), c.UserID)
	}
	if t, ok := data["Theme"].(string); ok && db.IsValidTheme(t) {
		theme = t
	}
	data["Theme"] = theme
	data["ThemeLabel"] = db.ThemeLabel(theme)
	// B136 (v1.3.20.6): per-user display prefs (font + size +
	// selection color). DB-persisted in portal_users so the
	// user's chosen display follows them across devices and
	// browsers (the operator's 2026-08-18 "wherever I open the
	// UI, my prefs are saved"). Auto-injected for every page
	// that goes through renderWithLayout — the layout template
	// reads {{.DisplayFont}} / {{.DisplayScale}} / {{.DisplaySelBg}}
	// and emits a <style id="user-display-prefs"> block that
	// overrides themes.css. The handler that owns /my/account
	// (auth.GetMyAccount) also explicitly passes these in its
	// data map to surface them in the form (so the dropdown
	// shows the current value), but the layout's auto-inject
	// here is the source of truth for rendering the override.
	if c != nil {
		prefs := db.GetUserDisplayPrefs(a.DB.Current(), c.UserID)
		data["DisplayFont"] = prefs.FontFamily
		data["DisplayScale"] = prefs.FontScale
		data["DisplaySelBg"] = prefs.SelectionBg
		// B157 (v1.5.0): in-web notification bell
		// badge + dropdown. Auto-inject the unread
		// count + the list of unread notifications
		// for every page that goes through
		// renderWithLayout — the layout template
		// renders the bell in the sidebar and the
		// dropdown on click. We load the list
		// here (rather than via a separate API
		// endpoint) so the dropdown opens
		// instantly without a JS roundtrip. The
		// cap of 50 matches the SQL helper's
		// default — the dropdown scrolls.
		// Failure mode: if the DB read fails, we
		// silently set UnreadCount=0 + nil list
		// so the bell still renders (just shows
		// "No notifications"). The user's real
		// notifications are still in the DB;
		// the next page load will retry.
		if count, cerr := notifications.CountUnread(a.DB.Current(), c.UserID); cerr == nil {
			data["UnreadCount"] = count
		} else {
			data["UnreadCount"] = 0
		}
		if list, lerr := notifications.ListUnreadByUser(a.DB.Current(), c.UserID, 50); lerr == nil {
			data["UnreadNotifications"] = list
		} else {
			data["UnreadNotifications"] = []db.Notification{}
		}
	}
	data["Version"] = a.Version

	// 2026-07-20: v0.18.1 — auto-inject ControlURL so every
	// page template can reference {{.ControlURL}} without
	// the handler having to remember to pass it. Previously
	// `user/preauth_result.html` and `admin/exit_nodes.html`
	// referenced {{.ControlURL}} but the handlers didn't
	// pass it, so the rendered HTML showed an empty
	// `--login-server=`. The fix: renderWithLayout always
	// populates ControlURL from a.ControlURL (which the
	// caller set in New from cfg.ControlURL — the human-
	// facing URL clients should connect to, e.g.
	// https://head.example.com). Handlers can still override
	// it by passing their own "ControlURL" in the data map
	// (the for-loop below preserves caller values).
	data["ControlURL"] = a.ControlURL

	// 2026-08-09: v0.33.1.24 (B73) — auto-inject GitHub
	// org/repo so the layout's "Open release" fallback
	// button can build a `github.com/<owner>/<repo>/releases`
	// URL without hardcoding the operator's GitHub org. The
	// pre-fix layout had a literal
	// `https://github.com/skygate-operator/skygate/releases`
	// fallback that leaked the original developer's org name
	// (the v0.32.29 no-personal-data policy violation; flagged
	// in the v0.33.1.23 release notes). Defaults to
	// "BarsSky" / "skygate" if Cfg is nil (test paths).
	githubOwner := "BarsSky"
	githubRepo := "skygate"
	if a.Cfg != nil {
		if a.Cfg.GitHubOwner != "" {
			githubOwner = a.Cfg.GitHubOwner
		}
		if a.Cfg.GitHubRepo != "" {
			githubRepo = a.Cfg.GitHubRepo
		}
	}
	data["GitHubOwner"] = githubOwner
	data["GitHubRepo"] = githubRepo

	// 2026-08-12: v1.1.0 (TD-1) — compute which sidebar section
	// the current page belongs to so the layout can auto-open
	// the corresponding <details> block. The 22 admin pages
	// are grouped into 6 sections; section membership is
	// declared in sectionPageSet() below. Regular user pages
	// (dashboard, /my/*) don't belong to any section — the
	// booleans are false and the sections stay closed.
	for section, inSection := range sectionPageSet(pageFromName(name)) {
		data[section] = inSection
	}

	// 2026-08-12: v1.1.5 — breadcrumb "вы здесь". Precompute
	// the section label + page label on the Go side (not in
	// the template — the project's template funcmap doesn't
	// include sprig's `dict`, and adding it just for this
	// would be overkill). Returns empty strings when the
	// current page isn't an admin page (the template's `if
	// and .BreadcrumbSection` guard hides the breadcrumb in
	// that case).
	data["BreadcrumbSection"] = sectionLabel(pageFromName(name))
	data["BreadcrumbPage"] = pageLabel(pageFromName(name))

	// 2026-07-15: v0.14.0 — release-monitor banner. We
	// only surface the banner to admins (regular users
	// don't need upgrade prompts). The data shape is
	// pre-computed here (rather than inside the template)
	// so the conditional is one line in the layout.
	if a.ReleaseMonitor != nil {
		latest, hasUpdate, checkedAt := a.ReleaseMonitor.Snapshot()
		if hasUpdate {
			data["UpdateAvailable"] = true
			// 2026-08-09: v0.33.1.23 (B72) — split the
			// release struct into two string fields so the
			// layout template can read them without runtime
			// type-dispatch. Before this fix, /admin/update
			// (which sets `UpdateLatest` to result.Latest, a
			// string) and the auto-injected banner (which
			// used to set it to the whole `Release` struct)
			// had inconsistent shapes; the template assumed
			// the struct shape (`{{.UpdateLatest.TagName}}`)
			// and crashed at execute time when /admin/update
			// set it as a string. Pin: UpdateLatest is
			// always a tag name string; UpdateLatestURL is
			// always a release-page URL string.
			data["UpdateLatest"] = latest.TagName
			data["UpdateLatestURL"] = latest.HTMLURL
			data["UpdateCheckedAt"] = checkedAt
		}
	}
	wrapper := map[string]any{
		"Page":         data["Page"],
		"BodyTemplate": name,
		"Title":        a.I18n.T(lang, pageTitle(name)),
		"Theme":        theme,
		"ThemeLabel":   db.ThemeLabel(theme),
	}
	for k, v := range data {
		wrapper[k] = v
	}
	if err := a.templates.ExecuteTemplate(w, "layout", wrapper); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

func pageFromName(name string) string {
	name = name[:len(name)-len(".html")]
	if name == "dashboard" {
		return "dashboard"
	}
	if name == "user/devices" || name == "user/preauth_result" {
		return "my/devices"
	}
	if name == "user/exit_nodes" {
		return "my/exit-nodes"
	}
	if strings.HasPrefix(name, "admin/") {
		// 2026-08-13: 3 admin pages have a URL-vs-template
		// mismatch — the URL uses hyphens (canonical:
		// /admin/exit-nodes, /admin/exit-rules, /admin/control-planes)
		// but the template filename uses underscores
		// (admin/exit_nodes.html, admin/exit_rules.html,
		// admin/control_planes.html). Without the translation
		// below, the runtime .Page value would be
		// "admin/exit_nodes" (underscore) but the sidebar
		// active-link check (`{{if eq .Page "admin/exit-nodes"}}`),
		// sectionPageSet() (auto-open the <details> block),
		// sectionLabel(), and pageLabel() (breadcrumb) all
		// look for "admin/exit-nodes" (hyphen) — so the
		// active highlight is never set, the section never
		// auto-opens, and the breadcrumb is empty.
		//
		// Note: /admin/system_tests and /admin/headscale_acl
		// use underscores in BOTH the URL and the template
		// name (different convention — kept as-is for
		// backward-compat with the v0.32.x era), so we
		// translate ONLY the 3 known mismatched pages.
		underscoreToHyphen := map[string]string{
			"admin/exit_nodes":       "admin/exit-nodes",
			"admin/exit_rules":       "admin/exit-rules",
			"admin/control_planes":   "admin/control-planes",
		}
		if hyphen, ok := underscoreToHyphen[name]; ok {
			return hyphen
		}
		return name
	}
	if name == "help" {
		return "help"
	}
	return name
}

// 2026-08-12: v1.1.0 (TD-1) — sectionPageSet returns the
// map of section keys to membership booleans for the given
// .Page. The keys are the data map fields the layout reads
// (InSectionDevices, InSectionAccess, etc.) and the values
// are true if the page is in that section, false otherwise.
// The set of pages per section MUST match the grouping in
// layout.html — drift between this map and the template's
// <details> blocks breaks the auto-open behaviour (the
// section just stays closed when you visit a page in it).
//
// Pin: B96 (verify_pre_deploy.sh) greps for these page
// paths in the layout, so adding a new admin page to a
// section requires updating BOTH this map and the
// corresponding <details> block in layout.html. The B96
// test catches the inverse direction too: removing a page
// from a section without updating this map.
func sectionPageSet(page string) map[string]bool {
	// 22 admin pages → 6 sections. The page strings are the
	// values returned by pageFromName() above, which strips
	// ".html" from the template name and uses the raw name
	// as the section identifier (e.g. "admin/devices" for
	// "admin/devices.html").
	sections := map[string][]string{
		"InSectionDevices": {
			"admin/devices", "admin/exit-nodes", "admin/meshes", "admin/subnets",
		},
		"InSectionAccess": {
			"admin/acls", "admin/exit-rules", "admin/headscale_acl",
		},
		"InSectionHealth": {
			"admin/system_tests", "admin/services", "admin/audit",
		},
		"InSectionIntegrations": {
			"admin/integrations", "admin/headscale", "admin/headplane",
			"admin/telegram", "admin/tailscale", "admin/derp",
			"admin/derp_relays", "admin/ha",       // v1.5.0 / B149
			"admin/deploy",                        // v1.5.0 / B150
			"admin/certificates",                  // v1.5.0 / B148
			"admin/cluster",                       // v1.5.0+ / B199
		},
		"InSectionData": {
			"admin/backup", "admin/invites", "admin/control-planes",
		},
		"InSectionSettings": {
			"admin/settings", "admin/users", "admin/update",
		},
	}
	out := make(map[string]bool, len(sections))
	for section, pages := range sections {
		out[section] = false
		for _, p := range pages {
			if p == page {
				out[section] = true
				break
			}
		}
	}
	return out
}

// pageTitle returns an i18n key (not a translated string) for the given
// template name. The caller (renderWithLayout) resolves it through the
// per-request language so the title follows the chosen language.
func pageTitle(name string) string {
	switch name {
	case "dashboard.html":
		return "title.dashboard"
	case "user/devices.html":
		return "title.my_devices"
	case "user/preauth_result.html":
		return "title.preauth"
	case "user/exit_nodes.html":
		return "title.my_exit_nodes"
	case "user/account.html":
		return "title.account"
	case "user/keys.html":
		return "title.my_keys"
	case "user/exit_rules_help.html":
		return "title.exit_rules_help"
	case "my_tokens.html":
		return "title.my_tokens"
	case "admin/users.html":
		return "title.admin_users"
	case "admin/devices.html":
		return "title.admin_devices"
	case "admin/acls.html":
		return "title.admin_acls"
	case "admin/audit.html":
		return "title.admin_audit"
	case "admin/derp.html":
		return "title.admin_derp"
	case "admin/cluster.html":
		return "title.admin_cluster"
	case "admin/backup.html":
		return "title.admin_backup"
	case "admin/settings.html":
		return "title.admin_settings"
	case "admin/telegram.html":
		return "title.admin_telegram"
	case "admin/exit_rules.html":
		return "title.admin_exit_rules"
	case "admin/exit_rules_cleanup.html":
		return "title.admin_exit_rules_cleanup"
	case "admin/exit_rules_nodes.html":
		return "title.admin_exit_rules_nodes"
	case "admin/exit_nodes.html":
		return "title.admin_exit_nodes"
	case "help.html":
		return "title.help"
	case "exit_rules.html":
		return "title.exit_rules"
	default:
		return "title.skygate"
	}
}

// sectionLabel returns an i18n key for the section the given
// page belongs to. Returns "" for non-admin pages (the template
// hides the breadcrumb in that case). Used by the v1.1.5
// "вы здесь" breadcrumb above the page content.
func sectionLabel(page string) string {
	switch {
	case page == "admin/devices" || page == "admin/exit-nodes" ||
		page == "admin/meshes" || page == "admin/subnets":
		return "nav.section_devices"
	case page == "admin/acls" || page == "admin/exit-rules" ||
		page == "admin/headscale_acl":
		return "nav.section_access"
	case page == "admin/system_tests" || page == "admin/services" ||
		page == "admin/audit":
		return "nav.section_health"
	case page == "admin/integrations" || page == "admin/headscale" ||
		page == "admin/headplane" || page == "admin/telegram" ||
		page == "admin/tailscale" || page == "admin/derp" ||
		page == "admin/cluster":
		return "nav.section_integrations"
	case page == "admin/backup" || page == "admin/invites" ||
		page == "admin/control-planes":
		return "nav.section_data"
	case page == "admin/settings" || page == "admin/users" ||
		page == "admin/update":
		return "nav.section_settings"
	}
	return ""
}

// pageLabel returns an i18n key for the given admin page,
// used by the v1.1.5 "вы здесь" breadcrumb. Returns "" if
// the page is unknown (the template hides that breadcrumb
// segment in that case). Keys match what the sidebar uses
// (so sidebar label = breadcrumb label always).
func pageLabel(page string) string {
	switch page {
	case "admin/devices":
		return "nav.devices_all"
	case "admin/exit-nodes":
		return "nav.exit_nodes_admin"
	case "admin/meshes":
		return "nav.meshes_admin"
	case "admin/subnets":
		return "admin.subnets.title"
	case "admin/acls":
		return "nav.acls"
	case "admin/exit-rules":
		return "nav.exit_rules_all"
	case "admin/headscale_acl":
		return "nav.headscale_acl"
	case "admin/system_tests":
		return "nav.system_tests"
	case "admin/services":
		return "title.admin_services"
	case "admin/audit":
		return "nav.audit"
	case "admin/integrations":
		return "nav.integrations"
	case "admin/headscale":
		return "nav.headscale"
	case "admin/headplane":
		return "nav.headplane"
	case "admin/telegram":
		return "nav.telegram"
	case "admin/tailscale":
		return "tailscale.title"
	case "admin/derp":
		return "nav.derp"
	case "admin/cluster":
		return "nav.cluster"
	case "admin/backup":
		return "nav.backup"
	case "admin/invites":
		return "nav.invites"
	case "admin/control-planes":
		return "nav.control_planes"
	case "admin/settings":
		return "nav.settings"
	case "admin/users":
		return "nav.users"
	case "admin/update":
		return "nav.update"
	}
	return ""
}

// currentUser parses JWT cookie and returns claims. nil if not authenticated.
func (a *App) currentUser(r *http.Request) *auth.Claims {
	c, err := r.Cookie("skygate_session")
	if err == nil && c.Value != "" {
		claims, err := auth.ParseJWT(a.JWTSecret, c.Value)
		if err == nil {
			return claims
		}
	}
	authHdr := r.Header.Get("Authorization")
	if strings.HasPrefix(authHdr, "Bearer ") {
		tok := strings.TrimPrefix(authHdr, "Bearer ")
		if tok != "" {
			// 2026-07-11: Этап 10 part 2 — SQL moved to db helpers.
			// We still need to walk every row because the stored
			// token_hash is a bcrypt hash (see auth.GenerateAPIToken),
			// so we have to CompareHashAndPassword every candidate
			// — there's no way to do an indexed lookup.
			// 2026-07-16: v0.15.5 — filter out expired tokens
			// (TTL = 0 means "never expires" — the pre-v0.15.5
			// behaviour, preserved for legacy rows).
			candidates, err := db.ListAPITokenHashesForLookup(a.DB.Current())
			if err == nil {
				now := time.Now().Unix()
				for _, c := range candidates {
					if c.ExpiresAt > 0 && c.ExpiresAt <= now {
						// Token has expired. Skip — keep walking
						// the candidates in case a sibling row
						// has a matching hash (extremely rare —
						// token_hash is unique, but be defensive).
						continue
					}
					if auth.CheckAPIToken(c.TokenHash, tok) {
						_ = db.TouchAPITokenLastUsed(a.DB.Current(), c.TokenHash)
						return &auth.Claims{UserID: c.UserID, Username: c.Username, IsAdmin: c.IsAdmin}
					}
				}
			}
		}
	}
	return nil
}

// audit writes a row to the audit log.
func (a *App) audit(userID int64, username, action, detail string) {
	// 2026-07-11: Этап 9 part 2 — INSERT moved to db.AppendAuditLog
	// so the SQL string lives in queries.go. Audit failures remain
	// best-effort (the error is intentionally dropped) — a transient
	// DB hiccup must not break the main action (login, rule add, etc).
	// B224: a.DB is a *db.ResettableDB. AppendAuditLog takes
	// *sql.DB, so we call .Current() to get the live pool
	// (post-watcher-swap, the embedded *sql.DB is re-pointed;
	// the wrapper's promoted methods would also work but
	// AppendAuditLog's signature is fixed).
	_ = db.AppendAuditLog(a.DB.Current(), userID, username, action, detail)
}

// SanitizeFilename is a thin re-export of httputil.SanitizeFilename
// kept for backward compatibility — it was the original home of
// the helper (Phase B step 3b.5, 2026-07-29) and several
// non-feature-package files still import handlers.SanitizeFilename.
// The single source of truth now lives in internal/httputil/.
//
// refactor-v0.30 Phase D1 (2026-07-29): the implementation
// moved to internal/httputil/sanitize.go (eliminating 3
// near-identical copies in handlers.go + feature/admin +
// feature/my). This function delegates to it.
func SanitizeFilename(s string) string {
	return httputil.SanitizeFilename(s)
}

// ---------- FILE INDEX ----------
//
// handlers.go owns only shared infra: App struct, render helpers,
// currentUser, audit, getMaxRulesForUser.
//
// All per-feature handlers live in focused siblings:
//   - handlers_settings.go          — /settings/theme (theme switcher)
//   - handlers_help.go              — /help
//   - handlers_my_preauth.go        — POST /my/preauth
//   - handlers_my_exit_nodes.go     — GET  /my/exit-nodes
//   - handlers_my_keys.go           — /my/keys (list + expire)
//   - handlers_my_devices.go        — GET  /my/devices
//   - handlers_dashboard.go         — /dashboard + TailnetMetrics
//   - handlers_auth.go              — login / logout / lang
//   - handlers_my_account.go        — /my/account (password change)
//   - handlers_api_tokens.go        — /my/tokens (API tokens)
//   - handlers_admin_users.go       — /admin/users
//   - handlers_admin_nodes.go       — /admin/devices (tag/untag)
//   - handlers_admin_pages.go       — /admin/audit, /admin/acls
//   - handlers_derp.go              — /admin/derp + DERP types
//
// Node-ownership backfill now lives in internal/nodeownership/
// (Phase D2, 2026-07-29). The thin App wrapper
// `BackfillNodeOwnershipFn` in handlers_export.go is the
// only remaining bridge — feature/my uses it via the
// Service.BackfillNodeOwnership callback.
//
// See AGENTS.md "Sister files" for current line counts.
