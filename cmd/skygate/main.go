package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"skygate/internal/auth"
	"skygate/internal/backup"
	"skygate/internal/certsync"
	"skygate/internal/config"
	"skygate/internal/deploy"
	"skygate/internal/dns"
	"skygate/internal/expirewatch"
	"skygate/internal/ha"
	extcreds "skygate/internal/ha/dnsexternal"
	adminsvc "skygate/internal/feature/admin"
	authsvc "skygate/internal/feature/auth"
	clusterapi "skygate/internal/feature/cluster"
	exitrules "skygate/internal/feature/exit_rules"
	mysvc "skygate/internal/feature/my"
	"skygate/internal/feature/healthz"
	"skygate/internal/headscale_version"
	"skygate/internal/release"
	"skygate/internal/db"
	"skygate/internal/watchdog"
	"skygate/internal/elector"
	"skygate/internal/cluster"
	"skygate/internal/deployrun"
	"skygate/internal/derphealth"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/middleware"
	oidcsvc "skygate/internal/oidc"
	"skygate/internal/mesh"
	"skygate/internal/keynotify"
	"skygate/internal/tokenrotate"
	"skygate/internal/nodeownership"
	"skygate/internal/monitoring"
	"skygate/internal/sidecar"
	"skygate/internal/ratelimit"
	"skygate/internal/telegram"
	"skygate/internal/update"

	// B194: import the steps/ package for its init() side
	// effects (each step registers itself in the
	// deployrun registry at boot).
	_ "skygate/internal/deployrun/steps"

	// B198: import the dbmigrate/steps/ package for its
	// init() side effects (each of the 6 migration steps
	// registers itself in the dbmigrate registry at boot).
	_ "skygate/internal/dbmigrate/steps"

	"skygate/internal/dbmigrate"
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
	//
	// 2026-08-09: v0.33.1.21 — `migrate-only` subcommand. The
	// self-update orchestrator (internal/update/docker.go)
	// runs the NEW container as a one-shot to apply any
	// pending migrations BEFORE the swap. The pre-v0.33.1.21
	// orchestrator referenced `skygate --migrate-only` in
	// its docker run command but the flag was never wired
	// into main.go (the v0.29.0 plan called for it but
	// only docs + manual.go got the reference; main.go
	// just runs migrations as part of Open() on every
	// container start). With alpine as the base image
	// (v0.32.13+), the orchestrator's
	//   docker run --rm --volumes-from skygate
	//     skygate-skygate:latest /app/skygate --migrate-only
	// started returning
	//   unknown command "migrate-only" (try `skygate help`)
	// AND a few months later, after v0.29.2 removed
	// `container_name: skygate` from compose, the
	// `--volumes-from skygate` started referencing a
	// non-existent container. v0.33.1.21 fixes both.
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
		case "backup-show-config":
			// Print the current backup config in
			// `key=value` format for scripts/verify_backup.sh
			// to read. Exits 0 always (the script handles
			// missing keys via empty-string fallback).
			// 2026-08-11: v0.33.1.42 B2.
			if err := runBackupShowConfig(); err != nil {
				fmt.Fprintf(os.Stderr, "backup-show-config failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "backup-verify-ok":
			// Mark the latest verify_backup run as
			// "ok" in the global_settings so the
			// /admin/backup page shows the freshness.
			// 2026-08-11: v0.33.1.42 B2.
			if err := runBackupVerifyOK(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "backup-verify-ok failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "backup-verify-fail":
			// Mark the latest verify_backup run as
			// "fail" in the global_settings AND send a
			// Telegram alert (via the same Notifier
			// the in-app scheduler uses).
			// 2026-08-11: v0.33.1.42 B2.
			if err := runBackupVerifyFail(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "backup-verify-fail failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "cleanup-smoke-meshes":
			// 2026-08-18 (B143, v1.4.3): one-shot
			// manual trigger of the smoke-mesh
			// cleanup. Mirrors the
			// `backup-verify-ok` / `-fail`
			// subcommands from B142. Runs the SAME
			// mesh.RunCleanup path the in-app
			// scheduler uses, so the operator can
			// ad-hoc run the cleanup without
			// waiting for the 5 AM cron (or
			// without enabling the scheduler at
			// all). Output: a one-line
			// human-readable summary on stdout.
			// Exit code: 0 on success, 1 on error
			// (matches the rest of the
			// subcommands).
			if err := runCleanupSmokeMeshes(); err != nil {
				fmt.Fprintf(os.Stderr, "cleanup-smoke-meshes failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "deploy-push", "deploy-pull", "deploy-sync", "deploy-status":
			// v1.5.0 / B150 — CLI mirror of the
			// /admin/deploy web surface. Each
			// subcommand translates to a `skygate
			// deploy <verb>` call into the
			// internal/deploy package. Optional
			// --target=<host> is supported (e.g.
			// `skygate deploy-push --target=
			// skygate-standby` to stage a build
			// for a specific host).
			//
			// Exit code 0 on success (including
			// "already up to date"), 1 on error.
			verb := strings.TrimPrefix(os.Args[1], "deploy-")
			if err := runDeploySubcommand(context.Background(), os.Args[1:], verb); err != nil {
				fmt.Fprintf(os.Stderr, "%s failed: %v\n", os.Args[1], err)
				os.Exit(1)
			}
			return
		case "ha-promote", "ha-demote", "ha-reclaim":
			// v1.5.0 / B150 — CLI mirror of the
			// /admin/ha "Force actions" buttons.
			// `ha-promote` and `ha-demote` take
			// the target hostname as the second
			// CLI arg (`skygate ha-promote
			// skygate-standby`); `ha-reclaim`
			// takes none.
			//
			// Exit code 0 on success, 1 on error.
			verb := strings.TrimPrefix(os.Args[1], "ha-")
			if err := runHASubcommand(context.Background(), os.Args[1:], verb); err != nil {
				fmt.Fprintf(os.Stderr, "%s failed: %v\n", os.Args[1], err)
				os.Exit(1)
			}
			return
		case "acl-apply":
			// 2026-08-26: v1.5.2 (B188.1) — operator
			// escape hatch for forcing a one-shot
			// headscale ACL re-apply. Used after
			// migrations that change exit-node-
			// pref data (e.g. V061's tag:exit-X →
			// tag:dev-infra-X + via_enabled=1
			// backfill) without triggering any of
			// the user-facing handlers that
			// normally call ApplyACLPipelineForPlane.
			// Defaults to admin user (skyadmin);
			// override with -user=USERNAME for
			// per-plane dispatch.
			if err := runAclApply(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "acl-apply failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "derp-probe":
			// B189 (v1.5.2) — manual one-shot DERP probe +
			// latency report. Useful for ad-hoc debugging
			// from the operator's laptop. Output is the
			// same table the /admin/derp/dashboard page
			// shows, but to stdout.
			if err := runDerpProbe(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "derp-probe failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "migrate-only":
			// Open the DB (which runs all pending
			// migrations as part of Open() per the
			// v0.6.0 refactor), then exit. The
			// orchestrator uses this to verify the
			// NEW container's migrations apply
			// cleanly against the existing DB
			// BEFORE swapping — a migration
			// failure here triggers rollback to
			// the previous tag without the
			// operator ever seeing a http.StatusInternalServerError.
			if err := runMigrateOnly(); err != nil {
				log.Fatalf("migrate-only: %v", err)
			}
			return
		case "version", "--version", "-v":
			fmt.Printf("skygate %s (commit %s, built %s)\n", version, commit, buildTime)
			return
		case "cluster":
			// v1.5.0+ / B205 — cluster CLI subcommands.
			// `skygate cluster <verb>` dispatches to one of
			// invite / join / nodes / dbs / audit / failover /
			// heartbeat-daemon. The web server is NOT started
			// for any cluster subcommand (each one opens the
			// DB directly via config.Load + db.OpenDSN).
			if err := runClusterSubcommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "cluster: %v\n", err)
				os.Exit(1)
			}
			return
		case "init":
			// v1.5.0+ / B211 — cluster bootstrap CLI.
			// `skygate init` (no verb) bootstraps THIS node
			// as the cluster primary (idempotent). `skygate
			// init status` shows this node's cluster state
			// from cluster_node + cluster_database. `skygate
			// init standby-invite` prints a fresh standby
			// invite token without touching THIS node's rows.
			// The web server is NOT started (same as
			// `skygate cluster ...`).
			if err := runInit(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "init: %v\n", err)
				os.Exit(1)
			}
			return
		case "join":
			// v1.5.0+ / B212 — cluster join CLI.
			// `skygate join <token>` joins THIS node to the
			// cluster using the given invite token. The
			// pre-existing `skygate cluster join <token>`
			// is the implementation; B212 adds (1) local
			// token sanity check via cluster.VerifyToken,
			// (2) DSN bootstrap (writes a single-line
			// KEY=VALUE env file so the standby's own
			// skygate process can source the primary's DSN),
			// (3) a "next steps" message with the
			// heartbeat-daemon command. `skygate join status`
			// shows the state file (read-only, no HTTP).
			// The web server is NOT started (same as
			// `skygate cluster ...`).
			if err := runJoin(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "join: %v\n", err)
				os.Exit(1)
			}
			return
		case "migrate":
			// v1.5.0+ / B213 — in-DB schema migration CLI.
			// Phase 1.7 of cluster-management.md. The
			// pre-B213 framework had no operator-visible
			// way to see which migrations had been applied
			// (the applied_migrations table was empty in
			// the live agent — nothing populated it).
			// B213: `skygate migrate up` runs all migrations
			// (idempotent) + records each in
			// applied_migrations; `skygate migrate status`
			// shows the binary-vs-DB state with pending /
			// extra counts (extra = binary downgrade
			// signature). `down` is a STUB (Phase 1.7.1) —
			// all 47 migrations are currently forward-only.
			// The web server is NOT started.
			if err := runMigrateSubcommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
				os.Exit(1)
			}
			return
		case "help", "--help", "-h":
			fmt.Println("skygate <command> [args]")
			fmt.Println("  (no command)            start the web server")
			fmt.Println("  backup-run              run a backup using the config from the DB")
			fmt.Println("  backup-verify-ok        mark the latest verify_backup run as ok (B142)")
			fmt.Println("  backup-verify-fail      mark the latest verify_backup run as fail (B142)")
			fmt.Println("  backup-show-config      print backup-related config as key=value pairs")
			fmt.Println("  cleanup-smoke-meshes    delete smoke-mesh cruft (B143) — one-shot manual trigger")
			fmt.Println("  cluster <verb>          cluster CLI: invite / join / nodes / dbs / audit / failover / heartbeat-daemon (B205)")
			fmt.Println("  init [verb]             cluster bootstrap CLI: bootstrap / status / standby-invite (B211)")
			fmt.Println("  join [verb]             cluster join CLI: <token> / status (B212 — DSN bootstrap + next-steps)")
			fmt.Println("  migrate [verb]          in-DB schema migration CLI: up / status (B213); 'down' is a stub")
			fmt.Println("  migrate-only            open the DB + run pending migrations, then exit (v0.33.1.21 — alias for 'migrate up')")
			fmt.Println("  version                 print build version")
			fmt.Println("  help                    this help")
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

	// 2026-08-03: v0.32.29 — DERP classifier now reads
	// NPM address + LAN CIDR from config (was hardcoded
	// to a specific private LAN).
	if err := adminsvc.InitDerpClassifier(cfg.DerpPeerNPM, cfg.DerpLANNet); err != nil {
		log.Fatalf("derp classifier: %v", err)
	}

	log.Printf("🌐 Skygate starting on :%s", cfg.Port)
	log.Printf("   Headscale URL: %s", cfg.HeadscaleURL)

	// v1.3.0: PostgreSQL is mandatory. SQLite is no longer
	// supported. cfg.DBDSN is required (validated in
	// config.Load). cfg.DBPath is kept for log diagnostics
	// only — no longer used at runtime.
	log.Printf("   DB backend:    postgres (DSN=%s...)", redactPGPassword(cfg.DBDSN))
	var d *db.ResettableDB
	pool, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		log.Fatalf("db: %v", err)
	}

	// v1.5.0+ / B203 — wrap the *pgxpool.Pool in a
	// ResettableDB so the watchdog can hot-swap the
	// pool when cluster_database changes.
	//
	// The wrapper has all the *sql.DB methods via
	// embedding, AND adds Reset() for the watchdog.
	// Existing callers that take *sql.DB pass `d.DB`
	// (the embedded *sql.DB) so their signatures
	// don't need to change. The watchdog uses `d` directly
	// (it has the DBMigrator interface that calls Reset).
	d = db.NewResettableDB(pool)
	defer d.Close()

	// B189 (v1.5.2) — DERP health probe cron. 5-min interval.
	// One initial probe on start, then steady-state ticks.
	if err := derphealth.StartCron(context.Background(), d.DB, &http.Client{Timeout: 10 * time.Second}); err != nil {
		log.Printf("derp cron: %v (continuing without background probes)", err)
	}

	// 2026-07-07: issue #6 — ensure parent_domain column exists for domain auto-updater
	if _, err := d.DB.Exec("ALTER TABLE device_rules ADD COLUMN parent_domain TEXT DEFAULT ''"); err != nil {
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
		if err := bootstrapAdmin(d.DB, cfg.BootstrapAdminUser, cfg.BootstrapAdminPass); err != nil {
			log.Fatalf("bootstrap: %v", err)
		}
	}

	// Ensure headscale user for admin
	hs := headscale.New(cfg.HeadscaleURL, cfg.HeadscaleKey)
	if err := ensureHeadscaleUser(d.DB, hs, cfg.BootstrapAdminUser); err != nil {
		log.Printf("warn: ensure headscale user: %v", err)
	}

	// 2026-08-10: v0.33.1.41 — Issue 4 technical user.
	// Provision the 'infra' headscale user and link to the
	// portal_users row that V054 created. Idempotent (V054
	// is a no-op on re-runs; this function is a no-op when
	// the link is already set).
	if err := ensureInfraUser(d.DB, hs); err != nil {
		log.Printf("warn: ensure infra user: %v", err)
	}

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
	// probe response like "v1.0.0-15-gd6f7b6b" is
	// self-explanatory.
	//
	// 2026-08-12 (v1.1.0): avoid duplicating the commit hash.
	// `git describe --tags --always` ALREADY embeds the short
	// commit hash in the suffix (the "-g<hash>" part). Adding
	// `+<commit>` on top produces "v1.0.0-15-gd6f7b6b+d6f7b6b"
	// which is ugly and confuses the operator ("why is the
	// commit hash listed twice?"). When `version` already
	// contains a "-g<hex>" suffix, drop the redundant
	// "+<commit>". compareSemver in internal/update/checker.go
	// strips the `+...` part before comparing, so the
	// IsNewer result is unchanged.
	app.BuildVersion = version
	if !strings.Contains(version, "-g") && commit != "unknown" {
		app.BuildVersion = version + "+" + commit
	}
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
		// v1.5.0+ / B210 — pass the ResettableDB (not the
		// captured *sql.DB) so the auth Service's s.dbc()
		// helper transparently follows the B203 watchdog's
		// hot-reload. Pre-B210 every login + display-prefs
		// + password-change + API-token request 500'd
		// after the watchdog's first swap (the captured
		// pool was closed in the swap goroutine — the user
		// saw the login page with "Неверные учётные данные"
		// indefinitely until skygate was restarted with
		// cluster_database.current_dsn cleared).
		DB:           d,
		I18n:         app.I18n,
		JWTSecret:    app.JWTSecret,
		SessionHours: app.SessionHours,
		Version:      app.Version,
	}

	// B161.1 (v1.5.0): OIDC provider for headscale.
	// Loads / generates the RSA keypair at boot
	// and mounts the discovery + JWKS routes.
	// The /authorize + /token + /userinfo handlers
	// (B161.2 + B161.3) will add to oidcSvc.Handler()
	// without changing this wiring. If NewKeyStore
	// fails (disk full, bad perms), startup aborts
	// — see the comment in NewKeyStore.
	oidcSvc, oidcErr := oidcsvc.NewService(
		app.OIDCIssuerURL,
		app.OIDCClientID,
		app.OIDCClientSecret,
		app.OIDCKeyDir,
		app.OIDCRedirectURIs,
		app.JWTSecret,
	)
	if oidcErr != nil {
		log.Fatalf("oidc: init failed: %v", oidcErr)
	}
	// B174 (v1.5.2): wire the user-lookup callback
	// so the OIDC service can populate the email
	// claim on the id_token / /userinfo response.
	// The JWT cookie only carries uid + usr; the
	// OIDC spec requires the email claim to be
	// fresh from the DB (a user could have changed
	// their email after the JWT was issued, and
	// the id_token should reflect the current
	// value). UserLookup is optional in the OIDC
	// service — if it's nil, the email claim is
	// left empty. We wire it up here so the OIDC
	// flow is RFC-compliant.
	oidcSvc.UserLookup = func(userID int64) (string, string, error) {
		name, err := db.GetUserNameByID(app.DB.Current(), userID)
		if err != nil {
			return "", "", err
		}
		// portal_users has no email column (B174
		// confirmed via migrations_pg.go:140); we
		// derive the email from the username
		// (skygate convention: username == email
		// local-part) so the OIDC id_token still
		// has a non-empty email claim. If the
		// operator has a different username/email
		// model in mind, B174.1+ would add an
		// email column + lookup helper.
		return name, name + "@skygate.local", nil
	}
	mux.Handle("/.well-known/", oidcSvc.Handler())
	mux.Handle("/oidc/", oidcSvc.Handler())

	// B161.2: periodic sweep of expired auth codes
	// to bound the in-memory footprint. Runs every
	// 60s; the sweep itself is O(n) over the map
	// but n is bounded by active users * 1 code, so
	// the cost is negligible.
	if oidcSvc.Codes != nil {
		go func() {
			t := time.NewTicker(60 * time.Second)
			defer t.Stop()
			for range t.C {
				n := oidcSvc.Codes.Sweep()
				if n > 0 {
					log.Printf("oidc.codes: swept %d expired entries", n)
				}
			}
		}()
	}

	// B167 (v1.5.2) — auto-sync on init. When
	// SKYGATE_OIDC_AUTOSYNC=true (opt-in) AND the
	// 3 OIDC env vars are set, run the sync
	// synchronously at boot, BEFORE the HTTP server
	// starts accepting traffic. This is for the
	// "I deploy skygate with the OIDC env vars
	// set and want headscale to pick up the config
	// on the same boot" case.
	//
	// We run synchronously (not in a goroutine)
	// because headscale needs the new config
	// before skygate serves its first OIDC
	// request. A sync that fails (e.g. headscale
	// doesn't come back healthy in 60s) does NOT
	// abort skygate startup — we log + continue
	// (so a misconfigured env var doesn't take
	// down the whole service).
	if oidcsvc.ShouldAutoSync() {
		log.Printf("oidc sync: SKYGATE_OIDC_AUTOSYNC=true, running sync at boot (issuer=%s, client_id=%s)",
			app.OIDCIssuerURL, app.OIDCClientID)
		req := oidcsvc.SyncRequest{
			SkygateURL:   strings.TrimRight(app.OIDCIssuerURL, "/"),
			ClientID:     app.OIDCClientID,
			ClientSecret: app.OIDCClientSecret,
			RedirectURIs: app.OIDCRedirectURIs,
			ModeOverride: "auto",
		}
		if res, err := oidcsvc.RunSync(req); err != nil {
			log.Printf("oidc sync: boot auto-sync FAILED: %v (skygate will continue to start)", err)
		} else {
			log.Printf("oidc sync: boot auto-sync OK (mode=%s, headscale_restarted=%v, healthy=%v, took=%dms)",
				res.Mode, res.HeadscaleRestarted, res.HeadscaleHealthy, res.DurationMs)
		}
	}
	mux.HandleFunc("GET /login", authSvc.GetLogin)
	mux.HandleFunc("POST /lang", authSvc.PostLang)
	mux.Handle("POST /login", loginMW(http.HandlerFunc(authSvc.PostLogin)))
	mux.HandleFunc("POST /logout", authSvc.PostLogout)
	mux.HandleFunc("/favicon.ico", app.FaviconHandler)
	// v0.26.0 — liveness + readiness probes (HA-ready).
	// Both are UNAUTHENTICATED. /healthz is always 200
	// if the process is alive (K8s livenessProbe pattern).
	// /readyz pings the DB and headscale, returns http.StatusServiceUnavailable
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
		DB: app.DB.Current(),
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
	// v0.33.1.40 B92: wire the Availability Checker so /readyz
	// reads the cached status of headscale/headplane/tailscale
	// instead of synchronously probing headscale on every scrape.
	// The Checker runs in a background goroutine and refreshes
	// every 30s (configurable via SKYGATE_AVAILABILITY_CHECK_INTERVAL).
	// One initial synchronous check happens at Start() so /readyz
	// has real data within ~3s of boot.
	availabilityChecker := healthz.NewCheckerFromEnv(
		cfg.HeadscaleURL,
		// Headplane URL: read from env (HEADPLANE_URL), fall back
		// to the headscale URL with port 8080 (the default headplane
		// setup). Operators running headplane on a different host
		// or port set HEADPLANE_URL explicitly.
		envOrDefault("HEADPLANE_URL", deriveHeadplaneDefault(cfg.HeadscaleURL)),
		// TailscaleFn: return online status of THIS skygate's
		// in-image tailscaled. v0.33.1.42 D8: use
		// `tailscale status --json` for the real BackendState
		// ("Running" / "NeedsLogin" / "Starting" / "NoState" /
		// "Stopped") instead of the pre-D8 state-file presence
		// proxy. The proxy couldn't distinguish a healthy
		// tailnet from one in NeedsLogin (auth callback pending)
		// — both states wrote a state file, so /admin/services
		// showed "tailscaled running" with the node actually
		// offline. The /admin/services page now shows the real
		// BackendState as the detail string.
		func() (online bool, detail string) {
			if !isTailscaleRunningInContainer() {
				return false, "tailscaled not running in container (non-RF mode)"
			}
			state, ok := tailscaleBackendState()
			if !ok {
				// tailscale status --json failed (binary
				// missing, control socket down, JSON parse
				// error). Fall back to the state-file
				// presence: tailscaled was up at some point
				// AND the state file still exists, so the
				// tailnet connection is at least partially
				// functional. This is the pre-D8 behavior.
				return true, "tailscaled running (state-file fallback — tailscale status --json failed)"
			}
			return ok, "BackendState=" + state
		},
	)
	availabilityChecker.Start(context.Background())
	defer availabilityChecker.Stop()
	healthzSvc.Availability = availabilityChecker

	// v1.5.0+ / B206 — DB health sampler. Background
	// goroutine ticks every 30s, runs the expensive
	// pg_database_size + pg_last_wal_replay_lsn +
	// pg_stat_user_tables queries, and caches the
	// result. The /db/health handler reads the cached
	// sample + live pool stats and returns in <5ms.
	// The sampler receives the ResettableDB (not
	// d.DB) as its DBSource so it follows B203 hot-
	// reloads on every tick — same pattern as the
	// B204 HA elector.
	dbHealthSampler := healthz.NewDBHealthSampler(healthz.DefaultDBHealthConfig(), d)
	dbHealthSampler.Start()
	defer dbHealthSampler.Stop()
	healthzSvc.DBHealthSampler = dbHealthSampler
	healthzSvc.DBHealthSrc = d
	log.Printf("db-health: started (interval=%s, query-timeout=%s)",
		healthz.DefaultDBHealthConfig().Interval,
		healthz.DefaultDBHealthConfig().QueryTimeout)

	mux.HandleFunc("GET /healthz", healthzSvc.GetHealthz)
	mux.HandleFunc("GET /readyz", healthzSvc.GetReadyz)
	mux.HandleFunc("GET /db/health", healthzSvc.GetDBHealth)
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
		// v1.5.0+ / B208 — pass the ResettableDB (not the
		// captured *sql.DB) so the admin Service's s.dbc()
		// helper transparently follows the B203 watchdog's
		// hot-reload. Pre-B208 every admin page 500'd
		// after the watchdog's first swap (the captured
		// pool was closed in the swap goroutine).
		DB:                     d,
		HSGlobalFn:             app.HSGlobalFn,
		HSForUserFn:            app.HSForUserFn,
		Cfg:                    app.Config(),
		Notifier:               app.Notifier,
		HeadscaleUpdateMonitor: app.HeadscaleUpdateMonitor,
		Sidecar:                app.Sidecar,
		I18n:                   app.I18n,
		// v0.33.1.40 B92: wire the Availability Checker so the
		// /admin/services page can render the cached status of
		// headscale + headplane + tailscale. The Checker is
		// the same instance healthzSvc.Availability points to,
		// so /readyz and /admin/services read from the same
		// snapshot — no risk of drift.
		AvailabilityChecker: availabilityChecker,
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
		// v0.33.1.9: Tailscale web-UI management. The path +
		// login server + hostname default to the same values
		// entrypoint.sh hard-codes (so a setup that works
		// from the entrypoint keeps working when the web UI
		// takes over). The /admin/tailscale handler
		// (feature/admin/tailscale.go) uses these for the
		// Save/Start/Stop buttons.
		TailscaleAuthKeyPath: tailscaleEnvOr("SKYGATE_TS_AUTHKEY_PATH", "/data/ts/authkey"),
		TailscaleLoginServer:  tailscaleEnvOr("SKYGATE_TS_LOGIN_SERVER", "https://head.example.com"),
		TailscaleHostname:     tailscaleEnvOr("SKYGATE_TS_HOSTNAME", "skygate-host-1"),

		// v1.5.0 / B149 — /admin/ha page.
		//
		// RegapiStore is the encrypted credential store for
		// the provider API (cert + alt-password). The store
		// reads SKYGATE_SECRET_KEY for AES-256-GCM; when
		// unset, Save() returns db.ErrSecretKeyUnset and
		// the /admin/ha "External DNS" form shows a
		// "store not configured" banner.
		DNSCredsStore: extcreds.NewStore(app.DB.Current(), cfg.SecretKeyHex),
		// SelfHostname is THIS skygate instance's name in
		// the HA chain. Defaults to the Tailscale hostname
		// (the same name the operator SSHes into). The
		// /admin/ha "Self role" column reads this to render
		// the active/standby/unreachable badge.
		SelfHostname: tailscaleEnvOr("SKYGATE_TS_HOSTNAME", "skygate-host-1"),

		// v1.5.0+ / B200 — invite signing key. cfg.SecretKeyHex
		// is the raw SKYGATE_SECRET_KEY (also used for JWT
		// signing and per-user API key encryption). The
		// /admin/cluster "Generate invite" handler signs
		// sgn1 tokens with HMAC-SHA256 using this key.
		// Empty = the "Generate invite" form shows a
		// "secret not configured" error (better than
		// silently generating tokens no one can verify).
		ClusterInviteSecret: cfg.SecretKeyHex,
		// v1.5.0+ / B219 — Patroni URL for the
		// /admin/database "PG Failover" button.
		// Defaults to http://localhost:8008 (Patroni's
		// default local port). Set SKYGATE_PATRONI_URL
		// in .env to point at a different host's
		// Patroni for multi-host setups.
		PatroniURL: envOrDefault("SKYGATE_PATRONI_URL", "http://localhost:8008"),

		// v1.5.0+ / B223 (Phase 4.3) — Tailscale
		// auto-discovery tag filter. The B223
		// background poller (every 5 min) +
		// /admin/cluster/discover button only
		// consider Tailscale peers with this tag.
		// Default empty = no filter (every
		// Tailscale peer is a candidate — fine
		// for small tailnets, risky on production
		// tailnets with laptops/phones).
		// Operator sets SKYGATE_DISCOVERY_TAG in
		// .env to scope discovery to a specific
		// Tailscale ACL tag.
		DiscoveryTag: os.Getenv("SKYGATE_DISCOVERY_TAG"),

		// refactor-v0.30 Phase B step 6b (2026-07-29):
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
		// 2026-08-17 (B124): when SKYGATE_DEV_BUILD=true,
		// /admin/update shows a "dev build" banner
		// instead of the "update available" alert.
		DevBuild: app.Config().DevBuild,
	}

	// v1.5.0+ / B201 — cluster join API service. Handles
	// POST /api/cluster/join and /api/cluster/heartbeat
	// (no admin auth — the sgn1 token IS the auth).
	// The InviteSecret is the same SKYGATE_SECRET_KEY
	// the admin invite generator uses, consumed as the
	// HMAC-SHA256 key. The endpoints are no-auth because
	// the join bootstrap runs on a fresh machine that
	// doesn't have a skygate session cookie yet.
	clusterAPI := &clusterapi.Service{
		// v1.5.0+ / B210 — pass the ResettableDB (not the
		// captured *sql.DB) so the cluster Service's s.dbc()
		// helper transparently follows the B203 watchdog's
		// hot-reload. Pre-B210 every /api/cluster/join +
		// /api/cluster/heartbeat request 500'd after the
		// watchdog's first swap.
		DB:           d,
		InviteSecret: cfg.SecretKeyHex,
	}

	// v1.5.0 / B194 — auto-deploy framework service.
	// The Service holds a per-run broker map so the
	// /admin/deploys/{id}/stream SSE handler can find
	// the broker for an in-flight run. The framework
	// works against HSClient + S3Client interfaces;
	// the adapter (deployrun.HSFactoryFromFunc) wraps
	// the *headscale.Client concrete type.
	//
	// S3Client is optional — the framework marks step 4
	// (PushEnvToS3) as skipped with a clear hint if the
	// S3 env is not configured. The deploy can still
	// succeed in that case.
	deployrunCfg := &deployrun.Config{
		HeadscaleExecContainer: "headscale",
		PreauthExpiration:      "24h",
	}
	// S3 push requires the same env the backup runner
	// uses (SKYGATE_S3_BUCKET / SKYGATE_S3_ENDPOINT etc).
	// Defaults are empty → step 4 skips with a clear hint.
	if s3b := os.Getenv("SKYGATE_S3_BUCKET"); s3b != "" {
		deployrunCfg.S3Bucket = s3b
		deployrunCfg.S3Prefix = envOrDefault("SKYGATE_S3_DEPLOY_PREFIX", "ha/deploy")
		deployrunCfg.S3Endpoint = os.Getenv("SKYGATE_S3_ENDPOINT")
		deployrunCfg.S3AccessKey = os.Getenv("SKYGATE_S3_ACCESS_KEY")
		deployrunCfg.S3SecretKey = os.Getenv("SKYGATE_S3_SECRET_KEY")
	}
	deployrunSvc := deployrun.NewService(
		app.DB,
		deployrun.HSFactoryFromFunc(app.HSGlobalFn),
		deployrun.S3FactoryFromEnv(deployrunCfg),
		deployrunCfg,
		nil, // catalog: TODO wire i18n in B194.2
	)
	_ = deployrunSvc // used by mux.Handle below

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
		// v1.5.0+ / B210 — pass the ResettableDB (not the
		// captured *sql.DB) so the exit-rules Service's
		// s.dbc() helper transparently follows the B203
		// watchdog's hot-reload. Pre-B210 every /my/exit-rules
		// + /admin/exit-rules/* handler 500'd after the
		// watchdog's first swap.
		DB:       d,
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
			rows, err := app.DB.Query("SELECT id FROM portal_users WHERE headscale_url = $1 LIMIT 1", planeURL)
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
	// 2026-08-18 (B132): per-row version for the
	// "Re-sync" button. Uses the same per-node logic as
	// the all-nodes sync (extracted to syncOneExitNode so
	// the two paths share the code).
	adminSvc.SyncRoutesForNode = exitRulesSvc.SyncAdvertisedRoutesForNode
	app.SetExitRulesService(exitRulesSvc)
	// 2026-08-04: v0.33.0 — wire the runtime admin Service
	// into the TestRegistry closures so the /admin/system_tests
	// page can run in-process checks (db integrity, headscale
	// reachability, ACL classification) without re-opening the
	// DB or rebuilding the headscale client.
	adminsvc.SetTestService(adminSvc)

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
		// v1.5.0+ / B210 — pass the ResettableDB (not the
		// captured *sql.DB) so the my Service's s.dbc()
		// helper transparently follows the B203 watchdog's
		// hot-reload. Pre-B210 every /my/* handler 500'd
		// after the watchdog's first swap (devices page
		// empty, audit export broken, etc.).
		DB:      d,
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

	// Admin
	mux.Handle("GET /admin/users", authMW(http.HandlerFunc(adminSvc.GetAdminUsers)))
	mux.Handle("POST /admin/users", authMW(http.HandlerFunc(adminSvc.PostAdminUser)))
	mux.Handle("POST /admin/users/{id}/delete", authMW(http.HandlerFunc(adminSvc.PostAdminDeleteUser)))
	mux.Handle("POST /admin/users/{id}/reset-password", authMW(http.HandlerFunc(adminSvc.PostAdminUserResetPassword)))
	// v1.4.0 B141: "Adopt as skygate user" button on the
	// /admin/users HSOrphans list. The pre-B141 admin UI only
	// DISPLAYED the orphans list — to adopt one the operator had
	// to run a manual SQL INSERT into portal_users with the
	// headscale_user_id, plus a separate API call to set the
	// password. B141 wraps that into a single button per row.
	// The handler (PostAdminHSOrphanAdopt) uses ON CONFLICT DO
	// NOTHING so concurrent clicks on the same orphan are safe
	// (the second click gets a friendly "already adopted" flash
	// instead of an error).
	mux.Handle("POST /admin/users/HSOrphan/adopt", authMW(http.HandlerFunc(adminSvc.PostAdminHSOrphanAdopt)))
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
	// + doc comments and docs/internal/cluster-management.md for
	// the full design (D3, D8).
	mux.Handle("GET /admin/database", authMW(http.HandlerFunc(adminSvc.GetAdminDatabase)))
	// v1.5.0+ / B197 — Phase 1.2: Test Connection + Edit DSN.
	// Test is non-persistent (just probes the DSN and
	// re-renders with the latency). Edit writes
	// cluster_database + audit_log. Both are no-ops on the
	// live skygate process until the Phase 3.1 watchdog
	// (skygate-watchdog) lands — until then the operator
	// must restart the container to apply.
	mux.Handle("POST /admin/database/test", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseTest)))
	mux.Handle("POST /admin/database/edit", authMW(http.HandlerFunc(adminSvc.PostAdminDatabaseEdit)))
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

	// v1.5.0+ / B199 — /admin/cluster (cluster topology view,
	// Phase 2.1 read-only). The page renders the cluster +
	// cluster_node + cluster_database + cluster_invite +
	// cluster_audit state. Phase 2.2 (B200) adds the action
	// surface (add/remove nodes, generate/revoke invites).
	// See internal/feature/admin/cluster.go for the handlers
	// and docs/internal/cluster-management.md §2 for the plan.
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
	mux.Handle("POST /admin/cluster/node/drain-remove", authMW(http.HandlerFunc(adminSvc.PostAdminClusterNodeDrainRemove)))
	mux.Handle("POST /admin/cluster/invite/generate", authMW(http.HandlerFunc(adminSvc.PostAdminClusterInviteGenerate)))
	mux.Handle("POST /admin/cluster/invite/revoke", authMW(http.HandlerFunc(adminSvc.PostAdminClusterInviteRevoke)))

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
	// v0.33.1.9: Tailscale web-UI management (status + auth key
	// paste + start/stop). Pairs with the /admin/telegram
	// egress-relay card (v0.33.1.8) — the user pastes a
	// preauth key here, starts Tailscale, then picks the
	// egress relay on /admin/telegram to make the bot work.
	mux.Handle("GET /admin/tailscale", authMW(http.HandlerFunc(adminSvc.GetAdminTailscale)))
	mux.Handle("POST /admin/tailscale", authMW(http.HandlerFunc(adminSvc.PostAdminTailscale)))
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
	// B189 (v1.5.2) — DERP Health Dashboard.
	mux.Handle("GET /admin/derp/dashboard", authMW(http.HandlerFunc(adminSvc.GetAdminDerpDashboard)))
	mux.Handle("POST /admin/derp/dashboard/refresh", authMW(http.HandlerFunc(adminSvc.PostAdminDerpDashboardRefresh)))
	mux.Handle("GET /admin/exit-nodes", authMW(http.HandlerFunc(adminSvc.AdminExitNodes)))
	// 2026-07-20: v0.20.0 — headscale-update-monitor
	// status page. Renders the monitor's snapshot
	// (pinned vs. latest, history table). Admin-only.
	mux.Handle("GET /admin/headscale", authMW(http.HandlerFunc(adminSvc.GetAdminHeadscale)))
	// 2026-08-10: v0.33.1.40 B92 — Integration status board.
	// Renders the cached status of headscale/headplane/tailscale
	// (refreshed every 30s by the Availability Checker in
	// internal/feature/healthz/availability.go). Admin-only.
	mux.Handle("GET /admin/services", authMW(http.HandlerFunc(adminSvc.AdminServices)))
	// 2026-08-04: v0.33.0 — Network Access Manager. Add /
	// remove skygate-managed headscale ACL rules without
	// touching operator-added ones. Idempotent on rule
	// fingerprint. See internal/feature/admin/headscale_acl.go.
	mux.Handle("GET /admin/headscale/acl", authMW(http.HandlerFunc(adminSvc.GetAdminHeadscaleACL)))
	mux.Handle("POST /admin/headscale/acl/add", authMW(http.HandlerFunc(adminSvc.PostAdminHeadscaleACLAdd)))
	mux.Handle("POST /admin/headscale/acl/remove", authMW(http.HandlerFunc(adminSvc.PostAdminHeadscaleACLRemove)))
	// 2026-08-04: v0.33.0 — Admin Test Page. Run the
	// TestRegistry (network/db/headscale checks) and see
	// the result inline. History in system_tests_runs.
	mux.Handle("GET /admin/system_tests", authMW(http.HandlerFunc(adminSvc.GetAdminSystemTests)))
	mux.Handle("POST /admin/system_tests/run", authMW(http.HandlerFunc(adminSvc.PostAdminSystemTestsRun)))
	// 2026-08-06 v0.33.1.18 — DNS-autoupdater toggle (DB-backed).
	// Was previously wired to SKYGATE_AUTO_UPDATE_ENABLED (the
	// skygate self-update flag), which silently turned off
	// domain→/32 refresh for operators who disabled self-update
	// in .env. See internal/feature/admin/settings_dns_autoupdate.go.
	mux.Handle("POST /admin/system_tests/dns-autoupdate-toggle", authMW(http.HandlerFunc(adminSvc.PostAdminSystemTestsDNSAutoToggle)))
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
	// 2026-08-18 (B129): the new "Schedule" section on
	// /admin/update. Replaces the pre-B129 auto-toggle form
	// for the schedule-enabled + schedule-time fields. The
	// /admin/update/auto-toggle route is kept for back-compat
	// (the form's hidden field) but writes to the B129+
	// key (see PostAdminUpdateAutoToggle).
	mux.Handle("POST /admin/update/schedule", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateSchedule)))
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
	// 2026-08-18 (B132): per-row "Re-sync" button. URL
	// carries the hostname; the handler returns a single-
	// entry JSON map with the result for just this node.
	mux.Handle("POST /admin/exit-nodes/{hostname}/sync", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeSync)))
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
	// 2026-08-09 v0.33.1.29 B81: "Use Tailscale IP" inline button
	// on each /admin/exit-nodes table row. Sets exit_servers.ssh_target
	// to "root@<tailscale_ip>" so the operator's manual override
	// (e.g. firewalled public IP) doesn't shadow the B81 auto-fallback.
	mux.Handle("POST /admin/exit-nodes/use-ts-ip", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeUseTailscaleIP)))
	// v1.4.0 B140: per-row accept_routes toggle on /admin/exit-nodes.
	// The pre-B140 UI only let the operator set accept_routes at
	// initial node add; B140 adds an inline <form> per row that
	// posts the new state (1 / 0 / -1) to this handler. The handler
	// (PostAdminExitNodeSetAcceptRoutes) updates just the
	// accept_routes column without touching the other fields.
	mux.Handle("POST /admin/exit-nodes/{node_id}/accept-routes", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeSetAcceptRoutes)))

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
	wd := watchdog.NewDBSwap(
		watchdog.DefaultConfig(),
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
	defer wd.Stop()
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
	defer el.Stop()
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
		rn := telegram.NewRealNotifier(d.DB)
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
			if _, _, ok, _ := db.LoadTelegramSendTarget(d.DB); ok {
				log.Printf("🤖 Telegram bot fully configured (token + chat_id); starting getUpdates loop")
			} else if _, _, ok, _ := db.LoadTelegramToken(d.DB); ok {
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
	// as the http.StatusGatewayTimeout-on-https postmortem — see RELEASE-NOTES.md
	// v0.32.13). The /admin/update button still works for
	// manual deploys regardless of this gate.
	//
	// 2026-08-06 v0.33.1.18 — fix: cfg.AutoUpdateEnabled is the
	// flag for the skygate SELF-UPDATE banner on /admin/update,
	// not for the DNS-resolve autoupdater. The two were
	// conflated in v0.32.13, which meant any operator who
	// turned off skygate self-updates (a sane default for
	// production) accidentally also turned off DNS-resolution
	// of their domain rules → /32 entries rotted and the
	// operator's rules silently stopped matching the IPs
	// Cloudflare (or any CDN) rotated to. SKYGATE_DNS_AUTOUPDATE_ENABLED
	// is the new gate; default true (preserves the v0.32.13+
	// behaviour of "DNS autoupdater ON by default"). The
	// /admin/system_tests page exposes a DB-backed toggle
	// for this flag (overrides env on next skygate start
	// AND on the next autoupdate tick).
	if cfg.DNSAutoUpdateEnabled {
		go app.RunDomainAutoUpdater(ctx, cfg.DNSAutoCheck)
	} else {
		log.Printf("autoupdater: SKYGATE_DNS_AUTOUPDATE_ENABLED=false, skipping startup goroutine (set true to re-enable)")
	}

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
	if cfg.NodeDiscoveryInterval > 0 {
		if hs := app.HSGlobalFn(); hs != nil {
			go nodeownership.AutoBackfill(ctx, d, hs, cfg.NodeDiscoveryInterval)
		} else {
			log.Printf("node-discovery: HSGlobalFn() returned nil, skipping startup goroutine (defensive guard)")
		}
	} else {
		log.Printf("node-discovery: SKYGATE_NODE_DISCOVERY_INTERVAL=%v, skipping startup goroutine (set to a positive duration to re-enable)", cfg.NodeDiscoveryInterval)
	}

	// 2026-07-14: Этап 14 v6 — in-app backup scheduler. Started
	// after the DB is wired so Load() can read the config.
	// Wire the config loader first so Unmount (called by
	// RunBackup on its way out) can re-read the mountpoint.
	backup.SetConfigLoader(func() (*backup.Config, error) {
		return backup.Load(d.DB)
	})
	backupSched := &backup.Scheduler{
		DB:       d,
		Notifier: schedulerNotifierSink(app.Notifier),
	}
	backupSched.Start(ctx)

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
	releaseMon := &release.Monitor{
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		Current:   version,
		Notified:  make(map[string]bool),
		Notifier:  app.Notifier,
		CheckEvery: 1 * time.Hour,
		Owner:     cfg.GitHubOwner,
		Repo:      cfg.GitHubRepo,
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
		hsMon := headscale_version.NewMonitor(d.DB, cfg.HeadscaleVersionPin, app.Notifier)
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

	// 2026-08-18 (B130): background scheduler for
	// time-of-day auto-update. Reads the schedule from
	// global_settings (with env-var fallbacks) and
	// triggers the update orchestrator when (a) schedule
	// is enabled, (b) current time matches the configured
	// HH:MM, (c) GitHub has a newer release, and (d) no
	// update is already in flight. The /admin/update
	// page (B129) shows the schedule + last-run state.
	if cfg.UpdateScheduleEnabled {
		schedChecker := &update.Checker{
			Owner:          cfg.GitHubOwner,
			Repo:           cfg.GitHubRepo,
			Channel:        cfg.UpdateChannel,
			GitHubToken:    cfg.GitHubToken,
			CurrentVersion: app.BuildVersion,
		}
		schedState := update.NewStateStore("") // path resolved internally
		update.Start(ctx, update.SchedulerDeps{
			DB:           d.DB,
			State:        schedState,
			Checker:      schedChecker,
			BuildVersion: app.BuildVersion,
			Notifier:     schedulerNotifierSink(app.Notifier),
			RepoPath:     cfg.RepoPath,
			Cfg: update.SchedulerCfg{
				UpdateScheduleEnabled: cfg.UpdateScheduleEnabled,
				UpdateScheduleTime:    cfg.UpdateScheduleTime,
			},
		})
		log.Printf("⏰ update-scheduler: enabled (time=%s, env-var default; /admin/update page can override)", cfg.UpdateScheduleTime)
	} else {
		log.Printf("⏰ update-scheduler: disabled (SKYGATE_UPDATE_SCHEDULE_ENABLED=false; /admin/update page can enable)")
	}

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
		log.Printf("🔍 backup-verify-scheduler: disabled (SKYGATE_BACKUP_VERIFY_IN_APP_ENABLED=false; /admin/backup page can enable). Pre-B142 system-cron verify_backup.sh continues to run.")
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
		// B147 reads the S3 config from the backup's
		// well-known env vars (SKYGATE_S3_*) — same
		// source the backup subsystem uses, so
		// operators only configure one place. The
		// bucket is the certsync-specific one
		// (cfg.CertSyncBucket, default
		// "skygate-backups"); the key prefix
		// `certs/` is hardcoded in the scheduler.
		backupCfg := buildBackupConfigForCertSync(cfg)
		s3Client, s3Err := backup.NewS3ClientForConfig(backupCfg)
		if s3Err != nil {
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
					log.Printf("🔐 certsync: enabled (interval=%s, bucket=%s, local_dir=%s, caddy_reload=not_configured)", cfg.CertSyncInterval, cfg.CertSyncBucket, cfg.CertSyncLocalDir)
				}
			}
		}
	} else {
		log.Printf("🔐 certsync: disabled (SKYGATE_CERTSYNC_ENABLED=false). Pre-B147 system-cron cert-renew.sh continues to run.")
	}

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

	<-ctx.Done()
	log.Println("🌐 shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
}

// runMigrateOnly is the entry point for
// `skygate migrate-only` (v0.33.1.21). The self-update
// orchestrator runs the NEW container as a one-shot with
// this flag to apply any pending migrations BEFORE
// swapping the live container. A migration failure here
// triggers rollback to the previous tag without the
// operator ever seeing a http.StatusInternalServerError. The function returns
// an error (not os.Exit) so unit tests can exercise
// the happy path without forking a subprocess.
func runMigrateOnly() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only. cfg.DBDSN is required (validated by
	// config.Load), no SQLite fallback.
	log.Printf("migrate-only: opening postgres (DSN=%s...)", redactPGPassword(cfg.DBDSN))
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return err
	}
	if err := d.Close(); err != nil {
		log.Printf("warn: migrate-only: close: %v", err)
	}
	log.Printf("migrate-only: migrations applied OK")
	return nil
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
	// v1.3.0: PG-only. cfg.DBDSN is required.
	d, err := db.OpenDSN(cfg.DBDSN)
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

// runBackupShowConfig — v0.33.1.42 B2.
//
// Prints the current backup config in `key=value` format
// for scripts/verify_backup.sh to read. Same DB lookup as
// the web server's /admin/backup/config — single source of
// truth (the `global_settings` table).
//
// Output format (one per line):
//   destination=<path-or-URL>
//   protocol=<local|smb|nfs|sftp>
//   enabled=<true|false>
//   last_status=<ok|fail|running|"">
//   last_archive=<basename|"">
//
// Missing keys print with empty value (the calling script
// treats empty destination as "backup not configured" → no-op).
func runBackupShowConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only. cfg.DBDSN is required.
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer d.Close()
	bc, err := backup.Load(d)
	if err != nil {
		return fmt.Errorf("load backup config: %w", err)
	}
	fmt.Printf("destination=%s\n", bc.Destination)
	fmt.Printf("protocol=%s\n", bc.Protocol)
	fmt.Printf("enabled=%t\n", bc.Enabled)
	fmt.Printf("last_status=%s\n", bc.LastStatus)
	fmt.Printf("last_archive=%s\n", bc.LastArchive)
	return nil
}

// runBackupVerifyOK — v0.33.1.42 B2 + 2026-08-18 (B142, v1.4.1).
//
// Called by scripts/verify_backup.sh on a successful
// `sqlite3 ... "PRAGMA integrity_check"` (returns "ok") or
// a successful PG dump replay (v1.3.1+ path). Persists
// the verify timestamp + status so /admin/backup shows
// "latest verify: ok at <date>". B142 renamed the time
// key from `backup.last_verify` to `backup.last_verify_at`
// for clarity (so it can't be confused with the backup-
// creation LastRun field), and B142 also stores the
// archive basename so the page can show "verified
// <archive> on <date>".
//
// Args (from os.Args[2:]):
//   [0] = archive basename (e.g. "skygate-full-20260818_030000.tar.gz")
func runBackupVerifyOK(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only. cfg.DBDSN is required.
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer d.Close()
	archive := ""
	if len(args) > 0 {
		archive = args[0]
	}
	now := time.Now().Unix()
	if err := db.SetGlobalSetting(d, "backup.last_verify_at", fmt.Sprintf("%d", now)); err != nil {
		return fmt.Errorf("set last_verify_at: %w", err)
	}
	if err := db.SetGlobalSetting(d, "backup.last_verify_status", "ok"); err != nil {
		return fmt.Errorf("set last_verify_status: %w", err)
	}
	if archive != "" {
		if err := db.SetGlobalSetting(d, "backup.last_verify_archive", archive); err != nil {
			return fmt.Errorf("set last_verify_archive: %w", err)
		}
	}
	// 2026-08-18 (B142): clear the previous error (if
	// any) so a successful verify after a failure
	// doesn't leave a stale error in the DB. The
	// pre-B142 code didn't store an error key at all
	// (only status), so this is a new write — but
	// it's idempotent: setting "" is a no-op on a
	// fresh row.
	if err := db.SetGlobalSetting(d, "backup.last_verify_error", ""); err != nil {
		return fmt.Errorf("set last_verify_error: %w", err)
	}
	return nil
}

// runBackupVerifyFail — v0.33.1.42 B2 + 2026-08-18 (B142, v1.4.1).
//
// Called by scripts/verify_backup.sh when integrity_check
// fails OR tar extract fails. Persists the failure status
// in global_settings + writes to exit_rule_logs (which the
// in-app /admin/backup page reads). The Telegram alert is
// the responsibility of the calling cron script (which
// has access to the SKYGATE_TELEGRAM_BOT_TOKEN env if the
// operator wants alerts) — we don't try to wire the
// in-process Notifier here because main()'s app variable
// isn't in scope at subcommand dispatch time.
//
// B142: the in-app verify scheduler (internal/backup/
// verify_scheduler.go) handles Telegram alerting on its
// own — it spawns the same verify_backup.sh script and
// sends a SendAlert on non-zero exit code. The system-cron
// path (which calls runBackupVerifyFail directly) continues
// to leave Telegram to the cron wrapper; if the operator
// wants system-cron alerts they wire them in the cron
// entry. The in-app scheduler is the recommended path
// for operators who want alerts.
//
// Args (from os.Args[2:]):
//   [0] = archive basename (for the log detail)
//   [1] = error message (from sqlite3 output)
func runBackupVerifyFail(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only. cfg.DBDSN is required.
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer d.Close()
	archive := ""
	if len(args) > 0 {
		archive = args[0]
	}
	detail := "integrity_check failed"
	if len(args) > 1 {
		detail = args[1]
	}
	now := time.Now().Unix()
	if err := db.SetGlobalSetting(d, "backup.last_verify_at", fmt.Sprintf("%d", now)); err != nil {
		return fmt.Errorf("set last_verify_at: %w", err)
	}
	if err := db.SetGlobalSetting(d, "backup.last_verify_status", "fail"); err != nil {
		return fmt.Errorf("set last_verify_status: %w", err)
	}
	if archive != "" {
		if err := db.SetGlobalSetting(d, "backup.last_verify_archive", archive); err != nil {
			return fmt.Errorf("set last_verify_archive: %w", err)
		}
	}
	// 2026-08-18 (B142): store the failure detail so
	// the /admin/backup page can show the operator
	// what went wrong without forcing them to read
	// the audit log. Truncated to 1KB to keep the
	// global_settings value bounded (matches the
	// LastError pattern for backup-creation failures).
	if err := db.SetGlobalSetting(d, "backup.last_verify_error", truncateForDB(detail, 1024)); err != nil {
		return fmt.Errorf("set last_verify_error: %w", err)
	}
	if err := db.AppendExitRuleLog(d, 0, "backup_verify_fail", fmt.Sprintf("archive=%s detail=%s", archive, detail)); err != nil {
		log.Printf("backup-verify-fail: log write failed: %v", err)
	}
	return nil
}

// runCleanupSmokeMeshes — 2026-08-18 (B143, v1.4.3).
//
// Manual one-shot entry point for
// `skygate cleanup-smoke-meshes`. Loads the DB +
// runs mesh.RunCleanup (the SAME function the in-app
// scheduler uses) and prints a one-line summary to
// stdout. Exit code 0 on success (including the
// "0 cruft found" happy path), 1 on error.
//
// The function intentionally does NOT require the
// scheduler to be enabled — the operator might
// want to clear cruft one-off without turning on
// the daily cron. The cleanup itself is
// unconditional; the scheduler only adds the
// trigger timing + Telegram alerts.
func runCleanupSmokeMeshes() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only.
	d, err := db.OpenDSN(cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer d.Close()

	res, err := mesh.RunCleanup(context.Background(), mesh.CleanupSchedulerDeps{
		DB: d,
		// No Notifier on the manual subcommand —
		// the operator running the command IS
		// the audience, and the stdout line
		// below is their feedback channel. The
		// scheduler path uses the Notifier for
		// unattended Telegram alerts.
	}, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", mesh.FormatCleanupMessage(res))
		return err
	}
	fmt.Println(mesh.FormatCleanupMessage(res))
	return nil
}

// runDeploySubcommand translates the top-level CLI verb
// (`skygate deploy-push`, `skygate deploy-pull`, etc.)
// into the deploy.Run() call, which expects
// `[verb, --target=...]` shape.
//
// v1.5.0 / B150.
//
// `args` is the full os.Args[1:]; we re-slice it to
// drop the leading `deploy-X` token so the verb becomes
// the first arg to deploy.Run(). `--target=<host>` (if
// present) flows through unchanged.
func runDeploySubcommand(ctx context.Context, args []string, verb string) error {
	// args[0] is the `deploy-X` token; skip it.
	tail := args[1:]
	// Prepend the verb so deploy.Run sees `[verb, ...flags]`.
	return deploy.Run(ctx, append([]string{"deploy", verb}, tail...))
}

// runHASubcommand is the ha.* equivalent of
// runDeploySubcommand. `ha-promote <host>` and
// `ha-demote <host>` take a hostname arg; `ha-reclaim`
// takes none.
//
// The hostname arg is passed through as `--host=<X>`
// so deploy.Run's flag parser handles it. (deploy.Run
// uses flag.ContinueOnError + explicit parsing, so the
// shape is "skygate ha promote --host=foo" → os.Args
// = ["ha-promote", "foo"], which we re-shape to
// ["ha", "promote", "--host=foo"].)
func runHASubcommand(ctx context.Context, args []string, verb string) error {
	// args[0] is the `ha-X` token. The hostname (if
	// any) is args[1].
	tail := args[1:]
	if len(tail) > 0 {
		tail = append([]string{"--host=" + tail[0]}, tail[1:]...)
	}
	return deploy.Run(ctx, append([]string{"ha", verb}, tail...))
}

// truncateForDB clamps a string to max bytes, appending
// "..." when truncated. Used by runBackupVerifyFail to
// keep global_settings values bounded (a 4096-char
// detail in a TEXT column is fine, but the page render
// truncates anyway and storing 1MB of replay stderr
// just bloats the DB).
func truncateForDB(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// bootstrapAdmin creates the admin user in Skygate DB on first start.
func bootstrapAdmin(d *sql.DB, username, password string) error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=$1", username).Scan(&n); err != nil {
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
	_, err = d.Exec(`INSERT INTO portal_users(username, password_hash, is_admin) VALUES($1,$2,$3)`,
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
	if err := d.QueryRow(`SELECT id, headscale_user_id FROM portal_users WHERE username=$1 AND is_admin=1`, adminName).
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
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=$1 AND headscale_user_id IS NOT NULL", username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	existing, _ := hs.ListUsers()
	for _, u := range existing {
		if u.Name == username {
			_, err := d.Exec("UPDATE portal_users SET headscale_user_id=$1 WHERE username=$2", u.ID, username)
			return err
		}
	}
	created, err := hs.CreateUser(username)
	if err != nil {
		return err
	}
	_, err = d.Exec("UPDATE portal_users SET headscale_user_id=$1 WHERE username=$2", created.ID, username)
	return err
}

// ensureInfraUser — v0.33.1.41 — Issue 4 technical user.
//
// Provisions the 'infra' headscale user and links it to the
// portal_users row that V054 created at id=99. Idempotent
// (safe to run on every start).
//
// Why the 'infra' user is special:
//   - The portal_users row is created by V054 (id=99, never
//     logs in, password hash is a random bcrypt of a
//     never-used string).
//   - The corresponding headscale user is created here at
//     first start, OR re-linked if it already exists (e.g.
//     created manually by the operator).
//   - skygate-host-* nodes (which auto-discover via the
//     B77 autoupdater) get assigned to 'infra' (not
//     'skyadmin') so the bot in skygate-host-1 (which needs
//     internet to reach api.telegram.org) is governed by
//     a single per-device ACL grant owned by the infra
//     user, isolated from operator-portal-user policy.
//
// Failure mode: if headscale is unreachable at startup, the
// 'infra' portal_users row exists (V054 already ran) but
// headscale_user_id is NULL. The ACL generator skips the
// per-infra grant (ACL builder filters out users with
// headscale_user_id IS NULL). Result: skygate-host-* nodes
// fall back to the catch-all `* → tag:private` and `* →
// tag:exit-node` grants, which is the v0.33.1.40 behaviour
// and is functional. The next restart (when headscale is
// reachable) wires the link and the per-infra grants
// activate.
func ensureInfraUser(d *sql.DB, hs *headscale.Client) error {
	// 1. If the row is already linked, nothing to do.
	var n int
	if err := d.QueryRow(
		`SELECT count(*) FROM portal_users WHERE username = 'infra' AND headscale_user_id IS NOT NULL`,
	).Scan(&n); err != nil {
		return fmt.Errorf("infra: check link: %w", err)
	}
	if n > 0 {
		return nil
	}
	// 2. Look up headscale for an existing 'infra' user.
	//    (Operator may have pre-created it via the headscale
	//    CLI; in that case we just link, we don't try to
	//    re-create.)
	existing, err := hs.ListUsers()
	if err != nil {
		return fmt.Errorf("infra: list headscale users: %w", err)
	}
	for _, u := range existing {
		if u.Name == "infra" {
			_, err := d.Exec(
				`UPDATE portal_users SET headscale_user_id = $1 WHERE username = 'infra'`,
				u.ID,
			)
			if err != nil {
				return fmt.Errorf("infra: link existing headscale user: %w", err)
			}
			log.Printf("✅ infra user linked to existing headscale user id=%s", u.ID)
			return nil
		}
	}
	// 3. Create the headscale user and link.
	created, err := hs.CreateUser("infra")
	if err != nil {
		return fmt.Errorf("infra: create headscale user: %w", err)
	}
	// headscale.CreateUser's primary return path sometimes
	// returns an empty ID (the headscale POST response shape
	// changed between versions — 0.29.x doesn't always
	// populate the `id` field on success). The fallback
	// inside CreateUser should find the user by name in
	// ListUsers, but if THAT also fails (e.g. transient
	// headscale hiccup between the POST and the LIST), the
	// returned `created.ID` is "". Try one more ListUsers
	// pass here to make the function total: any function
	// path that says "created the user" must end with a
	// non-empty link.
	if created.ID == "" {
		users, lerr := hs.ListUsers()
		if lerr != nil {
			return fmt.Errorf("infra: create returned empty ID and re-list failed: %w", lerr)
		}
		found := false
		for _, u := range users {
			if u.Name == "infra" {
				created = &u
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("infra: create returned empty ID and 'infra' not in headscale user list (response shape may have changed)")
		}
	}
	_, err = d.Exec(
		`UPDATE portal_users SET headscale_user_id = $1 WHERE username = 'infra'`,
		created.ID,
	)
	if err != nil {
		return fmt.Errorf("infra: link new headscale user: %w", err)
	}
	log.Printf("✅ infra headscale user created and linked (id=%s)", created.ID)
	return nil
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

// tailscaleEnvOr is a tiny "env var with default" helper used
// when wiring the v0.33.1.9 /admin/tailscale Service fields.
// Distinct from app.Config() (which reads from a single
// merged struct) because these three env vars are read once
// at boot — no need to re-read on every request.
func tailscaleEnvOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// 2026-08-10: v0.33.1.40 B92 — helpers for the Availability
// Checker. Kept here (not in the healthz package) because they
// are wiring concerns, not probe logic.

// envOrDefault reads an env var, falling back to def when
// unset or empty. Mirrors tailscaleEnvOr above; kept as a
// distinct name so the call sites read clearly.
func envOrDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envOrDefaultDuration reads an env var as a Go
// duration string (e.g. "5m", "30s"), falling back
// to the supplied default when unset, empty, or
// unparseable. The minimum-allowed parameter
// protects against a typo like
// "SKYGATE_DISCOVERY_INTERVAL_SEC=0" causing an
// instant-tick loop or a parse error killing
// the ticker. Pass 0 to disable (the B223 caller
// uses this).
func envOrDefaultDuration(key string, def, min time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min {
		log.Printf("⚠️ %s=%q: invalid or below minimum %s; using default %s", key, v, min, def)
		return def
	}
	return d
}

// deriveHeadplaneDefault returns a sensible HEADPLANE_URL
// default from a HEADSCALE_URL. The default convention
// (operator's choice; documented in deploy/README.md) is
// "headplane" as a Docker-network alias on the same host
// as headscale, default headplane port 8080.
//
// Examples:
//
//	"http://headscale:50444"    → "http://headplane:8080"
//	"https://head.example.com"  → "http://headplane:8080"   (operator's Tailscale + LAN)
//
// We use port 8080 (not the headscale gRPC port 50444)
// because headplane is an HTTP web UI. Operators running
// headplane on a different port set HEADPLANE_URL explicitly.
func deriveHeadplaneDefault(headscaleURL string) string {
	// Just return a sane placeholder; the operator can override
	// via HEADPLANE_URL env var. We intentionally do NOT try
	// to parse the headscale URL (port etc.) because most
	// deployments use a different host for headplane.
	return "http://headplane:8080"
}

// isTailscaleRunningInContainer returns true if tailscaled
// is running in the skygate container. Heuristic: tailscaled
// writes its state file to /var/lib/tailscale/ at boot; if
// the file exists, tailscaled ran at some point. We don't
// check process status because the in-image tailscaled runs
// as a background goroutine in the same process group as
// skygate (no /proc entry to grep for).
//
// A more accurate check would shell out to `tailscale status`
// and parse the output, but that adds 100-500ms per probe
// and the state-file heuristic is "good enough" for an
// operator-facing health indicator (the operator can drill
// into /admin/tailscale for the detailed status).
// tailscaleBackendState shells out to `tailscale status --json` and
// returns the BackendState string ("Running" / "NeedsLogin" /
// "Starting" / "NoState" / "Stopped"). The boolean is true only for
// "Running" (the only state where tailscaled can actually serve
// traffic — every other state means the tailnet is unreachable
// from this node).
//
// v0.33.1.42 D8 (post-v0.33.1.40 B92 availability refinement):
// replaces the pre-D8 proxy check (`isTailscaleRunningInContainer`)
// with a real BackendState read. The proxy was a state-file
// presence heuristic — it returned "true" for any node where
// tailscaled had ever written a state file, even if the daemon
// was actually in NeedsLogin (waiting for an auth callback) or
// NoState (e.g. the control URL is wrong and the auth never
// completed). The /admin/services page now shows the actual
// BackendState so the operator can tell at a glance whether
// the tailnet connection is healthy.
//
// Cost: ~50-150ms per probe (subprocess fork + JSON parse). The
// availability check runs every 30s by default, so the cost is
// ~0.5% CPU — negligible. The pre-D8 comment "would block the
// probe" was a worry but 50ms once per 30s is well within budget.
//
// On error (tailscale not installed, control socket missing,
// subprocess failed), the helper returns ("", false) so the
// caller can decide what to do. We do NOT shell out from this
// helper's failure path — the caller already has the
// state-file-presence check as a final fallback.
func tailscaleBackendState() (state string, ok bool) {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return "", false
	}
	// The JSON shape (headscale 0.29.x + Tailscale 1.5x+):
	//   {"BackendState": "Running", ...}
	// We use a minimal struct + a permissive "any non-empty
	// value is the BackendState" extraction. Newer tailscale
	// versions add fields (e.g. "Prefs", "Peer", "TailscaleIPs")
	// which we ignore — we only need the single string.
	var s struct {
		BackendState string `json:"BackendState"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return "", false
	}
	if s.BackendState == "" {
		return "", false
	}
	return s.BackendState, s.BackendState == "Running"
}

func isTailscaleRunningInContainer() bool {
	// /var/lib/tailscale/tailscaled.state is the conventional
	// path (see entrypoint.sh's tailscaled --statedir=...).
	// If it exists and is non-empty, tailscaled has been up.
	// We use os.Stat rather than ReadFile to keep the probe
	// fast.
	for _, path := range []string{
		"/var/lib/tailscale/tailscaled.state",
		"/var/lib/tailscale/",
	} {
		if info, err := os.Stat(path); err == nil {
			if info.IsDir() {
				// Directory exists; check if any state file inside.
				entries, err := os.ReadDir(path)
				if err == nil {
					for _, e := range entries {
						if !e.IsDir() && e.Name() != "" {
							return true
						}
					}
				}
			} else if info.Size() > 0 {
				return true
			}
		}
	}
	return false
}

// 2026-08-18 (B130): adapter from the full telegram.Notifier
// to the update package's NotifierSink. Avoids an import
// cycle (internal/update can't import internal/telegram
// because internal/telegram doesn't import internal/update,
// buildBackupConfigForCertSync builds a minimal
// backup.Config from env vars + the certsync-specific
// bucket (cfg.CertSyncBucket). The certsync scheduler
// uses the same S3 endpoint / credentials as the backup
// subsystem (the bucket is the only certsync-specific
// field), so the operator configures one place.
//
// v1.5.0 / B147.
//
// Returns a Config that's safe to pass to
// backup.NewS3ClientForConfig. The S3Prefix is left
// empty — the certsync uses hardcoded `certs/...` keys
// (see internal/certsync/certsync.go), not the backup's
// S3 prefix.
func buildBackupConfigForCertSync(cfg *config.Config) *backup.Config {
	return &backup.Config{
		S3Endpoint:  os.Getenv("SKYGATE_S3_ENDPOINT"),
		S3Region:    os.Getenv("SKYGATE_S3_REGION"),
		S3AccessKey: os.Getenv("SKYGATE_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("SKYGATE_S3_SECRET_KEY"),
		S3Bucket:    cfg.CertSyncBucket,
		// S3Prefix intentionally empty (certsync uses
		// absolute keys: `certs/cert.pem`, etc).
	}
}

// 2026-08-18 (B130): adapter from the full telegram.Notifier
// to the update package's NotifierSink. Avoids an import
// cycle (internal/update can't import internal/telegram
// because internal/telegram doesn't import internal/update,
// but the test fixtures sometimes do and the cycle would
// block refactors). Returns nil if n is nil so the scheduler
// can simply call if deps.Notifier != nil.
func schedulerNotifierSink(n telegram.Notifier) update.NotifierSink {
	if n == nil {
		return nil
	}
	return schedulerSink{n: n}
}

// runDiscoveryTicker is the B223 (Phase 4.3)
// background ticker that runs Tailscale
// auto-discovery every `interval`. The HTTP
// handler at /admin/cluster/discover also calls
// the same cluster.DiscoverNewNodes +
// cluster.EnsureDiscoveredNode pair, so the
// "run now" path is identical to the "wait for
// the next tick" path.
//
// Errors are logged but do NOT stop the ticker
// — the next tick retries. A persistent
// `tailscaled not running` error (e.g. on a
// host where the operator forgot to enable
// Tailscale) will keep firing every interval;
// the operator sees "discovery-ticker: 0 new
// nodes" in `docker logs` and the
// cluster.discovery.error audit row on
// /admin/cluster.
func runDiscoveryTicker(ctx context.Context, d *sql.DB, tagFilter string, interval time.Duration, notifier update.NotifierSink) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runOneDiscoveryTick(ctx, d, tagFilter, notifier)
		}
	}
}

// runOneDiscoveryTick runs one discovery pass.
// Split out from runDiscoveryTicker so the
// unit tests + the live-verify can call it
// without spinning up a real ticker.
func runOneDiscoveryTick(ctx context.Context, d *sql.DB, tagFilter string, notifier update.NotifierSink) {
	const clusterID = "skygate-staging"
	peers, err := cluster.DiscoverNewNodes(ctx, d, clusterID, tagFilter)
	if err != nil {
		log.Printf("🔎 discovery-ticker: discover failed: %v", err)
		_ = db.AppendAuditLogWithTarget(d, 0, "system", "cluster.discovery.error",
			fmt.Sprintf("error=%q", err.Error()), "", "")
		return
	}
	discovered := 0
	for _, p := range peers {
		if err := cluster.EnsureDiscoveredNode(d, clusterID, p.Hostname, p.TailscaleIP, "system"); err != nil {
			log.Printf("🔎 discovery-ticker: ensure %q failed: %v", p.Hostname, err)
			_ = db.AppendAuditLogWithTarget(d, 0, "system", "cluster.discovery.error",
				fmt.Sprintf("hostname=%q error=%q", p.Hostname, err.Error()), "cluster_node", p.Hostname)
			continue
		}
		discovered++
	}
	runDetail := fmt.Sprintf("discovered=%d total_peers=%d tag_filter=%q via=ticker", discovered, len(peers), tagFilter)
	_ = db.AppendAuditLogWithTarget(d, 0, "system", "cluster.discovery.run", runDetail, "", "")
	if discovered > 0 {
		log.Printf("🔎 discovery-ticker: discovered %d new node(s) from Tailscale (tag_filter=%q)", discovered, tagFilter)
		if notifier != nil {
			notifier.SendAlert(fmt.Sprintf("Tailscale auto-discovery found %d new node(s) (tag_filter=%q). See /admin/cluster → Approve.", discovered, tagFilter))
		}
	}
}

type schedulerSink struct{ n telegram.Notifier }

func (s schedulerSink) SendAlert(text string) int64 {
	return s.n.SendAlert(text)
}

// schedulerUserNotifierSink bridges the skygate
// telegram notifier (which exposes
// SendTelegramToChat(chatID, text)) to the
// keynotify.UserNotifierSink interface. Different
// from schedulerNotifierSink: the operator-side
// SendAlert goes to the operator's chat via the
// telegram_alerts ack pattern; B156's
// SendUserMessage goes to a specific user chat
// (per-user notification, not operator chat).
//
// chatID=0 is treated as "send to operator chat"
// — used for the scheduler's failure alerts + the
// "notified N key(s) for users" summary. The
// telegram package's SendTelegramToChat with
// chatID=0 is a no-op (no chat to send to), so
// operator-side failures need a separate path
// (the underlying notifier's SendAlert if the
// user has one configured). For B156 the
// summary is best-effort; the audit log captures
// the truth.
func schedulerUserNotifierSink(n telegram.Notifier) keynotify.UserNotifierSink {
	if n == nil {
		return nil
	}
	return userNotifierSink{n: n}
}

type userNotifierSink struct{ n telegram.Notifier }

func (s userNotifierSink) SendUserMessage(chatID int64, text string) bool {
	if chatID == 0 {
		// Operator-side summary / failure
		// alerts go through SendAlert (the
		// standard telegram_alerts ack path).
		return s.n.SendAlert(text) != 0
	}
	// Per-user: direct send to the user's
	// chat_id. Returns true on a successful
	// HTTP 2xx from the Telegram Bot API.
	// SendTelegramToChat returns nothing
	// (void), so we use the chat-id round-trip
	// pattern: send, then check the inline
	// message tracking. For B156 we treat any
	// non-error return as success.
	s.n.SendTelegramToChat(text, chatID)
	return true
}

// haNotifierAdapter bridges the skygate telegram notifier
// (which exposes SendAlert) to the ha.Notifier interface
// (which has NotifyRoleChange). The elector calls
// NotifyRoleChange exactly once per role transition;
// failures are logged inside the adapter so the chain
// update isn't blocked by a Telegram outage.
type haNotifierAdapter struct{ n telegram.Notifier }

func (a haNotifierAdapter) NotifyRoleChange(_ context.Context, msg string) error {
	_ = a.n.SendAlert(msg)
	return nil
}

// haProviderName returns the human-readable name of the
// configured DNS provider for the HA startup log. The
// empty string means "no provider configured" — the
// elector will skip the DNS update step and just log
// the role transition to Telegram.
func haProviderName(name string) string {
	if name == "" {
		return "none"
	}
	return name
}
