// internal/watchdog/dbswap_b248_test.go — B248 ErrClusterDatabaseNotFound exemption tests.
//
// B248 (2026-09-15): the watchdog's tick() used to lump
// db.ErrClusterDatabaseNotFound in with true I/O errors, so every
// fresh deploy (cluster_database table is empty until the admin
// configures a hot-swap) fired a false-positive "PG health DEGRADED"
// alert after 15 seconds (= 3 ticks × 5s Interval). The fix: detect
// the sentinel via errors.Is and short-circuit to the success-
// equivalent transition (reset counter, no alert).
//
// These tests drive the actual tick() method directly (not just the
// detectReadFailure/Success helpers), so the regression is pinned at
// the public API the production code path uses.
package watchdog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"skygate/internal/db"
)

// errReader is a DSNReader (func type) that always returns the
// given error. Used to simulate ErrClusterDatabaseNotFound or a
// real I/O failure.
func errReaderFunc(err error) DSNReader {
	return func(ctx context.Context) (*ClusterDatabaseRow, error) {
		return nil, err
	}
}

// okReader is a DSNReader that returns a successful row.
//
//lint:ignore U1000 kept as a ready-made fixture for future tick() tests
func okReaderFunc(row *ClusterDatabaseRow) DSNReader {
	return func(ctx context.Context) (*ClusterDatabaseRow, error) {
		return row, nil
	}
}

// noopMigrator satisfies DBMigrator — used to keep the watchdog from
// panicking on Reset (which tick() never calls in the
// not-found / no-row paths).
type noopMigrator struct{ cur *sql.DB }

func (n noopMigrator) Current() *sql.DB  { return n.cur }
func (n noopMigrator) Reset(*sql.DB)     {}
func (n noopMigrator) Migrate(*sql.DB) error { return nil }

// makeB248Watchdog builds a watchdog wired for B248 tests. Logger
// is silenced; Notifier captures every SendAlert.
func makeB248Watchdog(reader DSNReader) (*DBSwap, *recordingSink) {
	rec := &recordingSink{}
	w := &DBSwap{
		cfg: Config{
			Interval:             5 * time.Second,
			PingTimeout:          3 * time.Second,
			Logger:               func(string, ...any) {},
			Notifier:             rec,
			ReadFailureThreshold: 3,
			ClusterID:            "skygate-test",
		},
		migrator: noopMigrator{cur: nil},
		reader:   reader,
	}
	return w, rec
}

// TestTick_FreshDeployNoRow_NoAlert is the B248 regression. Before
// B248, 3 consecutive ticks with db.ErrClusterDatabaseNotFound would
// fire a "PG health DEGRADED" alert — every fresh deploy triggered
// it. After B248, 10 (or 100) such ticks fire NO alert because the
// sentinel is informational, not a DB outage.
func TestTick_FreshDeployNoRow_NoAlert(t *testing.T) {
	w, rec := makeB248Watchdog(errReaderFunc(db.ErrClusterDatabaseNotFound))
	for i := 0; i < 10; i++ {
		w.tick()
	}
	if n := rec.count(); n != 0 {
		t.Errorf("after 10 ticks with ErrClusterDatabaseNotFound: alerts = %d, want 0 (sentinel is informational, not failure)", n)
	}
	if c := w.consecutiveReadFailures; c != 0 {
		t.Errorf("consecutiveReadFailures = %d, want 0 (counter must NOT increment on sentinel)", c)
	}
}

// TestTick_RealErrorCountsTowardAlert: with a real I/O error (NOT
// the sentinel), the failure counter increments and the alert fires
// on the threshold edge. This pins the existing B225.2 behaviour
// and proves B248's exemption is narrow (only the sentinel is
// exempted).
func TestTick_RealErrorCountsTowardAlert(t *testing.T) {
	w, rec := makeB248Watchdog(errReaderFunc(errors.New("connection refused")))
	// Tick 1: baseline (no alert).
	w.tick()
	if n := rec.count(); n != 0 {
		t.Errorf("tick 1 (baseline): alerts = %d, want 0", n)
	}
	// Tick 2: counter = 2 (below threshold), no alert.
	w.tick()
	if n := rec.count(); n != 0 {
		t.Errorf("tick 2 (counter=2): alerts = %d, want 0", n)
	}
	// Tick 3: counter = 3 (= threshold), ALERT.
	w.tick()
	if n := rec.count(); n != 1 {
		t.Fatalf("tick 3 (counter=3, threshold): alerts = %d, want 1", n)
	}
	if c := w.consecutiveReadFailures; c != 3 {
		t.Errorf("consecutiveReadFailures = %d, want 3", c)
	}
}

// TestTick_NotFoundThenRecovered: ErrClusterDatabaseNotFound never
// increments the counter, so a subsequent real-error tick is still
// "tick 1 (baseline)" from the alert counter's perspective — no
// false-positive alert at tick 3 if the sequence was
// not-found × N, then real-error × 3.
func TestTick_NotFoundThenRecovered(t *testing.T) {
	w, rec := makeB248Watchdog(errReaderFunc(db.ErrClusterDatabaseNotFound))
	// 5 ticks of "not found" — counter stays at 0, no alerts.
	for i := 0; i < 5; i++ {
		w.tick()
	}
	if c := w.consecutiveReadFailures; c != 0 {
		t.Errorf("after 5 not-found ticks: counter = %d, want 0", c)
	}
	// Switch to real errors (operator recreated the cluster_database
	// row, but the new DSN points at an unreachable PG).
	w.reader = errReaderFunc(errors.New("dial tcp: i/o timeout"))
	w.tick() // counter 1, baseline
	if n := rec.count(); n != 0 {
		t.Errorf("real-error tick 1: alerts = %d, want 0 (baseline)", n)
	}
	w.tick() // counter 2, below threshold
	if n := rec.count(); n != 0 {
		t.Errorf("real-error tick 2: alerts = %d, want 0", n)
	}
	w.tick() // counter 3, ALERT
	if n := rec.count(); n != 1 {
		t.Errorf("real-error tick 3: alerts = %d, want 1", n)
	}
}

// TestTick_WrappedSentinelIsExempt: the sentinel might come through
// fmt.Errorf("...%w...") wrappers in production (e.g. a future
// helper that adds context). errors.Is must unwrap to find the
// sentinel — we pin this so the exemption works regardless of
// wrapping.
func TestTick_WrappedSentinelIsExempt(t *testing.T) {
	wrapped := fmt.Errorf("cluster_database probe: %w", db.ErrClusterDatabaseNotFound)
	w, rec := makeB248Watchdog(errReaderFunc(wrapped))
	for i := 0; i < 5; i++ {
		w.tick()
	}
	if n := rec.count(); n != 0 {
		t.Errorf("wrapped sentinel: alerts = %d, want 0 (errors.Is must unwrap)", n)
	}
	if c := w.consecutiveReadFailures; c != 0 {
		t.Errorf("wrapped sentinel: counter = %d, want 0", c)
	}
}

// TestTick_SuccessResetsCounterEvenAfterNotFound: the
// not-found path calls detectReadSuccessTransition, so the counter
// stays at 0 — but if there was a real error sequence in between
// (counter > 0) and then a not-found, the counter resets and the
// recovery alert fires (same as a normal successful read would).
// This pins that the recovery edge is handled correctly after the
// B248 exemption.
func TestTick_SuccessResetsCounterEvenAfterNotFound(t *testing.T) {
	w, rec := makeB248Watchdog(errReaderFunc(errors.New("boom")))
	// Drive 3 real errors → counter = 3, DEGRADED alert fires.
	w.tick()
	w.tick()
	w.tick()
	if n := rec.count(); n != 1 {
		t.Fatalf("after 3 real errors: alerts = %d, want 1 (DEGRADED)", n)
	}
	// Switch to not-found — recovery-equivalent transition.
	// detectReadSuccessTransition sees prev=3 (>=threshold) and
	// fires the RECOVERED alert. The counter resets to 0.
	w.reader = errReaderFunc(db.ErrClusterDatabaseNotFound)
	w.tick()
	if c := w.consecutiveReadFailures; c != 0 {
		t.Errorf("after not-found tick: counter = %d, want 0 (reset)", c)
	}
	if n := rec.count(); n != 2 {
		t.Errorf("after not-found tick: alerts = %d, want 2 (1 DEGRADED + 1 RECOVERED)", n)
	}
	// Subsequent not-found ticks → no more alerts (counter already 0
	// and stable; the first not-found was the recovery edge).
	for i := 0; i < 5; i++ {
		w.tick()
	}
	if n := rec.count(); n != 2 {
		t.Errorf("after 5 more not-found ticks: alerts = %d, want still 2 (no alert spam)", n)
	}
}

// TestTick_LogMessageOnNotFound pins the user-visible log line. The
// pre-B248 message was the same as for real errors
// ("dbmigrate-watchdog: read cluster_database: ... (keeping current
// pool)"), which was confusing — the operator saw
// "read cluster_database: cluster_database not found" repeated 5x
// per 25s and didn't know if their DB was healthy. The B248 message
// explicitly says "fresh deploy or no override configured" so the
// operator can immediately tell it's not an outage.
func TestTick_LogMessageOnNotFound(t *testing.T) {
	var logged []string
	var mu sync.Mutex
	w, _ := makeB248Watchdog(errReaderFunc(db.ErrClusterDatabaseNotFound))
	w.cfg.Logger = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		// %v args → fmt.Sprintf.
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	w.tick()
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 {
		t.Fatalf("logged messages = %d, want 1", len(logged))
	}
	got := logged[0]
	for _, want := range []string{
		"cluster_database row not present",
		"fresh deploy",
		"no override configured",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("not-found log message missing %q\n  full: %q", want, got)
		}
	}
}

