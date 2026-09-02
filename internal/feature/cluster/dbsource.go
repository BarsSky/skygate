// Package cluster (feature) — dbsource.go defines the
// DBSource interface for the cluster Service. B210
// (v1.5.0+, 2026-09-02) — fixes the B203 hot-reload
// regression for the cluster join + heartbeat HTTP API.
//
// The regression
//
// The cluster Service captured `*sql.DB` at construction
// time (DB: app.DB in main.go). After the B203 watchdog's
// first swap on container start, the captured pointer
// points at a closed pool → "sql: database is closed" →
// the join + heartbeat endpoints 500 indefinitely.
//
// The fix
//
// Replace `DB *sql.DB` (frozen pointer) with
// `DB DBSource` (live getter). `s.dbc()` returns the
// current pool on every call. The ResettableDB from
// internal/db (B203) satisfies DBSource directly via its
// Current() method — main.go passes the wrapper instead
// of the captured *sql.DB. NewService accepts a DBSource
// (the ResettableDB) and the field is the DBSource; the
// two call sites (`cluster.Join(s.dbc(), ...)` and
// `cluster.Heartbeat(s.dbc(), ...)`) read the live pool
// on every HTTP request.
//
// Why this regression slipped past B-check + live-verify
//
// Same root cause as the B208.1 admin + B210 auth
// regressions. The B-check scripts (B201) call the
// endpoint in isolation and the join+heartbeat paths
// don't trigger a watchdog swap in the test (the test
// DBs are ephemeral SQLite or fresh PG with no
// cluster_database.current_dsn row). The live agent
// has the row (set by B200 verify) and the watchdog
// swaps on every container start.

package cluster

import "database/sql"

// DBSource is the minimum surface the cluster Service
// needs to obtain the current *sql.DB. The ResettableDB
// wrapper from internal/db (B203) satisfies it via its
// Current() method.
type DBSource interface {
	Current() *sql.DB
}

// dbc returns the cluster Service's current *sql.DB.
// Used at every cluster.Join / cluster.Heartbeat call
// site so the watchdog's hot-reload is followed
// transparently.
func (s *Service) dbc() *sql.DB {
	if s.DB == nil {
		return nil
	}
	return s.DB.Current()
}
