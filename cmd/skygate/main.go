package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"skygate/internal/backup"
	"skygate/internal/certsync"
	"skygate/internal/config"
	"skygate/internal/db"
	_ "skygate/internal/dbmigrate/steps"
	"skygate/internal/deployrun"
	_ "skygate/internal/deployrun/steps"
	"skygate/internal/derpcfg"
	"skygate/internal/derphealth"
	"skygate/internal/dns"
	"skygate/internal/elector"
	"skygate/internal/expirewatch"
	adminsvc "skygate/internal/feature/admin"
	authsvc "skygate/internal/feature/auth"
	clusterapi "skygate/internal/feature/cluster"
	exitrules "skygate/internal/feature/exit_rules"
	"skygate/internal/feature/healthz"
	mysvc "skygate/internal/feature/my"
	"skygate/internal/ha"
	extcreds "skygate/internal/ha/dnsexternal"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/headscale_version"
	"skygate/internal/keynotify"
	"skygate/internal/mesh"
	"skygate/internal/metrics"
	"skygate/internal/middleware"
	"skygate/internal/module"
	tailscalemod "skygate/internal/module/tailscale"
	"skygate/internal/monitoring"
	"skygate/internal/nodeownership"
	oidcsvc "skygate/internal/oidc"
	"skygate/internal/ratelimit"
	"skygate/internal/release"
	"skygate/internal/sidecar"
	"skygate/internal/startup"
	"skygate/internal/telegram"
	"skygate/internal/tokenrotate"
	"skygate/internal/update"
	"skygate/internal/watchdog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
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

// listenAddr turns the configured port into a listen address. B269: the
// provisional listener binds BEFORE the database, migrations and service
// construction, so a bind failure is the FIRST thing the process reports
// (instead of a `listen: ...` line hours later, buried between goroutine
// logs) and the operator's port is provably held from the second line of
// the journal onward.
func listenAddr(port string) (string, error) {
	p := strings.TrimSpace(port)
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid SKYGATE_PORT %q (want 1-65535)", port)
	}
	return ":" + p, nil
}

// handlerBox gives the boot-time handler swap a single CONCRETE type for
// atomic.Value. Storing an http.HandlerFunc first and an *http.ServeMux later
// panics with "sync/atomic: store of inconsistently typed value into Value",
// which killed the process exactly at handover (found live by the B270 probe).
type handlerBox struct{ h http.Handler }

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
		case "derp-metrics-proxy":
			// B315 — the host-side loopback bridge for derper's
			// /debug/* endpoints. derper admits loopback (and
			// tailnet) sources only, so the container always got
			// `403 debug access denied` and /admin/derp drew zeros
			// where the metrics should be. This runs ON THE HOST and
			// re-serves five read-only debug paths to the bridge.
			// See internal/derpmetricsproxy.
			if err := runDerpMetricsProxy(os.Args[2:]); err != nil {
				if err == flag.ErrHelp {
					return
				}
				fmt.Fprintf(os.Stderr, "derp-metrics-proxy failed: %v\n", err)
				os.Exit(2)
			}
			return
		case "regapi-credentials":
			// B237.21 (v1.5.2+) — CLI mirror of the
			// /admin/ha "External DNS" form. Pre-B237.21
			// the only path to set the reg.ru creds was
			// the web form, which blocks the operator's
			// "one-shot bootstrap" flow (clone repo →
			// start skygate → set creds via CLI → run
			// b146_regapi_live.sh). The dispatcher
			// handles 4 verbs: set / show / test / delete.
			// Full per-verb docs in regapi_credentials.go.
			if err := runRegAPICredsSubcommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "%s failed: %v\n", os.Args[1], err)
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
		case "db-migrate":
			// B-mod-sqlite-pg-bidi v1.5.4 Task 3 — convert
			// schema + data between skygate DBs (SQLite↔PG).
			// See cmd/skygate/db_migrate.go for the subcommand
			// surface; the heavy lifting lives in
			// internal/db/convert.go.
			if err := runDBMigrateSubcommand(context.Background(), os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "db-migrate failed: %v\n", err)
				os.Exit(1)
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
		case "oidc-export":
			// B304 (v1.5.69) — print the OIDC configuration headscale must be
			// given (the same precedence /admin/oidc uses: the saved row wins,
			// the env is the fallback). Run on the host, so the stored
			// client_secret can reach headscale's config without ever travelling
			// over HTTP or into the admin UI's HTML.
			if err := runOIDCExportSubcommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "oidc-export: %v\n", err)
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
			fmt.Println("  derp-metrics-proxy      serve derper's loopback-only /debug endpoints to the container (B315)")
			fmt.Println("  cluster <verb>          cluster CLI: invite / join / nodes / dbs / audit / failover / heartbeat-daemon (B205)")
			fmt.Println("  regapi-credentials     External DNS provider creds: set / show / test / delete (B237.21)")
			fmt.Println("  oidc-export            Print the OIDC config headscale needs (env block or headscale block) (B304)")
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
	// B269: phase tracking for the whole startup sequence. A native install
	// runs config → DB → migrations → services → routes → handover; any
	// block in that window produced a process that systemd calls "active"
	// while nothing listens on the HTTP port, and neither the journal tail
	// nor /admin/update said where it stuck (live case: `healthz did not
	// report build 'v1.5.11' within 90s (last build: none)` while the unit
	// was `active` and the DB watchdogs were ticking). From here on the
	// journal carries `startup: phase=...` lines, so the last one logged IS
	// the blocker.
	startup.SetBuild(version)
	startup.Enter("config")

	// B269 — the socket is bound HERE, before the DB, before migrations and
	// before every service is constructed, so that:
	//
	//   * a port conflict is the first thing reported (pre-B269 the fatal
	//     `listen:` line came last, after minutes of successful-looking
	//     background work — the B268 applier's baseline instead saw only
	//     "service 'skygate' is 'active'" and "nothing is listening");
	//   * the self-updater's `healthz must report build X` check can pass
	//     as soon as the process is up, because the provisional handler
	//     already answers with the build string;
	//   * requests are routed to the REAL mux as soon as routes are wired
	//     (same listener, no gap, no second bind, no port takeover window).
	//
	// If binding fails we exit non-zero for the same fatal path, so a
	// supervisor restarts us and the updater sees a non-active unit.
	addr, err := listenAddr(cfg.Port)
	if err != nil {
		startup.SetFatal(err.Error())
		log.Fatalf("listen: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		startup.SetFatal(fmt.Sprintf("bind %s: %v", addr, err))
		log.Fatalf("listen: %v", err)
	}
	// The handler starts as the provisional startup handler and is
	// replaced by the real mux once the route table is complete.
	//
	// Both stores go through handlerBox: atomic.Value panics with
	// "store of inconsistently typed value" when the concrete types differ
	// (a bare http.HandlerFunc here, *http.ServeMux there), so the boot
	// would die exactly at handover — a live-tested regression caught by the
	// B270 probe, not by the source contracts.
	var handler atomic.Value
	handler.Store(handlerBox{startup.StageHandler()})
	bootSrv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.Load().(handlerBox).h.ServeHTTP(w, r) }),
		ReadHeaderTimeout: 10 * time.Second,
	}
	releaseProvisional := sync.OnceFunc(func() { _ = bootSrv.Close() })
	go func() {
		if err := bootSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("startup: provisional listener stopped: %v", err)
		}
	}()
	log.Printf("🌐 Skygate %s startup: listening on %s (provisional /healthz until routes are wired)", version, addr)

	// B269 — a panic after this point is reported WITH its stack and the
	// phase it happened in, and the listener is released so a supervisor
	// sees a genuinely free port instead of a half-dead process holding it.
	// os.Exit(3) marks a startup crash (systemd Restart=on-failure, and the
	// updater sees a non-active unit) rather than the silent
	// `active`-but-mute state B269 exists to eliminate.
	defer func() {
		if r := recover(); r != nil {
			startup.SetFatal(fmt.Sprintf("panic in phase %q: %v", startup.Phase(), r))
			log.Printf("startup: PANIC in phase=%s: %v\n%s", startup.Phase(), r, debug.Stack())
			releaseProvisional()
			os.Exit(3)
		}
	}()

	// 2026-08-03: v0.32.29 — DERP classifier now reads
	// NPM address + LAN CIDR from config (was hardcoded
	// to a specific private LAN).
	if err := adminsvc.InitDerpClassifier(cfg.DerpPeerNPM, cfg.DerpLANNet); err != nil {
		log.Fatalf("derp classifier: %v", err)
	}

	log.Printf("🌐 Skygate starting on :%s", cfg.Port)
	log.Printf("   Headscale URL: %s", cfg.HeadscaleURL)

	// v1.3.0 said "PostgreSQL is mandatory"; v1.5.4 restored SQLite
	// (B-mod-sqlite-pg-bidi) with the DSN as the source of truth. The
	// label below was hardcoded to "postgres" long after that, so a
	// native SQLite install logged
	//   DB backend: postgres (DSN=sqlite:/var/lib/skygate/skygate.db...)
	// while failing to open that very database — actively misleading
	// during the B261 canary debugging. Report the detected dialect.
	dialectKind := db.DetectDSN(cfg.DBDSN).Kind
	log.Printf("   DB backend:    %s (DSN=%s...)", dialectKind, redactPGPassword(cfg.DBDSN))
	startup.Enter("db-open+migrate")
	var d *db.ResettableDB
	pool, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
	if err != nil {
		startup.SetFatal(fmt.Sprintf("db open/migrate: %v", err))
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
	startup.Enter("background-crons")

	// B189 (v1.5.2) — DERP health probe cron. 5-min interval.
	// One initial probe on start, then steady-state ticks.
	if err := derphealth.StartCron(context.Background(), d.DB, &http.Client{Timeout: 10 * time.Second}); err != nil {
		log.Printf("derp cron: %v (continuing without background probes)", err)
	}

	// B251 (v1.5.6+, 2026-09-15) — DERP cert auto-renewal cron.
	// 24h interval (LE renews at 30 days remaining, so once-a-day
	// fires within 1 day of every renewal). Each row in derp_cert_sync
	// runs its mode-specific flow: npm=fetch+write+SIGHUP,
	// letsencrypt=monitor-expiry, manual=monitor-expiry. Failures
	// are recorded per-row (last_error) so a single bad row doesn't
	// block the rest. The cron is no-op after the first successful
	// StartCertSyncCron call so a reload / main.go re-run doesn't
	// double-start the loop.
	if err := adminsvc.StartCertSyncCron(context.Background(), d.DB); err != nil {
		log.Printf("derp cert sync cron: %v (continuing without auto-renewal)", err)
	}

	// 2026-09-15 (B-bug-fix): auto-register the bundled derper in
	// derp_relays if /var/lib/derper/derper.conf exists AND no
	// bundled row is already present. Live case: agent VM
	// 192.168.13.69 had a running derper (systemd unit active
	// since 2026-09-12) but no derp_relays row for region 900,
	// because AutoMigrateDerpRelays only fires when
	// global_settings.derp.bundled_enabled=="1" — and that key
	// was set to "0" by deploy.sh. Result: headscale never learned
	// about region 900 and 0 Tailscale clients used the local
	// DERP. Idempotent — safe to call on every boot.
	derpHostname := os.Getenv("SKYGATE_DERP_HOSTNAME")
	if derpHostname == "" {
		derpHostname = os.Getenv("DERP_HOSTNAME")
	}
	if derpHostname != "" {
		if inserted, reason, err := adminsvc.EnsureBundledDerpRelay(d.DB, derpHostname, "", "", ""); err != nil {
			log.Printf("derp_relays_auto: insert failed: %v (continuing — headscale still has bundled Tailscale DERP)", err)
		} else if inserted {
			log.Printf("derp_relays_auto: inserted bundled derp_relays row for %q (region 900)", derpHostname)
		} else {
			log.Printf("derp_relays_auto: no-op for %q — %s", derpHostname, reason)
		}
	} else {
		log.Printf("derp_relays_auto: SKYGATE_DERP_HOSTNAME / DERP_HOSTNAME not set — skipping bundled derper auto-register")
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
	startup.Enter("headscale-client")
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
	startup.Enter("services+telegram")
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
	// 2026-09-18 (B261 / plan §12.15): reconcile a native (systemd /
	// bare) self-update that finished while we were down. The
	// privileged helper writes its verdict to <update_dir>/result.*
	// and then restarts this service, so the NEW process is the first
	// one able to report it. Doing it here (in addition to the
	// /admin/update render path) means the verdict lands even if
	// nobody logs in — the operator's `cat` of the state file, the
	// Telegram alert path, and the page all see the same thing.
	// No-op for Docker jobs and when there is nothing to fold.
	update.ConfirmNativeSwap(updateStore, cfg.UpdateDir)
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
		for range t.C {
			app.RateLimiter.Sweep()
		}
	}()
	loginMW := middleware.RequireLoginLimit(app.RateLimiter)
	apiMW := middleware.RequireAPILimit(app.RateLimiter)
	_ = apiMW // exposed for explicit endpoint wrapping (currently routes attach via authMW only)

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
	// B366: the rule now lives in buildVersionString() (self_version.go), so
	// /healthz, the panel footer, `skygate version` and the version a JOIN
	// registers cannot drift apart.
	app.BuildVersion = buildVersionString()
	log.Printf("🌐 Skygate %s (commit %s, built %s)", version, commit, buildTime)

	startup.Enter("routes")
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
		Backend: app,
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
	// without changing this wiring.
	//
	// B270 (2026-09-19): this is NOT fatal any more. A live native install
	// failed here with "oidc: mkdir ./data: permission denied" (relative
	// SKYGATE_OIDC_KEY_DIR + a systemd CWD that was not the data dir) and the
	// process exited BEFORE binding its HTTP port — the unit stayed 'active'
	// with nothing listening, the self-updater could never verify a build, and
	// OIDC was not even configured (SKYGATE_OIDC_ISSUER was unset). NewService
	// B-oidc-setup (v0.75, 2026-09-21): DB-first OIDC config.
	// The boot sequence reads the oidc_settings DB row (if any)
	// and falls back to env vars. Pre-fix there was no DB row,
	// and the only way to change OIDC config was to edit env
	// vars + restart. Now the operator can fill the form on
	// /admin/oidc and the values persist.
	//
	// Honour SKYGATE_OIDC_ENABLED: "false" / "0" / "no" forces
	// OIDC off even if the DB row has enabled=true (the operator
	// used the toggle to disable, then realised env wins for
	// an emergency off-switch). Empty string falls back to the
	// DB row's enabled flag, which itself defaults to the legacy
	// "issuer non-empty" rule.
	effectiveIssuer := app.OIDCIssuerURL
	effectiveClientID := app.OIDCClientID
	effectiveClientSecret := app.OIDCClientSecret
	effectiveKeyDir := app.OIDCKeyDir
	effectiveRedirectURIs := app.OIDCRedirectURIs
	dbRow, dbErr := db.GetOIDCSettingsDecrypted(app.DB.Current(), app.SecretKeyHex)
	envEnabled := strings.ToLower(strings.TrimSpace(app.OIDCEnabledEnv))
	envOff := envEnabled == "false" || envEnabled == "0" || envEnabled == "no"
	if dbErr == nil {
		// DB row exists — it wins for every field the operator
		// saved (the form fields are pre-filled from DB on render).
		if dbRow.Issuer != "" {
			effectiveIssuer = dbRow.Issuer
		}
		if dbRow.ClientID != "" {
			effectiveClientID = dbRow.ClientID
		}
		// ClientSecret may be empty after a form save that
		// left the password field blank (we don't echo the live
		// secret). When empty, fall back to env — the operator
		// can keep using SKYGATE_OIDC_CLIENT_SECRET for rotation
		// without re-saving the form.
		if dbRow.ClientSecret != "" {
			effectiveClientSecret = dbRow.ClientSecret
		}
		if dbRow.RedirectURIs != "" {
			effectiveRedirectURIs = dbRow.RedirectURIs
		}
		if dbRow.KeyDir != "" {
			effectiveKeyDir = dbRow.KeyDir
		}
		log.Printf("oidc: loaded config from DB (issuer=%s, client_id=%s, enabled=%v)", effectiveIssuer, effectiveClientID, dbRow.Enabled)
	} else if dbErr != db.ErrOIDCSettingsNotFound {
		log.Printf("oidc: oidc_settings read failed: %v (continuing with env-only config)", dbErr)
	}
	oidcSvc, oidcErr := oidcsvc.NewService(
		effectiveIssuer,
		effectiveClientID,
		effectiveClientSecret,
		effectiveKeyDir,
		effectiveRedirectURIs,
		app.JWTSecret,
	)
	// SKYGATE_OIDC_ENABLED=false, or a DB row with enabled=0 (the /admin/oidc
	// switch), disables the provider by emptying the issuer — the service's own
	// handlers answer 503 in that state ("OIDC provider disabled"), so the routes
	// stay mounted and the operator can flip the feature back on from the UI
	// WITHOUT a restart (B290).
	//
	// B290 (2026-09-22): this used to set oidcSvc = nil, which both panicked on
	// the very next line (oidcSvc.UserLookup = …) whenever the env switch was
	// used and made the UI switch unrepresentable: a form that could save a row
	// nothing read. The issuer is now the single source of truth for "enabled".
	if envOff {
		log.Printf("oidc: SKYGATE_OIDC_ENABLED=false — routes will answer 503 until the env flips back")
		effectiveIssuer = ""
	} else if dbErr == nil && !dbRow.Enabled {
		log.Printf("oidc: oidc_settings.enabled=0 (switched off on /admin/oidc) — routes will answer 503 until it is enabled there")
		effectiveIssuer = ""
	}
	if oidcErr != nil {
		log.Printf("oidc: init failed: %v (continuing — OIDC routes will answer 503; the portal and /healthz are unaffected)", oidcErr)
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
	//
	// B225.1 (Phase 4.4 follow-up): the sampler
	// now has a Notifier field — the local
	// DBHealthAlertSink interface (in the
	// healthz package) is satisfied by
	// *telegram.RealNotifier. The config below
	// sets Notifier to the operator's bot
	// (when configured) or the silent
	// NoopAlertSink when no bot token is set.
	// The transition detector inside the
	// sampler fires on every ok→degraded and
	// degraded→ok edge, so the operator gets a
	// real-time Telegram push the moment the DB
	// stops responding to pings.
	dbHealthCfg := healthz.DefaultDBHealthConfig()
	dbHealthCfg.Notifier = schedulerNotifierSink(app.Notifier)
	// B271: the collector's queries are dialect-specific. Without this the
	// PostgreSQL catalog queries (pg_is_in_recovery, pg_database_size,
	// pg_stat_user_tables, pg_current_wal_lsn) ran against SQLite every 30 s
	// and produced five "SQL logic error: no such function" entries per tick
	// (live: the aro host, 2026-09-19) — a permanently degraded DB-health
	// badge on a healthy install.
	dbHealthCfg.Dialect = dialectKind.String()
	dbHealthSampler := healthz.NewDBHealthSampler(dbHealthCfg, d)
	dbHealthSampler.Start()
	defer dbHealthSampler.Stop()
	healthzSvc.DBHealthSampler = dbHealthSampler
	healthzSvc.DBHealthSrc = d
	log.Printf("db-health: started (interval=%s, query-timeout=%s, dialect=%s)",
		dbHealthCfg.Interval,
		dbHealthCfg.QueryTimeout,
		dbHealthCfg.Dialect)

	// 2026-09-03 / B226 (Phase 4.5) — Prometheus
	// exporter. The collector samples skygate
	// state every 30s (same cadence as the B206
	// healthz sampler so operators can correlate
	// /db/health transitions with /metrics
	// deltas during an incident). The /metrics
	// handler (registered below) serves the
	// updated gauges in textfmt.
	metricsSrc := &metrics.DBPoolSource{DB: d, Cluster: "skygate-staging"}
	metricsStop := metrics.StartCollector(context.Background(), metricsSrc, 30*time.Second)
	defer metricsStop()
	// B226 build info — one-time set, never
	// changes for the life of the process.
	metrics.BuildInfoGauge.
		WithLabelValues(version, runtime.Version()).
		Set(1)
	log.Printf("metrics: /metrics endpoint registered (collector interval=30s)")

	mux.HandleFunc("GET /healthz", healthzSvc.GetHealthz)
	mux.HandleFunc("GET /readyz", healthzSvc.GetReadyz)
	mux.HandleFunc("GET /db/health", healthzSvc.GetDBHealth)
	// 2026-09-03 / B226: Prometheus exporter
	// (Phase 4.5). The /metrics endpoint serves
	// the B226 in-house metrics registry in
	// textfmt. UNAUTHENTICATED (the Prometheus
	// scraper doesn't carry cookies) — same
	// exposure level as /healthz + /db/health.
	// If the operator wants auth, layer a
	// sidecar with basic auth in front of it.
	mux.Handle("GET /metrics", metrics.Default().Handler())
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
		Backend: app,
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
		// B323 (2026-09-25): the /admin/service page reports the uptime of THIS
		// process, so the operator can distinguish "the restart worked" from
		// "the service never came back". Wired from the same StartedAt /healthz
		// uses, so the two surfaces cannot disagree.
		StartedAt: app.StartedAt,
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
		SecretKeyHex:        app.SecretKeyHex,
		InvalidateHSCacheFn: app.InvalidateHSCache,
		// B290 (2026-09-22): let /admin/oidc drive the RUNNING provider, so
		// enabling OIDC from the web UI needs no env edit and no restart — the
		// point of the operator's report («нет удобного выставления включения
		// OIDC, пока нет в env строчки нельзя никак настроить»). ApplyConfig is
		// safe to call at any time (it takes the service's config lock) and an
		// empty issuer simply disables the routes again.
		OIDCApplier: func(issuer, clientID, clientSecret, redirectURIs string, enabled bool) {
			if oidcSvc == nil {
				return
			}
			if !enabled {
				issuer = ""
			}
			oidcSvc.ApplyConfig(issuer, clientID, clientSecret, redirectURIs)
			log.Printf("oidc: configuration applied from /admin/oidc (enabled=%v issuer=%s client_id=%s secret_set=%v)",
				enabled && issuer != "", issuer, clientID, clientSecret != "")
		},
		OIDCStatusFn: func() (string, string, string, string) {
			if oidcSvc == nil {
				return "", "", "", ""
			}
			return oidcSvc.ConfigSnapshot()
		},
		// B304 (v1.5.69): key_dir stops being a form field with no effect. The
		// store is moved live (Reload loads or generates the pair at the new path
		// and swaps it under the write lock, so a bad path leaves the running
		// /oidc/jwks.json untouched), and the panel can also REPAIR a store whose
		// boot-time directory could not be created (B270 leaves Keys == nil and the
		// routes answering 503) without an env edit or a restart.
		OIDCKeyDirApplier: func(dir string) error {
			if oidcSvc == nil {
				return fmt.Errorf("the OIDC provider is not initialised in this process")
			}
			if ks := oidcSvc.KeysRef(); ks != nil {
				return ks.Reload(dir)
			}
			ks, err := oidcsvc.NewKeyStore(dir)
			if err != nil {
				return err
			}
			oidcSvc.SetKeys(ks)
			log.Printf("oidc: key store created at %s from /admin/oidc (kid=%s) — the routes stop answering 503 now", dir, ks.KID())
			return nil
		},
		OIDCKeyDirFn: func() (string, string, bool) {
			if oidcSvc == nil {
				return "", "", false
			}
			ks := oidcSvc.KeysRef()
			if ks == nil {
				return "", "", false
			}
			return ks.Dir(), ks.KID(), ks.Ready()
		},
		OIDCDefaultKeyDir: app.OIDCKeyDir,
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
		//
		// B258: SKYGATE_TS_AUTHKEY_FILE matches the entrypoint.sh
		// env var name (the legacy _PATH form is honored for
		// older deployments; new installs should use _FILE
		// to align with the entrypoint). When set to "/dev/null"
		// the entrypoint correctly skips tailscaled AND the
		// UI handler now detects the same sentinel — see
		// tailscaleAuthKeyDisabled() in feature/admin/tailscale.go.
		TailscaleAuthKeyPath: tailscaleEnvOr(
			"SKYGATE_TS_AUTHKEY_FILE",
			tailscaleEnvOr("SKYGATE_TS_AUTHKEY_PATH", "/data/ts/authkey"),
		),
		TailscaleLoginServer: tailscaleEnvOr("SKYGATE_TS_LOGIN_SERVER", "https://head.example.com"),
		// B251: hostname `skygate-host` is reserved for the
		// single VM that runs the skygate container itself
		// (registered via /admin/tailscale). The previous
		// default `skygate-host-1` was a placeholder from
		// the v0.33.1.9 era; v1.5.2 collapses it to the
		// reserved name so BackfillInfra can attribute the
		// node to `infra` strictly on hostname equality.
		TailscaleHostname: tailscaleEnvOr("SKYGATE_TS_HOSTNAME", "skygate-host"),

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
		// B251: align with TailscaleHostname default `skygate-host`.
		SelfHostname: tailscaleEnvOr("SKYGATE_TS_HOSTNAME", "skygate-host"),

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

	// v1.5.2+ / B-mod-core re-merge (2026-09-10) — wire
	// the module.Plugin API Manager.
	//
	// The Manager owns the lifecycle of all skygate modules
	// (Tailscale is Module #1; cluster, telegram, derp, exit
	// are its 4 opt-in sub-features). Modules are registered
	// statically below, initialized (Init validates prereqs +
	// loads state), started (only if state.Enabled=true), and
	// monitored by a 30s health loop. On shutdown the Manager
	// gracefully stops every running module.
	//
	// In B-mod-core + B-mod-tailscale we register the REAL
	// Tailscale module (B-mod-tailscale replaced the original
	// stub from 3d80f573). The 4 sub-features (cluster /
	// telegram / derp / exit) are opt-in via
	// /admin/modules/{name} (B-mod-admin).
	//
	// The audit log callback writes every state transition
	// to the audit_log table (the same one /admin/audit shows)
	// so the operator can see "module.tailscale.init:ok" etc.
	// without SSH'ing into the VM.
	//
	// Pre-B-mod-core re-merge this was reverted in 82c74b38
	// because the Manager required a real SKYGATE_DB_DSN,
	// and the agent was running on bypass-PG (Patroni was
	// stopped at the time). 82c74b38 left the Manager
	// package in place but unwired; the operator can re-enable
	// it as soon as the DB is back. Now (2026-09-10): the
	// svi polygon has PG 18.6 on 13.66 (B-mod-pg-bypass +
	// skygate_test user + DSN), so we can re-enable.
	moduleMgr := module.NewManager(
		"/var/lib/skygate/modules", // dataDir: per-module state
		"/var/run/skygate/modules", // socketDir: per-module sockets (unused in B-mod-core)
		func(action, detail string) {
			// Best-effort audit write. We don't fail
			// the boot if the audit log is unavailable
			// — the audit row is operator debugging,
			// not a correctness requirement.
			_ = db.AppendAuditLogWithTarget(
				app.DB.Current(), 0, "system",
				action, detail, "module", "",
			)
		},
	)
	moduleMgr.SetEnv(collectModuleEnv(cfg))
	// v1.5.2+ / B-mod-core re-merge (2026-09-10): the
	// real Tailscale module (B-mod-tailscale) requires
	// a DBC accessor in ModuleConfig so it can read
	// audit_log + write its own audit rows + check
	// applied_migrations. The Manager.SetDBC setter is
	// the wire for that requirement.
	moduleMgr.SetDBC(func() *sql.DB { return app.DB.Current() })
	if err := moduleMgr.Register(tailscalemod.NewModule()); err != nil {
		log.Printf("warn: register tailscale module: %v", err)
	}
	if err := moduleMgr.InitAll(context.Background()); err != nil {
		log.Printf("warn: module InitAll: %v", err)
	}
	moduleMgr.StartHealthLoop(context.Background())
	defer moduleMgr.StopAll(context.Background())

	// v1.5.2+ / B-mod-admin (2026-09-10) — hand the
	// Manager to the admin Service so /admin/modules
	// can render the list + detail pages and dispatch
	// install/start/stop POSTs.
	adminSvc.Modules = moduleMgr

	// refactor-v0.30 Phase B step 4 (2026-07-29): exit_rules
	// feature service. Owns /my/exit-rules + the /admin/exit-rules
	// sub-actions, the REST API, the advertised-routes sync,
	// the DNS autoupdater, and the route-setup script generator.
	// The Service holds the shared headscale client + DB + cfg
	// + i18n; the *App.SyncAdvertisedRoutes / RunDomainAutoUpdater
	// wrappers route through it via exitRulesRunner (see
	// handlers.go SetExitRulesService).
	exitRulesSvc := &exitrules.Service{
		Backend: app,
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
	// B361 (2026-10-07): wire the ACL trigger the preference-writing surfaces use.
	// `my.PostMyDevicePreferredExit` / `my.PostAdminDevicePreferredExit` (the page
	// an operator uses to re-point a device) and the B356 reconciler all call
	// `exit_rules.ReapplyACLAfterPreferenceChange`, which is THIS method — the same
	// throttled, trigger-agnostic drift check the ownership flips and the rule churn
	// already use. One implementation, one 60s budget: a preference change reaches
	// headscale within a minute instead of waiting for an unrelated throttle, and a
	// burst of changes is still one apply.
	exitrules.SetPreferenceACLReapply(exitRulesSvc.ReapplyACLIfDrifted)
	app.SetExitRulesService(exitRulesSvc)
	// 2026-08-04: v0.33.0 — wire the runtime admin Service
	// into the TestRegistry closures so the /admin/system_tests
	// page can run in-process checks (db integrity, headscale
	// reachability, ACL classification) without re-opening the
	// DB or rebuilding the headscale client.
	adminsvc.SetTestService(adminSvc)

	// B321 (2026-09-25): Tailscale must survive a container recreate.
	//
	// The reference compose pins SKYGATE_TS_AUTHKEY_FILE=/dev/null and Docker freezes
	// the environment at container CREATION, so every update produced a container whose
	// entrypoint skipped tailscaled and the operator had to press Start on
	// /admin/tailscale again. The entrypoint cannot see the operator's decision (it
	// lives in global_settings), so the PROCESS re-applies it: a boot pass plus a
	// 5-minute tick bring the client up when tailscale.desired_state=on, and enforce
	// the resolved hostname (the frozen SKYGATE_TS_HOSTNAME pin must not rename the
	// node back to the legacy skygate-host-1 placeholder). Idempotent and never fatal.
	adminSvc.RunTailscaleAutostart(context.Background())

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
		DB:   d,
		HS:   app.HS,
		Cfg:  app.Config(),
		I18n: app.I18n,
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
	// The route table lives in routes.go (refactor Phase D): a pure move of
	// the block that used to sit here, so this file stays a boot sequence.
	registerRoutes(mux, app, adminSvc, authSvc, mySvc, exitRulesSvc, clusterAPI, deployrunSvc, authMW, apiMW, err)

	// B269: the real server takes over the listener bound at the top of
	// main(); Addr/ListenAndServe are deliberately unused so nothing can
	// re-bind (and re-fail) on a port we already own.
	startup.Enter("handover")
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	startup.Enter("background-services")

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
		// B269 — the socket has been held since the top of main() (the
		// provisional startup handler answered /healthz while routes were
		// being wired). Swapping the handler is the handover: no second
		// bind, no window in which the port is unowned, and therefore no
		// way for the updater's build check or the applier's baseline to
		// observe a running unit with nothing listening.
		handler.Store(handlerBox{mux})
		startup.MarkReady()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			startup.SetFatal(fmt.Sprintf("http serve: %v", err))
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

	// 2026-09-03: v1.5.2 (B229) — preferred-exit
	// auto-reconciler. Runs the per-(user, device)
	// reconciliation logic on a 1h ticker so that
	// `device_rules` rows paired with a missing or stale
	// `device_exit_node_prefs` row get the missing
	// `via:` clause attached to the per-CIDR ACL
	// grants. Without this, the operator's "cyborg →
	// emilia → youtube" rule was ALLOWED but not PINNED
	// (Tailscale routed through the default exit, not
	// emilia). Default behavior: read-only / dry-run
	// (logs what would change without writing). Flip
	// SKYGATE_PREFERRED_RECONCILER_LIVE=true to enable
	// writes. The flag is checked on every tick so the
	// operator can flip it without a redeploy.
	if cfg.PrefReconcileInterval > 0 {
		go app.RunPreferredExitReconciler(ctx, app.Notifier, cfg.PrefReconcileInterval)
	} else {
		log.Printf("preferred-reconciler: SKYGATE_PREFERRED_RECONCILE_INTERVAL=0, skipping startup goroutine (set to a positive duration to re-enable)")
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
		HTTP:       &http.Client{Timeout: 10 * time.Second},
		Current:    version,
		Notified:   make(map[string]bool),
		Notifier:   app.Notifier,
		CheckEvery: 1 * time.Hour,
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
	hsVer := hs.DetectServerVersion(ctx)
	if hsVer.Found() {
		log.Printf("headscale-version: running headscale is %s (via %s)", hsVer.Version, hsVer.Via)
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
