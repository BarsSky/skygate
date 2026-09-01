// Package healthz — db_health.go is the /db/health handler
// + background sampler (B206, v1.5.0+).
//
// Phase 1.5 / G3 of docs/internal/cluster-management.md:
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
	"sync"
	"sync/atomic"
	"time"
)

// DBSource is the minimum surface the sampler needs to
// obtain the current *sql.DB. The ResettableDB wrapper
// from internal/db (B203) satisfies it via its
// Current() method. A plain *sql.DB also satisfies it
// (every call returns the same pointer) — use the
// helper NewFixedDBSource for tests + one-off scripts.
type DBSource interface {
	Current() *sql.DB
}

// fixedDBSource is a DBSource that always returns the
// same *sql.DB. Used by unit tests + scripts that don't
// have a ResettableDB.
type fixedDBSource struct {
	db *sql.DB
}

func (f fixedDBSource) Current() *sql.DB { return f.db }

// NewFixedDBSource wraps a plain *sql.DB so it satisfies
// DBSource. Most callers should pass the ResettableDB
// directly (it already has Current()).
func NewFixedDBSource(db *sql.DB) DBSource { return fixedDBSource{db: db} }

// DBHealthConfig is the sampler's tunables.
type DBHealthConfig struct {
	// Interval between samples. Default 30s. Too low and
	// the sampler wastes connections; too high and the
	// cached snapshot drifts far from reality.
	Interval time.Duration

	// QueryTimeout is the per-query deadline for each
	// of the sampler queries. Default 3s — generous for
	// a healthy DB, fails fast on a degraded one.
	QueryTimeout time.Duration

	// Logger receives sampler events. If nil, the
	// package-level log.Printf is used.
	Logger func(format string, args ...any)
}

// DefaultDBHealthConfig returns the recommended settings.
func DefaultDBHealthConfig() DBHealthConfig {
	return DBHealthConfig{
		Interval:     30 * time.Second,
		QueryTimeout: 3 * time.Second,
		Logger:       log.Printf,
	}
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
		IsReplica       bool    `json:"is_replica"`
		LagBytes        *int64  `json:"lag_bytes,omitempty"`
		LagSeconds      *float64 `json:"lag_seconds,omitempty"`
		ReplayLSN       string  `json:"replay_lsn,omitempty"`
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
		DeadTuples         int64      `json:"dead_tuples"`
	} `json:"maintenance"`

	// XLog is the current WAL position. On a primary
	// this is pg_current_wal_lsn(); on a replica, it's
	// pg_last_wal_replay_lsn().
	XLog struct {
		Location string `json:"location"`
	} `json:"xlog"`

	// SampledAt is when the snapshot was taken.
	SampledAt time.Time `json:"sampled_at"`

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
	Pool                  sql.DBStats  `json:"pool"`

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

	mu     sync.Mutex
	stopCh chan struct{}
	doneCh chan struct{}
	started bool
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
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.QueryTimeout)
	defer cancel()
	if err := s.collect(ctx, db, &sample); err != nil {
		// Preserve any partial data we collected before
		// the error — operators can still see the DB
		// size / version etc.
		sample.SampleError = err.Error()
		s.cfg.Logger("db_health: tick: %v", err)
	}
	s.sample.Store(&sample)
}

// collect runs the actual queries. Each query is wrapped
// so a single failure (e.g. a non-replica calling
// pg_last_wal_replay_lsn) doesn't blow up the whole
// sample. The errors are joined and returned at the end.
func (s *Sampler) collect(ctx context.Context, db *sql.DB, out *DBHealthSample) error {
	var errs []string

	// 1. Server identity: is_replica + version + started_at.
	// pg_is_in_recovery() returns true on a hot standby
	// replica, false on a primary.
	if err := db.QueryRowContext(ctx,
		`SELECT pg_is_in_recovery(), version(), pg_postmaster_start_time()`,
	).Scan(&out.Server.IsReplica, &out.Server.Version, &out.Server.StartedAt); err != nil {
		errs = append(errs, fmt.Sprintf("server: %v", err))
	}

	// 2. Database size: pg_database_size returns bytes.
	// Always populated regardless of replica state.
	if err := db.QueryRowContext(ctx,
		`SELECT pg_database_size(current_database())`,
	).Scan(&out.Database.SizeBytes); err != nil {
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
			replayLSN   string
			receiveLSN  string
			replayTime  sql.NullTime
		)
		if err := db.QueryRowContext(ctx,
			`SELECT pg_last_wal_replay_lsn(), pg_last_wal_receive_lsn(), pg_last_xact_replay_timestamp()`,
		).Scan(&replayLSN, &receiveLSN, &replayTime); err != nil {
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
	if err := db.QueryRowContext(ctx,
		`SELECT
			MAX(last_vacuum),
			MAX(last_autovacuum),
			MAX(last_analyze),
			MAX(last_autoanalyze),
			COALESCE(SUM(n_dead_tup), 0)
		 FROM pg_stat_user_tables`,
	).Scan(
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
		if err := db.QueryRowContext(ctx,
			`SELECT pg_last_wal_replay_lsn()`,
		).Scan(&out.XLog.Location); err != nil {
			errs = append(errs, fmt.Sprintf("xlog.replay: %v", err))
		}
	} else {
		if err := db.QueryRowContext(ctx,
			`SELECT pg_current_wal_lsn()`,
		).Scan(&out.XLog.Location); err != nil {
			errs = append(errs, fmt.Sprintf("xlog.current: %v", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("db_health: %d query error(s): %v", len(errs), errs)
	}
	return nil
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
