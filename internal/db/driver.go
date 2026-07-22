// Package db — driver abstraction (v0.27.0 PostgreSQL HA migration).
//
// As of v0.27.0, skygate supports two database backends:
//
//   - SQLite (default, via github.com/mattn/go-sqlite3, registered as "sqlite3")
//   - PostgreSQL (via github.com/jackc/pgx/v5, registered as "pgx")
//
// Selection happens at Open() time by inspecting the dsn argument:
//
//   - if dsn starts with "postgres://" or "postgresql://" → PostgreSQL
//   - otherwise → SQLite (treated as a file path)
//
// This is purely additive; existing call sites that pass a SQLite
// file path (e.g. "/var/lib/skygate/skygate.db") continue to work
// unchanged. New deployments set SKYGATE_DB_DSN to a PostgreSQL
// connection string of the form:
//
//   postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable
//
// Migrations are duplicated per-backend: each version has both a
// SQLite version (existing migrations_v0.XX.go) and a PostgreSQL
// version (new migrations_v0.XX_pg.go). The dispatcher in
// migrate.go picks the right one based on BackendOf(d).
package db

import (
	"database/sql"
	"strings"
	"sync"
)

// Backend identifies which database engine a *sql.DB is connected to.
type Backend string

const (
	// BackendSQLite is the default. Single-file, single-writer.
	// Best for: solo deployments, unit tests, edge cases.
	BackendSQLite Backend = "sqlite"
	// BackendPostgres is the new HA-capable backend. Replicated,
	// concurrent-writer-safe, scales to 100+ users. v0.27.0+.
	BackendPostgres Backend = "postgres"
)

// String returns the lowercase name of the backend.
func (b Backend) String() string { return string(b) }

// IsPostgres reports whether the backend is PostgreSQL.
func (b Backend) IsPostgres() bool { return b == BackendPostgres }

// IsSQLite reports whether the backend is SQLite.
func (b Backend) IsSQLite() bool { return b == BackendSQLite }

// DetectBackend looks at a dsn string and returns the corresponding
// Backend. It does NOT open a connection — just inspects the prefix.
//
// Rules (intentionally simple; we don't try to be clever):
//
//   - starts with "postgres://" or "postgresql://" → BackendPostgres
//   - otherwise → BackendSQLite
func DetectBackend(dsn string) Backend {
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return BackendPostgres
	}
	return BackendSQLite
}

// registry maps each *sql.DB to the Backend it was opened with.
// We can't introspect the driver name (database/sql/driver.Driver
// has no Name() method) so we set this explicitly in openSQLite
// and openPostgres. The map is keyed by the *sql.DB pointer value,
// which is stable for the lifetime of the connection.
var (
	registryMu sync.RWMutex
	registry   = map[*sql.DB]Backend{}
)

// registerBackend records the backend for a freshly-opened *sql.DB.
// Called from openSQLite / openPostgres. Idempotent: re-registering
// the same backend is a no-op; re-registering a different backend
// for the same *sql.DB is treated as a programmer error and panics
// (which would mean we're double-opening the same connection, which
// shouldn't happen).
func registerBackend(d *sql.DB, b Backend) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if existing, ok := registry[d]; ok && existing != b {
		panic("db.registerBackend: double-open with different backend for " +
			"same *sql.DB pointer (existing=" + string(existing) +
			", new=" + string(b) + ")")
	}
	registry[d] = b
}

// unregisterBackend removes the entry. Called on Close() so we
// don't leak memory if the process opens and closes many DBs
// (not a concern in production skygate, but a hygiene measure).
func unregisterBackend(d *sql.DB) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(registry, d)
}

// BackendOf returns the Backend that d was opened with. Returns
// the empty string if d is nil or was not opened via db.Open().
//
// This is the canonical way for code in the rest of skygate to
// dispatch on backend type (e.g. migrations, query helpers that
// need different SQL).
func BackendOf(d *sql.DB) Backend {
	if d == nil {
		return ""
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[d]
}
