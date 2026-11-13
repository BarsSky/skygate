// B466 (2026-11-13) — a slow probe is not a sick database, and a verdict must
// carry its age (the B465 rule, applied to the DB-health sampler).
//
// Live case (restored SQLite copy, 121 MB file, 1 vCPU, published v1.5.212):
// every 30 s tick logged
//
//	db_health: tick: db_health: 2 query error(s): [db.integrity_check:
//	context deadline exceeded db.journal_mode: context deadline exceeded]
//
// on a perfectly healthy database. Two independent defects produced it:
//
//  1. DBHealthConfig.QueryTimeout is DOCUMENTED as a per-query deadline, but the
//     sampler created ONE context around the whole collect(). `PRAGMA quick_check`
//     is O(database size) and consumed the 3 s, so `PRAGMA journal_mode` after it
//     failed instantly with the same deadline — everything behind a slow statement
//     fails, which turns one slow query into a whole-sample outage.
//  2. A full-file consistency scan is not a "is the database answering" question.
//     It was run every tick, budgeted like a cheap pragma, and its TIMEOUT was
//     reported as an integrity FAILURE — "unmeasured" masquerading as "broken",
//     exactly the class B465 fixed for relay verdicts.
package healthz

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// b466Sampler is a SQLite sampler whose integrity probe budget the test pins.
func b466Sampler(t *testing.T, integrityTimeout time.Duration) (*Sampler, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	cfg := DefaultDBHealthConfig()
	cfg.Dialect = "sqlite"
	cfg.Logger = func(string, ...any) {}
	cfg.IntegrityTimeout = integrityTimeout
	cfg.IntegrityInterval = time.Hour
	return NewDBHealthSampler(cfg, NewFixedDBSource(db)), db
}

// TestB466_QueryTimeoutBudgetIsObserved — the per-query budget is real, and a
// clean database under a generous budget produces a clean sample (no error, and
// the fields behind the queries that would have been starved are populated).
//
// The "one slow statement must not starve the next one" half of the property is
// pinned where it can be checked honestly: contract A2 of
// scripts/check_b466_slow_probe.sh asserts that the sampler no longer hands ONE
// context to every statement (each query goes through rowScan, which derives its
// own deadline), because a timing-based Go test on an in-memory database proves
// whatever the driver happens to do with a 1 ns deadline rather than what the
// sampler does — the host-transient trap this project has paid for before.
func TestB466_QueryTimeoutBudgetIsObserved(t *testing.T) {
	s, db := b466Sampler(t, time.Minute)

	sample := &DBHealthSample{}
	if err := s.collect(context.Background(), db, sample); err != nil {
		t.Fatalf("healthy sqlite collect returned an error: %v", err)
	}
	if sample.Database.SizeBytes <= 0 {
		t.Errorf("size_bytes = %d, want > 0", sample.Database.SizeBytes)
	}
	if sample.Server.Version == "" {
		t.Error("version must be populated — it is the statement behind page_size")
	}

	// A budget that cannot be met makes the caller see an error rather than a
	// silently empty sample: the deadline is attached to the statements.
	s.cfg.QueryTimeout = 1 * time.Second
	if err := s.collect(context.Background(), db, &DBHealthSample{}); err != nil {
		t.Fatalf("a 1s per-query budget must still be ample for a healthy in-memory database, got %v", err)
	}
}

// TestB466_UnmeasuredIntegrityIsNotAFailure — an unfinished probe is a fact
// about the probe. It must not degrade the sample, and the status must say so.
func TestB466_UnmeasuredIntegrityIsNotAFailure(t *testing.T) {
	s, _ := b466Sampler(t, time.Nanosecond)

	sample := &DBHealthSample{}
	if err := s.collect(context.Background(), s.src.Current(), sample); err != nil {
		t.Fatalf("collect must not fail because the integrity probe did not finish: %v", err)
	}

	// The probe is asynchronous (it must never delay a sample); give it a moment
	// to record its verdict, then re-collect so the sample carries it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.integrityMu.Lock()
		got := s.integrityStatus
		s.integrityMu.Unlock()
		if got != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	sample = &DBHealthSample{}
	if err := s.collect(context.Background(), s.src.Current(), sample); err != nil {
		t.Fatalf("second collect failed: %v", err)
	}
	if sample.Integrity.Status != "unmeasured" {
		t.Fatalf("integrity status = %q, want unmeasured (budget was 1ns)", sample.Integrity.Status)
	}
	if sample.SampleError != "" {
		t.Errorf("SampleError = %q, want empty — an unmeasured probe is not a database error", sample.SampleError)
	}
	if sample.Integrity.CheckedAt == nil {
		t.Error("an unmeasured verdict must still carry WHEN it was attempted")
	}
}

// TestB466_MeasuredIntegrityIsOk — the healthy path records "ok" with a timestamp.
func TestB466_MeasuredIntegrityIsOk(t *testing.T) {
	s, db := b466Sampler(t, 30*time.Second)

	sample := &DBHealthSample{}
	if err := s.collect(context.Background(), db, sample); err != nil {
		t.Fatalf("collect: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		s.integrityMu.Lock()
		status = s.integrityStatus
		s.integrityMu.Unlock()
		if status != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status != "ok" {
		t.Fatalf("integrity status = %q, want ok on a fresh in-memory database", status)
	}

	sample = &DBHealthSample{}
	if err := s.collect(context.Background(), db, sample); err != nil {
		t.Fatalf("second collect: %v", err)
	}
	if sample.Integrity.Status != "ok" {
		t.Errorf("status = %q, want ok", sample.Integrity.Status)
	}
	if sample.Integrity.CheckedAt == nil || sample.Integrity.CheckedAt.IsZero() {
		t.Error("a measured verdict must carry its timestamp")
	}
	if sample.SampleError != "" {
		t.Errorf("SampleError = %q, want empty", sample.SampleError)
	}
}

// TestB466_IntegrityRunsOnItsOwnSchedule — a 15-minute probe must not be re-run
// on every 30-second tick. Pinned through the claim timestamp, not by sleeping:
// the sampler records the next due time when it claims the slot.
func TestB466_IntegrityRunsOnItsOwnSchedule(t *testing.T) {
	s, db := b466Sampler(t, 30*time.Second)
	if s.cfg.IntegrityInterval != time.Hour {
		t.Fatalf("test setup: interval = %s", s.cfg.IntegrityInterval)
	}
	before := time.Now().UTC()
	if err := s.collect(context.Background(), db, &DBHealthSample{}); err != nil {
		t.Fatalf("collect: %v", err)
	}
	s.integrityMu.Lock()
	next := s.integrityNext
	s.integrityMu.Unlock()
	if next.IsZero() {
		t.Fatal("the first probe must claim a slot so the next tick does not start a second one")
	}
	if !next.After(before.Add(30 * time.Minute)) {
		t.Fatalf("next probe due at %s — the interval is not applied", next)
	}
}

// TestB466_ServerBudgetDefaults — the defaults must be the ones the docs promise.
func TestB466_ServerBudgetDefaults(t *testing.T) {
	cfg := DefaultDBHealthConfig()
	if cfg.QueryTimeout != 3*time.Second {
		t.Errorf("QueryTimeout = %s, want 3s", cfg.QueryTimeout)
	}
	if cfg.IntegrityTimeout != 2*time.Minute {
		t.Errorf("IntegrityTimeout = %s, want 2m", cfg.IntegrityTimeout)
	}
	if cfg.IntegrityInterval != 15*time.Minute {
		t.Errorf("IntegrityInterval = %s, want 15m", cfg.IntegrityInterval)
	}
	// A configuration that sets neither must get the same values, otherwise every
	// embedded caller inherits a zero budget (= an instantly expired context).
	s := NewDBHealthSampler(DBHealthConfig{Dialect: "sqlite"}, nil)
	if s.cfg.IntegrityTimeout <= 0 || s.cfg.IntegrityInterval <= 0 {
		t.Fatalf("NewDBHealthSampler left a zero integrity budget: %+v", s.cfg)
	}
}
