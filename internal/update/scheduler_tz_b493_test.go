// scheduler_tz_b493_test.go — TD-43: the schedule is expressed in the PANEL's timezone.
//
// The operator's report (2026-11-18): «я ставил расписание на 8 утра по МСК и также вводил учёт по
// таймзоне, где расположен хост». The host clock is Etc/UTC; the panel's own setting
// (`global_settings.timezone`, localtime.SettingKey) is Europe/Moscow. `scheduledRunDue` must therefore
// be evaluated against the PANEL's location, not the host's system zone — and that has to be a
// measured fact, not a reading of the code, because "the schedule is an hour off" is exactly the kind
// of defect nobody notices until an update lands at the wrong time.
package update

import (
	"testing"
	"time"

	"skygate/internal/localtime"
)

// b493WithTimezone pins the panel timezone for one test and restores it afterwards.
func b493WithTimezone(t *testing.T, name string) {
	t.Helper()
	prev := localtime.Name()
	if _, err := localtime.Set(name); err != nil {
		t.Fatalf("set panel timezone %q: %v", name, err)
	}
	t.Cleanup(func() { _, _ = localtime.Set(prev) })
}

// TestB493_ScheduleIsEvaluatedInThePanelTimezone is the whole point: with the panel set to
// Europe/Moscow, the "08:00" slot is 08:00 MSK = 05:00 UTC. A host whose system clock is UTC (the
// production shape) must NOT shift the slot to 11:00 MSK.
func TestB493_ScheduleIsEvaluatedInThePanelTimezone(t *testing.T) {
	b493WithTimezone(t, "Europe/Moscow")

	// 05:00 UTC is exactly 08:00 MSK.
	atSlot := time.Date(2026, 11, 18, 5, 0, 0, 0, time.UTC)
	panelNow := localtime.In(atSlot)
	if got := panelNow.Format("15:04 -0700"); got != "08:00 +0300" {
		t.Fatalf("localtime.In(05:00Z) = %s, want 08:00 +0300 (panel zone is not being applied)", got)
	}
	if due, late := scheduledRunDue(panelNow, "08:00"); !due || late != 0 {
		t.Fatalf("08:00 MSK must be DUE at 05:00Z (due=%v late=%v)", due, late)
	}

	// 08:00 UTC is 11:00 MSK — three hours AFTER the slot, outside the catch-up window.
	hourLater := localtime.In(time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC))
	if due, _ := scheduledRunDue(hourLater, "08:00"); due {
		t.Fatalf("08:00Z (= 11:00 MSK) must NOT be due for an 08:00 MSK schedule — the host zone leaked in")
	}

	// And the opposite shape: with the panel on UTC, the SAME instant must be due for an 08:00 UTC
	// schedule. This guards the other direction (pinning the panel zone is what changes the answer).
	b493WithTimezone(t, "UTC")
	if due, _ := scheduledRunDue(localtime.In(time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)), "08:00"); !due {
		t.Fatalf("08:00 UTC must be due for an 08:00 schedule when the panel zone is UTC")
	}
}

// TestB493_LastRunDedupUsesThePanelTimezone: "did this slot already run today" must be answered in
// the PANEL's day. The distinguishing case is when the panel day differs from the UTC day — a slot at
// 00:05 MSK reached by a run recorded at 21:10 UTC the previous UTC day. Comparing UTC dates would
// forget that run and fire the slot a second time.
func TestB493_LastRunDedupUsesThePanelTimezone(t *testing.T) {
	b493WithTimezone(t, "Europe/Moscow")

	// A run at 21:10Z on 17 Nov is 00:10 MSK on 18 Nov — the SAME panel day as the 00:05 MSK slot,
	// and five minutes after it. Deduped: the slot already ran.
	lastRun := localtime.In(time.Date(2026, 11, 17, 21, 10, 0, 0, time.UTC))
	nowMSK := localtime.In(time.Date(2026, 11, 17, 21, 20, 0, 0, time.UTC)) // 00:20 MSK, same panel day
	if !ranTodayAt("00:05", lastRun, nowMSK) {
		t.Fatal("a run 5 minutes after a 00:05 MSK slot must dedupe it even though the UTC day differs — the panel day is not being used")
	}

	// The same last run, but for a slot LATER in the panel day that has not run yet: not deduped.
	if ranTodayAt("08:00", lastRun, localtime.In(time.Date(2026, 11, 18, 5, 0, 0, 0, time.UTC))) {
		t.Fatal("a run at 00:10 MSK must not block an 08:00 MSK slot — that slot has not run yet")
	}

	// And a run on the PREVIOUS panel day never blocks today's slot.
	previousPanelDay := localtime.In(time.Date(2026, 11, 17, 20, 59, 0, 0, time.UTC)) // 23:59 MSK 17 Nov
	nowNextDay := localtime.In(time.Date(2026, 11, 18, 5, 0, 0, 0, time.UTC))         // 08:00 MSK 18 Nov
	if ranTodayAt("08:00", previousPanelDay, nowNextDay) {
		t.Fatal("a run on the previous PANEL day must not block today's slot")
	}
}
