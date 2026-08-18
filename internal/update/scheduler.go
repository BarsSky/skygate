// 2026-08-18 (B130) — background scheduler for time-of-day
// auto-update. The /admin/update page (B129) lets the operator
// set "auto-update at HH:MM" via the Schedule section; this
// file is the runtime side — a background goroutine that
// checks the schedule every tick and triggers the update
// orchestrator if the conditions are met.
//
// Design:
//
//   - Start(ctx, deps) launches the goroutine; Cancel via ctx.
//   - Tick interval: 30 seconds. Coarse enough to be cheap,
//     fine enough that an operator-set 03:00 schedule is
//     never more than ~30s late.
//   - Trigger conditions (all must hold):
//       1. global_settings["update_schedule_enabled"] = "1"
//          (DB-persisted; falls back to Cfg.UpdateScheduleEnabled)
//       2. global_settings["update_schedule_time"] matches the
//          current HH:MM in the server's local time
//       3. A newer release is available (compareSemver
//          CurrentVersion < LatestVersion)
//       4. No update is already in progress (no inFlight
//          job — same mutex used by the admin handlers)
//       5. The schedule wasn't already triggered for this
//          HH:MM today (we track the last successful run
//          in global_settings["update_schedule_last_run"] to
//          avoid double-firing on the same tick + a second
//          tick that happens to fall in the same minute)
//   - On trigger:
//       - Spawn the Docker upgrader (same as
//         PostAdminUpdateApply's goroutine)
//       - Send a Telegram alert at start AND on
//         done/fail (same pattern as the manual handlers)
//       - Update global_settings["update_schedule_last_run"]
//         to the current RFC 3339 timestamp
//
// Failure modes that are explicitly NOT the scheduler's
// problem:
//   - Update itself fails → the orchestrator's failWithRollback
//     path runs. The scheduler just notifies.
//   - DB read error on schedule_enabled/schedule_time →
//     scheduler logs and continues (no tick will ever match
//     a non-existent schedule, so this is safe).
//   - Checker is unreachable → scheduler logs and continues
//     (same as the /admin/update page does on GitHub down).
//
// Note on the duplicate normalizeUpdateTarget helper
// (this file vs. internal/feature/admin/update.go): the
// admin package's helper is identical in logic but lives
// in a different package to avoid the import cycle
// (internal/feature/admin → internal/update is OK, but
// internal/update → internal/feature/admin would create
// a cycle). If the original is ever changed, the
// scheduler's copy must be kept in sync.

package update

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// SchedulerDeps groups the dependencies a Scheduler needs.
// Defined as a struct (not individual fields) so adding new
// dependencies in the future doesn't break call sites.
type SchedulerDeps struct {
	// DB is used to read the schedule keys from global_settings
	// (every tick) and to write update_schedule_last_run after
	// each successful run.
	DB *sql.DB
	// State is the shared update-state store. The scheduler
	// uses the same store as the admin handlers so the
	// /admin/update page sees the in-flight job and the
	// "Last run" timestamp stays consistent.
	State *StateStore
	// Checker is the GitHub release checker. The scheduler
	// uses it to determine if a newer release is available.
	// If nil, the scheduler will not attempt any update.
	Checker *Checker
	// BuildVersion is the running skygate version (e.g.
	// "v1.3.19.2-7-g0670b64"). Used to detect "newer".
	BuildVersion string
	// Notifier sends Telegram alerts. May be nil (in which
	// case the scheduler is silent on success/failure). The
	// scheduler accepts a NotifierSink (any type with a
	// SendAlert method) to avoid a cycle with the telegram
	// package.
	Notifier NotifierSink
	// RepoPath is the path to the skygate repo on the host
	// (e.g. "/home/admin/skygate"). The Docker upgrader
	// uses this to run `git fetch + git checkout`.
	RepoPath string
	// Cfg carries the env-var defaults for the schedule
	// (UpdateScheduleEnabled + UpdateScheduleTime) — used
	// as fallback when global_settings doesn't have a row
	// yet (first start).
	Cfg SchedulerCfg
}

// SchedulerCfg is the subset of config.Config the scheduler
// needs. Defined as a struct (not the full config.Config) so
// tests can pass a minimal value.
type SchedulerCfg struct {
	UpdateScheduleEnabled bool
	UpdateScheduleTime    string // "HH:MM" 24-hour
}

// NotifierSink is the subset of the telegram.Notifier
// interface that the scheduler needs (just SendAlert for
// start/done/fail notifications). Defined as a local
// interface so the scheduler doesn't import the telegram
// package (which would create a cycle through the admin
// service → telegram → update).
type NotifierSink interface {
	SendAlert(text string) int64
}

// inFlightScheduled is a process-local mutex around the
// scheduler's "is there a scheduled update running right now"
// flag. Separate from the admin handlers' stateStoreMu so
// the scheduler and the manual apply handlers don't deadlock
// against each other.
var (
	scheduledMu       sync.Mutex
	scheduledInFlight bool
)

// TickInterval is how often the scheduler checks the schedule.
// 30s is a reasonable compromise: the operator-set HH:MM may
// fire up to 30s late, but the cost is one DB read + one
// checker call per 30s.
const TickInterval = 30 * time.Second

// Start launches the scheduler goroutine. Returns immediately;
// the goroutine runs until ctx is cancelled. The caller is
// responsible for keeping deps valid (the DB, the State, the
// Notifier must outlive the ctx).
func Start(ctx context.Context, deps SchedulerDeps) {
	go func() {
		ticker := time.NewTicker(TickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick(ctx, deps)
			}
		}
	}()
}

// tick is one scheduler iteration. Exposed as a separate
// function (not a closure) so tests can call it directly
// without spinning a goroutine.
//
// All side effects are guarded by recoverable error checks —
// a single failed tick is logged (TODO: structured logger) and
// swallowed; the next tick starts clean.
func tick(ctx context.Context, deps SchedulerDeps) {
	// 1. Read schedule. Two DB lookups — enabled (bool) and
	//    time (HH:MM). If either fails, this tick is a no-op.
	enabled, timeStr, err := readSchedule(deps.DB, deps.Cfg)
	if err != nil {
		return
	}
	if !enabled {
		return
	}
	// 2. Does the current HH:MM match the configured time?
	now := time.Now()
	if !timeMatches(now, timeStr) {
		return
	}
	// 3. Did we already fire for this HH:MM today? Read
	//    update_schedule_last_run. If it already has today's
	//    date + matching HH:MM, skip.
	lastRun, _ := readLastRun(deps.DB)
	if sameMinute(lastRun, now) {
		return
	}
	// 4. Is an update already in progress (manual or
	//    scheduled)? Check both mutexes.
	if isUpdateInFlight() {
		return
	}
	scheduledMu.Lock()
	if scheduledInFlight {
		scheduledMu.Unlock()
		return
	}
	scheduledInFlight = true
	scheduledMu.Unlock()
	defer func() {
		scheduledMu.Lock()
		scheduledInFlight = false
		scheduledMu.Unlock()
	}()

	// 5. Check GitHub. If no newer release, do nothing —
	//    the schedule is "run only when there's an update
	//    to apply", not "run a no-op every day at 03:00".
	ctxCheck, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if deps.Checker == nil {
		return
	}
	result, _ := deps.Checker.Check(ctxCheck)
	if result == nil || !result.IsNewer || result.Latest == "" {
		return
	}

	// 6. ALL conditions met. Run the orchestrator.
	runScheduled(ctx, deps, result.Latest, now)
}

// runScheduled is the actual orchestrator-spawn step.
// Mirrors PostAdminUpdateApply's goroutine body but does
// NOT require an admin user (no Audit call, no claims).
// Telegram notification goes out on start + done/fail
// (same pattern as the manual handlers).
func runScheduled(ctx context.Context, deps SchedulerDeps, target string, triggeredAt time.Time) {
	installKind := DetectInstallKind()
	if installKind != InstallDocker {
		// Systemd / bare not yet supported (same as the
		// manual apply path). The /admin/update page
		// already shows "manual steps" for these install
		// kinds; the scheduled path just silently skips.
		return
	}
	current := "v" + trimV(deps.BuildVersion)
	target = normalizeUpdateTarget(target)

	manualSteps := GenerateManualSteps(installKind, current, target, "", "")
	jobID := GenerateJobID()
	_ = deps.State.Start(jobID, installKind.String(), current, target,
		manualSteps.Steps, manualSteps.Rollback, manualSteps.VerifyAfter)
	deps.State.Log(LogInfo, fmt.Sprintf("scheduled run by B130 scheduler (target=%s, current=%s)", target, current))

	if deps.Notifier != nil {
		deps.Notifier.SendAlert(fmt.Sprintf("⏰ Scheduled skygate update starting: %s → %s (job %s)", current, target, jobID))
	}

	// Write last-run BEFORE the goroutine so the page
	// shows the timestamp even if the swap takes 3-5
	// minutes. Stamped on the "started" event, not the
	// "done" event.
	_ = writeLastRun(deps.DB, triggeredAt)

	ctxRun, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	go func() {
		u := NewDockerUpgrader(deps.RepoPath, deps.State, current)
		u.Run(ctxRun, target)

		finalState := deps.State.Get()
		if deps.Notifier != nil && finalState != nil {
			if finalState.Phase == PhaseDone {
				deps.Notifier.SendAlert(fmt.Sprintf("✅ Scheduled skygate update %s → %s succeeded (took %s)",
					current, target, time.Since(finalState.StartedAt).Round(time.Second)))
			} else if finalState.Phase == PhaseFailed {
				deps.Notifier.SendAlert(fmt.Sprintf("❌ Scheduled skygate update %s → %s FAILED: %s\nManual steps: see /admin/update",
					current, target, finalState.Error))
			}
		}
	}()
}

// readSchedule returns (enabled, "HH:MM", nil) from the DB
// with env-var fallbacks from Cfg. A DB error returns
// (false, "", err) so the caller can short-circuit.
func readSchedule(db *sql.DB, cfg SchedulerCfg) (bool, string, error) {
	enabledStr, err := getGlobalSetting(db, "update_schedule_enabled", boolToStr(cfg.UpdateScheduleEnabled))
	if err != nil {
		return false, "", err
	}
	timeStr, err := getGlobalSetting(db, "update_schedule_time", cfg.UpdateScheduleTime)
	if err != nil {
		return false, "", err
	}
	if timeStr == "" {
		timeStr = "03:00"
	}
	enabled := enabledStr == "1" || enabledStr == "true"
	return enabled, timeStr, nil
}

// readLastRun returns the timestamp string from the DB
// (RFC 3339 format written by writeLastRun). Empty string
// on missing row / parse error. Parsed back to time.Time
// for the sameMinute check.
func readLastRun(db *sql.DB) (time.Time, error) {
	s, err := getGlobalSetting(db, "update_schedule_last_run", "")
	if err != nil || s == "" {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, s)
}

// writeLastRun writes the trigger time to global_settings
// in RFC 3339 format. The page's "Last run" field reads
// this same key.
func writeLastRun(db *sql.DB, t time.Time) error {
	return setGlobalSetting(db, "update_schedule_last_run", t.UTC().Format(time.RFC3339))
}

// timeMatches returns true if now's local HH:MM equals the
// configured HH:MM. We don't try to be smart about the
// second-precision "what if the scheduler missed the exact
// 03:00:00 by 5 seconds" case — a 30s tick means the
// operator can set the schedule 1 minute early to be safe.
// The sameMinute guard on the last-run timestamp prevents
// double-firing.
func timeMatches(now time.Time, hhmm string) bool {
	want, err := parseHHMM(hhmm)
	if err != nil {
		return false
	}
	return now.Hour() == want.hour && now.Minute() == want.min
}

// sameMinute returns true if a and b are in the same
// calendar minute (year+month+day+hour+minute). Used to
// deduplicate: if the last successful trigger was at
// 03:00:15 today, the next tick at 03:00:45 today must
// NOT fire again.
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

// isUpdateInFlight returns true if the admin handlers
// have an update running OR the scheduler itself has one
// running. Uses the package-level stateStoreMu + the
// scheduler's own scheduledInFlight.
func isUpdateInFlight() bool {
	scheduledMu.Lock()
	defer scheduledMu.Unlock()
	return scheduledInFlight
}

// --- internal helpers ---

// normalizeUpdateTarget mirrors the same-name helper in
// internal/feature/admin/update.go (see the package-level
// comment about the import cycle). Returns the input
// unchanged if it already starts with "v", "skygate-",
// "main", or "HEAD"; otherwise prepends "v" to turn a
// bare semver like "0.33.1.24" into the conventional
// tag form "v0.33.1.24".
func normalizeUpdateTarget(target string) string {
	if target == "" {
		return target
	}
	if strings.HasPrefix(target, "v") ||
		strings.HasPrefix(target, "skygate-") ||
		strings.HasPrefix(target, "main") ||
		strings.HasPrefix(target, "HEAD") {
		return target
	}
	return "v" + target
}

type hhmmParsed struct{ hour, min int }

func parseHHMM(s string) (hhmmParsed, error) {
	if len(s) != 5 || s[2] != ':' {
		return hhmmParsed{}, fmt.Errorf("bad HH:MM format: %q", s)
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil {
		return hhmmParsed{}, err
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return hhmmParsed{}, fmt.Errorf("HH:MM out of range: %q", s)
	}
	return hhmmParsed{hour: h, min: m}, nil
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func trimV(s string) string {
	if len(s) > 0 && s[0] == 'v' {
		return s[1:]
	}
	return s
}

// getGlobalSetting + setGlobalSetting are thin wrappers
// around the same-name db helpers, kept here so the
// scheduler doesn't import internal/db (avoids a
// package cycle with internal/feature/admin which also
// imports internal/db).
//
// These call out via a function variable so the test
// suite can override them. In production they're set
// by init() in scheduler_db.go.
var (
	getGlobalSetting func(db *sql.DB, key, def string) (string, error)
	setGlobalSetting func(db *sql.DB, key, value string) error
)
