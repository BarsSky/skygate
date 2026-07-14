package backup

// 2026-07-14: Этап 14 v6 — scheduler tests.
//
// The Scheduler's tick loop reads the config from the DB
// and calls RunBackup when the cron schedule matches the
// current minute. We can't easily stub time.Now() (it's
// a free function), so the tests focus on:
//
//   - The "skip if InAppEnabled=false" gate (no RunBackup
//     call, no error).
//   - The "skip if Enabled=false" gate (master switch off).
//   - The "skip if schedule can't parse" gate (bad input).
//
// We verify by inspecting global_settings after the tick:
// a successful RunBackup would have set last_status; the
// "skip" paths leave it unchanged.
//
// isDueThisTick is tested with concrete time.Time values
// to verify the (hour, minute) match.

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// newTestDB returns a fresh shared-cache in-memory
// SQLite DB with the minimal schema Scheduler.tick()
// touches. We use a per-test unique name so concurrent
// tests don't see each other's tables; SQLite's
// ":memory:" is per-connection which would break the
// Go connection pool. The shared-cache DSN keeps
// everything in RAM so t.TempDir() cleanup races on
// Windows file locks don't bite us.
var memDBCounter int64

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	n := atomic.AddInt64(&memDBCounter, 1)
	dsn := fmt.Sprintf("file:skygate-test-backup-%d?mode=memory&cache=shared", n)
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE global_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestSchedulerTick_SkipsWhenInAppDisabled: the
// scheduler does nothing when in_app_enabled = 0.
// last_status is never written.
func TestSchedulerTick_SkipsWhenInAppDisabled(t *testing.T) {
	d := newTestDB(t)
	if err := Save(d, &Config{
		Destination: "/tmp/skygate-test", Protocol: ProtocolLocal,
		KeepCount: 5, Schedule: "0 3 * * *",
		Enabled: true, InAppEnabled: false,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s := &Scheduler{DB: d}
	s.tick(context.Background())
	if hasStatus(t, d) {
		t.Errorf("expected no last_status, found one")
	}
}

// TestSchedulerTick_SkipsWhenMasterDisabled: even with
// in_app_enabled = 1, the master Enabled switch off
// stops the scheduler.
func TestSchedulerTick_SkipsWhenMasterDisabled(t *testing.T) {
	d := newTestDB(t)
	if err := Save(d, &Config{
		Destination: "/tmp/skygate-test", Protocol: ProtocolLocal,
		KeepCount: 5, Schedule: "0 3 * * *",
		Enabled: false, InAppEnabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s := &Scheduler{DB: d}
	s.tick(context.Background())
	if hasStatus(t, d) {
		t.Errorf("expected no last_status, found one")
	}
}

// TestSchedulerTick_SkipsOnInvalidSchedule: a bad cron
// expression is logged and the scheduler moves on
// without firing.
func TestSchedulerTick_SkipsOnInvalidSchedule(t *testing.T) {
	d := newTestDB(t)
	if err := Save(d, &Config{
		Destination: "/tmp/skygate-test", Protocol: ProtocolLocal,
		KeepCount: 5, Schedule: "not a cron",
		Enabled: true, InAppEnabled: true,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s := &Scheduler{DB: d}
	s.tick(context.Background())
	if hasStatus(t, d) {
		t.Errorf("expected no last_status, found one")
	}
}

// TestIsDueThisTick_MatchesExactMinute: a 5-field cron
// "30 4 * * *" only fires at 04:30, not at 04:31 or
// 03:30.
func TestIsDueThisTick_MatchesExactMinute(t *testing.T) {
	at0430 := time.Date(2026, 7, 14, 4, 30, 0, 0, time.UTC)
	if !isDueThisTick(sched("30 4"), at0430) {
		t.Errorf("expected true at 04:30, got false")
	}
	at0431 := time.Date(2026, 7, 14, 4, 31, 0, 0, time.UTC)
	if isDueThisTick(sched("30 4"), at0431) {
		t.Errorf("expected false at 04:31, got true")
	}
	at0330 := time.Date(2026, 7, 14, 3, 30, 0, 0, time.UTC)
	if isDueThisTick(sched("30 4"), at0330) {
		t.Errorf("expected false at 03:30, got true")
	}
}

// TestIsDueThisTick_EveryMinute: "every minute" cron
// fires at any minute.
func TestIsDueThisTick_EveryMinute(t *testing.T) {
	for _, t0 := range []time.Time{
		time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 4, 30, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 12, 15, 0, 0, time.UTC),
		time.Date(2026, 7, 14, 23, 59, 0, 0, time.UTC),
	} {
		if !isDueThisTick(sched("*"), t0) {
			t.Errorf("expected true at %s, got false", t0)
		}
	}
}

// TestStart_NilDBDoesNotPanic: Start with a nil DB
// logs a warning and returns immediately. We don't
// want a bad wiring in main.go to crash the process.
func TestStart_NilDBDoesNotPanic(t *testing.T) {
	s := &Scheduler{DB: nil}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Start so the goroutine returns immediately
	s.Start(ctx)
	// No assertion needed — reaching here without
	// panicking is the test.
}

// sched is a tiny helper: parse the schedule string or
// fail the test.
func sched(s string) *Schedule {
	sc, err := ParseSchedule(s)
	if err != nil {
		panic("sched: " + err.Error())
	}
	return sc
}

// hasStatus reports whether backup.last_status is set.
func hasStatus(t *testing.T, d *sql.DB) bool {
	t.Helper()
	var got string
	err := d.QueryRow(`SELECT value FROM global_settings WHERE key='backup.last_status'`).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query last_status: %v", err)
	}
	return got != ""
}
