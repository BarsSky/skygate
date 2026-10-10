// 2026-10-10 (B375) — "is the scheduler ACTUALLY running?", and the boot
// sentence that has to tell the truth about it.
//
// THE MEASURED DEFECT (reference deployment, PostgreSQL, 2026-10-09/10)
// --------------------------------------------------------------------
// The operator set the schedule in the panel and nothing happened. Measured
// facts:
//
//   - global_settings said update_schedule_enabled = 1, update_schedule_time
//     = 08:00;
//   - the container env said SKYGATE_UPDATE_SCHEDULE_ENABLED=false and
//     /home/skyadmin/skygate/.env did not carry the variable at all, so
//     config.go's default "false" won;
//   - the journal held ZERO `update-scheduler:` lines since the container
//     started — the goroutine had never been created;
//   - main.go's else-branch claimed "(SKYGATE_UPDATE_SCHEDULE_ENABLED=false;
//     /admin/update page can enable)" — FALSE, and the most expensive kind of
//     false: the page could write global_settings, but it could not start a
//     goroutine that did not exist.
//
// B375 makes the arming UNCONDITIONAL (the database decides whether the
// scheduler ACTS) and makes the two questions separately answerable:
//
//	armed?   — process-level, race-safe, set by cmd/skygate/main.go at boot;
//	enabled? — global_settings, with the env value as the fallback for an
//	           install that never opened the panel.
//
// The trap the operator actually hit is exactly the pair (armed && !enabled):
// "расписание включено, но планировщик не запущен" once the goroutine was
// missing; post-B375 the mirror image is visible too — an armed scheduler with
// the schedule OFF, which the page must not describe as "not running".
//
// This file is also where the boot LOG SENTENCE lives, as a pure function, so
// the truth it tells is pinned by a unit test rather than by a grep against
// main.go (main() itself is not testable).
package update

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// CatchUpWindow (B375) is how late a scheduled run may still fire.
//
// The pre-B375 predicate demanded EXACT hour+minute equality, and the ticker
// runs every TickInterval (30 s): a container recreated at 08:00:40 — or one
// whose boot crossed 08:00 while the DB was still being migrated — lost that
// day's update entirely, silently, with the schedule still reading "включено".
// A bounded window keeps "runs at the configured time" honest while making a
// recreation at the scheduled minute survivable. The dedup on
// update_schedule_last_run (ranTodayAt) is what keeps the window from firing
// twice: a schedule whose time is already past today fires ONCE, never once
// per tick.
const CatchUpWindow = 10 * time.Minute

// schedulerArmMu guards the process-level arming state. A RWMutex rather than
// an atomic.Value: the reason is a string, and the read side (every render of
// /admin/update) is far more frequent than the single write at boot.
var (
	schedulerArmMu     sync.RWMutex
	schedulerArmed     bool
	schedulerArmReason string
)

// SchedulerArmed reports whether the process actually started the scheduler
// goroutine. False means NO schedule can ever fire, whatever the database
// says — the state the operator could not see before B375.
func SchedulerArmed() bool {
	schedulerArmMu.RLock()
	defer schedulerArmMu.RUnlock()
	return schedulerArmed
}

// SchedulerArmedReason is the human-readable reason for the current arming
// state (empty when nothing has set it, e.g. a unit test that never boots
// main()). Rendered on /admin/update next to the state.
func SchedulerArmedReason() string {
	schedulerArmMu.RLock()
	defer schedulerArmMu.RUnlock()
	return schedulerArmReason
}

// SetSchedulerArmed records that the scheduler goroutine was started. Called
// exactly once from cmd/skygate/main.go's boot sequence; exported so the admin
// Service (which must NOT import cmd/) can read the answer. Setting it false
// with a reason is the shape a future "scheduler disabled at build time" path
// would use, and it is what the unit tests use to pin the trap case.
func SetSchedulerArmed(armed bool, reason string) {
	schedulerArmMu.Lock()
	defer schedulerArmMu.Unlock()
	schedulerArmed = armed
	schedulerArmReason = reason
}

// ReadSchedule is the Answer to "what does the database say", with the env
// value as the documented fallback. It is the SAME read the tick performs, so
// the boot sentence, the page and the ticker cannot disagree.
type ReadSchedule struct {
	// Enabled is the effective value (DB row, else cfg.UpdateScheduleEnabled).
	Enabled bool
	// Time is the effective "HH:MM" (DB row, else cfg, else "03:00").
	Time string
	// FromEnv is true when global_settings carries no usable
	// update_schedule_enabled row, i.e. the env default is in charge — the
	// state the boot log must name. (db.GetGlobalSetting folds "row missing"
	// and "row empty" into the default on purpose, see its own comment, so
	// both are reported here as "the env is in charge".)
	FromEnv bool
}

// ReadScheduleAnswers is the exported reader behind the boot log and
// /admin/update. A DB error yields Enabled=false (the scheduler cannot act
// either); callers must not read that as "the DB said off".
func ReadScheduleAnswers(db *sql.DB, cfg SchedulerCfg) ReadSchedule {
	enabledStr, err := getGlobalSetting(db, "update_schedule_enabled", boolToStr(cfg.UpdateScheduleEnabled))
	if err != nil {
		return ReadSchedule{Time: fallbackScheduleTime(cfg)}
	}
	timeStr, err := getGlobalSetting(db, "update_schedule_time", cfg.UpdateScheduleTime)
	if err != nil {
		return ReadSchedule{Time: fallbackScheduleTime(cfg)}
	}
	if timeStr == "" {
		timeStr = defaultScheduleTime
	}
	return ReadSchedule{
		Enabled: enabledStr == "1" || enabledStr == "true",
		Time:    timeStr,
		FromEnv: scheduleEnabledRowIsAbsent(db),
	}
}

// scheduleEnabledRowIsAbsent asks the DB the one question db.GetGlobalSetting
// cannot express through one call ("is there a row, or am I looking at the
// default?"), by re-reading with a sentinel that no operator can store. A
// missing row OR an empty value both come back as the sentinel — which is the
// same "the env is in charge" case the read above already treats as a
// fallback, so the two halves cannot disagree.
func scheduleEnabledRowIsAbsent(db *sql.DB) bool {
	if db == nil {
		return true
	}
	const sentinel = "\x00no-row"
	v, err := getGlobalSetting(db, "update_schedule_enabled", sentinel)
	return err != nil || v == sentinel
}

// SchedulerTrap is the exact state the operator hit on 2026-10-09: the schedule
// reads ENABLED but the process never started the scheduler, so the setting is a
// promise nothing can keep. Named so the admin page renders a warning instead of
// leaving the operator to infer it from two unrelated lines.
func SchedulerTrap(armed, enabled bool) bool {
	return enabled && !armed
}

// ScheduleEnabledFromDB answers the DB side of the question alone: is the
// schedule switched on, using cfg.UpdateScheduleEnabled as the fallback for an
// install that never opened /admin/update?
func ScheduleEnabledFromDB(db *sql.DB, cfg SchedulerCfg) bool {
	return ReadScheduleAnswers(db, cfg).Enabled
}

// SchedulerBootLog builds the boot-time sentence for the scheduler, together
// with whether the current configuration can ACT.
//
// The contract this function exists to enforce: the sentence may never claim
// that a page can start something the process did not start.
//
//	armed=true,  enabled=true   → armed + "the schedule is ON for HH:MM"
//	armed=true,  enabled=false  → armed, but the schedule is OFF, and the page
//	                              that turns it on is NAMED (this is the
//	                              operator's case: the goroutine exists and the
//	                              DB is the only thing left to switch)
//	armed=false, enabled=true   → THE TRAP: «расписание включено, но
//	                              планировщик не запущен». The sentence must say
//	                              so instead of promising that a page will fix
//	                              it.
//	armed=false, enabled=false  → not running and off; no page can help.
//
// `armed` is passed in rather than read from SchedulerArmed() so main.go can
// log what it is about to do and the test can drive every row of the table.
func SchedulerBootLog(armed bool, sched ReadSchedule) (msg string, active bool) {
	switch {
	case armed && sched.Enabled:
		msg = fmt.Sprintf("⏰ update-scheduler: ARMED (tick=%s, catch-up window=%s) and the schedule is ON at %s — the ticker decides when to run",
			TickInterval, CatchUpWindow, sched.Time)
		active = true
	case armed:
		msg = fmt.Sprintf("⏰ update-scheduler: ARMED (tick=%s, catch-up window=%s), but the schedule is OFF — nothing will run. Turn it on at /admin/update (the scheduler IS running; the page only has to set the database)",
			TickInterval, CatchUpWindow)
	default:
		msg = "⏰ update-scheduler: NOT RUNNING — the scheduler goroutine was never started by this process, so NO schedule can fire whatever the database says. This is NOT something the /admin/update page can fix: that page only writes global_settings; it cannot start a goroutine. Check the boot sequence (update.Start) and the boot log above."
	}
	if armed && sched.FromEnv {
		msg += fmt.Sprintf(" (global_settings has no update_schedule_enabled row yet — the env default SKYGATE_UPDATE_SCHEDULE_ENABLED=%s is in use until /admin/update saves a value)", boolToStr(sched.Enabled))
	}
	return msg, active
}
