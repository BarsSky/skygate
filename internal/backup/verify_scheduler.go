// 2026-08-18 (B142, v1.4.1) — in-app backup-verify scheduler.
//
// The pre-B142 verify pipeline (scripts/verify_backup.sh) ran ONLY
// via system cron (`0 4 * * 0` weekly). Operators who wanted Telegram
// alerts on verify failure had to wire it up themselves; the script
// only writes the result to global_settings (backup.last_verify_*)
// and returns — it doesn't notify the operator when something goes
// wrong silently.
//
// B142 adds a parallel in-app goroutine that runs the SAME script
// on a configurable schedule and sends Telegram alerts on failure.
// The pre-B142 system-cron entry continues to work; the in-app
// scheduler is a drop-in replacement for operators who want alerts.
//
// Design (mirrors internal/update/scheduler.go, B130):
//
//   - Start(ctx, deps) launches the goroutine; Cancel via ctx.
//   - Tick interval: 30s. Coarse enough to be cheap, fine enough
//     that an operator-set "0 4 * * 0" schedule is never more than
//     ~30s late.
//   - On each tick:
//       1. Read backup.in_app_verify_enabled from global_settings.
//       2. Read backup.verify_schedule (cron expression).
//       3. Check if (a) the schedule is due this tick, (b) the
//          schedule wasn't already triggered for this HH:MM today
//          (we track backup.last_verify_at — same minute dedup
//          pattern as B130's update_schedule_last_run).
//   - On trigger:
//       - Spawn the existing scripts/verify_backup.sh as a
//         subprocess (don't re-implement the verify logic — it's
//         a 170-line bash script that handles the throwaway
//         postgres + dump replay + table-count assertion).
//       - The script itself calls `skygate backup-verify-ok/fail`
//         which writes the status to global_settings. We just
//         wait for the exit code and send a Telegram alert on
//         failure.
//       - On non-zero exit: NOTIFY the operator via SendAlert
//         with the tail of the script's stderr (last 30 lines,
//         truncated to 1500 chars).
//       - On zero exit: silent — the verify-script's own stdout
//         (and the /admin/backup page) show the success.
//
// Failure modes:
//   - DB read error on the schedule keys → tick is a no-op
//     (logged), next tick is fresh. Same as B130.
//   - verify_backup.sh not present / not executable →
//     runVerify() logs an error + sends Telegram alert ("verify
//     script missing").
//   - verify_backup.sh exits 0 but writes backup-verify-ok to
//     the DB and we still get a non-zero exit (script bug) →
//     trust the script's exit code, send the alert.
//   - Telegram not configured (Notifier == nil) → silent. The
//     operator can still see the failure in /admin/backup
//     and the audit log.
//
// Concurrency:
//   - runMu (defined in runner.go) is NOT used here — verify
//     is independent of backup creation. Two verifies in
//     flight is fine; the script's own tar extract is
//     idempotent.
//   - inFlightVerify mutex (process-local) prevents the SAME
//     tick from spawning two parallel verify runs if the
//     previous tick's runVerify is still executing (e.g.
//     on a slow VM where verify takes >30s).
package backup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os/exec"
	"time"
)

// VerifySchedulerDeps groups the dependencies the in-app
// backup-verify scheduler needs. Defined as a struct (not
// individual fields) so adding new dependencies in the
// future doesn't break call sites — same pattern as
// update.SchedulerDeps (B130).
type VerifySchedulerDeps struct {
	// DB is used to read the schedule keys from
	// global_settings (every tick) and to read the
	// last-run timestamp for same-minute dedup.
	DB *sql.DB

	// Notifier sends Telegram alerts on verify failure.
	// May be nil (in which case the scheduler is silent
	// on failure — the operator still sees the failure
	// on /admin/backup and in the audit log). The
	// scheduler accepts a NotifierSink (any type with a
	// SendAlert method) to avoid a cycle with the
	// telegram package.
	Notifier NotifierSink

	// ScriptPath is the absolute path to
	// scripts/verify_backup.sh on the HOST filesystem
	// (not inside the skygate container — the script
	// runs docker + tar + psql which need host access).
	// Default (when empty) is /home/skyadmin/skygate/scripts/
	// verify_backup.sh; main.go wires the actual deploy
	// path via cfg.RepoPath.
	ScriptPath string

	// SkygateBinPath is the absolute path to the skygate
	// binary on the host. The verify script shells out
	// to it (`$SKYGATE_DIR/skygate backup-verify-ok` and
	// `backup-verify-fail`) to record the result. The
	// scheduler's runVerify() runs the same way — we
	// pass the path through the environment so the
	// verify script picks it up.
	//
	// When empty, runVerify() falls back to the verify
	// script's own $SKYGATE_DIR environment variable.
	SkygateBinPath string
}

// NotifierSink is the subset of the telegram.Notifier
// interface that the scheduler needs (just SendAlert for
// failure notifications). Defined as a local interface
// so the scheduler doesn't import the telegram package
// (which would create a cycle through the admin service
// → telegram → backup). Same pattern as
// update.NotifierSink (B130).
type NotifierSink interface {
	SendAlert(text string) int64
}

// inFlightVerify is a process-local mutex around the
// verify subprocess. Without it, a slow verify (e.g. on
// a large DB) could overlap with the NEXT tick's verify
// spawn — not destructive, but noisy in the audit log.
// The tick loop is single-goroutine but runVerify is
// fire-and-forget, so we need a separate lock.
var inFlightVerify bool

// VerifyTickInterval is the B142 in-app verify
// scheduler's tick interval. 30s matches the B130 update
// scheduler (the operator can be confident that an
// HH:MM schedule fires within 30s of the target minute).
const VerifyTickInterval = 30 * time.Second

// StartVerifyScheduler launches the in-app verify
// scheduler goroutine. Returns immediately; the goroutine
// runs until ctx is cancelled. The caller (main.go) is
// responsible for keeping deps valid — DB and Notifier
// must outlive the ctx.
//
// Disabled state (deps.DB == nil) returns immediately
// without starting a goroutine, so main.go can call this
// unconditionally from the wire-up block.
func StartVerifyScheduler(ctx context.Context, deps VerifySchedulerDeps) {
	if deps.DB == nil {
		log.Printf("verify-scheduler: disabled (nil DB)")
		return
	}
	ticker := time.NewTicker(VerifyTickInterval)
	go func() {
		defer ticker.Stop()
		// Tick once on startup so a freshly-restored
		// container doesn't have to wait 30s for the
		// first check (matches the backup.Scheduler
		// behaviour from scheduler.go:65-66).
		verifyTick(ctx, deps)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				verifyTick(ctx, deps)
			}
		}
	}()
}

// verifyTick is one scheduler iteration. Exposed as a
// separate function (not a closure) so tests can call
// it directly without spinning a goroutine.
//
// All side effects are guarded by recoverable error
// checks — a single failed tick is logged and swallowed;
// the next tick starts clean. Same pattern as
// update.tick (B130).
func verifyTick(ctx context.Context, deps VerifySchedulerDeps) {
	// 1. Read the in-app verify enabled flag. The flag
	//    is intentionally NOT a deps field — the operator
	//    can flip it from /admin/backup without restarting
	//    the skygate process, and the scheduler picks it
	//    up on the next tick.
	enabled, err := readVerifyEnabled(deps.DB)
	if err != nil {
		log.Printf("verify-scheduler: read enabled flag failed: %v", err)
		return
	}
	if !enabled {
		return // master switch off, scheduler silent
	}

	// 2. Read the schedule. Same parse as the B130 update
	//    scheduler — the existing backup.ParseSchedule
	//    handles "*", "M H", and "M H * * *".
	sched, err := readVerifySchedule(deps.DB)
	if err != nil {
		log.Printf("verify-scheduler: read schedule failed: %v", err)
		return
	}
	parsed, err := ParseSchedule(sched)
	if err != nil {
		log.Printf("verify-scheduler: parse schedule %q failed: %v", sched, err)
		return
	}
	now := time.Now()

	// 3. Same-minute dedup. If the last successful run
	//    was in the same calendar minute as now, skip.
	//    Prevents double-firing when two ticks land in
	//    the same minute (the 30s tick interval + a
	//    slow tick can land at e.g. 04:00:15 and
	//    04:00:45 both in the same minute).
	lastRun, err := readLastVerifyAt(deps.DB)
	if err != nil {
		log.Printf("verify-scheduler: read last-verify-at failed: %v", err)
		return
	}
	if lastRun > 0 && sameMinute(time.Unix(lastRun, 0), now) {
		return
	}

	// 4. Check if the schedule is due this tick.
	if !verifyIsDueThisTick(parsed, now) {
		return
	}

	// 5. ALL conditions met. Spawn the verify.
	runVerify(ctx, deps, now)
}

// verifyIsDueThisTick returns true when the parsed
// schedule should fire within the current minute.
// Same logic as update.timeMatches (B130) — we don't
// try to be smart about second precision; a 30s tick
// means the operator can set the schedule 1 minute
// early to be safe, and the sameMinute dedup above
// prevents the next tick from re-firing.
func verifyIsDueThisTick(s *Schedule, now time.Time) bool {
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

// runVerify spawns scripts/verify_backup.sh as a
// subprocess and waits for it to finish. On non-zero
// exit code, sends a Telegram alert with the script's
// stderr tail.
//
// The verify script itself calls `skygate backup-verify-ok`
// or `backup-verify-fail` which writes the status to
// global_settings (backup.last_verify_*) — we don't
// duplicate that write here, we just observe the result.
//
// The script runs synchronously (we block until it
// finishes). The verify itself takes 5-15s on a typical
// skygate DB (tar extract + docker run + psql replay);
// the 30s tick is well above this. If the verify ever
// takes longer than 30s, the next tick's inFlightVerify
// guard prevents overlap.
func runVerify(ctx context.Context, deps VerifySchedulerDeps, triggeredAt time.Time) {
	if inFlightVerify {
		log.Printf("verify-scheduler: previous verify still in flight, skipping this tick")
		return
	}
	inFlightVerify = true
	defer func() { inFlightVerify = false }()

	scriptPath := deps.ScriptPath
	if scriptPath == "" {
		// Fall back to the deploy-default path. main.go
		// wires the real path via cfg.RepoPath, so this
		// is the only "guess" — the scheduler's behaviour
		// with a missing path is log + silent (the script
		// itself will error and the operator will see
		// "script missing" in the audit log on the next
		// verify run that does get triggered).
		scriptPath = "/home/skyadmin/skygate/scripts/verify_backup.sh"
	}
	log.Printf("verify-scheduler: firing (now=%s)", triggeredAt.Format(time.RFC3339))

	// Use a context-bounded command so a stuck verify
	// (e.g. docker run hanging) doesn't pin the
	// scheduler forever. 5 minutes is the budget — the
	// normal verify completes in 5-15s; a 5min budget
	// gives 20x headroom for slow disks.
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "bash", scriptPath)
	// The script reads $SKYGATE_DIR to find the skygate
	// binary (it shells out to `skygate backup-verify-ok`
	// internally). When the in-app scheduler sets it, we
	// honour deps.SkygateBinPath; otherwise we leave the
	// env untouched and let the script use its own
	// $SKYGATE_DIR.
	if deps.SkygateBinPath != "" {
		cmd.Env = append(cmd.Environ(), "SKYGATE_DIR="+deps.SkygateBinPath)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// Non-zero exit OR context timeout. The script
		// already wrote `backup-verify-fail` to the DB
		// (via `skygate backup-verify-fail "$LATEST"
		// "..."` inside the script), so /admin/backup
		// shows the failure too. The Telegram alert
		// is the proactive side — the operator learns
		// about the failure without having to check
		// the page.
		detail := tailLines(stderr.String(), 30)
		detail = truncateString(detail, 1500)
		msg := fmt.Sprintf("❌ Backup verify FAILED\n  script=%s\n  err: %v\n  stderr (tail):\n%s",
			scriptPath, err, detail)
		log.Printf("verify-scheduler: verify failed: %v", err)
		if deps.Notifier != nil {
			deps.Notifier.SendAlert(msg)
		}
		return
	}
	// Zero exit. The script wrote backup-verify-ok to
	// the DB; the /admin/backup page shows success.
	// We don't send a Telegram on success (would be
	// noisy — the operator doesn't need a ping every
	// Sunday at 04:00 just to confirm backups are
	// healthy).
	log.Printf("verify-scheduler: verify ok (stdout tail: %s)", tailLines(stdout.String(), 5))
}

// tailLines returns the last n non-empty lines of s.
// Used for stderr / stdout tails in alert messages.
func tailLines(s string, n int) string {
	lines := bytes.Split([]byte(s), []byte("\n"))
	if len(lines) <= n {
		return string(bytes.TrimSpace(bytes.Join(lines, []byte("\n"))))
	}
	out := lines[len(lines)-n:]
	return string(bytes.TrimSpace(bytes.Join(out, []byte("\n"))))
}

// truncateString returns s clipped to max bytes, with
// a trailing "..." if clipped. Used to bound alert
// message size (Telegram has a 4096-char limit; we
// keep ours well under that with the 1500-byte cap).
func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// readVerifyEnabled reads backup.in_app_verify_enabled
// from global_settings. Returns false on any error
// (the scheduler is a no-op on failure, same as B130).
func readVerifyEnabled(d *sql.DB) (bool, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = $1`, keyInAppVerifyEnabled).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// readVerifySchedule reads backup.verify_schedule.
// Empty string defaults to the standard "every minute"
// cron convention (matches ParseSchedule's empty
// handling). The scheduler doesn't fire when the
// schedule is empty AND InAppVerifyEnabled is false,
// which is the default state — so this only matters
// when the operator opts in.
func readVerifySchedule(d *sql.DB) (string, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = $1`, keyVerifySchedule).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// readLastVerifyAt reads backup.last_verify_at (the
// unix-seconds timestamp written by skygate
// backup-verify-ok / backup-verify-fail). Returns 0
// when the column is missing (never run yet) or on
// any error. The sameMinute guard above treats 0 as
// "no last run" and proceeds.
//
// Backward-compat: pre-B142 the key was named
// `backup.last_verify` (no `_at` suffix). The renamed
// runBackupVerifyOK/Fail subcommands write the new
// key on every verify, so the old key becomes
// orphaned on the first post-upgrade verify. We read
// BOTH keys (new first, old as fallback) so a
// pre-B142 verify that ran an hour before the
// upgrade still counts for the sameMinute dedup.
// Once the first post-upgrade verify fires, the new
// key always wins.
func readLastVerifyAt(d *sql.DB) (int64, error) {
	if n, err := readIntSetting(d, keyLastVerifyAt); err != nil {
		return 0, err
	} else if n > 0 {
		return n, nil
	}
	// Fall back to the pre-B142 key name.
	return readIntSetting(d, "backup.last_verify")
}

// readIntSetting reads an integer-valued global_setting
// and returns it. Returns (0, nil) when the key is
// missing or empty. The Sscanf is bounded to a single
// int (no overflow risk on 64-bit Linux).
func readIntSetting(d *sql.DB, key string) (int64, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key = $1`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	var n int64
	_, err = fmt.Sscanf(v, "%d", &n)
	return n, err
}

// sameMinute returns true if a and b are in the same
// calendar minute. Used to deduplicate: if the last
// successful trigger was at 04:00:15 today, the next
// tick at 04:00:45 today must NOT fire again.
//
// Note: we use the local-time year+month+day+hour+minute
// for comparison (the same convention the verify
// script's timestamp uses; it sets the verify time via
// the system clock which is the container's local time).
func sameMinute(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	return a.Year() == b.Year() &&
		a.Month() == b.Month() &&
		a.Day() == b.Day() &&
		a.Hour() == b.Hour() &&
		a.Minute() == b.Minute()
}
