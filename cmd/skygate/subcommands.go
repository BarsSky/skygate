// subcommands.go — B526 (2026-11-27): the `skygate <command>` dispatchers.
//
// They used to live inside main() (a 2 272-line function whose first act was this
// switch). B526 moved them out in two mechanical steps — first into runSubcommand()
// in main.go, then into this file, because B372 caps main.go at 2 394 lines and the
// wrappers alone pushed it to 2 411. Every case block is VERBATIM from the original
// switch; the only rewrite was `return` -> `return true`. Keep it a dispatcher.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

func runSubcommand() bool {
	switch os.Args[1] {
	case "backup-run", "backup-show-config", "backup-verify-ok", "backup-verify-fail":
		return runSubcommandBackup()
	case "cleanup-smoke-meshes", "acl-apply", "derp-probe", "derp-metrics-proxy", "dns-records", "dns-set-a", "prefix-owner", "regapi-credentials":
		return runSubcommandOps()
	default:
		return runSubcommandCluster()
	}
}

// runSubcommandBackup handles the backup verbs: they read the same config from the DB
// and are called by scripts (backup_cron.sh, verify_backup.sh), never by the server.
func runSubcommandBackup() bool {
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
		return true
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
		return true
	case "backup-verify-ok":
		// Mark the latest verify_backup run as
		// "ok" in the global_settings so the
		// /admin/backup page shows the freshness.
		// 2026-08-11: v0.33.1.42 B2.
		if err := runBackupVerifyOK(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "backup-verify-ok failed: %v\n", err)
			os.Exit(1)
		}
		return true
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
		return true
	}
	return false
}

// runSubcommandOps handles the one-shot operational CLIs (exit rules, DERP, DNS
// records, prefix ownership) that an operator runs from a shell.
func runSubcommandOps() bool {
	switch os.Args[1] {
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
		return true
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
		return true
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
		return true
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
				return true
			}
			fmt.Fprintf(os.Stderr, "derp-metrics-proxy failed: %v\n", err)
			os.Exit(2)
		}
		return true
	case "dns-records", "dns-set-a", "prefix-owner":
		// B468/B470 — operator CLI verbs that act on the LIVE installation with the same code
		// paths the panel uses (DNS A-record flip; prefix ownership pins + rule relay moves).
		if err := runOpsCLI(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "%s failed: %v\n", os.Args[1], err)
			os.Exit(1)
		}
		return true
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
		return true
	}
	return false
}

// runSubcommandCluster handles everything else: deploy/ha/cluster/init/join/migrate/
// version/help, plus the unknown-command exit that belonged to the inline switch.
func runSubcommandCluster() bool {
	switch os.Args[1] {
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
		return true
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
		return true
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
		return true
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
		return true
	case "version", "--version", "-v":
		fmt.Printf("skygate %s (commit %s, built %s)\n", version, commit, buildTime)
		return true
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
		return true
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
		return true
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
		return true
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
		return true
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
		return true
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
		fmt.Println("  dns-records            print the zone's records and the A record a switch would move (B468)")
		fmt.Println("  dns-set-a              point one A record at a host, verify it, audit it (--dry-run available) (B468)")
		fmt.Println("  prefix-owner           which relay serves a prefix: list / pin / move the rules that claim it (B470)")
		fmt.Println("  oidc-export            Print the OIDC config headscale needs (env block or headscale block) (B304)")
		fmt.Println("  init [verb]             cluster bootstrap CLI: bootstrap / status / standby-invite (B211)")
		fmt.Println("  join [verb]             cluster join CLI: <token> / status (B212 — DSN bootstrap + next-steps)")
		fmt.Println("  migrate [verb]          in-DB schema migration CLI: up / status (B213); 'down' is a stub")
		fmt.Println("  migrate-only            open the DB + run pending migrations, then exit (v0.33.1.21 — alias for 'migrate up')")
		fmt.Println("  version                 print build version")
		fmt.Println("  help                    this help")
		return true
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (try `skygate help`)\n", os.Args[1])
		os.Exit(2)
	}
	return false
}
