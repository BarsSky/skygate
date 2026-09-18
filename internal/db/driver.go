// Package db — driver abstraction (v0.27.0 PostgreSQL HA migration,
// v1.3.0 PG-only, v1.5.4 dual-support restored).
//
// v1.5.4 (B-mod-sqlite-pg-bidi + B246-dialect-retry) restores explicit
// SQLite support alongside PostgreSQL. Operators that want a self-
// hosted single-node deploy can run skygate with the default SQLite
// DSN (a bare /var/lib/skygate/skygate.db path or :memory: for tests);
// operators running a replicated prod cluster use the libpq-style
// PG DSN. The driver is selected at OpenDSN-with-retry time by
// dialect.DetectDSN() (in dialect.go) — pgx for PG, modernc.org/sqlite
// for SQLite.
//
// The query helpers that branch on backend type
// (e.g. backup/config.go, system_tests.go) dispatch via BackendOf()
// — which now returns BackendPostgres OR BackendSQLite. Per-version
// migration chains are duplicated (migrations_v0.XX.go for SQLite,
// migrations_pg.go for PG) — same data shape, different SQL (PRAGMA
// vs ALTER, ? placeholders vs $N, strftime vs EXTRACT, etc.).
package db

import (
	"database/sql"
	"strings"
	"sync"
)

// Backend identifies which database engine a *sql.DB is connected to.
// v1.5.4+: BackendPostgres (libpq network DSN, pgx driver) and
// BackendSQLite (modernc.org/sqlite pure-Go driver, file or :memory:)
// are both first-class. The dialect is selected at open time via
// dialect.DetectDSN; this constant is what BackendOf() returns so
// downstream callers can branch.
type Backend string

const (
	// BackendPostgres is the PG backend (libpq DSN, pgx/stdlib driver).
	// Replicated, concurrent-writer-safe, scales to 100+ users.
	BackendPostgres Backend = "postgres"

	// BackendSQLite is the SQLite backend (modernc.org/sqlite pure-Go
	// driver). v1.5.4+ restores this for self-host single-node deploys
	// and integration-test :memory: mode. The driver sets the three
	// required PRAGMAs (foreign_keys, journal_mode=WAL, busy_timeout)
	// via DSN query params (see open_sqlite.go).
	BackendSQLite Backend = "sqlite"
)

// String returns the lowercase name of the backend.
func (b Backend) String() string { return string(b) }

// IsPostgres reports whether the backend is PostgreSQL.
func (b Backend) IsPostgres() bool { return b == BackendPostgres }

// IsSQLite reports whether the backend is SQLite. Used by query helpers
// that branch on backend type (e.g. backup/config.go queries SQLite-
// only PRAGMA tables).
func (b Backend) IsSQLite() bool { return b == BackendSQLite }

// DetectBackend looks at a dsn string and returns the corresponding
// Backend. It does NOT open a connection — just inspects the prefix.
//
// Rules:
//
//   - starts with "postgres://" or "postgresql://" → BackendPostgres
//   - starts with "sqlite:", "file:", or matches a bare path /
//     ":memory:" → BackendSQLite (v1.5.4+ restored)
//   - anything else → BackendPostgres (legacy: PG-only behaviour; the
//     subsequent sql.Open / Ping fails loudly on the malformed DSN).
//
// For richer detection (e.g. file: URI handling, empty-string defaults),
// dialect.DetectDSN is the source of truth — DetectBackend is a thin
// wrapper for callers that only need the Backend enum, not a full
// *Dialect struct.
func DetectBackend(dsn string) Backend {
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return BackendPostgres
	}
	if strings.HasPrefix(lower, "sqlite:") || strings.HasPrefix(lower, "file:") {
		return BackendSQLite
	}
	if dsn == "" || dsn == ":memory:" {
		return BackendSQLite
	}
	if !strings.Contains(dsn, "://") {
		// Bare path → SQLite (the v1.5.x self-host default).
		return BackendSQLite
	}
	// Has "://" but no recognised scheme — default to PG so the next
	// sql.Open / Ping fails loudly on the unrecognised URL.
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
	if existing, ok := registry[d]; ok && existing != b {
		registryMu.Unlock()
		panic("db.registerBackend: double-open with different backend for " +
			"same *sql.DB pointer (existing=" + string(existing) +
			", new=" + string(b) + ")")
	}
	registry[d] = b
	registryMu.Unlock()
	// 2026-09-18: also record the process-wide active dialect so the
	// SQL-fragment shims (nowUnixSQL et al) can branch. Both open paths
	// funnel through here — openDSNPing for the dual-dialect runtime
	// path and openSQLite for direct SQLite opens — so this is the one
	// place the wiring needs to live. See active_dialect.go.
	SetActiveDialect(backendToDialectKind(b))
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
