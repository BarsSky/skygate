package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"skygate/internal/auth"
	"skygate/internal/backup"
	"skygate/internal/config"
	"skygate/internal/expirewatch"
	adminsvc "skygate/internal/feature/admin"
	authsvc "skygate/internal/feature/auth"
	exitrules "skygate/internal/feature/exit_rules"
	mysvc "skygate/internal/feature/my"
	"skygate/internal/feature/healthz"
	"skygate/internal/headscale_version"
	"skygate/internal/release"
	"skygate/internal/db"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/middleware"
	"skygate/internal/monitoring"
	"skygate/internal/sidecar"
	"skygate/internal/ratelimit"
	"skygate/internal/telegram"
	"skygate/internal/update"
)

// Build-time variables, overridden via -ldflags by entrypoint.sh:
//
//	go build -ldflags "\
//	    -X main.version=$(git describe --tags --always) \
//	    -X main.commit=$(git rev-parse --short HEAD) \
//	    -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// `version` is the only one shown to end-users (web footer + telegram
// /version). `commit` and `buildTime` are for /version and the startup
// log line. The defaults below are used when the binary is built
// without -ldflags (e.g. `go run` on a developer machine).
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

// redactPGPassword replaces the password in a postgres:// DSN
// with "***" for safe log output. Used by the startup banner
// when SKYGATE_DB_DSN is set (Phase 4.1, v0.32.22). The DSN
// format is "postgres://user:pass@host:port/db?params" — we
// only redact the user:pass segment.
func redactPGPassword(dsn string) string {
	const prefix = "://"
	prefixIdx := strings.Index(dsn, prefix)
	if prefixIdx < 0 {
		return dsn
	}
	rest := dsn[prefixIdx+len(prefix):]
	atIdx := strings.Index(rest, "@")
	if atIdx < 0 {
		return dsn // no user:pass@host segment
	}
	scheme := dsn[:prefixIdx+len(prefix)]
	creds := rest[:atIdx]
	host := rest[atIdx+1:]
	colonIdx := strings.Index(creds, ":")
	if colonIdx < 0 {
		return dsn // no password (e.g. trust auth)
	}
	return scheme + creds[:colonIdx+1] + "***@" + host
}

func main() {
	// 2026-07-14: Этап 14 v6 — subcommand routing.
	// The default (no args) starts the web server.
	// `skygate backup-run` is the system-cron entry point:
	// it reads the same config from the DB and runs the
	// backup. This is what scripts/backup_cron.sh
	// invokes. We keep the subcommand surface minimal
	// (only one for now) so we don't have to refactor the
	// rest of the boot path.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup-run":
			// Use a dedicated flag set so we don't
			// inherit the web-server flags.
			fs := flag.NewFlagSet("backup-run", flag.ExitOnError)
			if err := fs.Parse(os.Args[2:]); err != nil {
				log.Fatalf("backup-run: %v", err)
			}
			if err := runBackupSubcommand(); err != nil {
				fmt.Fprintf(os.Stderr, "backup-run failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "version", "--version", "-v":
			fmt.Printf("skygate %s (commit %s, built %s)\n", version, commit, buildTime)
			return
		case "help", "--help", "-h":
			fmt.Println("skygate <command> [args]")
			fmt.Println("  (no command)        start the web server")
			fmt.Println("  backup-run          run a backup using the config from the DB")
			fmt.Println("  version             print build version")
			fmt.Println("  help                this help")
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (try `skygate help`)\n", os.Args[1])
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("🌐 Skygate starting on :%s", cfg.Port)
	log.Printf("   Headscale URL: %s", cfg.HeadscaleURL)

	// 2026-08-03: v0.32.22 (Phase 4.1) — DB backend
	// selection. If SKYGATE_DB_DSN is set, OpenDSN picks
	// SQLite (default) or PostgreSQL based on the DSN
	// prefix. If DSN is empty, falls back to Open(DBPath)
	// for the historical SQLite-only path. Phase 4.2+
	// (HA setup on skygate-host-2) is when the PG path
	// becomes the default.
	var d *sql.DB
	if cfg.DBDSN != "" {
		log.Printf("   DB backend:    postgres (DSN=%s...)", redactPGPassword(cfg.DBDSN))
		d, err = db.OpenDSN(cfg.DBDSN)
	} else {
		log.Printf("   DB backend:    sqlite (%s)", cfg.DBPath)
		d, err = db.Open(cfg.DBPath)
	}
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer d.Close()

	// 2026-07-07: issue #6 — ensure parent_domain column exists for domain auto-updater
	if _, err := d.Exec("ALTER TABLE device_rules ADD COLUMN parent_domain TEXT DEFAULT ''"); err != nil {
		// column may already exist; log only if it's not a duplicate-column error
		if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "exists") {
			log.Printf("warn: ALTER device_rules add parent_domain: %v", err)
		}
	}

	// Bootstrap admin user
	if cfg.BootstrapAdminPass == "" {
		log.Printf("⚠️  SKYGATE_ADMIN_PASS empty - no admin user bootstrapped")
		log.Printf("    Set SKYGATE_ADMIN_PASS in env to create admin on first start")
	} else {
		if err := bootstrapAdmin(d, cfg.BootstrapAdminUser, cfg.BootstrapAdminPass); err != nil {
			log.Fatalf("bootstrap: %v", err)
		}
	}

	// Ensure headscale user for admin
	hs := headscale.New(cfg.HeadscaleURL, cfg.HeadscaleKey)
	if err := ensureHeadscaleUser(d, hs, cfg.BootstrapAdminUser); err != nil {
		log.Printf("warn: ensure headscale user: %v", err)
	}

	// Bootstrap Telegram credentials: copy from .env to DB once on
	// startup if no DB record exists. After that, the admin page at
	// /admin/telegram is the source of truth.
	if err := bootstrapTelegramFromEnv(d); err != nil {
		log.Printf("warn: bootstrap telegram: %v", err)
	}

	// Backfill node_owner_map: any headscale node with tag:public whose
	// original owner we don't know is attributed to the bootstrap admin.
	if err := backfillNodeOwners(d, hs, cfg.BootstrapAdminUser); err != nil {
		log.Printf("warn: backfill node owners: %v", err)
	}

	app := handlers.New(d, hs, cfg.HeadscaleKey, cfg.JWTSecret, cfg.ControlURL, cfg.SSHKeyPath, cfg.SessionHours, cfg)
	// 2026-07-27: v0.29.0 — initialize the auto-update
	// state store. Loads any persisted state from the
	// status file so a restart renders the most recent
	// in-flight / completed job (the operator can pick
	// up where they left off). The store is bound to
	// the bind-mounted /data volume so the file
	// survives a container recreate.
	//
	// refactor-v0.30 Phase B step 6c (2026-07-29):
	// the state store moved out of internal/handlers
	// (was a package-level singleton + InitUpdateStateStore
	// exported func). It's now constructed inline here
	// and wired into adminSvc.UpdateState below.
	updateStore := update.NewStateStore(cfg.UpdateStatePath)
	if _, err := updateStore.Load(); err != nil {
		// Load failure is non-fatal: the next apply
		// will overwrite the file with a fresh state.
		_ = err
	}
	// 2026-07-31: v0.32.13 — gate the DNS auto-updater goroutine
	// on AutoUpdateEnabled. Pre-fix the goroutine launched
	// unconditionally if cfg.DNSAutoCheck > 0, which fired
	// `DomainAutoUpdater()` synchronously at startup. That call
	// does a DB scan of all device_rules + staggeredSync of the
	// result to every exit node (366 rules across 1 node
	// `relay-3` in this VM's case), which on the live VM with
	// concurrent admin requests held the SQLite WAL write
	// lock for 30+ seconds and wedged every other query with
	// `context deadline exceeded`. The opt-in gate matches the
	// existing semantics: AutoUpdateEnabled is the operator's
	// switch for ALL background auto-update behaviour
	// (orchestrator, DNS check, everything), not just the
	// /admin/update button.
	_ = cfg.AutoUpdateEnabled // see below
	// 2026-07-15: v0.12.0 — wire SKYGATE_SECRET_KEY into the
	// per-user control plane router. Empty string means
	// "encryption not configured" — the router falls through
	// to the global client (no per-user planes are
	// honoured). Operators who want multi-control-plane
	// should generate a 32-byte key (openssl rand -hex 32)
	// and put it in .env.
	app.SecretKeyHex = cfg.SecretKeyHex
	// 2026-07-15: v0.10.12 — when HEADPLANE_EXTERNAL_URL is set,
	// /admin/acls (and a few other admin pages) link to the
	// existing Headplane instead of the local sidecar.
	app.HeadplaneExternalURL = cfg.HeadplaneExternalURL

		// 2026-07-10: rate limiting for /login (per-user + per-IP) and /api endpoints
		// (per-IP). In-memory token bucket; auto-cleans stale entries.
		app.RateLimiter = ratelimit.New()
		go func() {
			t := time.NewTicker(5 * time.Minute)
			defer t.Stop()
			for range t.C { app.RateLimiter.Sweep() }
		}()
		loginMW := middleware.RequireLoginLimit(app.RateLimiter)
		apiMW := middleware.RequireAPILimit(app.RateLimiter)
		_ = apiMW  // exposed for explicit endpoint wrapping (currently routes attach via authMW only)


	app.Version = version
	// v0.26.0 — set the BuildVersion once at boot, so
	// /healthz and /readyz can surface it. The format
	// mirrors what git tags + GitHub releases use, so a
	// probe response like "v0.26.0+4eed3a4" is
	// self-explanatory.
	app.BuildVersion = version + "+" + commit
	log.Printf("🌐 Skygate %s (commit %s, built %s)", version, commit, buildTime)

	mux := http.NewServeMux()

	// Public
	//
	// refactor-v0.30 Phase B step 2 (2026-07-29): /login, /logout,
	// and /lang moved from internal/handlers/handlers_auth.go to
	// internal/feature/auth/. The Service takes its dependencies
	// (DB, I18n, JWTSecret, SessionHours, Version) as plain fields
	// + a Backend interface that *App satisfies via the capital-letter
	// wrappers in internal/handlers/handlers_export.go.
	authSvc := &authsvc.Service{
		Backend:      app,
		DB:           app.DB,
		I18n:         app.I18n,
		JWTSecret:    app.JWTSecret,
		SessionHours: app.SessionHours,
		Version:      app.Version,
	}
	mux.HandleFunc("GET /login", authSvc.GetLogin)
	mux.HandleFunc("POST /lang", authSvc.PostLang)
	mux.Handle("POST /login", loginMW(http.HandlerFunc(authSvc.PostLogin)))
	mux.HandleFunc("POST /logout", authSvc.PostLogout)
	mux.HandleFunc("/favicon.ico", app.FaviconHandler)
	// v0.26.0 — liveness + readiness probes (HA-ready).
	// Both are UNAUTHENTICATED. /healthz is always 200
	// if the process is alive (K8s livenessProbe pattern).
	// /readyz pings the DB and headscale, returns 503
	// if either is down (K8s readinessProbe pattern).
	//
	// refactor-v0.30 Phase B step 1 (2026-07-29): handlers
	// moved from internal/handlers/handlers_healthz.go to
	// internal/feature/healthz/. The Service takes its
	// dependencies as plain fields (DB, HeadscaleFn, etc.)
	// instead of methods on *App, so this package has no
	// import dependency on internal/handlers/.
	//
	// HeadscaleFn is a func() headscale.Pingable, not
	// func() *headscale.Client — we re-read the active
	// client on every probe so a v0.12.0+ per-user headscale
	// swap doesn't leave the readiness probe stuck on a
	// stale client. The closure below adapts the
	// method-value app.HSGlobal to the Pingable signature.
	healthzSvc := &healthz.Service{
		DB: app.DB,
		HeadscaleFn: func() headscale.Pingable {
			return app.HSGlobalFn() // *headscale.Client satisfies Pingable
		},
		// (Phase D4, 2026-07-29: was app.HSGlobal() — the
		// *App.HSGlobal method was deleted; the wrapper
		// HSGlobalFn now routes directly to a.Router.Global().)
		InstanceID:   app.InstanceID,
		BuildVersion: app.BuildVersion,
		StartedAt:    app.StartedAt,
	}
	mux.HandleFunc("GET /healthz", healthzSvc.GetHealthz)
	mux.HandleFunc("GET /readyz", healthzSvc.GetReadyz)
	mux.HandleFunc("/favicon.svg", app.FaviconHandler)
	mux.HandleFunc("/static/", app.StaticHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})

	// Settings (theme switcher) - accessible to all.
	// The actual handler is wired below (after mySvc is
	// declared) — refactor-v0.30 Phase B step 6e
	// (2026-07-29) moved the handler to
	// internal/feature/my/settings.go.
	// mux.HandleFunc("GET /settings/theme", mySvc.PostSettingsTheme)
	// mux.HandleFunc("POST /settings/theme", mySvc.PostSettingsTheme)

	// Authenticated
	authMW := middleware.RequireAuth(cfg.JWTSecret)
	// refactor-v0.30 Phase B step 6e (2026-07-29):
	// /help moved to feature/auth/help.go
	// (authSvc.GetHelp).
	mux.Handle("GET /help", authMW(http.HandlerFunc(authSvc.GetHelp)))

	// User self-service
	// (the rest of /my/* routes are below, after mySvc
	// is constructed — /my/devices + /my/exit-nodes +
	// /my/preauth + /my/keys + /my/meshes + per-device
	// preferred-exit + /my/account/audit all route
	// through mySvc)
	// 2026-07-25: v0.28.4 — per-device preferred exit-node.
	// The user can pin a specific device (e.g. their
	// Android phone) to a different exit-node than the
	// per-user default. The form posts hostname + tag;
	// the handler resolves the hostname to the user's
	// device and stores the pref in device_exit_node_prefs.
	// 2026-07-29: refactor-v0.30 Phase B step 5d — moved
	// to feature/my (route registered after mySvc
	// construction, see below).
	// mux.Handle("POST /my/devices/preferred-exit", ...) — see below
	// 2026-07-29: refactor-v0.30 Phase B step 5c —
	// /my/meshes + 3 POST endpoints moved to feature/my.
	// (Routes re-pointed after mySvc construction, see
	// the block immediately above the Admin section.)
	// workflow). The bot /mesh create|join|leave
	// commands are the BOT entry point; both
	// share the same internal/mesh package.
	// Three POST routes: create, join, leave —
	// refactor-v0.30 Phase B step 5c: now via mySvc
	// (routes registered after mySvc construction, see
	// below the /my/devices entry).

	// Admin — small handlers (users, devices, subnets, invites,
	// meshes, headscale-update-monitor, acls import/export) moved
	// from internal/handlers/handlers_admin_*.go + admin_*.go
	// into internal/feature/admin/ in refactor-v0.30 Phase B
	// step 3a (2026-07-29). Larger admin handlers (control
	// planes, user_subnet, telegram, integrations, exit-nodes,
	// backup, headplane, derp, settings, update) are still in
	// internal/handlers/ and will be moved in Phase B step 3b.
	adminSvc := &adminsvc.Service{
		Backend:                app,
		DB:                     app.DB,
		HSGlobalFn:             app.HSGlobalFn,
		HSForUserFn:            app.HSForUserFn,
		Cfg:                    app.Config(),
		Notifier:               app.Notifier,
		HeadscaleUpdateMonitor: app.HeadscaleUpdateMonitor,
		Sidecar:                app.Sidecar,
		I18n:                   app.I18n,
		// refactor-v0.30 Phase B step 3b.4 (2026-07-29):
		// per-user control plane admin (set/clear/provision/
		// decommission) encrypts the API key with this
		// secret + invalidates the per-URL HSForUser cache.
		SecretKeyHex:           app.SecretKeyHex,
		InvalidateHSCacheFn:    app.InvalidateHSCache,
		// refactor-v0.30 Phase B step 3b.3 (2026-07-29):
		// /admin/exit-nodes needs the default SSH key path
		// (shown as the "ssh_key_path" form default) + a
		// callback to the legacy SyncAdvertisedRoutes
		// (the "Sync now" button). The exit-node health
		// monitor is wired further below once exitMon is
		// created (it doesn't exist yet at this point).
		SSHKeyPath: app.SSHKeyPath,
		SyncRoutes: app.SyncAdvertisedRoutes,
		// refactor-v0.30 Phase B step 3b.6 (2026-07-29):
		// /admin/settings renders ControlURL (URL field
		// + PublicDomain) + masked JWTSecret and
		// HeadscaleKey. Settings stays in feature/admin
		// because it's a single admin page; moving it
		// out would just create a per-feature dependency
		// for a one-file use site.
		ControlURL:   app.ControlURL,
		JWTSecret:    app.JWTSecret,
		HeadscaleKey: app.HeadscaleKey,
		// refactor-v0.30 Phase B step 6a (2026-07-29):
		// /admin/derp's collectDerpStatus seeds its initial
		// DerpStatus with this URL (defaults to the hardcoded
		// fallback when empty). Wired once at boot from
		// app.DerpBaseURL.
		DerpBaseURL: app.DerpBaseURL,
		// refactor-v0.30 Phase B step 6b (2026-07-29):
		// /admin/acls links to this URL (when non-empty)
		// instead of the bundled Headplane sidecar.
		HeadplaneExternalURL: app.HeadplaneExternalURL,
		// refactor-v0.30 Phase B step 6c (2026-07-29):
		// the self-update admin page + orchestrator
		// goroutine both hold a long-lived reference to
		// the state store. Constructed above (was a
		// package-level singleton in internal/handlers).
		UpdateState: updateStore,
		// refactor-v0.30 Phase B step 6c (2026-07-29):
		// /admin/update renders the current build
		// version in the page header and uses it as
		// the Checker's CurrentVersion.
		BuildVersion: app.BuildVersion,
	}
	// refactor-v0.30 Phase B step 3b.1a (2026-07-29): wire
	// the adminSvc into *App so the existing thin wrappers
	// (app.AdminTelegram, app.AdminTelegramPost) route through
	// it. The wrappers exist for the test surface only — new
	// routes go directly to adminSvc via mux.HandleFunc below.
	app.SetAdminService(adminSvc)

	// refactor-v0.30 Phase B step 4 (2026-07-29): exit_rules
	// feature service. Owns /my/exit-rules + the /admin/exit-rules
	// sub-actions, the REST API, the advertised-routes sync,
	// the DNS autoupdater, and the route-setup script generator.
	// The Service holds the shared headscale client + DB + cfg
	// + i18n; the *App.SyncAdvertisedRoutes / RunDomainAutoUpdater
	// wrappers route through it via exitRulesRunner (see
	// handlers.go SetExitRulesService).
	exitRulesSvc := &exitrules.Service{
		Backend:  app,
		DB:       app.DB,
		HS:       app.HS,
		Cfg:      app.Config(),
		I18n:     app.I18n,
		Notifier: app.Notifier,
		// refactor-v0.30 Phase B step 4e (2026-07-29):
		// per-plane ACL reapply handler needs a
		// per-plane headscale client resolver. The
		// default behaviour (mirrors the legacy
		// App-method closure in form_reapply.go) is:
		// empty planeURL → app.HSGlobal(); non-empty
		// → first user_id with that headscale_url →
		// app.HSForUser(uid); fallback HSGlobal() on
		// any DB error.
		ResolveHSForPlane: func(planeURL string) *headscale.Client {
			if planeURL == "" {
				return app.HSGlobalFn()
			}
			rows, err := app.DB.Query("SELECT id FROM portal_users WHERE headscale_url = ? LIMIT 1", planeURL)
			if err != nil {
				return app.HSGlobalFn()
			}
			defer rows.Close()
			if !rows.Next() {
				return app.HSGlobalFn()
			}
			var uid int64
			if err := rows.Scan(&uid); err != nil {
				return app.HSGlobalFn()
			}
			return app.HSForUserFn(uid)
			// (Phase D4, 2026-07-29: was app.HSGlobal() /
			// app.HSForUser(uid) — the *App methods were
			// deleted; the wrapper Fn methods now route
			// directly to a.Router.Global() / .ForUser(uid).)
		},
	}
	// adminSvc.SyncRoutes is the "Sync now" button on
	// /admin/exit-nodes. Re-point it to the new Service
	// implementation (was: app.SyncAdvertisedRoutes; same
	// behaviour because app.SyncAdvertisedRoutes wraps the
	// Service, but this removes the indirect call).
	adminSvc.SyncRoutes = exitRulesSvc.SyncAdvertisedRoutes
	app.SetExitRulesService(exitRulesSvc)

	// refactor-v0.30 Phase B step 5 (2026-07-29):
	// /my/* feature service. The /my/account, /my/tokens
	// and /my/telegram routes already live in feature/auth
	// (step 2); this Service owns the remaining
	// self-service pages (devices, exit-nodes, preauth,
	// keys, audit, per-device exit pref). Step 5a wires
	// preauth + exit-nodes + keys; step 5b/5c/5d follow
	// with devices / meshes / audit / device_exit_pref.
	mySvc := &mysvc.Service{
		Backend: app,
		DB:      app.DB,
		HS:      app.HS,
		Cfg:     app.Config(),
		I18n:    app.I18n,
		// refactor-v0.30 Phase B step 5 — Notifier not
		// used by the 3 handlers in 5a (none of them
		// sends an operator alert). Wired for the
		// upcoming 5b/5c/5d handlers that do (the
		// device-pref + per-plane-ACL flow).
		Notifier: app.Notifier,
		// refactor-v0.30 Phase B step 5b — the
		// /my/devices backfill is a 250-line helper
		// that lives in handlers_node_ownership.go
		// and is also used by /admin/devices. Wire
		// it as a callback so the feature package
		// doesn't carry a copy.
		BackfillNodeOwnership: app.BackfillNodeOwnershipFn,
	}
	// 2026-07-29: refactor-v0.30 Phase B step 6d —
	// /dashboard is a user-facing page, lives in feature/my
	// next to the other /my/* routes. Registered here
	// (after mySvc construction) so the closure can reference
	// the mySvc variable.
	mux.Handle("GET /dashboard", authMW(http.HandlerFunc(mySvc.GetDashboard)))
	// refactor-v0.30 Phase B step 6e (2026-07-29):
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

	// Admin
	mux.Handle("GET /admin/users", authMW(http.HandlerFunc(adminSvc.GetAdminUsers)))
	mux.Handle("POST /admin/users", authMW(http.HandlerFunc(adminSvc.PostAdminUser)))
	mux.Handle("POST /admin/users/{id}/delete", authMW(http.HandlerFunc(adminSvc.PostAdminDeleteUser)))
	mux.Handle("POST /admin/users/{id}/reset-password", authMW(http.HandlerFunc(adminSvc.PostAdminUserResetPassword)))
	// 2026-07-15: v0.12.0 — per-user headscale control plane
	// (multi-tailnet). /admin/control-planes is the landing;
	// /admin/users/{id}/plane is the per-user edit form.
	mux.Handle("GET /admin/control-planes", authMW(http.HandlerFunc(adminSvc.GetAdminControlPlanes)))
	mux.Handle("POST /admin/control-planes/test", authMW(http.HandlerFunc(adminSvc.PostAdminControlPlanesTest)))
	mux.Handle("GET /admin/users/{id}/plane", authMW(http.HandlerFunc(adminSvc.GetAdminUserControlPlane)))
	mux.Handle("POST /admin/users/{id}/plane", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlane)))
	mux.Handle("POST /admin/users/{id}/plane/clear", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlaneClear)))
	// 2026-07-21: v0.23.0 Phase 1 — one-click provisioning of a
	// per-user headscale container. The Provision action runs the
	// bootstrap script (creates container + user + API key, returns
	// JSON) and persists the result to portal_users. The
	// Decommission action reverses it: tears down the container and
	// clears the DB row (data on disk is preserved for recovery).
	mux.Handle("POST /admin/users/{id}/plane/provision", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlaneProvision)))
	mux.Handle("POST /admin/users/{id}/plane/decommission", authMW(http.HandlerFunc(adminSvc.PostAdminUserControlPlaneDecommission)))
	// 2026-07-17: v0.16.0 — per-user subnets admin page.
	// GET shows the user's subnet status; POSTs allocate
	// / disable / run a sanity check.
	mux.Handle("GET /admin/users/{id}/subnet", authMW(http.HandlerFunc(adminSvc.GetAdminUserSubnet)))
	mux.Handle("POST /admin/users/{id}/subnet/allocate", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetAllocate)))
	mux.Handle("POST /admin/users/{id}/subnet/disable", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetDisable)))
	// 2026-08-03: v0.32.18 — full subnet-router lifecycle
	// (Provision creates, Remove destroys). Disables the
	// headscale node and clears all related state in one
	// atomic flow; the older Disable button is the softer
	// "opt out without losing the row" option.
	mux.Handle("POST /admin/users/{id}/subnet/remove", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetRemove)))
	mux.Handle("POST /admin/users/{id}/subnet/test", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetTest)))
	mux.Handle("POST /admin/users/{id}/subnet/provision", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetProvision)))
	// v0.24.2: download a self-contained tar.gz bundle
	// (setup.sh + README.md + commands.txt with the
	// preauth key + CIDR.txt) for the user to scp to
	// their router host and untar. Issues a fresh
	// preauth on each call (same as the "Issue preauth
	// key" button).
	mux.Handle("GET /admin/users/{id}/subnet/download", authMW(http.HandlerFunc(adminSvc.GetAdminUserSubnetDownload)))
	mux.Handle("POST /admin/users/{id}/subnet/share", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetShare)))
	mux.Handle("POST /admin/users/{id}/subnet/revoke", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetRevoke)))
	// 2026-07-24: v0.28.1 — admin override for the
	// user's preferred exit-node (operator-driven
	// exit-node assignment).
	mux.Handle("POST /admin/users/{id}/subnet/preferred-exit", authMW(http.HandlerFunc(adminSvc.PostAdminUserSubnetPreferredExit)))
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
	mux.Handle("GET /admin/subnets", authMW(http.HandlerFunc(adminSvc.GetAdminSubnets)))
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
	mux.Handle("GET /admin/derp", authMW(http.HandlerFunc(adminSvc.GetAdminDERP)))
	// 2026-07-15: Этап 14 v14 (v0.11.0) — runtime-editable
	// integration config. The /admin/integrations landing page
	// shows the current state of every pluggable component;
	// /admin/derp/config and /admin/headplane are the per-component
	// edit forms. The save handlers persist to global_settings;
	// v0.11.1 will add a runtime renderer (re-apply headscale
	// config + restart) so the user doesn't have to run
	// ./deploy/deploy.sh after a save.
	mux.Handle("GET /admin/integrations", authMW(http.HandlerFunc(adminSvc.GetAdminIntegrations)))
	mux.Handle("GET /admin/derp/config", authMW(http.HandlerFunc(adminSvc.GetAdminDerpConfig)))
	mux.Handle("POST /admin/derp/config", authMW(http.HandlerFunc(adminSvc.PostAdminDerpConfig)))
	mux.Handle("GET /admin/headplane", authMW(http.HandlerFunc(adminSvc.GetAdminHeadplane)))
	mux.Handle("POST /admin/headplane", authMW(http.HandlerFunc(adminSvc.PostAdminHeadplane)))
	mux.Handle("GET /admin/backup", authMW(http.HandlerFunc(adminSvc.GetAdminBackup)))
	mux.Handle("POST /admin/backup/save", authMW(http.HandlerFunc(adminSvc.PostAdminBackupSave)))
	mux.Handle("POST /admin/backup/restore", authMW(http.HandlerFunc(adminSvc.PostAdminBackupRestore)))
	mux.Handle("GET /admin/backup/download", authMW(http.HandlerFunc(adminSvc.GetAdminBackupDownload)))
	// 2026-07-14: Этап 14 v6 — destination & schedule config.
	// /admin/backup itself serves the form; the four action
	// endpoints accept POSTs from the form buttons. No CSRF
	// (admin-only; the legacy /admin/backup/save also has
	// none).
	mux.Handle("GET /admin/backup/config", authMW(http.HandlerFunc(adminSvc.GetAdminBackupConfig)))
	mux.Handle("POST /admin/backup/config", authMW(http.HandlerFunc(adminSvc.PostAdminBackupConfig)))
	mux.Handle("POST /admin/backup/test", authMW(http.HandlerFunc(adminSvc.PostAdminBackupTest)))
	mux.Handle("POST /admin/backup/run", authMW(http.HandlerFunc(adminSvc.PostAdminBackupRun)))
	mux.Handle("POST /admin/backup/toggle", authMW(http.HandlerFunc(adminSvc.PostAdminBackupToggle)))
	mux.Handle("GET /admin/settings", authMW(http.HandlerFunc(adminSvc.GetAdminSettings)))
	mux.Handle("GET /admin/telegram", authMW(http.HandlerFunc(adminSvc.AdminTelegram)))
	mux.Handle("POST /admin/telegram", authMW(http.HandlerFunc(adminSvc.AdminTelegramPost)))
	mux.Handle("GET /my/tokens", authMW(http.HandlerFunc(authSvc.GetMyTokens)))
	mux.Handle("POST /my/token", authMW(http.HandlerFunc(authSvc.PostMyToken)))
	mux.Handle("POST /my/token/{id}/revoke", authMW(http.HandlerFunc(authSvc.PostMyTokenRevoke)))
	mux.Handle("GET /my/account", authMW(http.HandlerFunc(authSvc.GetMyAccount)))
	mux.Handle("POST /my/account/password", authMW(http.HandlerFunc(authSvc.PostMyAccountPassword)))
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
	mux.Handle("GET /my/exit-rules", authMW(http.HandlerFunc(exitRulesSvc.GetMyExitRules)))
	mux.Handle("POST /my/exit-rules", authMW(apiMW(http.HandlerFunc(exitRulesSvc.PostMyExitRule))))
	mux.Handle("POST /my/exit-rules/delete", authMW(http.HandlerFunc(exitRulesSvc.PostDeleteExitRule)))
	mux.Handle("GET /my/exit-rules/api", authMW(apiMW(http.HandlerFunc(exitRulesSvc.GetExitRulesAPI))))
	mux.Handle("POST /my/exit-rules/api", authMW(apiMW(http.HandlerFunc(exitRulesSvc.PostExitRulesAPI))))
	mux.Handle("GET /my/exit-rules/help", authMW(http.HandlerFunc(exitRulesSvc.GetExitRulesAPIHelp)))
	mux.Handle("GET /admin/exit-rules", authMW(http.HandlerFunc(exitRulesSvc.AdminExitRules)))
	mux.Handle("POST /admin/exit-rules/rollback", authMW(http.HandlerFunc(exitRulesSvc.PostAdminRollbackACL)))
	// 2026-07-14: Этап 14 v7 — re-apply ACL without
	// touching rules. Use when GenerateACL() output
	// changed (e.g. new SSH rule) but no exit-rule
	// add/delete has fired SetPolicy yet.
	mux.Handle("POST /admin/exit-rules/reapply", authMW(http.HandlerFunc(exitRulesSvc.PostAdminACLReapply)))
	mux.Handle("GET /admin/exit-rules/sync", authMW(http.HandlerFunc(exitRulesSvc.PostSyncAdvertisedRoutes)))
	mux.Handle("GET /admin/exit-rules/nodes", authMW(http.HandlerFunc(exitRulesSvc.GetAdminNodesLoad)))
	mux.Handle("GET /admin/exit-rules/cleanup", authMW(http.HandlerFunc(exitRulesSvc.AdminCleanupRules)))
	mux.Handle("POST /admin/exit-rules/cleanup/apply", authMW(http.HandlerFunc(exitRulesSvc.AdminCleanupRulesApply)))
	mux.Handle("POST /admin/settings", authMW(http.HandlerFunc(adminSvc.PostAdminSettings)))
	mux.Handle("GET /admin/derp/refresh", authMW(http.HandlerFunc(adminSvc.GetAdminDERPRefresh)))
	mux.Handle("GET /admin/exit-nodes", authMW(http.HandlerFunc(adminSvc.AdminExitNodes)))
	// 2026-07-20: v0.20.0 — headscale-update-monitor
	// status page. Renders the monitor's snapshot
	// (pinned vs. latest, history table). Admin-only.
	mux.Handle("GET /admin/headscale", authMW(http.HandlerFunc(adminSvc.GetAdminHeadscale)))
	// 2026-07-27: v0.29.0 — self-update page. Shows
	// current vs latest GitHub release + copy-pasteable
	// manual steps for the detected install kind. The
	// "Check now" button forces an immediate GitHub
	// poll (bypasses the 6h success / 15m failure cache).
	mux.Handle("GET /admin/update", authMW(http.HandlerFunc(adminSvc.GetAdminUpdate)))
	mux.Handle("POST /admin/update/check-now", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateCheck)))
	// 2026-07-27: v0.29.0 — auto-update apply/rollback/dismiss.
	// The "Apply" button kicks off a background updater
	// goroutine that runs git pull + docker compose build +
	// container recreate + healthz poll, with automatic
	// rollback on any failure. "Rollback now" is the
	// operator-initiated escape hatch. "Dismiss" clears
	// the persisted status file when the operator has
	// read the success/failure banner.
	mux.Handle("POST /admin/update/apply", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateApply)))
	mux.Handle("POST /admin/update/rollback", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateRollback)))
	mux.Handle("POST /admin/update/dismiss", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateDismiss)))
	// 2026-07-30: v0.32.3 — manual "Push update" trigger
	// that works regardless of SKYGATE_AUTO_UPDATE_ENABLED.
	// For when the operator wants to force a rebuild +
	// restart right now, without waiting for a newer
	// release to be detected.
	mux.Handle("POST /admin/update/push", authMW(http.HandlerFunc(adminSvc.PostAdminUpdatePush)))
	// 2026-08-03: v0.32.20 — UI toggle for auto-update. The
	// operator flips the auto-update mode on /admin/update
	// without editing .env or restarting skygate. Persisted in
	// global_settings (key='auto_update_enabled'). The
	// orchestrator below reads this same DB value at every
	// tick (5s), so the change takes effect without a restart.
	mux.Handle("POST /admin/update/auto-toggle", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateAutoToggle)))
	// 2026-07-20: v0.20.0 — "Run check now" button on
	// /admin/headscale. Forces the monitor to re-poll
	// GitHub immediately. Same pattern as
	// /admin/exit-nodes/health-now.
	mux.Handle("POST /admin/headscale/check-now", authMW(http.HandlerFunc(adminSvc.PostAdminHeadscaleCheckNow)))
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
	mux.Handle("POST /admin/exit-nodes/add", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesAdd)))
	mux.Handle("POST /admin/exit-nodes/delete", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesDelete)))
	mux.Handle("POST /admin/exit-nodes/sync", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesSync)))
	// 2026-07-15: v0.13.0 — "Run health check now" button on
	// /admin/exit-nodes. Admin-only. Triggers the background
	// monitor's CheckNow synchronously and redirects back to
	// the page so the operator sees the fresh state. The
	// monitor's own internal mutex serialises concurrent
	// clicks.
	mux.Handle("POST /admin/exit-nodes/health-now", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesHealthNow)))

	// 2026-07-17: v0.18.1 — "Tag as exit-node" / "Untag" buttons.
	// These replace the operator's two manual `docker exec
	// headscale headscale nodes …` calls with a single click.
	// The handler approves 0.0.0.0/0 + ::/0 (NOT the full
	// availableRoutes, to avoid accidentally approving
	// relay-3's 200+ subnets) and applies tag:exit-node.
	// The existing ACL (`* → tag:exit-node:*`) already allows
	// the tagged node, so no ACL re-push is required — the
	// Tailscale clients pick up the new tag on their next
	// ACL poll (usually <60s).
	mux.Handle("POST /admin/exit-nodes/tag-as-exit", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeTagAsExitNode)))
	mux.Handle("POST /admin/exit-nodes/untag", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeUntagAsExitNode)))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	// 2026-07-17: v0.16.7 — per-user subnet sidecar
	// auto-approver. Hoisted before the RealNotifier block
	// so we can hand the same manager to the bot via
	// rn.SetSidecar() below.
	sidecarMgr := sidecar.New(d, app.HSForUserFn, log.Default(), cfg.SidecarSyncPeriod)
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
	expireWatchMgr := expirewatch.New(d, hs, log.Default(), cfg.ExpireWatchInterval)
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
		rn := telegram.NewRealNotifier(d)
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
			app.Notifier = rn
			// 2026-07-13: split the startup message by what's
			// actually configured. The polling gate in Run()
			// uses Configured() which is now token-only, so the
			// bot can start receiving /login as soon as the
			// admin saves the token (chat_id is needed only
			// for outgoing notifications, not for receiving
			// commands).
			if _, _, ok, _ := db.LoadTelegramSendTarget(d); ok {
				log.Printf("🤖 Telegram bot fully configured (token + chat_id); starting getUpdates loop")
			} else if _, _, ok, _ := db.LoadTelegramToken(d); ok {
				log.Printf("🤖 Telegram bot token set (no chat_id yet — receive-only); starting getUpdates loop. Use the 'Send test' button on /admin/telegram to populate chat_id.")
			} else {
				log.Printf("🤖 Telegram bot not configured; hot-swap armed (will re-check DB on every send/poll)")
			}
			go rn.Run(ctx)
			// 2026-07-15: Этап 14 v13 — register the per-language
			// command menu. Best-effort: a Telegram-side failure
			// is logged inside SetMyCommandsAll and the bot
			// keeps running without a menu. The user can still
			// type commands from memory; the menu is a
			// convenience, not a gate.
			go func() {
				if err := rn.SetMyCommandsAll(context.Background(), telegram.DefaultMyCommandsSpec); err != nil {
					log.Printf("🤖 setMyCommandsAll: %v", err)
				}
			}()
	defer stop()

	go func() {
		log.Printf("🌐 ready at http://localhost:%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	// 2026-07-07: issue #6 — start domain auto-updater goroutine.
	// 2026-07-31: v0.32.13 — gated on cfg.AutoUpdateEnabled
	// (env SKYGATE_AUTO_UPDATE_ENABLED). Pre-fix the goroutine
	// launched unconditionally and its synchronous initial
	// `DomainAutoUpdater()` call held the SQLite WAL write
	// lock for 30+ seconds, which wedged every other query
	// with `context deadline exceeded` (the same root cause
	// as the 504-on-https postmortem — see RELEASE-NOTES.md
	// v0.32.13). The /admin/update button still works for
	// manual deploys regardless of this gate.
	if cfg.AutoUpdateEnabled {
		go app.RunDomainAutoUpdater(ctx, cfg.DNSAutoCheck)
	} else {
		log.Printf("autoupdater: SKYGATE_AUTO_UPDATE_ENABLED=false, skipping startup goroutine")
	}

	// 2026-07-14: Этап 14 v6 — in-app backup scheduler. Started
	// after the DB is wired so Load() can read the config.
	// Wire the config loader first so Unmount (called by
	// RunBackup on its way out) can re-read the mountpoint.
	backup.SetConfigLoader(func() (*backup.Config, error) {
		return backup.Load(d)
	})
	backupSched := &backup.Scheduler{DB: d}
	backupSched.Start(ctx)

	// 2026-07-14: Этап 14 v8 — release-monitor goroutine.
	// Polls GitHub Releases once an hour and emits a
	// Notifier.SendAlert when a newer version is available.
	// Independent of system cron / external tooling — the
	// bot carries the message to admin and the operator
	// decides when to upgrade (see AGENTS.md "Updating").
	releaseMon := &release.Monitor{
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		Current:   version,
		Notified:  make(map[string]bool),
		Notifier:  app.Notifier,
		CheckEvery: 1 * time.Hour,
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
		AutoSync:     cfg.ExitNodeAutoSync,
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
	if cfg.HeadscalePollInterval > 0 {
		hsMon := headscale_version.NewMonitor(d, cfg.HeadscaleVersionPin, app.Notifier)
		hsMon.CheckEvery = cfg.HeadscalePollInterval
		hsMon.Start(ctx)
		app.HeadscaleUpdateMonitor = hsMon
		// 2026-07-20: v0.20.0 — also wire the
		// monitor to the bot so /headscale renders
		// the same status the /admin/headscale
		// page does. rn was hoisted out of the
		// Telegram block above so this call is
		// in scope.
		rn.SetHeadscaleUpdateMonitor(hsMon)
		log.Printf("📡 headscale-update-monitor: polling every %s, pinned=%q (set SKYGATE_HEADSCALE_VERSION_PIN to enable alerts)",
			cfg.HeadscalePollInterval, cfg.HeadscaleVersionPin)
	} else {
		log.Printf("📡 headscale-update-monitor: disabled (SKYGATE_HEADSCALE_POLL_INTERVAL=0). /admin/headscale still works as a manual look-up.")
	}

	// 2026-07-17: v0.16.7 — per-user subnet sidecar
	// auto-approver moved earlier so the RealNotifier
	// can pick up the same manager via SetSidecar().
	// (See "Telegram bot" block above.)

	<-ctx.Done()
	log.Println("🌐 shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
}

// runBackupSubcommand is the entry point for
// `skygate backup-run`. It loads the config from the DB
// (no flags, no env vars — the UI is the source of
// truth) and calls backup.RunBackup. Exit code is 0 on
// success, 1 on any error so a system cron will
// silently swallow the failure (cron emails by default
// but we want it visible in /var/log/syslog too).
func runBackupSubcommand() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer d.Close()
	// Wire the loader (same as the web server does) so
	// the runner's Unmount path can re-read the
	// mountpoint.
	backup.SetConfigLoader(func() (*backup.Config, error) {
		return backup.Load(d)
	})
	bc, err := backup.Load(d)
	if err != nil {
		return fmt.Errorf("load backup config: %w", err)
	}
	if !bc.Enabled {
		log.Printf("backup-run: backup.enabled = false in DB; skipping (return 0 so cron doesn't alert)")
		return nil
	}
	log.Printf("backup-run: starting (protocol=%s, destination=%s, keep=%d)", bc.Protocol, bc.Destination, bc.KeepCount)
	res, err := backup.RunBackup(d, bc)
	if err != nil {
		if res != nil {
			log.Printf("backup-run: status=%s error=%s archive=%s", res.Status, res.Error, res.Archive)
		} else {
			log.Printf("backup-run: error: %v", err)
		}
		return err
	}
	log.Printf("backup-run: ok archive=%s bytes=%d dur=%s", res.Archive, res.Bytes, res.FinishedAt.Sub(res.StartedAt))
	return nil
}

// bootstrapAdmin creates the admin user in Skygate DB on first start.
func bootstrapAdmin(d *sql.DB, username, password string) error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=?", username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		log.Printf("   bootstrap: user %q already exists, skipping", username)
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT INTO portal_users(username, password_hash, is_admin) VALUES(?,?,?)`,
		username, hash, 1)
	if err != nil {
		return err
	}
	log.Printf("✅ bootstrap admin created: %q", username)
	return nil
}

func backfillNodeOwners(d *sql.DB, hs *headscale.Client, adminName string) error {
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return err
	}
	var adminID sql.NullInt64
	var adminHSID sql.NullInt64
	if err := d.QueryRow(`SELECT id, headscale_user_id FROM portal_users WHERE username=? AND is_admin=1`, adminName).
		Scan(&adminID, &adminHSID); err != nil {
		return err
	}
	if !adminID.Valid || !adminHSID.Valid {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, n := range nodes {
		isPublic := false
		for _, t := range n.Tags {
			if t == "tag:public" {
				isPublic = true
				break
			}
		}
		if !isPublic {
			continue
		}
		if n.UserName != "tagged-devices" {
			continue
		}
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.InsertIgnoreNodeOwner.
		if err := db.InsertIgnoreNodeOwner(tx, n.ID, adminHSID.Int64, adminName, "tag:public", adminID.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureHeadscaleUser(d *sql.DB, hs *headscale.Client, username string) error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=? AND headscale_user_id IS NOT NULL", username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	existing, _ := hs.ListUsers()
	for _, u := range existing {
		if u.Name == username {
			_, err := d.Exec("UPDATE portal_users SET headscale_user_id=? WHERE username=?", u.ID, username)
			return err
		}
	}
	created, err := hs.CreateUser(username)
	if err != nil {
		return err
	}
	_, err = d.Exec("UPDATE portal_users SET headscale_user_id=? WHERE username=?", created.ID, username)
	return err
}

// bootstrapTelegramFromEnv copies the Telegram bot token and chat id
// from .env into the global_settings table the first time the app
// starts. After that, /admin/telegram is the canonical source — the
// admin page can rotate / disable the bot without touching .env.
func bootstrapTelegramFromEnv(d *sql.DB) error {
	_, _, ok, err := db.LoadTelegramToken(d)
	if err != nil {
		return err
	}
	if ok {
		return nil // already configured via UI
	}
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	chat := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if token == "" && chat == "" {
		return nil
	}
	return db.SaveTelegramToken(d, token, chat)
}
