// Command-line subcommands of the skygate binary.
//
// These are the `skygate <verb>` entry points that run WITHOUT starting the web
// server: the migrate-only hook the container orchestrator calls before the
// swap (B261), the backup/verify/cleanup one-shots cron and the operator invoke,
// and the CLI mirrors of /admin/deploy and /admin/ha (B150). They were the tail
// of cmd/skygate/main.go (refactor Phase D, 2026-10-02); the dispatch that
// reaches them is still in main().

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"skygate/internal/backup"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/deploy"
	"skygate/internal/mesh"
	"time"
)

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
	// v1.5.4 restored SQLite; the DSN decides the dialect (same
	// hardcoded-label bug as the startup banner above).
	log.Printf("migrate-only: opening %s (DSN=%s...)", db.DetectDSN(cfg.DBDSN).Kind, redactPGPassword(cfg.DBDSN))
	d, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
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
	d, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
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
//
//	destination=<path-or-URL>
//	protocol=<local|smb|nfs|sftp>
//	enabled=<true|false>
//	last_status=<ok|fail|running|"">
//	last_archive=<basename|"">
//
// Missing keys print with empty value (the calling script
// treats empty destination as "backup not configured" → no-op).
func runBackupShowConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only. cfg.DBDSN is required.
	d, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
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
//
//	[0] = archive basename (e.g. "skygate-full-20260818_030000.tar.gz")
func runBackupVerifyOK(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only. cfg.DBDSN is required.
	d, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
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
//
//	[0] = archive basename (for the log detail)
//	[1] = error message (from sqlite3 output)
func runBackupVerifyFail(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// v1.3.0: PG-only. cfg.DBDSN is required.
	d, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
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
	d, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
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
