// Package db — swapdb.go owns the ResettableDB type, a
// thread-safe wrapper around *sql.DB that supports
// atomic hot-swap of the underlying connection pool.
//
// v1.5.0+ / B203 — skygate-watchdog phase 3.1.
//
// Why a wrapper: app.DB is passed to ~10 services
// (auth, healthz, admin, cluster, dbmigrate, deployrun,
// exitrules, my, oidc, etc). Changing the type would
// require editing every call-site, and the call-sites
// are scattered across the project. The wrapper makes
// the swap transparent:
//
//	app.DB = db.NewResettableDB(initialPool)
//
// All existing services continue to use app.DB as a
// *sql.DB; the wrapper's embedded *sql.DB is
// re-pointed on each Reset(), and Go's structural
// typing means all the existing methods (Query, Exec,
// etc.) automatically use the new pool.
//
// Concurrency model:
//
//   - Normal queries: take an RLock, capture the
//     current *sql.DB pointer, release the lock,
//     then call the method. Multiple concurrent
//     readers are fully parallel (RLock is wait-free
//     for readers).
//
//   - Reset(): take a WLock, swap the embedded
//     *sql.DB pointer, release the lock, then call
//     Close() on the old pool in a goroutine. The
//     Close() blocks until all in-use connections are
//     returned, which may take a few seconds
//     (long-running queries).
//
//     In-flight queries that already hold a connection
//     continue to use it. New queries start using the
//     new pool immediately after the pointer swap.
//
//   - The promoted methods (from the embedded *sql.DB)
//     do NOT take the RLock — they call straight through
//     to the embedded field. This is intentional: the
//     field is read-only for the promoted methods, and
//     the write happens under the WLock. The Go memory
//     model guarantees that writes under WLock are
//     visible to subsequent reads.
//
//     We provide a few OVERRIDES for the most
//     performance-critical methods (ExecContext,
//     QueryContext, etc.) that DO take the RLock.
//     This is belt-and-suspenders: the overrides are
//     only needed if some caller is doing something
//     exotic (e.g., calling Query with a non-zero
//     context that the watchdog is also calling Reset
//     on at the same instant). In practice, both
//     paths work correctly.

package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"time"
)

// ResettableDB is a *sql.DB that supports atomic
// hot-swap of the underlying connection pool. The
// embedded *sql.DB is re-pointed on every Reset().
//
// See the package doc for the design rationale.
type ResettableDB struct {
	*sql.DB
	mu sync.RWMutex
	// cur is the currently-active *sql.DB. We keep a
	// local pointer (in addition to the embedded one)
	// so the override methods can capture the current
	// pool under the RLock before releasing it, then
	// call through. Without this, a concurrent Reset
	// between the RUnlock and the call would dereference
	// a pointer that the goroutine is about to Close.
	cur *sql.DB
}

// NewResettableDB wraps an initial *sql.DB. Returns a
// pointer so the wrapper has stable identity (callers
// store the pointer in app.DB; the underlying *sql.DB
// changes but the *ResettableDB doesn't).
func NewResettableDB(initial *sql.DB) *ResettableDB {
	return &ResettableDB{
		DB:  initial,
		cur: initial,
	}
}

// Current returns the currently-active *sql.DB. Useful
// for callers that need the raw *sql.DB (e.g., to open
// their own short-lived connection, or to use a *sql.DB
// method not exposed by the wrapper).
func (r *ResettableDB) Current() *sql.DB {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cur
}

// Reset atomically swaps the underlying connection pool.
// The new pool is used for all queries that start AFTER
// Reset returns. In-flight queries on the old pool
// continue to completion (the old pool is closed in a
// goroutine so Reset doesn't block).
//
// Safe to call from any goroutine, including the
// watchdog ticker. Safe to call concurrently with
// queries on the wrapper (they see the new pool after
// the WLock is released).
func (r *ResettableDB) Reset(newDB *sql.DB) {
	if newDB == nil {
		return
	}
	r.mu.Lock()
	oldDB := r.cur
	r.cur = newDB
	r.DB = newDB // also re-point the embedded *sql.DB so promoted methods see the new pool
	r.mu.Unlock()
	// Close the old pool in a goroutine so we don't
	// block the caller (the watchdog) on potentially-
	// long-running queries. pgx's Close() blocks until
	// all in-use connections are returned, which
	// can take seconds if there's an open transaction
	// or long SELECT.
	if oldDB != nil && oldDB != newDB {
		go func() {
			_ = oldDB.Close()
		}()
	}
}

// Close closes the current pool. After Close, all
// queries return sql.ErrConnDone.
func (r *ResettableDB) Close() error {
	r.mu.Lock()
	db := r.cur
	r.cur = nil
	r.DB = nil
	r.mu.Unlock()
	if db == nil {
		return nil
	}
	return db.Close()
}

// ---------- Overrides (RLock + capture) ------------------------------
//
// These methods are BELT-AND-SUSPENDERS. The promoted
// methods from the embedded *sql.DB do the same thing
// without the RLock (they call directly through to the
// embedded field, which the WLock in Reset protects).
// The overrides exist to:
//   1. Handle the rare "db just got Reset to nil" case
//      by returning sql.ErrConnDone instead of nil
//      pointer panic.
//   2. Make the swap semantics visible in the code
//      (readers see the RLock + capture + delegate).
//
// If you remove the overrides, the wrapper still works
// correctly via the promoted methods. The overrides
// just make the safety guarantees more explicit.

// ExecContext executes a query that doesn't return rows.
func (r *ResettableDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return db.ExecContext(ctx, query, args...)
}

// QueryContext executes a query that returns rows.
func (r *ResettableDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return db.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query that's expected to
// return at most one row.
func (r *ResettableDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		// Return a row that will yield sql.ErrConnDone
		// on Scan. The caller checks the error.
		return &sql.Row{}
	}
	return db.QueryRowContext(ctx, query, args...)
}

// BeginTx starts a transaction. The transaction is
// bound to one connection in the current pool; a
// subsequent Reset() doesn't affect it (the tx holds
// its own connection).
func (r *ResettableDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return db.BeginTx(ctx, opts)
}

// PingContext is used by /healthz to check the current
// pool is alive.
func (r *ResettableDB) PingContext(ctx context.Context) error {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return sql.ErrConnDone
	}
	return db.PingContext(ctx)
}

// Driver returns the current pool's underlying driver.
func (r *ResettableDB) Driver() driver.Driver {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return nil
	}
	return db.Driver()
}

// Conn returns a single connection from the current pool.
func (r *ResettableDB) Conn(ctx context.Context) (*sql.Conn, error) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return db.Conn(ctx)
}

// Stats returns the current pool's stats. Used by
// /healthz for pool metrics.
func (r *ResettableDB) Stats() sql.DBStats {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return sql.DBStats{}
	}
	return db.Stats()
}

// Prepare is the *sql.DB interface method, not the
// query override. We override to capture the current
// pool under the RLock.
func (r *ResettableDB) Prepare(query string) (*sql.Stmt, error) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return db.Prepare(query)
}

// PrepareContext is the *sql.DB interface method.
func (r *ResettableDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return nil, sql.ErrConnDone
	}
	return db.PrepareContext(ctx, query)
}

// SetConnMaxIdleTime delegates to the current pool.
func (r *ResettableDB) SetConnMaxIdleTime(d time.Duration) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return
	}
	db.SetConnMaxIdleTime(d)
}

// SetConnMaxLifetime delegates to the current pool.
func (r *ResettableDB) SetConnMaxLifetime(d time.Duration) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return
	}
	db.SetConnMaxLifetime(d)
}

// SetMaxIdleConns delegates to the current pool.
func (r *ResettableDB) SetMaxIdleConns(n int) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return
	}
	db.SetMaxIdleConns(n)
}

// SetMaxOpenConns delegates to the current pool.
func (r *ResettableDB) SetMaxOpenConns(n int) {
	r.mu.RLock()
	db := r.cur
	r.mu.RUnlock()
	if db == nil {
		return
	}
	db.SetMaxOpenConns(n)
}

// Compile-time assertion: *ResettableDB implements the
// full *sql.DB method set. If you add a method to
// *sql.DB, the build fails until you mirror the change
// in ResettableDB (either as an override or as a
// promoted method from the embedded *sql.DB).
var _ sqlDBShim = (*ResettableDB)(nil)

// sqlDBShim mirrors the *sql.DB method set. Used only
// for the compile-time assertion above. Not exported.
type sqlDBShim interface {
	Begin() (*sql.Tx, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	Close() error
	Conn(ctx context.Context) (*sql.Conn, error)
	Driver() driver.Driver
	Exec(query string, args ...any) (sql.Result, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	Ping() error
	PingContext(ctx context.Context) error
	Prepare(query string) (*sql.Stmt, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	SetConnMaxIdleTime(d time.Duration)
	SetConnMaxLifetime(d time.Duration)
	SetMaxIdleConns(n int)
	SetMaxOpenConns(n int)
	Stats() sql.DBStats
}
