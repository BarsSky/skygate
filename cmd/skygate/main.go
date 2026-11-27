package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"skygate/internal/config"
	"skygate/internal/db"
	_ "skygate/internal/dbmigrate/steps"
	_ "skygate/internal/deployrun/steps"
	"skygate/internal/derpcfg"
	"skygate/internal/derphealth"
	adminsvc "skygate/internal/feature/admin"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/ratelimit"
	"skygate/internal/startup"
	"skygate/internal/telegram"
	"skygate/internal/update"
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
	if len(os.Args) > 1 && runSubcommand() {
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	// B465 — the ONE early decision of a read-only copy. Called here, before any service or
	// scheduler is constructed, because the start sites are scattered across this whole
	// function: the users reconcile cron, the DNS auto-updater, the preferred-exit reconciler,
	// the exit-node and headscale monitors, the update scheduler, the backup schedulers,
	// certsync and the HA chain all sit at different points of it, and the first shape of this
	// fix (a check inside the two scheduler-wiring functions at the END) provably missed most of
	// them on the live restored copy. See config.ApplyReadOnlyCopy for the measurement.
	if off := cfg.ApplyReadOnlyCopy(); off != nil {
		log.Printf("🛑 READ-ONLY COPY: %d writer pass(es) DISABLED in the configuration: %s — this process serves the panel and nothing else", len(off), strings.Join(off, ", "))
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
	_, _ = updateStore.Load() // failure is non-fatal: the next apply rewrites the file
	// B429: install the panel's clock before any renderer or scheduler runs.
	installPanelTimezone(d, cfg)
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
	// 2026-10-17 (B393): resolve a Docker update job that the panel could not
	// finish. The swap subprocess is killed when the old container is
	// removed, so the FIRST process able to witness "did the swap really
	// happen?" is the new one — us. Doing it here (not only on an
	// /admin/update render) means the verdict lands even if nobody logs in:
	// the state file the operator cats, the Telegram alert path and the page
	// all see the same thing.
	//
	// Before B393 the answer came from "does /healthz answer 200?", which the
	// OLD container also answers — so a job whose swap never ran was reported
	// as done. ResolvePendingSwap compares the serving build with the job
	// target and the serving container with the one that requested the swap,
	// and declares `failed` (with the swap log tail) once the grace period
	// has passed with no evidence. No-op for native jobs and terminal states.
	if update.ResolvePendingSwap(context.Background(), updateStore, app.BuildVersion) {
		if st := updateStore.Get(); st != nil {
			log.Printf("update: pending swap resolved at boot — phase=%s (build %s)", st.Phase, app.BuildVersion)
		}
	}
	log.Printf("🌐 Skygate %s (commit %s, built %s)", version, commit, buildTime)

	startup.Enter("routes")
	// B526 step 9: the lifecycle exists before the routes phase, because that phase
	// starts four long-lived services of its own (availability checker, DB-health
	// sampler, metrics collector, modules) whose defer used to live in main().
	lc := &bootLifecycle{}
	defer lc.Stop()
	mux, adminSvc := wireRoutes(cfg, d, app, err, dialectKind, updateStore, lc)
	startup.Enter("handover")
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	startup.Enter("background-services")
	rn := telegram.NewRealNotifier(d.DB)
	bc := bootCtx{ctx: ctx, cfg: cfg, db: d, hs: hs, app: app, rn: rn}
	startBackgroundServices(ctx, cfg, d, app, rn, hs, adminSvc, lc)
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

	wireLateServices(ctx, cfg, d, app, adminSvc, mux, bc)
	<-ctx.Done()
	log.Println("🌐 shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
}

// runSubcommand answers `skygate <command> …` (B526, extracted from main() on 2026-11-27).
//
// It returns true when a command was handled — the caller then returns from main without
// starting the web server; every case here either finishes its job or exits. A false
// return means "no subcommand matched", which today can only happen if this switch grows
// a fallthrough: the `default` case exits with 2, exactly as it did inside main().
//
// The body is a mechanical move of the switch that used to sit inline in main() (2272
// lines made the boot sequence impossible to follow, and the B525 ratchet froze that
// number). No statement was rewritten: `return` became `return true` and the indentation
// dropped one level. Keep it that way — this function is a dispatcher, not a place for
// new boot logic.
// runSubcommand answers `skygate <command> …` (B526). Three-way dispatch: each family
// lives in its own function, so neither this function nor main() becomes the place
// where twenty unrelated verbs pile up. Every family function carries its case blocks
// VERBATIM from the single switch this block split; the unknown-command exit stays here.
