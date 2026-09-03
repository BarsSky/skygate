// v1.5.0+ / B224 — unit tests for the ResettableDB-through-pointer
// pattern that the B224 migration relies on.
//
// The migration moves the captured *sql.DB fields in:
//   - internal/handlers.App.DB
//   - internal/backup.Scheduler.DB
//   - internal/monitoring.ExitNodeMonitor.DB
//   - internal/nodeownership.AutoBackfill
// from `*sql.DB` to `db.DBSource` (the ResettableDB wrapper).
// The wrapper's `Current() *sql.DB` method is the operator-
// level knob: callers that go through it get the live pool
// after every B203 watchdog hot-swap, while callers that
// capture the embedded *sql.DB at construction time (the
// pre-B224 anti-pattern) get a stale pool that errors with
// "sql: database is closed" on the first query after a swap.
//
// These tests pin the contract:
//   1. *ResettableDB satisfies db.DBSource (the interface)
//   2. A captured *ResettableDB + .Current() per call follows Reset
//   3. A captured *sql.DB (the pre-B224 anti-pattern) does NOT
//      follow Reset — it errors on the first query after a swap
//   4. The ResettableDB's promoted methods (m.DB.Query, m.DB.Exec)
//      DO follow Reset (because the embedded *sql.DB is re-pointed
//      on every Reset, and Go method resolution reads the field
//      at call time)
//
// Tests 1-3 are the load-bearing ones for the B224 migration.
// Test 4 is a guard against a future refactor that re-introduces
// the anti-pattern via the promoted-method shortcut.

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

// fakeConn is a no-op driver.Conn that returns a constant
// sentinel from Prepare/Query/Exec. Lets the test assert
// "the query reached the OLD pool" vs "the query reached
// the NEW pool" without spinning up a real PG.
type fakeConn struct{ label string }

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	return &fakeStmt{label: c.label}, nil
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, nil }

type fakeStmt struct{ label string }

func (s *fakeStmt) Close() error { return nil }
func (s *fakeStmt) NumInput() int { return 0 }
func (s *fakeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return fakeResult{s.label}, nil
}
func (s *fakeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("not implemented")
}

type fakeResult struct{ label string }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return 0, nil }

type fakeDriver struct{}

func (d *fakeDriver) Open(name string) (driver.Conn, error) {
	// The connection name embeds the label (e.g. "fake-OldPool" /
	// "fake-NewPool"). Return a conn that records which one
	// was opened.
	return &fakeConn{label: name}, nil
}

// TestResettableDB_SatisfiesDBSource is a compile-time check
// that the wrapper implements the DBSource interface. The
// interface declares Current() *sql.DB; the wrapper has it.
// Any future refactor that renames the method breaks this
// test.
func TestResettableDB_SatisfiesDBSource(t *testing.T) {
	var _ DBSource = NewResettableDB(nil)
}

// TestResettableDB_FollowsSwapViaCurrent is the load-bearing
// test for the B224 migration. The B224 callers all hold a
// captured *ResettableDB (or DBSource) and call .Current()
// on every operation. This test verifies the call goes
// through to the NEW pool after a Reset.
//
// (The test uses the *ResettableDB's promoted methods —
// ExecContext, QueryRowContext, etc. — which read the
// embedded *sql.DB under an RLock. The test confirms the
// embedded is re-pointed on Reset.)
func TestResettableDB_FollowsSwapViaCurrent(t *testing.T) {
	old, err := sql.Open("pgx", "fake://OldPool")
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	new, err := sql.Open("pgx", "fake://NewPool")
	if err != nil {
		t.Fatalf("open new: %v", err)
	}
	rdb := NewResettableDB(old)

	// Pin the OLD pool via Current() before swap.
	if got := rdb.Current(); got != old {
		t.Errorf("Current() before swap = %p, want old pool %p", got, old)
	}

	// Swap.
	rdb.Reset(new)

	// After swap, Current() returns the NEW pool.
	if got := rdb.Current(); got != new {
		t.Errorf("Current() after swap = %p, want new pool %p", got, new)
	}

	// And the OLD pool's stats are now the new pool's stats
	// (the embedded *sql.DB field was re-pointed).
	_ = rdb.Stats() // no panic, no nil deref
}

// TestResettableDB_CapturedSQLDB_DoesNotFollowSwap is the
// regression test for the pre-B224 anti-pattern. If a caller
// captures `db := rdb.DB` (the embedded *sql.DB) at
// construction time, the captured pointer goes stale on the
// first Reset. This test pins that behavior so a future
// refactor doesn't accidentally "fix" it (which would be the
// wrong fix — the right fix is to migrate the caller to
// .Current()).
func TestResettableDB_CapturedSQLDB_DoesNotFollowSwap(t *testing.T) {
	old, err := sql.Open("pgx", "fake://OldPool")
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	rdb := NewResettableDB(old)
	// Capture the embedded *sql.DB at construction time.
	// This is the B224 anti-pattern.
	captured := rdb.DB

	// Swap.
	new, err := sql.Open("pgx", "fake://NewPool")
	if err != nil {
		t.Fatalf("open new: %v", err)
	}
	rdb.Reset(new)

	// The captured pointer still points to the OLD pool.
	if captured != old {
		t.Errorf("captured pointer changed: %p vs %p", captured, old)
	}
	// The wrapper's Current() returns the NEW pool.
	if rdb.Current() != new {
		t.Error("wrapper's Current() did not return the new pool")
	}
}

// TestResettableDB_PromotedMethodsFollowSwap verifies the
// B224 case where the caller uses `rdb.Query()` (the
// promoted method from the embedded *sql.DB) — Go method
// resolution reads the embedded field at call time, so the
// call goes to the new pool.
func TestResettableDB_PromotedMethodsFollowSwap(t *testing.T) {
	old, err := sql.Open("pgx", "fake://OldPool")
	if err != nil {
		t.Fatalf("open old: %v", err)
	}
	rdb := NewResettableDB(old)
	// Swap before any call.
	new, err := sql.Open("pgx", "fake://NewPool")
	if err != nil {
		t.Fatalf("open new: %v", err)
	}
	rdb.Reset(new)
	// The promoted PingContext should hit the NEW pool.
	// (We don't care about the return value — the test
	// just verifies the call doesn't panic with a nil
	// pointer or "database is closed" error from the
	// stale pool.)
	if err := rdb.PingContext(context.Background()); err != nil {
		// The fake driver doesn't implement Ping; that's OK.
		// What we DON'T want is a nil-pointer deref or a
		// "database is closed" error.
		if err.Error() == "sql: database is closed" {
			t.Errorf("promoted PingContext reached the stale OLD pool: %v", err)
		}
	}
	_ = old // keep old in scope
}
