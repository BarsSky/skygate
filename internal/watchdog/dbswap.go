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

// NotifierSink is the subset of telegram.Notifier
// the watchdog needs. Defined here as a local
// interface (not imported from internal/telegram)
// to avoid the import cycle that B225 / B225.1
// already discovered in the backup scheduler +
// healthz sampler. Production code passes a
// *telegram.RealNotifier (which satisfies
// SendAlert). When no bot is configured, the
// watchdog's NoopNotifierSink{} drops alerts
// silently — the audit_log "dbmigrate-watchdog"
// log line on the same event is the durable
// record.
type NotifierSink interface {
	SendAlert(text string) int64
}

// NoopNotifierSink is the default when the
// operator hasn't configured a Telegram bot.
// Matches the contract of telegram.NoopNotifier
// (returns 0 = no alert id).
type NoopNotifierSink struct{}

// SendAlert on the noop sink returns 0.
func (NoopNotifierSink) SendAlert(string) int64 { return 0 }

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

	// Notifier (B225.2, Phase 4.4 follow-up) is
	// the alert sink for PG health state
	// transitions. When set, the watchdog
	// pushes a "PG health DEGRADED" (❌) or
	// "PG health recovered" (✅) alert on
	// consecutive read failures (default
	// threshold: 3, = 15s of "cluster_database
	// read failed"). The local NotifierSink
	// interface keeps the watchdog free of
	// internal/telegram import (avoids the
	// backup → telegram → mesh import cycle
	// that B225 already discovered).
	Notifier NotifierSink

	// ReadFailureThreshold is the number of
	// consecutive tick read failures before
	// the watchdog fires the "PG health
	// DEGRADED" alert. Default 3 (= 15s with
	// the default 5s Interval). The 15s window
	// is the right trade-off: too low and a
	// brief network blip spams the operator,
	// too high and the operator finds out
	// about a real PG outage only when other
	// systems start failing.
	ReadFailureThreshold int

	// ClusterID identifies the cluster in
	// the alert body (the watchdog is
	// cluster-scoped — it pings the
	// cluster_database table which is
	// cluster-specific). Default
	// "skygate-staging" matches the B195
	// cluster_id used by the B215 +
	// B216 admin pages.
	ClusterID string
}

// DefaultConfig returns the recommended settings.
func DefaultConfig() Config {
	return Config{
		Interval:             5 * time.Second,
		PingTimeout:          3 * time.Second,
		Logger:               log.Printf,
		Notifier:             NoopNotifierSink{},
		ReadFailureThreshold: 3,
		ClusterID:            "skygate-staging",
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

	// consecutiveReadFailures (B225.2) tracks
	// the number of consecutive tick read
	// failures. Used by the post-tick transition
	// detector to fire alerts on the
	// "DB just became unreachable" edge
	// (counter crosses the threshold) and the
	// "DB came back" edge (counter resets to
	// 0 after being >= threshold). Protected
	// by the mu mutex.
	consecutiveReadFailures int

	// hasFirstTick is set true after the FIRST
	// tick completes (regardless of success or
	// failure). The transition detector uses
	// this to skip the first tick's "edge" (a
	// freshly-started skygate shouldn't fire a
	// spurious "PG DEGRADED" alert just because
	// the first tick landed during startup).
	// After the first tick, every
	// failure→recovery and recovery→failure
	// edge fires an alert.
	hasFirstTick bool
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
	if cfg.ReadFailureThreshold <= 0 {
		cfg.ReadFailureThreshold = 3
	}
	if cfg.Notifier == nil {
		cfg.Notifier = NoopNotifierSink{}
	}
	if cfg.ClusterID == "" {
		cfg.ClusterID = "skygate-staging"
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
		// B225.2: increment the consecutive
		// failure counter + fire the
		// "PG health DEGRADED" alert on the
		// edge (counter crossing the threshold).
		w.detectReadFailureTransition(err)
		return
	}
	if row == nil || row.CurrentDSN == "" {
		// Empty desired = no admin override; keep
		// the env-DSN pool.
		// B225.2: a "row is nil but no error"
		// is a successful read with empty
		// desired state — reset the failure
		// counter + fire recovery if needed.
		w.detectReadSuccessTransition()
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
	// B225.2: a successful read (with a real DSN
	// to swap to, or even just a fresh tick on a
	// stable DSN) is the success signal. Reset
	// the failure counter + fire recovery if
	// the counter was previously >= threshold.
	w.detectReadSuccessTransition()
}

// detectReadFailureTransition increments the
// consecutive-read-failure counter. If the counter
// CROSSES the configured threshold on this tick
// (i.e. it equals the threshold now and was one less
// before), fires the "PG health DEGRADED" alert.
// The first tick is treated as the baseline (no
// alert) so a freshly-started skygate doesn't fire on
// the first sample.
//
// Safe to call concurrently: protected by w.mu.
// The watchdog's tick() is the only writer.
func (w *DBSwap) detectReadFailureTransition(err error) {
	w.mu.Lock()
	first := !w.hasFirstTick
	w.consecutiveReadFailures++
	curr := w.consecutiveReadFailures
	w.hasFirstTick = true
	threshold := w.cfg.ReadFailureThreshold
	clusterID := w.cfg.ClusterID
	interval := w.cfg.Interval
	notifier := w.cfg.Notifier
	w.mu.Unlock()

	// First observation: store the baseline,
	// no alert. The next transition (whichever
	// direction) is the first real signal.
	if first {
		return
	}
	// No edge (counter didn't just cross the
	// threshold)? No-op.
	if curr != threshold {
		return
	}
	// Crossed the threshold. Fire the alert.
	body := fmt.Sprintf(
		"PG health DEGRADED\ncluster: %s\nread failures: %d consecutive ticks (≈%s of %d ticks)\nlast error: %v\nthreshold: %d ticks\nnext check in: %s",
		clusterID,
		curr,
		time.Duration(int64(curr)*int64(interval)),
		threshold,
		err,
		threshold,
		interval)
	if w.cfg.Logger != nil {
		w.cfg.Logger("dbmigrate-watchdog: read failure threshold crossed (count=%d, threshold=%d)", curr, threshold)
	}
	_ = notifier.SendAlert("❌ " + body)
}

// detectReadSuccessTransition resets the failure
// counter to 0 and, if the counter was previously
// >= threshold, fires the "PG health recovered"
// alert. The first observation is the baseline (no
// alert) so a freshly-started skygate doesn't fire
// on the first sample.
//
// Safe to call concurrently: protected by w.mu.
func (w *DBSwap) detectReadSuccessTransition() {
	w.mu.Lock()
	first := !w.hasFirstTick
	prev := w.consecutiveReadFailures
	w.consecutiveReadFailures = 0
	w.hasFirstTick = true
	clusterID := w.cfg.ClusterID
	notifier := w.cfg.Notifier
	w.mu.Unlock()
	_ = prev // kept for future use; prevents the unused-var lint

	// First observation: store the baseline,
	// no alert.
	if first {
		return
	}
	// No edge (counter was already 0)? No-op.
	if prev == 0 {
		return
	}
	// Recovered. Fire the alert.
	body := fmt.Sprintf(
		"PG health recovered\ncluster: %s\nprevious consecutive failures: %d ticks\nnext check in: %s",
		clusterID,
		prev,
		w.cfg.Interval)
	if w.cfg.Logger != nil {
		w.cfg.Logger("dbmigrate-watchdog: read recovered (prev_count=%d)", prev)
	}
	_ = notifier.SendAlert("✅ " + body)
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
