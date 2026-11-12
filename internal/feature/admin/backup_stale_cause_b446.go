// backup_stale_cause_b446.go — B446. «Надо наконец разобрать что не так» — the panel's own
// diagnostic was answering the wrong question.
//
// THE LIVE CASE (2026-10-30, reference install). Two system tests failed:
//
//	backup.recent — "the last recorded backup run is 216h0m0s old ... the destination may have
//	                 stopped accepting archives"
//	backup.verify — "the last successful backup is 216h0m0s old (under 48 h expected)"
//
// Both blamed the S3 DESTINATION. The truth, measured in the same container:
//
//	backup.enabled=1   backup.in_app_enabled=1   backup.schedule="0 4 * * *"
//	backup.last_status=ok   backup.last_error=""          ← the destination never failed
//	journal: the app BOOTED at 04:02:37 UTC that morning
//
// The process kept being restarted across the 04:00 minute the schedule named, and the old
// scheduler only fired inside that exact minute — so no run happened for nine days. `last_status`
// stayed "ok" because nothing ran at all. A diagnostic that says "the destination may have
// stopped accepting archives" when NOTHING TRIED sends the operator to the wrong system.
//
// This file answers the actual question — WHY is the last run old — from the same settings the
// scheduler reads, and every sentence names the next action.
package admin

import (
	"fmt"
	"strings"
	"time"

	"skygate/internal/backup"
	"skygate/internal/db"
	"skygate/internal/localtime"
)

// backupStaleCause explains a stale backup, or returns "" when there is nothing to explain.
//
// The order is the order of likelihood, and each branch is DISTINGUISHED from the others on
// purpose: "disabled", "nothing schedules it", "scheduled but the slot was missed" and "the run
// failed" have four different remedies.
func backupStaleCause(s *Service, lastRun int64) string {
	return backupStaleCauseAt(s, lastRun, time.Now())
}

// backupStaleCauseAt is backupStaleCause with an injected clock (B464).
//
// WHY THE SEAM EXISTS — measured on the reference host, 2026-11-12 05:37 UTC: `go test
// ./...` failed in this package with "a fresh run produced an excuse: «the scheduler is
// enabled (schedule \"0 4 * * *\") and the next tick will catch up the missed slot …»".
// Nothing was wrong with the product and nothing was wrong with the tree: the test asks
// "does a FRESH run produce an excuse?", passes lastRun = now−2 h, and the answer depends on
// the WALL CLOCK — within two hours after 04:00 the 04:00 slot does fall between lastRun and
// now, so the catch-up sentence is correct and the test's premise ("a fresh run") no longer
// holds. The gate therefore failed for two hours a day, on B1, B132 and B176 at once, and the
// verdict looked like a product regression (AGENTS trap #15: a host fact must never be
// reported as a product defect).
//
// The fix is the seam, not a different assertion: production keeps `time.Now()`, and the test
// pins a moment in which no slot is due, so the assertion is about the CODE and not about the
// hour the gate happened to run.
func backupStaleCauseAt(s *Service, lastRun int64, now time.Time) string {
	if s == nil || s.dbc() == nil {
		return ""
	}
	get := func(key string) string {
		v, _ := db.GetGlobalSetting(s.dbc(), key, "")
		return strings.TrimSpace(v)
	}
	enabled := get("backup.enabled") == "1"
	inApp := get("backup.in_app_enabled") == "1"
	schedule := get("backup.schedule")
	status := get("backup.last_status")
	lastErr := get("backup.last_error")

	// 1. Nothing will ever create a backup.
	if !enabled {
		return "backups are DISABLED on /admin/backup (backup.enabled=0) — nothing will create one until you enable them"
	}
	// 2. A scheduler exists only if the in-app one is on; system cron is invisible to the panel,
	//    so it cannot be credited (the pre-B446 text assumed it: "system-cron continues to run").
	if !inApp {
		detail := "the in-app scheduler is OFF (backup.in_app_enabled=0), so a backup can only come from a system-cron entry for scripts/backup_cron.sh — the panel cannot see one, and nothing has run since"
		if lastRun > 0 {
			return fmt.Sprintf("%s %s. Either enable the in-app scheduler on /admin/backup or install the cron entry, then press «Запустить сейчас».", detail, localtime.FormatUnix(lastRun))
		}
		return detail + " this installation has ever run a backup."
	}
	// 3. Scheduled, enabled — so the interesting case: the slot was missed.
	if schedule == "" {
		return "the in-app scheduler is ON but has NO schedule (backup.schedule is empty), so it only runs when you press «Запустить сейчас»"
	}
	sched, err := backup.ParseSchedule(schedule)
	if err != nil {
		return fmt.Sprintf("the schedule %q cannot be parsed (%v), so the in-app scheduler never fires", schedule, err)
	}
	if sched.EveryMinute {
		return "" // every-minute schedules cannot be "stale" in this sense
	}
	// 4. A run happened but FAILED: this is the strongest evidence there is, so it is stated
	//    BEFORE the catch-up sentence — the scheduler will retry, but the reason the last one
	//    failed is what the operator has to fix. The stored error IS the answer.
	if status != "" && status != "ok" {
		return fmt.Sprintf("the last run finished with status %q (%s) — that error is the cause", status, lastErr)
	}
	if backup.ScheduleDue(sched, now, lastRun, status).Due {
		// The scheduler WILL fire on its next tick — that is the catch-up this block added, so
		// the message must not send the operator hunting for a cause.
		return fmt.Sprintf("the scheduler is enabled (schedule %q) and the next tick will catch up the missed slot — an installation running this build does this automatically; wait one minute and re-run the tests", schedule)
	}
	return ""
}
