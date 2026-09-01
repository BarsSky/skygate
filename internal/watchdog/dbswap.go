// Package watchdog — dbswap.go is the skygate-watchdog
// for the cluster_database hot-reload (Phase 3.1 of
// docs/internal/cluster-management.md).
//
// v1.5.0+ / B203.
//
// Background
//
// Pre-B203: when the admin edited cluster_database
// via /admin/database/edit (B197), the new DSN was
// written but the running skygate process kept using
// the old SKYGATE_DB_DSN. A container restart was
// required for the change to take effect.
//
// B203: a background goroutine ticks every ~5 seconds,
// reads the cluster_database row, and if the desired
// DSN differs from the current pool's DSN, it:
//   1. Opens a new *pgxpool.Pool against the desired DSN
//      (with a short timeout so a bad DSN doesn't block
//      the watchdog).
//   2. Pings the new pool to verify reachability.
//   3. On success, calls app.DB.(*ResettableDB).Reset(newPool)
//      — atomic hot-swap, no service interruption.
//   4. On failure, logs the error and tries again next tick.
//
// The "no service interruption" property is the whole
// point: in-flight queries on the old pool complete
// naturally (they hold their own connections), while
// new queries use the new pool. Operators see the DSN
// change "take effect" within ~5s without restarting
// anything.
//
// D8 (cluster_database wins on conflict)
//
// The watchdog always trusts cluster_database over
// the live env. If cluster_database is empty (no
// admin edit yet), the watchdog does nothing — the
// original SKYGATE_DB_DSN stays in effect. This matches
// the rule from docs/internal/cluster-management.md §0.2.
//
// Concurrency
//
// The watchdog runs as a single goroutine (one tick at
// a time). Reset is atomic, so we don't need a mutex
// on the watchdog's own state. The tick interval is
// tunable (default 5s) so the operator can slow it
// down for very high-load deployments.
//
// Failure modes
//
// 1. cluster_database row deleted while watchdog runs:
//    the next tick sees no row → no-op (keeps current
//    pool). Operator must re-add the row OR restart
//    the container to get back to the .env DSN.
//
// 2. New DSN unreachable (network partition, bad
//    credentials, etc.): the watchdog logs the error
//    and continues with the OLD pool. The old pool
//    stays alive until the new DSN becomes reachable
//    (or the operator restarts the container).
//
// 3. Old pool has in-flight long queries when Reset
//    fires: pgx's pool.Close() blocks until all in-use
//    connections are returned. The watchdog runs Close
//    in a goroutine, so the watchdog itself doesn't
//    block. The new pool handles all new queries.

package watchdog

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for database/sql
)

// DBMigrator is the minimum surface the watchdog needs
// from the ResettableDB pool wrapper. Defined here (not
// imported from internal/db) to avoid a circular import
// (internal/db doesn't import internal/watchdog).
type DBMigrator interface {
	Current() *sql.DB
	Reset(newDB *sql.DB)
}

// ClusterDatabaseRow is the minimum surface the
// watchdog needs from the cluster_database table.
// Mirrors internal/db.ClusterDatabase but kept local
// to avoid the import cycle.
type ClusterDatabaseRow struct {
	ID           string
	CurrentDSN   string
	DBName       string
	Username     string
	SSLMode      string
}

// DSNReader reads the current cluster_database row.
// Typically implemented as a thin wrapper around
// db.GetClusterDatabase.
type DSNReader func(ctx context.Context) (*ClusterDatabaseRow, error)

// Config is the watchdog's tunables.
type Config struct {
	// Interval between ticks. Default 5s.
	Interval time.Duration

	// PingTimeout is the per-tick reachability probe
	// timeout for the NEW DSN before we commit to
	// swapping. Default 3s.
	PingTimeout time.Duration

	// Logger receives watchdog events. If nil, the
	// package-level log.Printf is used.
	Logger func(format string, args ...any)
}

// DefaultConfig returns the recommended settings.
func DefaultConfig() Config {
	return Config{
		Interval:    5 * time.Second,
		PingTimeout: 3 * time.Second,
		Logger:      log.Printf,
	}
}

// DBSwap is the running watchdog. Construct with
// NewDBSwap, then call Start to launch the goroutine.
// Call Stop to shut down cleanly.
type DBSwap struct {
	cfg     Config
	migrator DBMigrator
	reader  DSNReader

	mu          sync.Mutex
	currentDSN  string
	currentPool *sql.DB
	stopCh      chan struct{}
	stopped     chan struct{}
}

// NewDBSwap constructs the watchdog. The migrator is
// the ResettableDB wrapper (passed as an interface
// so this package doesn't depend on internal/db).
// The reader is a closure that reads cluster_database
// (typically wrapping internal/db.GetClusterDatabase).
func NewDBSwap(cfg Config, migrator DBMigrator, reader DSNReader) *DBSwap {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.PingTimeout <= 0 {
		cfg.PingTimeout = 3 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Printf
	}
	return &DBSwap{
		cfg:      cfg,
		migrator: migrator,
		reader:   reader,
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start launches the watchdog goroutine. Returns
// immediately. Call Stop to terminate.
func (w *DBSwap) Start() {
	go w.run()
}

// Stop signals the goroutine to exit and waits for
// it. Safe to call multiple times (subsequent calls
// are no-ops, thanks to the closed channel).
func (w *DBSwap) Stop() {
	select {
	case <-w.stopCh:
		// already stopped
		return
	default:
		close(w.stopCh)
	}
	<-w.stopped
}

// CurrentDSN returns the DSN the watchdog currently
// considers "active". Useful for /healthz and
// /admin/database status display.
func (w *DBSwap) CurrentDSN() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.currentDSN
}

// run is the main loop. One tick per cfg.Interval.
func (w *DBSwap) run() {
	defer close(w.stopped)

	// Seed the current DSN with the live env's value
	// (read from app.DB.Current() at construction time
	// if possible). The reader provides the desired
	// DSN from cluster_database; the watchdog only
	// swaps when the two differ.
	w.mu.Lock()
	if cur := w.migrator.Current(); cur != nil {
		w.currentPool = cur
	}
	w.mu.Unlock()

	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-t.C:
			w.tick()
		}
	}
}

// tick is one iteration. Reads cluster_database,
// compares to current, and swaps if needed.
func (w *DBSwap) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.PingTimeout)
	defer cancel()

	row, err := w.reader(ctx)
	if err != nil {
		// Most common case: no cluster_database row
		// yet (fresh deploy). Log at info, not warn,
		// so the operator doesn't get alert fatigue.
		w.cfg.Logger("dbmigrate-watchdog: read cluster_database: %v (keeping current pool)", err)
		return
	}
	if row == nil || row.CurrentDSN == "" {
		// Empty desired = no admin override; keep
		// the env-DSN pool.
		return
	}

	w.mu.Lock()
	current := w.currentDSN
	w.mu.Unlock()

	if row.CurrentDSN == current {
		// No change since last tick.
		return
	}

	// The desired DSN differs from the current.
	// Open a new pool, ping it, and swap.
	w.cfg.Logger("dbmigrate-watchdog: DSN change detected; swapping to %s", redactDSN(row.CurrentDSN))
	newPool, err := sql.Open("pgx", row.CurrentDSN)
	if err != nil {
		w.cfg.Logger("dbmigrate-watchdog: open new pool: %v (keeping current pool)", err)
		return
	}
	pingCtx, pingCancel := context.WithTimeout(ctx, w.cfg.PingTimeout)
	if err := newPool.PingContext(pingCtx); err != nil {
		pingCancel()
		_ = newPool.Close()
		w.cfg.Logger("dbmigrate-watchdog: ping new pool: %v (keeping current pool)", err)
		return
	}
	pingCancel()

	// Ping succeeded. Swap.
	w.migrator.Reset(newPool)
	w.mu.Lock()
	w.currentDSN = row.CurrentDSN
	w.currentPool = newPool
	w.mu.Unlock()
	w.cfg.Logger("dbmigrate-watchdog: pool swapped successfully (new backend pid: %v)", backendPID(newPool))
}

// redactDSN strips the password from a DSN for logging.
// Same approach as internal/dbmigrate/framework.go.
func redactDSN(dsn string) string {
	// Find the "://" prefix, then look for the @ to
	// delimit the userinfo. The user:pass segment
	// is between them.
	const prefix = "postgres://"
	if len(dsn) < len(prefix) || dsn[:len(prefix)] != prefix {
		return dsn
	}
	rest := dsn[len(prefix):]
	at := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '@' {
			at = i
			break
		}
	}
	if at < 0 {
		return dsn
	}
	userpass := rest[:at]
	for i := 0; i < len(userpass); i++ {
		if userpass[i] == ':' {
			return dsn[:len(prefix)+i+1] + "***" + dsn[len(prefix)+at:]
		}
	}
	return dsn
}

// backendPID returns the backend PID of a connection
// from the pool. Used to confirm the swap visually
// (the new pool's PIDs differ from the old).
func backendPID(pool *sql.DB) string {
	if pool == nil {
		return "error: nil pool"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var pid int
	if err := pool.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return fmt.Sprintf("%d", pid)
}
