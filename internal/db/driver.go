// Package db — driver abstraction (v0.27.0 PostgreSQL HA migration,
// v1.3.0 PG-only).
//
// As of v1.3.0, skygate is PostgreSQL-only. SQLite is no longer
// supported. The driver abstraction in this file remains because
// query helpers (e.g. backup/config.go, system_tests.go) dispatch
// on backend type — but the only valid value is BackendPostgres.
//
// Selection happens at OpenDSN() time: the dsn must start with
// "postgres://" or "postgresql://". The PG path runs
// MigratePostgres on every Open so the container start re-applies
// the idempotent migration chain.
//
// Migrations are duplicated per-version: migrations_v0.XX.go runs
// the SQLite-style SQL, migrations_pg.go runs the PG-equivalent.
// The same data shape is produced in both, but the SQL differs
// (PRAGMA → ALTER, ? placeholders → $N, strftime → EXTRACT, etc.).
package db

import (
	"database/sql"
	"strings"
	"sync"
)

// Backend identifies which database engine a *sql.DB is connected to.
// v1.3.0: the only valid value is BackendPostgres. BackendSQLite
// has been removed.
type Backend string

const (
	// BackendPostgres is the only supported backend as of v1.3.0.
	// Replicated, concurrent-writer-safe, scales to 100+ users.
	BackendPostgres Backend = "postgres"
)

// String returns the lowercase name of the backend.
func (b Backend) String() string { return string(b) }

// IsPostgres reports whether the backend is PostgreSQL.
func (b Backend) IsPostgres() bool { return b == BackendPostgres }

// DetectBackend looks at a dsn string and returns the corresponding
// Backend. It does NOT open a connection — just inspects the prefix.
//
// Rules:
//
//   - starts with "postgres://" or "postgresql://" → BackendPostgres
//   - anything else → BackendPostgres (v1.3.0: PG-only, no SQLite)
//
// The SQLite branch was removed in v1.3.0. Callers that passed
// a non-DSN string previously got a SQLite file path; now they
// get a PG-shaped *sql.DB (which will fail at sql.Open with
// "sql: unknown driver" if pgx is not registered, OR at Ping if
// the dsn is malformed). Both are loud failures that an operator
// notices immediately — better than the silent SQLite fallback
// pre-v1.3.0.
func DetectBackend(dsn string) Backend {
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return BackendPostgres
	}
	// v1.3.0: PG-only. Treat any non-DSN string as a malformed
	// PG DSN (the next sql.Open / Ping will fail loudly).
	return BackendPostgres
}

// registry maps each *sql.DB to the Backend it was opened with.
// We can't introspect the driver name (database/sql/driver.Driver
// has no Name() method) so we set this explicitly in OpenDSN.
var (
	registryMu sync.RWMutex
	registry   = map[*sql.DB]Backend{}
)

// registerBackend records the backend for a freshly-opened *sql.DB.
// Called from OpenDSN. Idempotent: re-registering the same backend
// is a no-op; re-registering a different backend for the same
// *sql.DB is treated as a programmer error and panics.
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

// BackendOf returns the Backend that d was opened with. Returns
// the empty string if d is nil or was not opened via db.OpenDSN().
//
// This is the canonical way for code in the rest of skygate to
// dispatch on backend type. v1.3.0: the only non-empty return
// value is BackendPostgres.
func BackendOf(d *sql.DB) Backend {
	if d == nil {
		return ""
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[d]
}
