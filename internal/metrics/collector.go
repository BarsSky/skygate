// Package metrics — collector.go is the B226
// periodic sampler that updates the registered
// gauges + counters from the live skygate state
// (DB pool stats, cluster_node counts, audit log
// deltas, etc). It runs on a 30s ticker (same
// cadence as the B206 healthz sampler — operators
// can correlate /db/health transitions with
// /metrics deltas during an incident).
//
// The collector is decoupled from the rest of
// the B226 surface (the metrics package itself
// is just the in-memory store + textfmt encoder)
// so the test suite can pin the textfmt
// formatting without spinning up a DB.

package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	skygatedb "skygate/internal/db"
)

// SourceProvider supplies the live data for the
// collector. Production wiring (cmd/skygate/main.go)
// passes a closure that queries skygate's
// *db.ResettableDB. The interface is what the
// unit tests pin against (the tests pass a
// sqlmock-style fake to assert the SQL shape).
type SourceProvider interface {
	// PingDB returns nil if the DB is reachable
	// (for the skygate_db_health gauge).
	PingDB(ctx context.Context) error
	// ListNodeStateCounts returns the count of
	// cluster_node rows by state, plus the
	// primary_node_id (so the elector gauge
	// can label its series).
	ListNodeStateCounts(ctx context.Context) (clusterID string, counts map[string]int, primaryID string, err error)
	// ListFailoverState returns the last_failover
	// record (if any) for the failover_state
	// gauge. nil result = no last failover.
	ListFailoverState(ctx context.Context) (clusterID string, pending bool, err error)
	// DBPoolStats returns the live *sql.DB.Stats()
	// snapshot for the skygate_db_pool_connections
	// gauges.
	DBPoolStats() sql.DBStats
}

// StartCollector launches the B226 sampler
// goroutine. Returns a stop function that closes
// the underlying context. Safe to call once at
// process startup; the goroutine exits cleanly on
// context cancellation.
//
// interval = 0 disables the collector (the
// /metrics endpoint still serves the in-memory
// values from NewCounter / NewGauge declarations,
// they just stop being updated).
func StartCollector(ctx context.Context, src SourceProvider, interval time.Duration) (stop func()) {
	if interval <= 0 {
		log.Printf("metrics: collector disabled (interval=0)")
		return func() {}
	}
	cctx, cancel := context.WithCancel(ctx)
	var running atomic.Bool
	go func() {
		running.Store(true)
		defer running.Store(false)
		t := time.NewTicker(interval)
		defer t.Stop()
		// Run one tick immediately so the metrics
		// are populated on the first /metrics
		// scrape (otherwise the operator sees an
		// empty page for the first 30s after
		// skygate boot).
		runTick(cctx, src)
		for {
			select {
			case <-cctx.Done():
				return
			case <-t.C:
				runTick(cctx, src)
			}
		}
	}()
	return func() {
		cancel()
		// Wait briefly for the goroutine to
		// exit. The "running" flag is for
		// test assertions, not for the wait
		// itself (we don't need a sync.WaitGroup
		// because the goroutine exits within
		// one tick after cancel).
		for i := 0; i < 100; i++ {
			if !running.Load() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func runTick(ctx context.Context, src SourceProvider) {
	// DB health gauge: 1 if PingDB succeeds, 0
	// otherwise. The cluster label uses the
	// "default" cluster (the B195 cluster that
	// the B215 + B216 admin pages use).
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	dbOK := src.PingDB(pingCtx) == nil
	cancel()
	DBHealthGauge.WithLabelValues("skygate-staging").Set(boolToFloat(dbOK))

	// DB size gauge. Read pg_database_size once
	// per tick (cheap query — just reads a single
	// int). The B206 healthz sampler also queries
	// this for its JSON response; B226 queries it
	// independently so the metrics endpoint is
	// decoupled from the B206 ticker.
	if dbSize, ok := queryDBSizeBytes(ctx, src); ok {
		DBSizeBytesGauge.WithLabelValues("skygate-staging").Set(dbSize)
	}

	// Cluster node state counts.
	_, counts, primaryID, err := src.ListNodeStateCounts(ctx)
	if err != nil {
		log.Printf("metrics: ListNodeStateCounts: %v", err)
	} else {
		// Reset the per-state gauges to 0 (so
		// states that disappeared in this tick
		// don't carry over a stale value), then
		// set the current counts.
		ClusterNodesGauge.Reset()
		for state, n := range counts {
			ClusterNodesGauge.WithLabelValues("skygate-staging", state).Set(float64(n))
		}
		// Set the primary_node_id (1 if a
		// primary is elected, 0 otherwise).
		// The label is the primary_node_id
		// (or "" if none).
		ElectorIsPrimaryGauge.Reset()
		if primaryID != "" {
			ElectorIsPrimaryGauge.WithLabelValues(primaryID).Set(1)
		}
	}

	// Last failover state.
	_, pending, err := src.ListFailoverState(ctx)
	if err != nil {
		log.Printf("metrics: ListFailoverState: %v", err)
	} else {
		FailoverStateGauge.WithLabelValues("skygate-staging").Set(boolToFloat(pending))
	}

	// DB pool stats.
	pool := src.DBPoolStats()
	DBPoolOpenGauge.Set(float64(pool.OpenConnections))
	DBPoolIdleGauge.Set(float64(pool.Idle))
	DBPoolInUseGauge.Set(float64(pool.InUse))
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// queryDBSizeBytes is a one-off helper that
// reads pg_database_size for the skygate DB
// and returns it in bytes. Returns (0, false)
// on any error (the B226 collector logs +
// skips the update on failure).
func queryDBSizeBytes(ctx context.Context, src SourceProvider) (float64, bool) {
	type dbsizer interface {
		DBSizeBytes(ctx context.Context) (int64, error)
	}
	if s, ok := src.(dbsizer); ok {
		size, err := s.DBSizeBytes(ctx)
		if err != nil {
			return 0, false
		}
		return float64(size), true
	}
	// Fallback: query pg_database_size via the
	// pool (works for DBPoolSource).
	if p, ok := src.(*DBPoolSource); ok {
		conn := p.DB.Current()
		if conn == nil {
			return 0, false
		}
		var size int64
		err := conn.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size)
		if err != nil {
			return 0, false
		}
		return float64(size), true
	}
	return 0, false
}

// ----- The actual metric declarations -----
// These are exported so /metrics serves them, and
// the B-check script can grep for them.
//
// Naming: skygate_<area>_<metric>{<labels>}.
const clusterLabel = "cluster"

// ClusterNodesGauge is the number of cluster_node
// rows by state. Labels: cluster (always
// "skygate-staging" for v1), state (pending|ready|
// draining|failed).
var ClusterNodesGauge = NewGaugeVec("skygate_cluster_nodes",
	"Number of cluster_node rows, by state.",
	[]string{clusterLabel, "state"})

// ClusterNodesTotalGauge is the sum of all
// cluster_node states (the "total nodes" metric
// that operators want at a glance).
var ClusterNodesTotalGauge = NewGauge("skygate_cluster_nodes_total",
	"Total number of cluster_node rows across all states.")

// DBHealthGauge is 1 if the DB is reachable, 0
// otherwise (the operator can alert on a sustained
// 0 with a Prom expression).
var DBHealthGauge = NewGaugeVec("skygate_db_health",
	"DB health (1=ping ok, 0=ping failed).",
	[]string{clusterLabel})

// DBSizeBytesGauge is the current pg_database_size
// for the skygate-staging cluster. Updated by the
// B206 healthz sampler; the B226 collector just
// exposes it as a Prometheus gauge for scraping.
var DBSizeBytesGauge = NewGaugeVec("skygate_db_size_bytes",
	"Current pg_database_size in bytes.",
	[]string{clusterLabel})

// DBPoolOpenGauge / IdleGauge / InUseGauge are
// from sql.DBStats (live pool stats, not cached).
var (
	DBPoolOpenGauge = NewGauge("skygate_db_pool_open_connections",
		"Number of open connections in the pool.")
	DBPoolIdleGauge = NewGauge("skygate_db_pool_idle_connections",
		"Number of idle connections in the pool.")
	DBPoolInUseGauge = NewGauge("skygate_db_pool_in_use_connections",
		"Number of in-use connections in the pool.")
)

// ElectorIsPrimaryGauge is 1 if the skygate HA
// elector considers this node the primary, 0
// otherwise. Labels: node (the primary_node_id).
var ElectorIsPrimaryGauge = NewGaugeVec("skygate_elector_is_primary",
	"1 if this node is the skygate HA primary, 0 otherwise.",
	[]string{"node"})

// FailoverStateGauge is 1 if there's a pending
// last_failover (the B220 Rollback button is
// armed), 0 otherwise.
var FailoverStateGauge = NewGaugeVec("skygate_failover_state",
	"1 if there's a pending last_failover (rollback armed), 0 otherwise.",
	[]string{clusterLabel})

// BuildInfoGauge is always 1, with labels for
// version + go_version. The Prometheus convention
// for exposing build metadata as a labelled gauge.
var BuildInfoGauge = NewGaugeVec("skygate_build_info",
	"Build metadata (always 1; labels carry the info).",
	[]string{"version", "go_version"})

// ----- Production SourceProvider -----

// DBPoolSource is a SourceProvider backed by a
// *db.ResettableDB. Used in main.go.
type DBPoolSource struct {
	DB      skygatedb.DBSource
	Cluster string
}

func (s *DBPoolSource) PingDB(ctx context.Context) error {
	if s.DB == nil {
		return fmt.Errorf("nil DB")
	}
	conn := s.DB.Current()
	if conn == nil {
		return fmt.Errorf("nil pool")
	}
	return conn.PingContext(ctx)
}

func (s *DBPoolSource) ListNodeStateCounts(ctx context.Context) (string, map[string]int, string, error) {
	clusterID := s.Cluster
	if clusterID == "" {
		clusterID = "skygate-staging"
	}
	conn := s.DB.Current()
	if conn == nil {
		return clusterID, map[string]int{}, "", fmt.Errorf("nil pool")
	}
	// Counts by state.
	rows, err := conn.QueryContext(ctx, `
		SELECT COALESCE(state, ''), COUNT(*)
		  FROM cluster_node
		 WHERE cluster_id = $1
		 GROUP BY state
	`, clusterID)
	if err != nil {
		return clusterID, map[string]int{}, "", err
	}
	counts := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			rows.Close()
			return clusterID, counts, "", err
		}
		if state != "" {
			counts[state] = n
		}
	}
	if err := rows.Err(); err != nil {
		return clusterID, counts, "", err
	}
	rows.Close()
	// The skygate HA primary's id lives on
	// cluster_database (the B204 schema), not
	// on cluster_node. Read it separately.
	var primaryID string
	if err := conn.QueryRowContext(ctx, `
		SELECT COALESCE(primary_node_id, '')
		  FROM cluster_database
		 WHERE id = $1
		 LIMIT 1
	`, clusterID).Scan(&primaryID); err != nil && err != sql.ErrNoRows {
		return clusterID, counts, primaryID, err
	}
	// Set the total gauge (sum of all states).
	ClusterNodesTotalGauge.Set(float64(sumValues(counts)))
	return clusterID, counts, primaryID, nil
}

func (s *DBPoolSource) ListFailoverState(ctx context.Context) (string, bool, error) {
	clusterID := s.Cluster
	if clusterID == "" {
		clusterID = "skygate-staging"
	}
	conn := s.DB.Current()
	if conn == nil {
		return clusterID, false, fmt.Errorf("nil pool")
	}
	// Read the B220 "last_failover" global setting.
	// The B220 schema stores it as a JSON blob in
	// global_settings.value. We treat presence as
	// "pending rollback armed" (the B220 row is
	// cleared by ClearLastFailover on successful
	// rollback).
	var value string
	err := conn.QueryRowContext(ctx, `
		SELECT value FROM global_settings
		 WHERE key = 'db.last_failover'
		 LIMIT 1
	`).Scan(&value)
	if err == sql.ErrNoRows {
		return clusterID, false, nil
	}
	if err != nil {
		return clusterID, false, err
	}
	return clusterID, value != "", nil
}

func (s *DBPoolSource) DBPoolStats() sql.DBStats {
	conn := s.DB.Current()
	if conn == nil {
		return sql.DBStats{}
	}
	return conn.Stats()
}

// DBSizeBytes returns pg_database_size for the
// current database. B226 uses this for the
// skygate_db_size_bytes gauge.
func (s *DBPoolSource) DBSizeBytes(ctx context.Context) (int64, error) {
	conn := s.DB.Current()
	if conn == nil {
		return 0, fmt.Errorf("nil pool")
	}
	var size int64
	err := conn.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&size)
	return size, err
}

func sumValues(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}
