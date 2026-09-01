// v1.5.0+ / B203 — unit tests for the ResettableDB
// type. Most tests don't need a real DB; they use
// sqlmock-style stubs via the real *sql.DB interface
// (the test just verifies the wrapper's plumbing:
// RLock + capture + delegate + Close-in-goroutine).

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubDriver implements driver.Driver + driver.Connector
// for a fake DB. Each call to Open returns a fresh
// *stubConn. We count Open/Close calls to verify the
// wrapper calls Close on the old pool.
type stubDriver struct {
	mu        sync.Mutex
	openCount int32
	closeCount int32
}

func (d *stubDriver) Open(_ string) (driver.Conn, error) {
	atomic.AddInt32(&d.openCount, 1)
	return &stubConn{driver: d}, nil
}

func (d *stubDriver) closeOne() {
	atomic.AddInt32(&d.closeCount, 1)
}

// stubConn satisfies driver.Conn minimally. We don't
// actually do any I/O; the test only verifies the
// wrapper's plumbing, not the conn's correctness.
type stubConn struct {
	driver *stubDriver
}

func (c *stubConn) Prepare(_ string) (driver.Stmt, error)       { return &stubStmt{}, nil }
func (c *stubConn) Close() error                                { c.driver.closeOne(); return nil }
func (c *stubConn) Begin() (driver.Tx, error)                   { return nil, errors.New("not impl") }
func (c *stubConn) ResetSession(_ context.Context) error         { return nil }

// stubStmt satisfies driver.Stmt minimally so that
// QueryContext can be called against the stub.
type stubStmt struct{}

func (s *stubStmt) Close() error                              { return nil }
func (s *stubStmt) NumInput() int                             { return -1 }
func (s *stubStmt) Exec(_ []driver.Value) (driver.Result, error) { return nil, nil }
func (s *stubStmt) Query(_ []driver.Value) (driver.Rows, error) { return nil, errors.New("not impl") }
func (s *stubStmt) CheckNamedArgs(_ []driver.NamedValue) (err error) { return nil }

// stubConnector is what database/sql.OpenDB uses
// (driver.Connector interface, not driver.Driver).
type stubConnector struct {
	d *stubDriver
}

func (c *stubConnector) Connect(_ context.Context) (driver.Conn, error) {
	return c.d.Open("")
}
func (c *stubConnector) Driver() driver.Driver { return c.d }

// openStubDB opens a *sql.DB backed by a stubDriver.
// The returned DB has no real backing — the test only
// exercises the ResettableDB plumbing.
func openStubDB(t *testing.T) (*sql.DB, *stubDriver) {
	t.Helper()
	d := &stubDriver{}
	connector := &stubConnector{d: d}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db, d
}

func TestResettableDB_ResetSwapsPool(t *testing.T) {
	oldDB, d := openStubDB(t)
	r := NewResettableDB(oldDB)

	// Initial: queries go to the initial pool.
	if r.Current() != oldDB {
		t.Errorf("Current() = %p, want %p", r.Current(), oldDB)
	}

	// Force the old pool to open a connection so the
	// subsequent Close has work to do. Without an
	// open conn, sql.DB.Close is a no-op.
	// We use ExecContext (which goes through Prepare +
	// Exec) instead of QueryContext (which goes through
	// Prepare + Query) because the stub doesn't implement
	// Query.
	if _, err := oldDB.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("warm up old pool: %v", err)
	}

	// Reset to a new pool.
	newDB, _ := openStubDB(t)
	r.Reset(newDB)
	if r.Current() != newDB {
		t.Errorf("after Reset, Current() = %p, want %p", r.Current(), newDB)
	}

	// The old pool's Close should fire (in a goroutine
	// from Reset). Wait briefly for it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&d.closeCount) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&d.closeCount) == 0 {
		t.Errorf("old pool not closed after Reset (closeCount=0)")
	}
}

func TestResettableDB_ResetNilIsNoop(t *testing.T) {
	oldDB, _ := openStubDB(t)
	r := NewResettableDB(oldDB)
	r.Reset(nil) // should not panic, should not change current
	if r.Current() != oldDB {
		t.Errorf("after Reset(nil), Current() = %p, want %p", r.Current(), oldDB)
	}
}

func TestResettableDB_CloseClearsCurrent(t *testing.T) {
	db, _ := openStubDB(t)
	r := NewResettableDB(db)
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// After Close, all queries return ErrConnDone.
	_, err := r.QueryContext(context.Background(), "SELECT 1")
	if !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("after Close, Query = %v, want ErrConnDone", err)
	}
}

func TestResettableDB_QueryContextDelegatesToCurrent(t *testing.T) {
	// Set up a real (in-memory sqlite) db. We use a real
	// DB to verify that QueryContext actually reaches
	// the current pool, not just the embedded *sql.DB.
	// Skipped in -short mode.
	if testing.Short() {
		t.Skip("needs a real DB; skipping in -short mode")
	}
	// We don't have sqlite in the test deps, so skip
	// this test. The Reset test above already covers
	// the plumbing; this is a sanity check.
	t.Skip("no sqlite in test deps; see TestResettableDB_QueryContextNoDBError")
}

func TestResettableDB_QueryContextNoDBError(t *testing.T) {
	// When the underlying DB is nil (after Close),
	// ExecContext / QueryContext return ErrConnDone.
	r := NewResettableDB(nil)
	_, err := r.ExecContext(context.Background(), "SELECT 1")
	if !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("ExecContext on nil DB = %v, want ErrConnDone", err)
	}
	_, err = r.QueryContext(context.Background(), "SELECT 1")
	if !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("QueryContext on nil DB = %v, want ErrConnDone", err)
	}
	_, err = r.BeginTx(context.Background(), nil)
	if !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("BeginTx on nil DB = %v, want ErrConnDone", err)
	}
}

func TestResettableDB_ImplementsSQLDB(t *testing.T) {
	// Compile-time check: *ResettableDB satisfies the
	// sqlDBShim interface. If the build passed at all,
	// this is true; the test just makes the intent
	// explicit so a future refactor doesn't drop
	// methods without noticing.
	var _ sqlDBShim = (*ResettableDB)(nil)
}

func TestResettableDB_ConcurrentReadersSafe(t *testing.T) {
	// Run many concurrent readers + a single writer
	// (Reset) to verify the RWMutex correctly serializes.
	// The test fails if any reader sees a nil db (which
	// would mean the Close goroutine beat a reader) or
	// if the runtime detects a race.
	oldDB, _ := openStubDB(t)
	r := NewResettableDB(oldDB)

	const N = 200
	var wg sync.WaitGroup
	wg.Add(N + 1)

	// One writer: do 50 Resets in a tight loop.
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			newDB, _ := openStubDB(t)
			r.Reset(newDB)
		}
	}()

	// Many readers: read Current() and confirm non-nil.
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if r.Current() == nil {
					t.Errorf("reader saw nil current pool")
					return
				}
				_ = r.Stats()
			}
		}()
	}

	wg.Wait()
}

// Ensure unused imports are referenced.
var _ = io.EOF
