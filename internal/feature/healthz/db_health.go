// Package healthz — db_health.go is the /db/health handler
// + background sampler (B206, v1.5.0+).
//
// Phase 1.5 / G3 of docs/ha.md:
// "DB health monitoring — connection pool, replication
// lag (if replica), slow query count, DB size, xlog
// position".
//
// What this endpoint returns:
//
//	GET /db/health → {
//	  "pool": { open, in_use, idle, max_open, wait_count, ... },
//	  "server": { is_replica, version, started_at },
//	  "database": { size_bytes, size_human },
//	  "replication": { is_replica, lag_bytes, lag_seconds },
//	  "maintenance": { last_vacuum_at, last_autovacuum_at,
//	                   last_analyze_at, dead_tuples },
//	  "xlog": { location },
//	  "slow_queries": <count from pg_stat_statements if available>,
//	  "sampled_at": <RFC3339>,
//	  "sample_interval_seconds": 30
//	}
//
// Why a background sampler
//
// The DB-side stats (size, xlog, replication, maintenance)
// require non-trivial queries: pg_database_size walks
// the relation tree; pg_last_wal_replay_lsn + the time
// delta for replication_lag_seconds requires a read on a
// replica. These can take 50-500ms each on a moderately
// large DB. The handler must return in <50ms for a
// reasonable scraping rate (every 10-30s from monitoring).
//
// The sampler runs every `Interval` (default 30s) in
// a background goroutine, stores the latest snapshot in
// a sync/atomic-protected struct, and the handler reads
// the cached snapshot. The handler also adds LIVE pool
// stats from *sql.DB.Stats() (cheap — atomic counters).
// Net result: handler returns in <5ms.
//
// B203 hot-reload compatibility
//
// Like B204's HA elector, the sampler receives a
// DBSource (not a fixed *sql.DB) and calls
// src.Current() on every tick. This way the B203
// watchdog's pool swap is followed transparently on
// the next tick — a stale *sql.DB pointer would read
// from a closed pool after Reset().

package healthz

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	skygatedb "skygate/internal/db"
)

// B210.1 (2026-09-02): the DBSource interface + the
// `fixedDBSource` test wrapper + `NewFixedDBSource`
// constructor moved to skygate/internal/db (one
// canonical copy across the whole codebase; B208.1 +
// B210 + B204 + B206 each had its own). The aliases
// below keep source-compat for existing B206 callers
// (`healthz.NewFixedDBSource(db)` still works; it
// delegates to db.FixedDBSource{DB: db}).
type DBSource = skygatedb.DBSource

//lint:ignore U1000 B210.1 type alias for source-compat with pre-B210 callers
type fixedDBSource = skygatedb.FixedDBSource

func NewFixedDBSource(db *sql.DB) skygatedb.DBSource {
	return skygatedb.FixedDBSource{DB: db}
}

// DBHealthAlertSink is the subset of telegram.Notifier
// the sampler needs. Defined here as a local interface
// (not imported from internal/telegram) to avoid the
// import cycle internal/feature/healthz →
// internal/telegram → internal/mesh → ... (the
// existing internal/backup import cycle pattern from
// B225). Production code passes a *telegram.RealNotifier
// (which satisfies the SendAlert method); the noop
// fallback is healthz.NoopAlertSink{}.
type DBHealthAlertSink interface {
	SendAlert(text string) int64
}

// NoopAlertSink is the fallback when the operator
// hasn't configured a Telegram bot. The sampler
// still tracks state transitions (for /db/health's
// "degraded for N seconds" badge) but the alert
// push is a no-op.
type NoopAlertSink struct{}

// SendAlert on the noop sink returns 0 (matches
// telegram.NoopNotifier's contract).
func (NoopAlertSink) SendAlert(string) int64 { return 0 }

// DBHealthConfig is the sampler's tunables.
type DBHealthConfig struct {
	// Interval between samples. Default 30s. Too low and
	// the sampler wastes connections; too high and the
	// cached snapshot drifts far from reality.
	Interval time.Duration

	// QueryTimeout is the deadline for ONE sampler query.
	// Default 3s — generous for a healthy DB, fails fast
	// on a degraded one.
	//
	// B466 (2026-11-13): this really is per QUERY now. Until B466 the
	// sampler created one context around the whole collect(), so a single
	// slow statement consumed the budget for every statement after it —
	// measured on a SQLite copy (121 MB file, 1 vCPU): `PRAGMA quick_check`
	// ate the 3 s and the two statements behind it failed instantly with
	// "context deadline exceeded", so a perfectly healthy database reported
	// `db_health: tick: db_health: 2 query error(s)` every 30 seconds.
	QueryTimeout time.Duration

	// IntegrityTimeout bounds ONE integrity probe (SQLite's
	// `PRAGMA quick_check`). Default 2m.
	//
	// B466: integrity is a DIFFERENT question from "is the database
	// answering" — a full-file consistency scan is O(database size) and on a
	// small or loaded host it legitimately takes longer than any sensible
	// per-query budget. It therefore gets its own budget, its own schedule
	// (IntegrityInterval) and its own three-state verdict: ok / unmeasured /
	// failed. An UNMEASURED check is not a failure and must never degrade the
	// badge — that is exactly the "a verdict must carry its age" rule of B465,
	// applied to the DB-health sampler.
	IntegrityTimeout time.Duration

	// IntegrityInterval is how often the integrity probe runs. Default 15m.
	// The result is cached between runs and reported with its age, so a
	// 15-minute-old "ok" is visible as such rather than implied fresh.
	IntegrityInterval time.Duration

	// Logger receives sampler events. If nil, the
	// package-level log.Printf is used.
	Logger func(format string, args ...any)

	// Notifier (B225.1, Phase 4.4 follow-up) is
	// the alert sink for DB health state
	// transitions. When set, the sampler pushes
	// a "DB health DEGRADED" or "DB health
	// recovered" alert to the operator's
	// Telegram chat on each transition. The
	// local DBHealthAlertSink interface keeps
	// the sampler free of internal/telegram
	// import (avoids the cycle that B225
	// already discovered in the backup
	// scheduler). When nil, the sampler uses
	// NoopAlertSink{} (silent + transition
	// tracking still works for /db/health's
	// UI badge).
	Notifier DBHealthAlertSink

	// ClusterID is the cluster_database.id used
	// as the target_id for the
	// db.health.degraded /
	// db.health.recovered audit rows. Default
	// "skygate-staging" (the B195 cluster that
	// the B215 + B216 admin pages use).
	ClusterID string

	// Dialect (B271, 2026-09-19) selects which SQL the
	// collector runs: "postgres" or "sqlite". Empty means
	// "postgres" — the historical behaviour, so every
	// existing caller that does not set it keeps the PG
	// queries.
	//
	// WHY: every query in collect() is PostgreSQL-specific
	// (pg_is_in_recovery, pg_database_size,
	// pg_stat_user_tables, pg_current_wal_lsn). On a
	// SQLite install the sampler still ran them every 30 s
	// and logged five "SQL logic error: no such function:
	// pg_is_in_recovery" errors per tick — hard-coded PG
	// SQL leaking into the SQLite path (L-16), and every
	// failing statement touches the database for nothing.
	// A plain string keeps internal/feature/healthz free of
	// a dialect enum import while main.go passes the value
	// it already computed with db.DetectDSN.
	Dialect string
}

// DefaultDBHealthConfig returns the recommended settings.
func DefaultDBHealthConfig() DBHealthConfig {
	return DBHealthConfig{
		Interval:          30 * time.Second,
		QueryTimeout:      3 * time.Second,
		IntegrityTimeout:  2 * time.Minute,
		IntegrityInterval: 15 * time.Minute,
		Logger:            log.Printf,
		Notifier:          NoopAlertSink{},
		ClusterID:         "skygate-staging",
		Dialect:           "postgres",
	}
}

// isSQLite reports whether the sampler must run the SQLite dialect queries.
func (c DBHealthConfig) isSQLite() bool {
	return c.Dialect == "sqlite"
}

// DBHealthSample is the cached snapshot. The handler
// serializes this as the response body. Fields are
// populated by the sampler's queries; pool stats are
// merged in live (not cached) by the handler.
type DBHealthSample struct {
	// Server identifies the PG instance.
	Server struct {
		IsReplica bool      `json:"is_replica"`
		Version   string    `json:"version"`
		StartedAt time.Time `json:"started_at"`
	} `json:"server"`

	// Database holds the on-disk size of the current
	// database (pg_database_size).
	Database struct {
		SizeBytes int64  `json:"size_bytes"`
		SizeHuman string `json:"size_human"`
	} `json:"database"`

	// Replication holds the replica's lag vs the primary.
	// On a primary, all fields are zero/empty. On a
	// replica, lag_bytes is receive_lsn - replay_lsn
	// (the WAL bytes still pending replay); lag_seconds
	// is the wall-clock delta from the last replayed
	// transaction to now.
	Replication struct {
		IsReplica       bool       `json:"is_replica"`
		LagBytes        *int64     `json:"lag_bytes,omitempty"`
		LagSeconds      *float64   `json:"lag_seconds,omitempty"`
		ReplayLSN       string     `json:"replay_lsn,omitempty"`
		ReplayTimestamp *time.Time `json:"replay_timestamp,omitempty"`
	} `json:"replication"`

	// Maintenance aggregates the most recent vacuum /
	// analyze across user tables. The single value per
	// field is the most recent across the database
	// (MAX of last_vacuum across all pg_stat_user_tables
	// rows). dead_tuples is the total across the same
	// view — a proxy for "how much work autovacuum has".
	Maintenance struct {
		LastVacuumAt      *time.Time `json:"last_vacuum_at,omitempty"`
		LastAutovacuumAt  *time.Time `json:"last_autovacuum_at,omitempty"`
		LastAnalyzeAt     *time.Time `json:"last_analyze_at,omitempty"`
		LastAutoanalyzeAt *time.Time `json:"last_autoanalyze_at,omitempty"`
		DeadTuples        int64      `json:"dead_tuples"`
	} `json:"maintenance"`

	// XLog is the current WAL position. On a primary
	// this is pg_current_wal_lsn(); on a replica, it's
	// pg_last_wal_replay_lsn().
	XLog struct {
		Location string `json:"location"`
	} `json:"xlog"`

	// SampledAt is when the snapshot was taken.
	SampledAt time.Time `json:"sampled_at"`

	// Integrity (B466) is the last consistency probe, cached across samples
	// because it runs on its own, much slower schedule (IntegrityInterval).
	// Status is "ok" / "unmeasured" / "failed"; empty means "never run".
	// Only "failed" is a real finding — "unmeasured" says the probe did not
	// finish in its budget, which is a fact about the PROBE, not the database.
	Integrity struct {
		Status    string     `json:"status,omitempty"`
		Detail    string     `json:"detail,omitempty"`
		CheckedAt *time.Time `json:"checked_at,omitempty"`
		AgeSec    int64      `json:"age_seconds,omitempty"`
	} `json:"integrity"`

	// SampleError is non-empty if the last sample failed
	// (one of the queries errored). The handler still
	// returns whatever fields WERE populated; the
	// operator can see the error in the JSON.
	SampleError string `json:"sample_error,omitempty"`
}

// DBHealthResponse is the full JSON shape returned by
// the handler. It's the sample + the live pool stats
// merged in. The field-level JSON tags mirror the cached
// DBHealthSample's substructs (so the handler copies
// fields from sample.* into the response).
//
// We don't embed DBHealthSample (Go's encoding/json
// doesn't support `inline` for substruct field
// flattening), so the field list is explicit. Adding a
// new field requires updating both DBHealthSample and
// DBHealthResponse — the test pins this.
type DBHealthResponse struct {
	Pool sql.DBStats `json:"pool"`

	// Server
	IsReplica bool      `json:"is_replica"`
	Version   string    `json:"version,omitempty"`
	StartedAt time.Time `json:"started_at"`

	// Database
	SizeBytes int64  `json:"size_bytes"`
	SizeHuman string `json:"size_human,omitempty"`

	// Replication
	ReplIsReplica   bool       `json:"replication_is_replica"`
	ReplLagBytes    *int64     `json:"replication_lag_bytes,omitempty"`
	ReplLagSeconds  *float64   `json:"replication_lag_seconds,omitempty"`
	ReplReplayLSN   string     `json:"replication_replay_lsn,omitempty"`
	ReplReplayStamp *time.Time `json:"replication_replay_timestamp,omitempty"`

	// Maintenance
	MaintLastVacuum      *time.Time `json:"last_vacuum_at,omitempty"`
	MaintLastAutovacuum  *time.Time `json:"last_autovacuum_at,omitempty"`
	MaintLastAnalyze     *time.Time `json:"last_analyze_at,omitempty"`
	MaintLastAutoanalyze *time.Time `json:"last_autoanalyze_at,omitempty"`
	MaintDeadTuples      int64      `json:"dead_tuples"`

	// XLog
	XLogLocation string `json:"xlog_location,omitempty"`

	// Metadata
	SlowQueries           int64     `json:"slow_queries"`
	SampledAt             time.Time `json:"sampled_at"`
	SampleError           string    `json:"sample_error,omitempty"`
	SampleIntervalSeconds int       `json:"sample_interval_seconds"`

	// Integrity (B466, SQLite) — the last consistency probe and its age.
	IntegrityStatus     string     `json:"integrity_status,omitempty"`
	IntegrityDetail     string     `json:"integrity_detail,omitempty"`
	IntegrityCheckedAt  *time.Time `json:"integrity_checked_at,omitempty"`
	IntegrityAgeSeconds int64      `json:"integrity_age_seconds,omitempty"`
}

// Sampler runs the background loop that populates the
// cached DBHealthSample. Construct with NewDBHealthSampler,
// call Start to launch, Stop to terminate.
type Sampler struct {
	cfg DBHealthConfig
	src DBSource

	// sample holds the latest cached snapshot. Reads
	// from the handler are lock-free via atomic.Pointer
	// (Go 1.19+); the sampler writes a new pointer on
	// every tick. Old snapshots are GC'd naturally.
	sample atomic.Pointer[DBHealthSample]

	// intervalSeconds is the configured interval,
	// surfaced in the JSON so monitoring can show the
	// staleness expectation.
	intervalSeconds int

	mu      sync.Mutex
	stopCh  chan struct{}
	doneCh  chan struct{}
	started bool

	// lastHealthy (B225.1) tracks the last-known
	// health state. true = last tick was healthy
	// (SampleError == ""), false = last tick was
	// degraded. Used by the post-tick transition
	// detector to fire alerts on ok→degraded and
	// degraded→ok edges. Protected by the mu
	// mutex (read+write are infrequent — only
	// on the sampler ticker, not on the hot
	// /db/health path).
	lastHealthy bool

	// hasFirstSample is set true after the FIRST
	// tick completes. The transition detector
	// uses this to skip the first tick's "edge"
	// (a freshly-started skygate shouldn't
	// fire a "DB DEGRADED" alert just because
	// the first tick landed while the pool
	// was still spinning up). After the first
	// tick, every ok→degraded or degraded→ok
	// edge fires an alert.
	hasFirstSample bool

	// integrityMu guards the cached integrity probe (B466). The probe runs on
	// its own schedule, which is much slower than the tick, so its result has
	// to outlive a single sample.
	integrityMu     sync.Mutex
	integrityStatus string    // "ok" | "unmeasured" | "failed" | "" (never run)
	integrityDetail string    //
	integrityAt     time.Time //
	integrityNext   time.Time //
}

// qctx bounds ONE query (B466). See DBHealthConfig.QueryTimeout for the
// measurement that made the old whole-sample budget untenable.
func (s *Sampler) qctx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, s.cfg.QueryTimeout)
}

// rowScan runs a single-row query under its own deadline. All sampler queries
// are literal statements with no parameters, so this stays a two-field helper
// instead of a general query builder.
func (s *Sampler) rowScan(ctx context.Context, db *sql.DB, query string, dest ...any) error {
	qc, cancel := s.qctx(ctx)
	defer cancel()
	return db.QueryRowContext(qc, query).Scan(dest...)
}

// NewDBHealthSampler constructs a sampler. The DBSource
// is consulted on every tick to obtain the current pool
// (B203 hot-reload transparency).
func NewDBHealthSampler(cfg DBHealthConfig, src DBSource) *Sampler {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = 3 * time.Second
	}
	if cfg.IntegrityTimeout <= 0 {
		cfg.IntegrityTimeout = 2 * time.Minute
	}
	if cfg.IntegrityInterval <= 0 {
		cfg.IntegrityInterval = 15 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Printf
	}
	return &Sampler{
		cfg:             cfg,
		src:             src,
		intervalSeconds: int(cfg.Interval.Seconds()),
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
}

// Start launches the sampler goroutine. Returns
// immediately. Safe to call once; subsequent calls are
// no-ops.
func (s *Sampler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return
	}
	s.started = true
	// Seed with an empty sample so the handler always
	// has something to return (even before the first
	// tick completes).
	s.sample.Store(&DBHealthSample{
		SampledAt: time.Now().UTC(),
	})
	go s.run()
}

// Stop signals the goroutine to exit and waits for it.
// Safe to call multiple times.
func (s *Sampler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return
	}
	select {
	case <-s.stopCh:
		return
	default:
		close(s.stopCh)
	}
	<-s.doneCh
}

// Sample returns the latest cached snapshot. Always
// non-nil (the sampler seeds an empty one at Start).
func (s *Sampler) Sample() *DBHealthSample {
	return s.sample.Load()
}

// IntervalSeconds returns the configured tick interval.
func (s *Sampler) IntervalSeconds() int { return s.intervalSeconds }

// run is the main loop. One tick per cfg.Interval.
func (s *Sampler) run() {
	defer close(s.doneCh)
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick is one iteration. Reads server identity, DB size,
// replication, maintenance, xlog, and stores the result.
// Errors don't stop the loop — the next tick retries.
// The handler surfaces the most recent error in the
// JSON so the operator can see "last sample failed at
// X, current snapshot is from Y".
func (s *Sampler) tick() {
	if s.src == nil {
		// A nil source is a programming error in
		// production (main.go always passes the
		// ResettableDB). Log and skip the tick — don't
		// panic, since a panic in a background goroutine
		// crashes the whole process.
		s.cfg.Logger("db_health: tick: source is nil (programming error)")
		return
	}
	db := s.src.Current()
	if db == nil {
		// The DBSource is set but the underlying pool
		// hasn't been opened yet (or was closed). Common
		// during boot; the next tick will retry.
		s.cfg.Logger("db_health: tick: no current DB (source returned nil)")
		return
	}
	sample := DBHealthSample{
		SampledAt: time.Now().UTC(),
	}
	// B466: the tick hands out a context with NO deadline of its own. Every
	// statement derives its own budget from it (rowScan → qctx), which is what
	// the QueryTimeout documentation always claimed. Before B466 this line was
	// `context.WithTimeout(context.Background(), s.cfg.QueryTimeout)` — one
	// deadline for the whole sample, so the slowest statement decided whether the
	// cheap ones behind it succeeded.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.collect(ctx, db, &sample); err != nil {
		// Preserve any partial data we collected before
		// the error — operators can still see the DB
		// size / version etc.
		sample.SampleError = err.Error()
		s.cfg.Logger("db_health: tick: %v", err)
	}
	s.sample.Store(&sample)
	// B225.1 (Phase 4.4 follow-up): transition
	// detection. Compare the new sample's
	// "healthy" status (SampleError == "") with
	// the last-known state. On a transition,
	// write an audit row + push a Telegram
	// alert via the configured Notifier. The
	// first tick (no prior state) is a special
	// case: the initial lastHealthy=false →
	// either transition is treated as "first
	// observation" so a freshly-started
	// skygate doesn't fire a spurious alert on
	// the first sample.
	s.detectTransition(&sample)
}

// detectTransition compares the new sample's
// health status with the sampler's last-known
// state. On an edge (ok→degraded or degraded→ok),
// it pushes a Telegram alert via the configured
// Notifier. The first sample is treated as the
// baseline (no alert) so a freshly-started
// skygate doesn't fire on the first tick.
//
// Safe to call concurrently: protected by s.mu.
// The /db/health hot path doesn't call this
// (only the sampler ticker does), so the lock
// is uncontended in production.
func (s *Sampler) detectTransition(sample *DBHealthSample) {
	healthy := sample.SampleError == ""

	s.mu.Lock()
	first := !s.hasFirstSample
	prev := s.lastHealthy
	s.lastHealthy = healthy
	s.hasFirstSample = true
	s.mu.Unlock()

	// First observation: store the baseline,
	// no alert. The next transition (whichever
	// direction) is the first real signal.
	if first {
		return
	}
	// No transition? No-op.
	if prev == healthy {
		return
	}

	// Transition. Format the alert text.
	var emoji, verb string
	if !healthy {
		emoji = "❌"
		verb = "DEGRADED"
	} else {
		emoji = "✅"
		verb = "recovered"
	}
	body := fmt.Sprintf(
		"DB health %s\ncluster: %s\nerror: %q\nsampled_at: %s\nnext_check_in: %s",
		verb,
		s.cfg.ClusterID,
		sample.SampleError, // "" for the recovered case
		sample.SampledAt.Format(time.RFC3339),
		s.cfg.Interval)
	if s.cfg.Logger != nil {
		s.cfg.Logger("db_health: transition %s → %s", prev, healthy)
	}
	_ = s.cfg.Notifier.SendAlert(emoji + " " + body)
}

// collect runs the actual queries. Each query is wrapped
// so a single failure (e.g. a non-replica calling
// pg_last_wal_replay_lsn) doesn't blow up the whole
// sample. The errors are joined and returned at the end.
//
// B271: the SQL is dialect-specific. On SQLite the PG
// catalog queries do not exist at all, so running them
// produced five errors per tick and nothing else — the
// SQLite branch below answers the same three operator
// questions (how big is the DB, is it healthy, how much
// bloat) with SQLite-native SQL and leaves the
// PostgreSQL-only panels (replication, xlog) empty.
func (s *Sampler) collect(ctx context.Context, db *sql.DB, out *DBHealthSample) error {
	if s.cfg.isSQLite() {
		return s.collectSQLite(ctx, db, out)
	}
	var errs []string

	// 1. Server identity: is_replica + version + started_at.
	// pg_is_in_recovery() returns true on a hot standby
	// replica, false on a primary.
	if err := s.rowScan(ctx, db,
		`SELECT pg_is_in_recovery(), version(), pg_postmaster_start_time()`,
		&out.Server.IsReplica, &out.Server.Version, &out.Server.StartedAt); err != nil {
		errs = append(errs, fmt.Sprintf("server: %v", err))
	}

	// 2. Database size: pg_database_size returns bytes.
	// Always populated regardless of replica state.
	if err := s.rowScan(ctx, db,
		`SELECT pg_database_size(current_database())`,
		&out.Database.SizeBytes); err != nil {
		errs = append(errs, fmt.Sprintf("database.size: %v", err))
	} else {
		out.Database.SizeHuman = humanBytes(out.Database.SizeBytes)
	}

	// 3. Replication: only populated on a replica.
	// On a primary, is_replica=false and the lag fields
	// are nil (the JSON omitempty drops them).
	out.Replication.IsReplica = out.Server.IsReplica
	if out.Server.IsReplica {
		var (
			replayLSN  string
			receiveLSN string
			replayTime sql.NullTime
		)
		if err := s.rowScan(ctx, db,
			`SELECT pg_last_wal_replay_lsn(), pg_last_wal_receive_lsn(), pg_last_xact_replay_timestamp()`,
			&replayLSN, &receiveLSN, &replayTime); err != nil {
			errs = append(errs, fmt.Sprintf("replication: %v", err))
		} else {
			out.Replication.ReplayLSN = replayLSN
			if replayTime.Valid {
				t := replayTime.Time
				out.Replication.ReplayTimestamp = &t
				// lag_seconds = wall-clock now - last replayed
				// transaction timestamp. This is the standard
				// "replica is N seconds behind primary" metric.
				lag := time.Since(t).Seconds()
				out.Replication.LagSeconds = &lag
			}
			// lag_bytes: receive_lsn - replay_lsn. We use
			// the simpler byte difference of the WAL
			// positions. The PG lsn2pg / pg_lsn_diff
			// function gives an exact value if needed.
			//
			// For now we report the receive_lsn as the
			// "bytes pending" proxy. A future iteration
			// could add pg_lsn_diff(replay_lsn, receive_lsn)
			// to get the exact value, but that requires
			// the extension to be installed.
			if replayLSN != "" && receiveLSN != "" && replayLSN != receiveLSN {
				// Best-effort: a string indicator of
				// "lag is non-zero". Exact bytes would
				// need pg_lsn_diff (extension).
				lagBytes := int64(1) // sentinel "lag detected"
				_ = lagBytes
				// We deliberately don't set LagBytes —
				// without pg_lsn_diff we can't compute
				// the exact number. The LagSeconds
				// field is the operator-facing metric.
			}
		}
	}

	// 4. Maintenance: aggregate across user tables.
	// MAX(last_vacuum) etc. gives "most recent vacuum
	// anywhere in this DB". SUM(n_dead_tup) is a
	// proxy for "how much bloat the autovacuum has to
	// clean up".
	if err := s.rowScan(ctx, db,
		`SELECT
			MAX(last_vacuum),
			MAX(last_autovacuum),
			MAX(last_analyze),
			MAX(last_autoanalyze),
			COALESCE(SUM(n_dead_tup), 0)
		 FROM pg_stat_user_tables`,
		&out.Maintenance.LastVacuumAt,
		&out.Maintenance.LastAutovacuumAt,
		&out.Maintenance.LastAnalyzeAt,
		&out.Maintenance.LastAutoanalyzeAt,
		&out.Maintenance.DeadTuples,
	); err != nil {
		errs = append(errs, fmt.Sprintf("maintenance: %v", err))
	}

	// 5. XLog position. On a primary this is the
	// current WAL insert location; on a replica it's
	// the last-replayed LSN (replication progress).
	// Either way, the field is populated.
	if out.Server.IsReplica {
		if err := s.rowScan(ctx, db,
			`SELECT pg_last_wal_replay_lsn()`,
			&out.XLog.Location); err != nil {
			errs = append(errs, fmt.Sprintf("xlog.replay: %v", err))
		}
	} else {
		if err := s.rowScan(ctx, db,
			`SELECT pg_current_wal_lsn()`,
			&out.XLog.Location); err != nil {
			errs = append(errs, fmt.Sprintf("xlog.current: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("db_health: %d query error(s): %v", len(errs), errs)
	}
	return nil
}

// collectSQLite is the SQLite counterpart of collect (B271).
//
// It answers the questions the /db/health page can honestly answer on a
// file-backed database, and deliberately leaves the PostgreSQL-only fields
// zero (they are `omitempty` in the JSON): SQLite has no server identity, no
// WAL/replication state and no autovacuum catalog. The previous behaviour —
// running the PG queries anyway — logged five errors per 30 s tick and gave an
// operator a permanently "degraded" DB health badge on a perfectly healthy
// install (live case: the aro host, 2026-09-19).
func (s *Sampler) collectSQLite(ctx context.Context, db *sql.DB, out *DBHealthSample) error {
	var errs []string

	// 1. Size: page_count * page_size is the canonical SQLite file size.
	// page_size must be read AFTER page_count on the same connection; both
	// are cheap pragmas.
	var pageCount, pageSize int64
	if err := s.rowScan(ctx, db, `PRAGMA page_count`, &pageCount); err != nil {
		errs = append(errs, fmt.Sprintf("database.page_count: %v", err))
	} else if err := s.rowScan(ctx, db, `PRAGMA page_size`, &pageSize); err != nil {
		errs = append(errs, fmt.Sprintf("database.page_size: %v", err))
	} else {
		out.Database.SizeBytes = pageCount * pageSize
		out.Database.SizeHuman = humanBytes(out.Database.SizeBytes)
	}

	// 2. Version is what `SELECT sqlite_version()` reports; SQLite has no
	// "postmaster start time", so StartedAt stays zero.
	if err := s.rowScan(ctx, db, `SELECT sqlite_version()`, &out.Server.Version); err != nil {
		errs = append(errs, fmt.Sprintf("server: %v", err))
	}
	// A file-backed SQLite database is never a replica.
	out.Server.IsReplica = false
	out.Replication.IsReplica = false

	// 3. Integrity (B466): its own schedule, its own budget, its own verdict.
	//
	// B271 ran `PRAGMA quick_check` on EVERY 30-second tick with the ordinary
	// 3-second budget, and treated a timeout as a query error. Measured on a
	// 121 MB SQLite file on a 1-vCPU host (the restored copy, 2026-11-13): the
	// scan does not finish in 3 s, so every tick logged
	//
	//	db_health: tick: db_health: 2 query error(s): [db.integrity_check:
	//	context deadline exceeded db.journal_mode: context deadline exceeded]
	//
	// — "2 query errors" on a perfectly healthy database, and a degraded badge
	// that would never clear. A full-file scan is O(size) and slow on purpose;
	// it belongs on IntegrityInterval with IntegrityTimeout, and its outcome
	// has three states, only one of which is a finding:
	//
	//	ok         the database answered "ok"
	//	unmeasured the probe did not finish (or failed to run) — a fact about
	//	           the PROBE, never reported as a database error
	//	failed     the database answered something other than "ok"
	s.applyIntegrityProbe(ctx, db, out)

	// 4. Journal mode (wal vs delete) — the operator-visible answer to
	// "am I in WAL mode", and cheap. Reported through the log only: the
	// response shape has no field for it, and inventing one here would
	// change the /db/health contract.
	var journalMode string
	if err := s.rowScan(ctx, db, `PRAGMA journal_mode`, &journalMode); err != nil {
		errs = append(errs, fmt.Sprintf("db.journal_mode: %v", err))
	} else if s.cfg.Logger != nil {
		s.cfg.Logger("db_health: sqlite journal_mode=%s size=%s", journalMode, out.Database.SizeHuman)
	}

	if len(errs) > 0 {
		return fmt.Errorf("db_health: %d query error(s): %v", len(errs), errs)
	}
	return nil
}

// applyIntegrityProbe runs `PRAGMA quick_check` at most once per
// IntegrityInterval and copies the CACHED verdict into the sample with its age.
// A cached "ok" that is 14 minutes old is reported as such — the sample carries
// CheckedAt/AgeSec, so the page cannot present it as a fresh measurement.
func (s *Sampler) applyIntegrityProbe(ctx context.Context, db *sql.DB, out *DBHealthSample) {
	now := time.Now().UTC()
	s.integrityMu.Lock()
	due := (!now.Before(s.integrityNext) || s.integrityStatus == "")
	if due {
		// Claim the slot BEFORE starting: the probe runs in its own goroutine
		// (a full-file scan must never delay a health sample, and the ticker
		// would otherwise block for up to IntegrityTimeout at boot) and two
		// overlapping probes would both write a verdict.
		s.integrityNext = now.Add(s.cfg.IntegrityInterval)
	}
	s.integrityMu.Unlock()

	if due {
		go s.runIntegrityProbe(db)
	}

	s.integrityMu.Lock()
	status, detail, at := s.integrityStatus, s.integrityDetail, s.integrityAt
	s.integrityMu.Unlock()
	out.Integrity.Status = status
	out.Integrity.Detail = detail
	if !at.IsZero() {
		t := at
		out.Integrity.CheckedAt = &t
		out.Integrity.AgeSec = int64(time.Since(at).Seconds())
	}
	// Only an ANSWERED "not ok" is a finding; an unfinished probe never is.
	if status == "failed" {
		out.SampleError = strings.TrimSpace(out.SampleError + " db.integrity_check: " + detail)
	}
}

// runIntegrityProbe performs ONE `PRAGMA quick_check` under IntegrityTimeout and
// records the three-state verdict. It is also the entry point the tests drive
// synchronously, which is why it takes no context: the budget comes from the
// configuration, so the test can pin it to a millisecond.
func (s *Sampler) runIntegrityProbe(db *sql.DB) {
	started := time.Now()
	qc, cancel := context.WithTimeout(context.Background(), s.cfg.IntegrityTimeout)
	defer cancel()
	var answer string
	err := db.QueryRowContext(qc, `PRAGMA quick_check`).Scan(&answer)

	status, detail := "ok", ""
	switch {
	case err != nil:
		// Not a finding about the database: the probe could not finish.
		status = "unmeasured"
		detail = fmt.Sprintf("%v (budget %s)", err, s.cfg.IntegrityTimeout)
	case answer != "ok":
		// This one IS a finding: the engine answered and said the file is not clean.
		status = "failed"
		detail = answer
	}

	s.integrityMu.Lock()
	s.integrityStatus = status
	s.integrityDetail = detail
	s.integrityAt = time.Now().UTC()
	s.integrityMu.Unlock()

	if s.cfg.Logger == nil {
		return
	}
	switch status {
	case "ok":
		s.cfg.Logger("db_health: sqlite integrity ok (probe took %s; next in %s)", time.Since(started).Round(time.Millisecond), s.cfg.IntegrityInterval)
	case "unmeasured":
		s.cfg.Logger("db_health: sqlite integrity UNMEASURED — the probe did not finish: %s. This is NOT a database error (a full-file scan on a small host can exceed its budget); the check runs again in %s", detail, s.cfg.IntegrityInterval)
	default:
		s.cfg.Logger("db_health: sqlite integrity FAILED — the engine answered %q", detail)
	}
}

// humanBytes returns a human-readable byte count
// (e.g. "539 MB", "1.2 GB"). The pg_database_size value
// can be in the gigabytes for a moderately-sized DB;
// a 64-bit int is sufficient (max ~9.2 EB).
func humanBytes(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.1f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// GetDBHealth is the HTTP handler for GET /db/health.
// It reads the cached sample (populated by the
// background Sampler) and merges in the live pool stats
// from *sql.DB.Stats() (cheap atomic reads).
//
// The handler does NOT execute any of the expensive
// DB-side queries — that's the sampler's job. If the
// sampler hasn't run yet (Start was called but the
// first tick hasn't fired), the response is still
// well-formed (the cached sample is the empty
// zero-value seeded at Start, and the live pool stats
// are always available).
func (s *Service) GetDBHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	// Build the response from the cached sample. We
	// don't lock — the atomic pointer load is sufficient.
	resp := DBHealthResponse{
		SampledAt: time.Now().UTC(), // best-effort
	}
	if s.DBHealthSampler != nil {
		if sample := s.DBHealthSampler.Sample(); sample != nil {
			// Copy the sample's substruct fields into
			// the flat response shape.
			resp.IsReplica = sample.Server.IsReplica
			resp.Version = sample.Server.Version
			resp.StartedAt = sample.Server.StartedAt
			resp.SizeBytes = sample.Database.SizeBytes
			resp.SizeHuman = sample.Database.SizeHuman
			resp.ReplIsReplica = sample.Replication.IsReplica
			resp.ReplLagBytes = sample.Replication.LagBytes
			resp.ReplLagSeconds = sample.Replication.LagSeconds
			resp.ReplReplayLSN = sample.Replication.ReplayLSN
			resp.ReplReplayStamp = sample.Replication.ReplayTimestamp
			resp.MaintLastVacuum = sample.Maintenance.LastVacuumAt
			resp.MaintLastAutovacuum = sample.Maintenance.LastAutovacuumAt
			resp.MaintLastAnalyze = sample.Maintenance.LastAnalyzeAt
			resp.MaintLastAutoanalyze = sample.Maintenance.LastAutoanalyzeAt
			resp.MaintDeadTuples = sample.Maintenance.DeadTuples
			resp.XLogLocation = sample.XLog.Location
			resp.SampledAt = sample.SampledAt
			resp.SampleError = sample.SampleError
			// B466: the integrity verdict travels with its age, so a cached
			// "ok" cannot be read as a fresh measurement.
			resp.IntegrityStatus = sample.Integrity.Status
			resp.IntegrityDetail = sample.Integrity.Detail
			resp.IntegrityCheckedAt = sample.Integrity.CheckedAt
			resp.IntegrityAgeSeconds = sample.Integrity.AgeSec
			resp.SampleIntervalSeconds = s.DBHealthSampler.IntervalSeconds()
		}
	}
	// Live pool stats from the current *sql.DB. The
	// sampler already updates its src.Current() on each
	// tick, so this is consistent with the cached
	// sample's view of the pool (the only difference is
	// a few ms of latency).
	if s.DBHealthSrc != nil {
		if db := s.DBHealthSrc.Current(); db != nil {
			resp.Pool = db.Stats()
		}
	}

	// slow_queries: not yet implemented. Reserved for
	// when pg_stat_statements is available + a slow_query
	// threshold is configured. Return 0 for now.
	resp.SlowQueries = 0

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
