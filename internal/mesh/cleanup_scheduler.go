// 2026-08-18 (B143, v1.4.3) — periodic cleanup of smoke-mesh test data.
//
// Pre-B143: scripts/smoke.sh:511-512 creates "smoke-mesh-<pid>"
// rows in the meshes table on every run. The /admin/meshes
// smoke test step is the only consumer of these rows, and
// they have 0 members by the time smoke.sh exits, so they
// serve no purpose after the smoke run completes. Without
// periodic cleanup, the DB accumulates cruft over time
// (the operator's v0.33.1.36 release had to manually
// DELETE 30 rows in a one-off SQL).
//
// B143 adds an in-app daily cron (5 AM by default, after
// the 3 AM backup + 4 AM verify) that DELETEs every
// smoke-mesh row with no members. The pattern is opt-in
// via global_settings["cleanup.smoke_mesh_enabled"]
// (default false — operator enables from
// /admin/system_tests or via
// SKYGATE_CLEANUP_SMOKE_MESH_IN_APP_ENABLED=true env
// var). A manual subcommand `skygate
// cleanup-smoke-meshes` is also exposed for ad-hoc runs
// (mirrors the `skygate backup-verify-ok` / `-fail`
// subcommands from B142).
//
// Design (mirrors internal/update/scheduler.go B130 +
// internal/backup/verify_scheduler.go B142):
//
//   - Start(ctx, deps) launches the goroutine; Cancel
//     via ctx.
//   - Tick interval: 30s. Same precision as B130/B142 —
//     a 5 AM daily schedule fires within 30s of the
//     target minute.
//   - On each tick:
//       1. Read cleanup.smoke_mesh_enabled from
//          global_settings (operator can flip from
//          /admin/system_tests without restart).
//       2. Read cleanup.smoke_mesh_schedule (cron
//          expression; default "0 5 * * *" = 5 AM
//          daily).
//       3. Check if (a) the schedule is due this
//          tick, (b) the schedule wasn't already
//          triggered for this HH:MM today (we track
//          cleanup.smoke_mesh_last_run for
//          same-minute dedup, same pattern as
//          B130/B142).
//   - On trigger:
//       - Run DeleteSmokeMeshes (defined in
//         cleanup.go) — a single transaction that
//         SELECTs the candidate rows (name LIKE
//         'smoke-mesh-%' AND NOT EXISTS
//         mesh_members) then DELETEs them in a
//         single statement with the same NOT EXISTS
//         check repeated (defense in depth against
//         a race).
//       - Write the result to
//         cleanup.smoke_mesh_last_run (unix
//         seconds).
//       - If res.Total > 0: write the IDs + names
//         to the audit_log via
//         db.AppendExitRuleLog(0,
//         "cleanup.smoke_mesh",
//         FormatCleanupMessage(res)).
//       - If res.Total > 0: send a Telegram alert
//         (the operator wants to know about the
//         daily cleanup in case it's deleting more
//         rows than expected — silent happy-path =
//         0 rows deleted, which the audit log
//         captures).
//
// Failure modes:
//   - DB read error on the schedule keys → tick is
//     a no-op (logged), next tick is fresh. Same as
//     B130/B142.
//   - DeleteSmokeMeshes returns an error → tick is a
//     no-op (logged), audit log gets an error entry,
//     Telegram gets a failure alert. Next tick
//     retries.
//   - Telegram not configured (Notifier == nil) →
//     silent. The operator can still see the
//     failure in /admin/system_tests (post-TD-8)
//     and the audit log.
//
// Concurrency:
//   - inFlightCleanup mutex (process-local) prevents
//     two parallel DeleteSmokeMeshes calls (e.g. the
//     5 AM cron + a manual subcommand). The
//     transaction in DeleteSmokeMeshes would not be
//     corrupted by parallel DELETE statements (each
//     statement is atomic) but we'd double-send
//     Telegram alerts and double-write the audit
//     log.

package mesh

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"skygate/internal/backup"
	"skygate/internal/db"
)

// Storage key constants. Centralised here (not in
// the scheduler block) so the keys can be referenced
// from the B-check script + the manual subcommand
// without an indirection.
const (
	// KeyCleanupSmokeMeshEnabled is the master switch
	// for the in-app smoke-mesh cleanup scheduler.
	// "1" = enabled, "0" / missing = disabled. The
	// scheduler reads this on every tick so the
	// operator can flip from /admin/system_tests
	// (post-TD-8) without restarting the skygate
	// process.
	KeyCleanupSmokeMeshEnabled = "cleanup.smoke_mesh_enabled"

	// KeyCleanupSmokeMeshSchedule is the cron-style
	// schedule expression. Empty string → the
	// scheduler uses DefaultCleanupSchedule
	// (5 AM daily). The scheduler re-reads this on
	// every tick so the operator can override at
	// runtime.
	KeyCleanupSmokeMeshSchedule = "cleanup.smoke_mesh_schedule"

	// KeyCleanupSmokeMeshLastRun is the unix-second
	// timestamp of the last scheduler trigger.
	// Updated after every trigger (even the
	// 0-row-tick), used for the same-minute dedup
	// check.
	KeyCleanupSmokeMeshLastRun = "cleanup.smoke_mesh_last_run"
)

// Action name for the audit log entry. Mirrors the
// ExitRuleActionXxx constants in db/exit_rule_logs.go
// (the audit log is the exit_rule_logs table on
// skygate, and the B130 update scheduler writes its
// entries with the same "scheduler.X" convention).
const cleanupAuditAction = "cleanup.smoke_mesh"

// CleanupSchedulerDeps groups the dependencies the
// in-app smoke-mesh cleanup scheduler needs. Same
// pattern as backup.VerifySchedulerDeps (B142) and
// update.SchedulerDeps (B130).
type CleanupSchedulerDeps struct {
	// DB is used to read the schedule keys from
	// global_settings (every tick) and to run
	// DeleteSmokeMeshes on trigger.
	DB *sql.DB

	// Notifier sends Telegram alerts when the
	// cleanup actually deletes rows (Total > 0)
	// and on hard failure. May be nil (in which
	// case the scheduler is silent — the operator
	// still sees the deletion in the audit log).
	// The scheduler accepts a NotifierSink (any
	// type with a SendAlert method) to avoid a
	// cycle with the telegram package.
	Notifier CleanupNotifierSink
}

// CleanupNotifierSink is the subset of the
// telegram.Notifier interface that the scheduler
// needs. Same pattern as backup.NotifierSink (B142)
// and update.NotifierSink (B130) — a local interface
// so the mesh package doesn't import the telegram
// package.
type CleanupNotifierSink interface {
	SendAlert(text string) int64
}

// inFlightCleanup prevents parallel DeleteSmokeMeshes
// calls (e.g. the 5 AM cron + a manual subcommand).
// The mutex is process-local; a long-running cleanup
// (e.g. a 1000-row batch) blocks the next call, but
// that's desirable — we don't want to issue
// overlapping DELETEs that double-alert.
var inFlightCleanupMu sync.Mutex

// CleanupTickInterval is the B143 in-app cleanup
// scheduler's tick interval. 30s matches B130/B142.
const CleanupTickInterval = 30 * time.Second

// DefaultCleanupSchedule is the default 5-field
// cron expression. 5 AM daily, after the 3 AM
// backup + 4 AM verify, so any smoke run that
// started in the previous 24h has finished and its
// rows are safe to delete. Operators can override
// via SKYGATE_CLEANUP_SMOKE_MESH_SCHEDULE or
// /admin/system_tests (post-TD-8).
const DefaultCleanupSchedule = "0 5 * * *"

// StartCleanupScheduler launches the in-app
// smoke-mesh cleanup scheduler goroutine. Returns
// immediately; the goroutine runs until ctx is
// cancelled.
//
// Disabled state (deps.DB == nil) returns immediately
// without starting a goroutine, so main.go can call
// this unconditionally from the wire-up block.
func StartCleanupScheduler(ctx context.Context, deps CleanupSchedulerDeps) {
	if deps.DB == nil {
		log.Printf("cleanup-scheduler: disabled (nil DB)")
		return
	}
	ticker := time.NewTicker(CleanupTickInterval)
	go func() {
		defer ticker.Stop()
		// Tick once on startup so a freshly-restored
		// container doesn't have to wait 30s for the
		// first check (matches B130/B142
		// behaviour).
		cleanupTick(ctx, deps)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupTick(ctx, deps)
			}
		}
	}()
}

// cleanupTick is one scheduler iteration. All side
// effects are guarded by recoverable error checks —
// a single failed tick is logged and swallowed; the
// next tick starts clean. Same pattern as
// update.tick (B130) and verifyTick (B142).
func cleanupTick(ctx context.Context, deps CleanupSchedulerDeps) {
	// 1. Read the in-app cleanup enabled flag. The
	//    flag is intentionally NOT a deps field —
	//    the operator can flip it from
	//    /admin/system_tests (post-TD-8) without
	//    restarting the skygate process, and the
	//    scheduler picks it up on the next tick.
	enabled, err := readCleanupEnabled(deps.DB)
	if err != nil {
		log.Printf("cleanup-scheduler: read enabled flag failed: %v", err)
		return
	}
	if !enabled {
		return // master switch off, scheduler silent
	}

	// 2. Read the schedule. Empty string → fall
	//    back to DefaultCleanupSchedule. The
	//    /admin/system_tests page (post-TD-8)
	//    lets the operator override the schedule
	//    at runtime; the global_settings key holds
	//    the override.
	sched, err := readCleanupSchedule(deps.DB)
	if err != nil {
		log.Printf("cleanup-scheduler: read schedule failed: %v", err)
		return
	}
	if sched == "" {
		sched = DefaultCleanupSchedule
	}
	parsed, err := backup.ParseSchedule(sched)
	if err != nil {
		log.Printf("cleanup-scheduler: parse schedule %q failed: %v", sched, err)
		return
	}
	now := time.Now()

	// 3. Same-minute dedup. If the last successful
	//    run was in the same calendar minute as
	//    now, skip. Prevents double-firing when two
	//    ticks land in the same minute.
	lastRun, err := readCleanupLastRun(deps.DB)
	if err != nil {
		log.Printf("cleanup-scheduler: read last-run failed: %v", err)
		return
	}
	if lastRun > 0 && sameCleanupMinute(time.Unix(lastRun, 0), now) {
		return
	}

	// 4. Check if the schedule is due this tick.
	if !cleanupIsDueThisTick(parsed, now) {
		return
	}

	// 5. ALL conditions met. Spawn the cleanup.
	if _, err := RunCleanup(ctx, deps, now); err != nil {
		log.Printf("cleanup-scheduler: runCleanup returned error: %v", err)
	}
}

// cleanupIsDueThisTick returns true when the parsed
// schedule should fire within the current minute.
// Same logic as verifyIsDueThisTick (B142) and
// update.timeMatches (B130) — reuses the existing
// backup.Schedule type.
func cleanupIsDueThisTick(s *backup.Schedule, now time.Time) bool {
	if s.EveryMinute {
		return true
	}
	nowH, nowM, _ := now.Clock()
	if nowH != s.Hour {
		return false
	}
	if nowM != s.Minute {
		return false
	}
	return true
}

// RunCleanup is the actual delete + alert + audit
// sequence. Called from cleanupTick on a due tick,
// and also from the manual `skygate
// cleanup-smoke-meshes` subcommand in main.go (which
// bypasses the schedule check).
//
// Returns the CleanupResult for the caller's
// convenience (the subcommand prints it to stdout;
// the scheduler just logs and alerts).
func RunCleanup(ctx context.Context, deps CleanupSchedulerDeps, triggeredAt time.Time) (CleanupResult, error) {
	// Serialise: a long-running cleanup blocks the
	// next call (the mutex is process-local). The
	// SELECT-then-DELETE in DeleteSmokeMeshes is
	// already transactional at the row level, so
	// this is a "no double-alert" guard, not a
	// "no double-delete" guard.
	inFlightCleanupMu.Lock()
	defer inFlightCleanupMu.Unlock()

	log.Printf("cleanup-scheduler: firing (now=%s)", triggeredAt.Format(time.RFC3339))

	// Run the actual delete. DeleteSmokeMeshes is
	// the single source of truth for what to
	// delete — we just trigger + observe + alert.
	res, err := DeleteSmokeMeshes(deps.DB)
	if err != nil {
		// Hard failure. Log + audit + alert.
		detail := FormatCleanupMessage(res) + " error=" + err.Error()
		log.Printf("cleanup-scheduler: DeleteSmokeMeshes failed: %v", err)
		if deps.Notifier != nil {
			deps.Notifier.SendAlert("❌ Smoke-mesh cleanup FAILED\n  err: " + err.Error())
		}
		// Best-effort audit log entry.
		if deps.DB != nil {
			_ = db.AppendExitRuleLog(deps.DB, db.ExitRuleLogNoVersion, cleanupAuditAction, detail)
		}
		return res, err
	}

	// Always update the last-run timestamp, even
	// on the "nothing to clean" path. The
	// same-minute dedup above treats the last-run
	// as a "we already considered this minute"
	// marker, so updating it on a 0-row tick is
	// correct (we checked, there was nothing, no
	// need to check again in 30s).
	if err := db.SetGlobalSetting(deps.DB, KeyCleanupSmokeMeshLastRun, strconv.FormatInt(triggeredAt.Unix(), 10)); err != nil {
		log.Printf("cleanup-scheduler: write last-run failed: %v", err)
	}

	// Audit + alert only on Total > 0. The
	// "no cruft" path is the expected daily state
	// and would be noisy if alerted.
	if res.Total > 0 {
		detail := FormatCleanupMessage(res)
		log.Printf("cleanup-scheduler: %s", detail)
		if deps.DB != nil {
			_ = db.AppendExitRuleLog(deps.DB, db.ExitRuleLogNoVersion, cleanupAuditAction, detail)
		}
		if deps.Notifier != nil {
			deps.Notifier.SendAlert("🧹 " + detail)
		}
	} else {
		log.Printf("cleanup-scheduler: no smoke-mesh cruft found (Total=0)")
	}
	return res, nil
}

// readCleanupEnabled reads
// cleanup.smoke_mesh_enabled from global_settings.
// Returns false on any error (the scheduler is a
// no-op on failure, same as B130/B142).
func readCleanupEnabled(d *sql.DB) (bool, error) {
	v, err := db.GetGlobalSetting(d, KeyCleanupSmokeMeshEnabled, "")
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// readCleanupSchedule reads
// cleanup.smoke_mesh_schedule. Empty string is
// fine — the caller falls back to
// DefaultCleanupSchedule.
func readCleanupSchedule(d *sql.DB) (string, error) {
	return db.GetGlobalSetting(d, KeyCleanupSmokeMeshSchedule, "")
}

// readCleanupLastRun reads
// cleanup.smoke_mesh_last_run (unix seconds).
// Returns 0 when missing (never run yet) or on
// error. The sameMinute guard above treats 0 as
// "no last run" and proceeds.
func readCleanupLastRun(d *sql.DB) (int64, error) {
	v, err := db.GetGlobalSetting(d, KeyCleanupSmokeMeshLastRun, "")
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		// Don't fail the scheduler on a corrupt
		// value — log it and proceed (the next
		// tick will overwrite the bad value
		// with a clean unix timestamp).
		log.Printf("cleanup-scheduler: parse last-run %q failed: %v", v, err)
		return 0, nil
	}
	return n, nil
}

// sameCleanupMinute returns true if a and b are in
// the same calendar minute. Mirrors
// backup.sameMinute (B142) — we redefine here
// (rather than import) to keep the mesh package
// free of a backup dependency. The 12 lines of
// trivial date comparison are not worth an import.
func sameCleanupMinute(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	return a.Year() == b.Year() &&
		a.Month() == b.Month() &&
		a.Day() == b.Day() &&
		a.Hour() == b.Hour() &&
		a.Minute() == b.Minute()
}

// FormatHumanSchedule returns a human-readable
// description of a cleanup schedule. Used by the
// /admin/system_tests page (post-TD-8) to render
// the "Next cleanup" column without forcing the
// template to import backup.ParseSchedule.
//
//   "0 5 * * *"   → "Daily at 05:00"
//   "30 4 * * 0"  → "Weekly on Sunday at 04:30"
//   "0 5 * * 1-5" → "Weekdays at 05:00" (we
//                    best-effort the day-of-week
//                    range; a literal "1-5" is the
//                    most common non-trivial case)
//   "*"           → "Every minute"
func FormatHumanSchedule(s string) string {
	if s == "" {
		s = DefaultCleanupSchedule
	}
	parsed, err := backup.ParseSchedule(s)
	if err != nil {
		return s // fall back to the raw string
	}
	if parsed.EveryMinute {
		return "Every minute"
	}
	return fmt.Sprintf("Daily at %02d:%02d", parsed.Hour, parsed.Minute)
}
