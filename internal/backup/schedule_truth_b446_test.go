// schedule_truth_b446_test.go — B446. The live case in a table: a daily schedule plus a process
// that keeps being restarted near the scheduled minute must NOT lose a day.
package backup

import (
	"testing"
	"time"
)

func b446Sched(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := ParseSchedule(expr)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", expr, err)
	}
	return s
}

// TestB446_MissedSlotIsCaughtUp is the nine-day incident: the schedule says 04:00, the last run
// is a week old, and right now is 05:00 (the process booted at 04:02 and skipped the minute).
func TestB446_MissedSlotIsCaughtUp(t *testing.T) {
	sched := b446Sched(t, "0 4 * * *")
	now := time.Date(2026, 10, 30, 5, 0, 0, 0, time.UTC)
	last := time.Date(2026, 10, 21, 4, 0, 5, 0, time.UTC).Unix()

	got := ScheduleDue(sched, now, last, "ok")
	if !got.Due {
		t.Fatalf("a missed 04:00 slot was not caught up: %+v", got)
	}
	if got.Reason == "" {
		t.Error("the catch-up must say why it fired — the log line is the operator's only trace")
	}
}

// TestB446_TheScheduledMinuteStillFiresImmediately keeps the normal path.
func TestB446_TheScheduledMinuteStillFiresImmediately(t *testing.T) {
	sched := b446Sched(t, "0 4 * * *")
	now := time.Date(2026, 10, 30, 4, 0, 37, 0, time.UTC)
	last := time.Date(2026, 10, 29, 4, 0, 5, 0, time.UTC).Unix()
	got := ScheduleDue(sched, now, last, "ok")
	if !got.Due {
		t.Fatalf("the scheduled minute did not fire: %+v", got)
	}
}

// TestB446_NoDoubleRunWithinTheSamePeriod: a run recorded AFTER the slot covers it, so the
// catch-up must not fire again on every tick until tomorrow.
func TestB446_NoDoubleRunWithinTheSamePeriod(t *testing.T) {
	sched := b446Sched(t, "0 4 * * *")
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	last := time.Date(2026, 10, 30, 4, 2, 5, 0, time.UTC).Unix() // the catch-up run itself
	if got := ScheduleDue(sched, now, last, "ok"); got.Due {
		t.Fatalf("the slot was already covered but the scheduler fired again: %+v", got)
	}
	// And the SAME run must still be non-due late in the day, minutes before the next slot.
	late := time.Date(2026, 10, 30, 23, 59, 0, 0, time.UTC)
	if got := ScheduleDue(sched, late, last, "ok"); got.Due {
		t.Fatalf("fired again before the next slot: %+v", got)
	}
}

// TestB446_ARunInFlightIsNeverDuplicated: a backup can take longer than the tick interval, and a
// second run started while one is in flight would upload two archives from the same tick.
func TestB446_ARunInFlightIsNeverDuplicated(t *testing.T) {
	sched := b446Sched(t, "0 4 * * *")
	now := time.Date(2026, 10, 30, 4, 0, 37, 0, time.UTC)
	last := time.Date(2026, 10, 21, 4, 0, 5, 0, time.UTC).Unix()
	if got := ScheduleDue(sched, now, last, "running"); got.Due {
		t.Fatalf("a run was started while another was in flight: %+v", got)
	}
	if got := ScheduleDue(sched, now, last, "ok"); !got.Due {
		t.Fatalf("the same tick must fire once the previous run has finished: %+v", got)
	}
}

// TestB446_FirstRunEverFires: a fresh install with a schedule must not wait a whole period.
func TestB446_FirstRunEverFires(t *testing.T) {
	sched := b446Sched(t, "0 4 * * *")
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if got := ScheduleDue(sched, now, 0, ""); !got.Due {
		t.Fatalf("a never-run installation did not fire: %+v", got)
	}
}

// TestB446_EveryMinuteStillEveryMinute: the "*" schedule keeps its meaning.
func TestB446_EveryMinuteStillEveryMinute(t *testing.T) {
	sched := b446Sched(t, "*")
	now := time.Date(2026, 10, 30, 9, 17, 3, 0, time.UTC)
	if got := ScheduleDue(sched, now, time.Now().Unix(), "ok"); !got.Due {
		t.Fatalf("every-minute schedule did not fire: %+v", got)
	}
}

// TestB446_PreviousSlotIsThePeriodReference pins the arithmetic that decides "covered or not".
func TestB446_PreviousSlotIsThePeriodReference(t *testing.T) {
	sched := b446Sched(t, "0 4 * * *")
	// 04:00 exactly — the slot is today's.
	at := time.Date(2026, 10, 30, 4, 0, 0, 0, time.UTC)
	if got := previousSlot(sched, at); !got.Equal(at) {
		t.Errorf("previousSlot(04:00) = %s, want %s", got, at)
	}
	// 03:59 — today's slot has not happened, so the reference is yesterday's.
	before := time.Date(2026, 10, 30, 3, 59, 0, 0, time.UTC)
	want := time.Date(2026, 10, 29, 4, 0, 0, 0, time.UTC)
	if got := previousSlot(sched, before); !got.Equal(want) {
		t.Errorf("previousSlot(03:59) = %s, want %s", got, want)
	}
}
