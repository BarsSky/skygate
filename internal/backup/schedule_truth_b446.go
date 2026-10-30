// schedule_truth_b446.go — B446 (2026-10-30) — A MISSED MINUTE MUST NOT COST A DAY.
//
// THE LIVE CASE. On the reference install `backup.schedule` is "0 4 * * *",
// `backup.in_app_enabled=1`, `backup.enabled=1`, and every guard the scheduler has passed — yet
// `backup.last_run` was 2026-10-21 04:00:05 UTC, nine days stale. Measured in the container on
// 2026-10-30: the app had BOOTED at 04:02:37 UTC that same morning (the verify-scheduler banner
// prints once, at startup), i.e. the process was restarting exactly across the 04:00 minute the
// schedule named. The old tick logic
//
//	if now.Hour() != sched.Hour || now.Minute() != sched.Minute { return }
//
// fires ONLY inside that one minute, so a restart, a clock jump or a slow boot silently skips the
// whole day — and there is no retry until the same minute tomorrow, which can be missed the same
// way. The panel then blamed the DESTINATION («the destination may have stopped accepting
// archives»), which was the wrong hypothesis and the reason this sat unresolved.
//
// This file makes the rule explicit and testable: a schedule means "one run per period", not
// "one run per exact minute". The exact-minute tick still fires immediately; a slot that has
// already passed without a run is caught up on the next tick.
package backup

import "time"

// DueDecision is the answer of ScheduleDue, with the reason spelled out so the scheduler's log
// line says WHY it fired (a catch-up and a scheduled run look identical in the DB otherwise).
type DueDecision struct {
	Due    bool
	Reason string
}

// ScheduleDue decides whether a scheduled backup must run on this tick.
//
// Order matters:
//  1. "* * * * *" — every minute.
//  2. A run is IN FLIGHT (last_status == "running") — never start a second one; a backup can
//     easily take longer than the tick interval, and the old code relied on the run mutex alone.
//  3. The exact scheduled minute — fire, this is the normal path.
//  4. No run has EVER been recorded — fire, so a fresh install with a schedule does not wait a
//     whole period for its first archive.
//  5. The most recent scheduled instant has passed without a run — fire (the CATCH-UP). This is
//     the case the live installation was in for nine days.
func ScheduleDue(s *Schedule, now time.Time, lastRun int64, lastStatus string) DueDecision {
	if s == nil {
		return DueDecision{}
	}
	if s.EveryMinute {
		return DueDecision{Due: true, Reason: "schedule is every minute"}
	}
	if lastStatus == "running" {
		return DueDecision{Reason: "a backup is already running"}
	}
	nowH, nowM, _ := now.Clock()
	if nowH == s.Hour && nowM == s.Minute {
		return DueDecision{Due: true, Reason: "scheduled minute reached"}
	}
	if lastRun == 0 {
		return DueDecision{Due: true, Reason: "no backup has ever been recorded"}
	}
	prev := previousSlot(s, now)
	if prev.IsZero() {
		return DueDecision{}
	}
	if lastRun < prev.Unix() {
		return DueDecision{Due: true, Reason: "catching up a missed slot at " + prev.UTC().Format(time.RFC3339)}
	}
	return DueDecision{}
}

// previousSlot returns the most recent instant of the schedule's (hour, minute) at or before now.
//
// It is the "one run per period" reference: a run recorded at or after it means this period is
// covered; a run older than it means the period's slot was missed.
func previousSlot(s *Schedule, now time.Time) time.Time {
	if s == nil || s.EveryMinute {
		return time.Time{}
	}
	candidate := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, now.Location())
	if candidate.After(now) {
		candidate = candidate.Add(-24 * time.Hour)
	}
	return candidate
}
