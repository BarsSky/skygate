// Package admin — dbsource.go defines the DBSource
// interface for the admin Service. B208 (v1.5.0+,
// 2026-09-01) — fixes a pre-existing B203 regression.
//
// The regression
//
// The skygate-watchdog (B203) hot-reloads the pgxpool
// every 5s when cluster_database.current_dsn differs
// from the env DSN. On each swap, the old *sql.DB is
// Close()d in a goroutine; the new pool is re-pointed on
// the ResettableDB. Code that captured `*sql.DB` at
// service-construction time (including ALL of the
// admin feature package: /admin/database, /admin/
// cluster, /admin/audit, /admin/nodes, /admin/ha,
// /admin/acls, /admin/users, etc.) keeps using the
// closed pool → every query returns
// "sql: database is closed" → every admin page 500s.
//
// Why this regression slipped past B199-B207 live
// verifies: the B-check scripts call handlers in
// sequence, and the B203 watchdog's first hot-reload
// fires ~5s after the container starts. The B-checks
// themselves pass (handler is reachable, query shape
// is correct), but the actual SQL query on a closed
// pool fails silently with a non-B-check-covered error.
// Live agent was in a broken state for hours until
// B207's live-verify surfaced it.
//
// The fix
//
// Replace `DB *sql.DB` (frozen pointer) with
// `DB DBSource` (live getter). Every call site reads
// the current pool via `s.dbc().Current()`. The ResettableDB
// satisfies DBSource directly via its Current() method
// — main.go passes the wrapper instead of the captured
// *sql.DB.
//
// Why a local DBSource (not import from watchdog/elector/healthz)
// All three of those packages have a copy of DBSource
// (B204 / B206) but they're unexported. Adding a 4th
// copy in admin matches the pattern. The shared shape
// is small (one method) and exporting it would create
// an import cycle (watchdog and admin are independent
// sibling packages under internal/). A future
// `internal/db/dbsource.go` consolidation is a separate
// refactor.

package admin

import "database/sql"

// DBSource is the minimum surface the admin Service
// needs to obtain the current *sql.DB. The ResettableDB
// wrapper from internal/db (B203) satisfies it via its
// Current() method. A plain *sql.DB also satisfies it
// (every call returns the same pointer) — the test
// suite uses a fixed adapter.
type DBSource interface {
	Current() *sql.DB
}

// dbc returns the admin Service's current *sql.DB. It's
// a one-liner that should be used at every call site:
//
//	rows, err := s.dbc().QueryContext(ctx, ...)
//
// instead of the captured-pointer pattern `s.DB.X`.
// The watchdog will call dbc on every handler call,
// transparently following B203 hot-reloads.
func (s *Service) dbc() *sql.DB {
	if s.DB == nil {
		return nil
	}
	return s.DB.Current()
}
