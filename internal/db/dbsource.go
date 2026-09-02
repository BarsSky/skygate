// Package db — dbsource.go defines the canonical
// DBSource interface + helpers for the B203 hot-reload
// pattern. Closes the "5 (now 6) copies of the same
// one-method interface" duplication that B208.1 + B210
// left behind.
//
// Background
//
// The B203 ResettableDB wrapper supports a hot-reload of
// the pgxpool: the watchdog detects a DSN change in
// cluster_database.current_dsn, closes the old pool in
// a goroutine, and re-points the embedded *sql.DB to
// the new pool. Any service that captured `*sql.DB` at
// construction time would hold a pointer to the old
// (now-closed) pool and get "sql: database is closed"
// on every subsequent query.
//
// B208.1 (admin) + B210 (auth/my/exit_rules/cluster) +
// earlier (B204 elector, B206 healthz) each defined their
// own:
//
//	type DBSource interface { Current() *sql.DB }
//
// with the same one-method shape. The ResettableDB
// satisfies it via its existing Current() method. This
// file collapses all 6 copies into one shared interface
// in the internal/db package (so future services can
// import it without re-declaring).
//
// Why internal/db hosts the canonical interface
//
// `internal/feature/...` packages already import
// `internal/db` (for ResettableDB, StringArray, etc.),
// so the dependency direction is clean. The reverse
// direction (internal/db importing internal/feature/...)
// would create import cycles.
//
// What's here
//
//   - DBSource: the canonical interface. One method.
//   - FixedDBSource: a test/one-off helper that wraps a
//     plain *sql.DB so it satisfies DBSource (every
//     Current() call returns the same pointer). Replaces
//     the duplicated `fixedDB`/`fixedDBSource` types in
//     B204 (elector) + B206 (healthz) + B210 tests.
//   - DBCurrent: a free function that does the nil-safe
//     "give me the current *sql.DB from this DBSource"
//     that each service's `dbc()` helper duplicated.
//
// What's NOT here
//
//   - The per-service `dbc()` method (each service's
//     signature is `s *Service`) — those stay in the
//     feature packages because they reference the
//     service's DB field, which lives in the feature
//     package. The free function `DBCurrent` covers the
//     "give me the current *sql.DB" logic for any other
//     caller (background tasks, tests, scripts).

package db

import "database/sql"

// DBSource is the minimum surface needed to obtain the
// current *sql.DB in a way that transparently follows
// the B203 ResettableDB hot-reload. The ResettableDB
// wrapper in this package satisfies DBSource via its
// existing Current() method. A plain *sql.DB also
// satisfies it (via FixedDBSource below).
type DBSource interface {
	Current() *sql.DB
}

// DBCurrent returns the current *sql.DB from a DBSource,
// with a nil-safe fallback. Equivalent to the per-service
// `s.dbc()` helper that B208.1 + B210 introduced — the
// only difference is DBCurrent is a free function so
// background tasks, tests, and one-off scripts can use
// it without going through a Service receiver.
//
// Usage in handler code:
//
//	db.DBCurrent(s.DB).QueryContext(ctx, "SELECT ...")
//
// (handlers should still use the per-service `s.dbc()`
// method — it's the same call but avoids a level of
// indirection in the most common case).
func DBCurrent(s DBSource) *sql.DB {
	if s == nil {
		return nil
	}
	return s.Current()
}

// FixedDBSource is a DBSource that always returns the
// same *sql.DB (every Current() call returns the same
// pointer). Use it to wrap a plain *sql.DB when you
// need to satisfy DBSource but don't have a ResettableDB
// (e.g. in unit tests that construct a *sql.DB via
// sql.Open or in scripts that use database/sql directly).
//
// Replaces the 3 different `fixedDB` / `fixedDBSource`
// struct types that B204 (elector), B206 (healthz), and
// B210 (auth/my/exit_rules test files) all defined
// independently.
type FixedDBSource struct {
	DB *sql.DB
}

func (f FixedDBSource) Current() *sql.DB { return f.DB }
