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
//       0. The goroutine is ARMED. Since B375 (2026-10-10) main.go arms it
//          UNCONDITIONALLY — the ticker always exists. Pre-B375 the whole
//          goroutine was gated on cfg.UpdateScheduleEnabled (the ENV value),
//          so a deployment whose env said false and whose /admin/update page
//          said "enabled" had NO scheduler at all: the page wrote
//          global_settings, the operator saw "включено", and nothing ever ran
//          (this is the measured live defect B375 fixes).
//       1. global_settings["update_schedule_enabled"] = "1"
//          (DB-persisted; falls back to Cfg.UpdateScheduleEnabled)
//       2. global_settings["update_schedule_time"] is DUE: the current local
//          time is at or after the configured HH:MM and strictly before
//          HH:MM + CatchUpWindow (B375 — the pre-B375 exact-minute equality
//          lost the whole day when a container recreate straddled the
//          scheduled minute)
//       3. A newer release is available (compareSemver
//          CurrentVersion < LatestVersion)
//       4. No update is already in progress (no inFlight
//          job — same mutex used by the admin handlers)
//       5. The schedule wasn't already triggered TODAY for that slot (we track
//          the last successful run in
//          global_settings["update_schedule_last_run"] to avoid double-firing
//          on the same tick, a second tick inside the catch-up window, or a
//          boot that happens after today's run)
//   - On trigger:
//       - Spawn the Docker upgrader (same as
//         PostAdminUpdateApply's goroutine). Install kinds the scheduled
//         path cannot drive are LOGGED (B375: the pre-B375 silent return
//         left the operator to guess why an armed, enabled schedule did
//         nothing on a native install)
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
	"log"
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

// defaultScheduleTime is the schedule used when neither the DB nor the config
// carries one. Named since B375 so readSchedule, the boot log and the
// catch-up arithmetic quote ONE value.
const defaultScheduleTime = "03:00"

// fallbackScheduleTime is the "HH:MM" an unreadable/absent schedule resolves
// to. Kept separate from readSchedule so the readers that answer the PAGE and
// the BOOT LOG (scheduler_state.go) can resolve a time without a second error
// path.
func fallbackScheduleTime(cfg SchedulerCfg) string {
	if cfg.UpdateScheduleTime == "" {
		return defaultScheduleTime
	}
	return cfg.UpdateScheduleTime
}

// Start launches the scheduler goroutine. Returns immediately;
// the goroutine runs until ctx is cancelled. The caller is
// responsible for keeping deps valid (the DB, the State, the
// Notifier must outlive the ctx).
//
// B375 (2026-10-10): the caller arms this UNCONDITIONALLY (the database decides
// whether it ACTS) and records that fact with SetSchedulerArmed, so
// /admin/update can distinguish "the schedule is off" from "there is no
// scheduler at all".
func Start(ctx context.Context, deps SchedulerDeps) {
	go func() {
		// Run one tick immediately. A container recreate that straddles the
		// scheduled minute is exactly the B375 case, and the pre-B375 shape
		// waited a full TickInterval before its first look — the difference
		// between catching up and losing the day.
		tick(ctx, deps)
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
	// 2. Is the run DUE? Since B375 this is a bounded window, not exact-minute
	//    equality: `now` at or after the configured HH:MM and strictly before
	//    HH:MM + CatchUpWindow. A schedule set to a time already past today
	//    still fires — once — and the dedup below is what stops the repeat.
	now := time.Now()
	due, late := scheduledRunDue(now, timeStr)
	if !due {
		return
	}
	// 3. Did we already fire for this slot TODAY? Read
	//    update_schedule_last_run. A stamp from today at or after the slot
	//    means the run happened (or was refused as a duplicate), so a
	//    catch-up window cannot fire once per tick.
	lastRun, _ := readLastRun(deps.DB)
	if ranTodayAt(timeStr, lastRun, now) {
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

	// 4b. B375: answer "why did nothing happen" for the install kinds the
	//     scheduled path cannot drive, BEFORE the release check, so the reason
	//     is in the journal even when GitHub is unreachable. Pre-B375 this
	//     answer lived only inside runScheduled, which returned silently.
	installKind := DetectInstallKind()
	if installKind != InstallDocker {
		logScheduledSkip(installKind)
		return
	}

	// 5. B346 (2026-10-04) — the pinned release wins over "the latest
	//    release".
	//
	//    The schedule's contract is "all instances converge on one
	//    release", not "run whenever GitHub publishes something". With a
	//    pin stored, the target is that tag and the ONLY reason to skip is
	//    that this instance already runs it. A pin older than the running
	//    build is applied on purpose: an operator pins a known-good release
	//    to bring a host that drifted forward back in line (the measured
	//    2026-10-04 case: `/healthz` said v1.5.94 while the working tree
	//    carried 21 untagged commits).
	if pin := PinnedReleaseFromDB(deps.DB); pin != "" {
		if target, run := PinnedTargetFor(pin, deps.BuildVersion); run {
			logScheduledTrigger(now, late, timeStr, target, true)
			runScheduled(ctx, deps, target, now)
		}
		return
	}

	// 6. No pin → check GitHub. If no newer release, do nothing —
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

	// 7. ALL conditions met. Run the orchestrator.
	logScheduledTrigger(now, late, timeStr, result.Latest, false)
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
		// Systemd / bare / OpenRC are not yet supported by the scheduled path
		// (same as the manual apply path). B375: this must be VISIBLE — the
		// pre-B375 silent return left an operator with an armed, enabled
		// schedule and no journal line explaining why nothing ran.
		logScheduledSkip(installKind)
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
		timeStr = defaultScheduleTime
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

// scheduledRunDue is the "is the configured HH:MM due right now?" predicate.
//
// B375 (2026-10-10) replaced exact hour+minute equality with a bounded window:
// the run is due when `now` is AT OR AFTER the scheduled minute and STRICTLY
// BEFORE scheduled + CatchUpWindow. The window is what makes a container
// recreate at the scheduled minute survivable; the ranTodayAt dedup below is
// what keeps it from firing once per tick.
//
// The returned `late` duration is the lateness when the run is due (0 for the
// exactly-on-time case), so the caller can LOG `late by Nm` instead of firing a
// catch-up silently.
func scheduledRunDue(now time.Time, hhmm string) (due bool, late time.Duration) {
	want, err := parseHHMM(hhmm)
	if err != nil {
		return false, 0
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(), want.hour, want.min, 0, 0, now.Location())
	if now.Before(slot) {
		// Before today's slot (including the "the schedule is set to 23:55
		// and it is 00:05 tomorrow" case, where slot is in the future).
		return false, 0
	}
	if now.Sub(slot) >= CatchUpWindow {
		return false, 0
	}
	return true, now.Sub(slot)
}

// ranTodayAt reports whether this slot already ran today: update_schedule_last_run
// is stamped on this calendar day at or after the slot's minute. Pre-B375 this
// was sameMinute (exact hour+minute equality); with a catch-up window the stamp
// no longer has to be in the SAME minute as the tick that reads it — it has to
// be in the same DAY and no earlier than the slot.
//
// Consequences, all intended:
//   - a run at 08:00:15 makes every later tick of 2026-10-10 a no-op (the
//     pre-B375 behaviour, preserved);
//   - a schedule set to a time already past today fires ONCE (the first tick
//     after the write stamps the day) and never again until tomorrow;
//   - a boot after today's run does NOT re-run it.
func ranTodayAt(hhmm string, lastRun, now time.Time) bool {
	if lastRun.IsZero() {
		return false
	}
	want, err := parseHHMM(hhmm)
	if err != nil {
		return false
	}
	if lastRun.Year() != now.Year() || lastRun.Month() != now.Month() || lastRun.Day() != now.Day() {
		return false
	}
	return !lastRun.Before(time.Date(lastRun.Year(), lastRun.Month(), lastRun.Day(), want.hour, want.min, 0, 0, lastRun.Location()))
}

// logScheduledSkip records the one thing the pre-B375 code left to guesswork:
// the scheduled path cannot drive this install kind, so an armed, enabled
// schedule is a no-op here.
func logScheduledSkip(kind InstallKind) {
	log.Printf("⏰ update-scheduler: NOT SUPPORTED on install kind %q — the scheduled path runs only on Docker (%q); use /admin/update with the manual steps for this kind",
		kind.String(), InstallDocker.String())
}

// logScheduledTrigger records every firing, so the journal answers "did the
// scheduler run, and was it on time?" without a second log line. A catch-up
// names its lateness (B375 requirement: a run that happens inside the window is
// not allowed to look like an on-time run).
func logScheduledTrigger(now time.Time, late time.Duration, hhmm, target string, pinned bool) {
	source := "latest release"
	if pinned {
		source = "pinned release"
	}
	if late >= time.Minute {
		log.Printf("⏰ update-scheduler: firing at %s for the %s slot (late by %s, inside the %s catch-up window) — target=%s (%s)",
			now.Format("15:04:05"), hhmm, late.Round(time.Minute), CatchUpWindow, target, source)
		return
	}
	log.Printf("⏰ update-scheduler: firing at %s for the %s slot (on time) — target=%s (%s)",
		now.Format("15:04:05"), hhmm, target, source)
}

// sameMinute returns true if a and b are in the same
// calendar minute (year+month+day+hour+minute). Kept for the
// callers that still need minute identity (B130's contract) —
// the scheduler's own dedup now uses ranTodayAt, because a
// catch-up run is stamped in a LATER minute than the slot.
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
