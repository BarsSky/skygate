// internal/db/dialect.go — dialect abstraction for the B-mod-sqlite-pg-bidi
// dual-support refactor.
//
// Pre-v1.5.4 (B-mod-db-retry era) skygate was PG-only. The hard-coded
// `BackendPostgres` enum + `OnConflictDoNothing`/`NowUnixSQL`/`PlaceholdersList`
// shadow-files worked because there was only one dialect. v1.5.4 restores
// explicit SQLite support alongside PG (operator asked for it 2026-09-11).
//
// This file defines:
//
//   - DialectKind: enum of supported SQL dialects (SQLite, Postgres,
//     Unknown). Methods on the enum produce the dialect-native SQL
//     fragment (placeholders, unix epoch, INSERT IGNORE form, BOOL type,
//     BOOL literal). Putting the methods on the enum (instead of on a
//     per-dialect struct) lets callers write `d.Placeholders(3)` where
//     `d` is a DialectKind variable — no method dispatch through an
//     interface, no allocations.
//
//   - Dialect: concrete struct with a Kind field + an OpenDialect
//     method that opens the actual *sql.DB. DetectDSN returns a
//     *Dialect populated from a DSN string; OpenWithDialect adds the
//     *sql.DB open on top.
//
//   - DetectDSN: pure-function DSN → *Dialect. Accepts every form
//     skygate recognizes: explicit sqlite: prefix, file: URI, bare
//     path (default SQLite for self-host), postgres:// / postgresql://,
//     :memory: for test mode, empty (defaults to SQLite for v1.5.x
//     self-host backward compat).
//
// The actual SQLite connection open lives in open_sqlite.go (shadow
// file) so the SQLite driver import is only compiled when the operator
// opts into SQLite (Task 6 wires the SKYGATE_DB env var). The PG open
// is in driver_postgres.go (existing) and is reused unchanged.
package db

import (
	"database/sql"
	"fmt"
	"strings"

	// Pure-Go SQLite driver. modernc.org/sqlite is a no-CGO translation
	// of the SQLite C source — works on Windows + Linux, registers the
	// "sqlite" driver name with database/sql. Pre-v1.5.4 (B-mod-db-retry)
	// skygate used mattn/go-sqlite3 (CGO) but that required a C compiler
	// on every target. modernc.org is the modern, cross-platform choice.
	_ "modernc.org/sqlite"

	// Existing PG driver — registers "pgx" name with database/sql.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// DialectKind identifies which SQL dialect a DSN or *sql.DB is using.
// DialectUnknown is the zero value; DetectDSN returns DialectSQLite
// for empty / unrecognised DSNs (v1.5.x self-host default) — callers
// that want a hard failure on garbage DSNs should check for
// DialectUnknown explicitly (DetectDSN returns it for DSNs that
// contain "://" but no recognised scheme, e.g. "mysql://...").
type DialectKind int

const (
	// DialectUnknown is the zero value; DetectDSN returns it only for
	// DSNs that look like a URL (contain "://") but match no recognised
	// scheme. Used as a sentinel for explicit "unknown dialect" errors.
	DialectUnknown DialectKind = iota

	// DialectSQLite is the SQLite dialect (pure-Go modernc.org driver,
	// no CGO, file-based or in-memory).
	DialectSQLite

	// DialectPostgres is the PostgreSQL dialect (pgx/v5 driver, network
	// DSN form). Used for prod / replicated deployments.
	DialectPostgres
)

// String returns the lowercase name of the dialect kind. Used by the
// B-check scripts (Task 5: scripts/check_b_mod_sqlite_pg.sh) to print
// the dialect alongside error messages.
func (k DialectKind) String() string {
	switch k {
	case DialectSQLite:
		return "sqlite"
	case DialectPostgres:
		return "postgres"
	default:
		return "unknown"
	}
}

// Placeholders returns the comma-joined placeholder list for n query
// parameters, using the dialect-native form.
//
//   - PG: "$1, $2, ..., $n" (pgx/extended-protocol requires $N
//     placeholders; SQLite's "?" form crashes PG with
//     "syntax error at or near ?" because PG treats ? as an
//     operator-named-parameter marker, not a positional placeholder).
//   - SQLite: "?, ?, ..., ?" (one positional placeholder; PG's $N form
//     crashes SQLite with "no such column: $1").
//
// n==0 returns "" — useful for the case where a SQL statement has
// dynamic placeholder counts (e.g. "IN ()" with 0 IDs).
func (k DialectKind) Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	switch k {
	case DialectPostgres:
		out := make([]byte, 0, 3*n)
		for i := 1; i <= n; i++ {
			if i > 1 {
				out = append(out, ',')
			}
			out = append(out, '$')
			if i >= 10 {
				out = append(out, byte('0'+(i/10)%10))
			}
			out = append(out, byte('0'+i%10))
		}
		return string(out)
	case DialectSQLite:
		out := make([]byte, 0, 2*n)
		for i := 0; i < n; i++ {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, '?')
		}
		return string(out)
	default:
		return ""
	}
}

// UnixEpoch returns the SQL expression for "convert <expr> to a unix
// epoch integer". Caller passes any expression — "now()", "created_at",
// "CURRENT_TIMESTAMP" — and the dialect emits its native form:
//
//   - PG: "EXTRACT(EPOCH FROM <expr>)::bigint" (PG's canonical
//     seconds-since-epoch, cast to BIGINT to match INTEGER storage
//     on the SQLite side after Task 4 conversion).
//   - SQLite: "CAST(strftime('%s', <expr>) AS INTEGER)" (strftime %s
//     returns TEXT in some SQLite builds; the CAST forces INTEGER so
//     PG ↔ SQLite comparisons work without type coercion).
//
// Pre-v0.33.1.12 the backup/config.go code path hardcoded
// "strftime('%s','now')" which crashed PG. The DialectKind.UnixEpoch
// method is the single point that fixes that and similar cases for
// both directions.
func (k DialectKind) UnixEpoch(expr string) string {
	switch k {
	case DialectPostgres:
		return fmt.Sprintf("EXTRACT(EPOCH FROM %s)::bigint", expr)
	case DialectSQLite:
		return fmt.Sprintf("CAST(strftime('%%s', %s) AS INTEGER)", expr)
	default:
		return ""
	}
}

// InsertIgnore rewrites the given SQL statement into the
// dialect-native idempotent-INSERT form:
//
//   - PG: appends " ON CONFLICT (<keys>) DO NOTHING" suffix to the
//     end of the statement. The caller is responsible for using
//     PG-native placeholders ($1, $2, ...) — this function does NOT
//     rewrite placeholders.
//   - SQLite: rewrites the leading "INSERT INTO" to "INSERT OR IGNORE
//     INTO". SQLite has no concept of conflict-target columns for
//     INSERT OR IGNORE — it ignores on ANY unique/primary-key
//     violation, which matches the PG DO NOTHING semantics for the
//     skygate use cases (idempotent row inserts keyed by natural
//     unique columns).
//
// sql must start with "INSERT INTO " (with trailing space) — the
// SQLite rewrite uses strings.Replace with N=1 to swap the leading
// occurrence only. PG does not rewrite; it just appends.
//
// keys are the conflict-target column list (PG only; ignored for
// SQLite). Multiple keys are comma-joined without spaces: "a, b, c".
func (k DialectKind) InsertIgnore(sql string, keys []string) string {
	switch k {
	case DialectPostgres:
		return sql + " ON CONFLICT (" + strings.Join(keys, ", ") + ") DO NOTHING"
	case DialectSQLite:
		// Rewrite leading "INSERT INTO" → "INSERT OR IGNORE INTO".
		// N=1 limits the rewrite to the first occurrence so a VALUES
		// clause containing the literal text "INSERT INTO" is not
		// touched (rare but possible in string-column data).
		return strings.Replace(sql, "INSERT INTO", "INSERT OR IGNORE INTO", 1)
	default:
		return sql
	}
}

// BooleanType returns the dialect-native column type for boolean
// values. The migration tool (Task 4) uses this to translate
// BOOLEAN columns when copying schema PG↔SQLite.
//
//   - PG: "BOOLEAN" (native).
//   - SQLite: "INTEGER" (no native BOOL; 0/1 storage convention).
func (k DialectKind) BooleanType() string {
	switch k {
	case DialectPostgres:
		return "BOOLEAN"
	case DialectSQLite:
		return "INTEGER"
	default:
		return ""
	}
}

// BooleanLiteral returns the dialect-native literal form for a
// boolean value. Used by migrations + schema writers when emitting
// DDL with DEFAULT clauses or CHECK constraints.
//
//   - PG: "true" / "false" (SQL-standard).
//   - SQLite: "1" / "0" (portable; "true"/"false" literals can be
//     coerced to TEXT in some edge cases and fail the INTEGER cast).
func (k DialectKind) BooleanLiteral(b bool) string {
	if b {
		switch k {
		case DialectPostgres:
			return "true"
		case DialectSQLite:
			return "1"
		}
	} else {
		switch k {
		case DialectPostgres:
			return "false"
		case DialectSQLite:
			return "0"
		}
	}
	return ""
}

// Dialect is the concrete dialect handle returned by DetectDSN and
// OpenWithDialect. It pairs a Kind (the SQL-dialect enum) with the
// original DSN string (so OpenDialect can build the actual
// connection without re-parsing the DSN).
//
// Dialect is a STRUCT (not an interface) so callers can read .Kind
// directly (field access, no method call). The SQL-fragment methods
// live on DialectKind (the enum) so `dialect.Placeholders(3)` works
// regardless of whether the caller has a Dialect struct or a bare
// DialectKind value.
type Dialect struct {
	// Kind identifies the SQL dialect.
	Kind DialectKind
	// dsn is the original DSN string DetectDSN was called with.
	// OpenDialect uses it to build the actual *sql.DB connection.
	dsn string
}

// DSN returns the original DSN string DetectDSN was called with.
// Used by error messages + the B-check scripts.
func (d *Dialect) DSN() string { return d.dsn }

// OpenDialect opens a *sql.DB connection for the dialect + DSN this
// Dialect struct was created from. Dispatches on Kind to the
// dialect-specific open path (openSQLite for SQLite,
// openPostgres for PG).
//
// Returns an error if the dialect is unknown or the connection
// fails (bad DSN, network down, file not writable, etc).
func (d *Dialect) OpenDialect() (*sql.DB, error) {
	switch d.Kind {
	case DialectSQLite:
		return openSQLite(d.dsn)
	case DialectPostgres:
		return openPostgres(d.dsn)
	default:
		return nil, fmt.Errorf("db.OpenDialect: unknown dialect kind %v for DSN %q",
			d.Kind, d.dsn)
	}
}

// DetectDSN inspects a DSN string and returns the corresponding
// Dialect (with Kind populated). Does NOT open a connection — just
// classifies the string.
//
// Accepted forms:
//
//   - "sqlite:/path/to/file.db"           → DialectSQLite
//   - "sqlite::memory:"                   → DialectSQLite
//   - "file:/path/to/file.db"             → DialectSQLite (modernc.org URI form)
//   - "file::memory:?cache=shared"        → DialectSQLite
//   - ":memory:"                          → DialectSQLite
//   - "/abs/path" or "./rel/path" or "C:\windows\path" → DialectSQLite
//     (bare path = SQLite, the v1.5.x self-host default)
//   - "postgres://..." or "postgresql://..." → DialectPostgres
//   - "" (empty)                          → DialectSQLite (backward compat)
//
// Anything else (e.g. "mysql://...", "foo://...") → DialectUnknown.
// Caller must handle DialectUnknown explicitly (typically: error out
// with "unrecognised DSN scheme").
//
// Windows paths (e.g. "C:\skygate\skygate.db") are detected as
// SQLite because they don't contain "://". The lone colon in "C:" is
// drive-letter, not a URL scheme separator.
func DetectDSN(dsn string) *Dialect {
	dsn = strings.TrimSpace(dsn)

	// Empty + :memory: → SQLite (v1.5.x self-host default + test mode).
	if dsn == "" || dsn == ":memory:" {
		return &Dialect{Kind: DialectSQLite, dsn: dsn}
	}

	lower := strings.ToLower(dsn)

	// SQLite explicit prefix.
	if strings.HasPrefix(lower, "sqlite:") || strings.HasPrefix(lower, "file:") {
		return &Dialect{Kind: DialectSQLite, dsn: dsn}
	}

	// PostgreSQL.
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return &Dialect{Kind: DialectPostgres, dsn: dsn}
	}

	// Bare path (no scheme) → SQLite. We test for "://" anywhere in the
	// string first to filter out "mysql://" / "foo://" style unknown
	// schemes — those should return DialectUnknown, not silently become
	// SQLite.
	if !strings.Contains(dsn, "://") {
		return &Dialect{Kind: DialectSQLite, dsn: dsn}
	}

	// Has "://" but no recognised scheme.
	return &Dialect{Kind: DialectUnknown, dsn: dsn}
}

// OpenWithDialect detects the dialect from the DSN, opens a *sql.DB
// connection for it, and returns both the Dialect (for later SQL-
// fragment calls) and the live *sql.DB. The caller is responsible
// for closing the *sql.DB when done.
//
// Errors:
//
//   - DialectUnknown → "unrecognised DSN scheme"
//   - SQLite open failure → wraps the modernc.org/sqlite error
//     (file-not-writable, parent-dir-missing, etc).
//   - PG open failure → wraps the pgx error (DSN malformed, network
//     down, auth failed, etc).
//
// SKYGATE_DB env var: NOT consulted here. Callers that want env-var
// support should call config.GetDSN() (Task 6: internal/config/
// config.go) and pass the result here. Keeping env-var lookup out
// of the dialect package means the package is testable without
// touching the environment.
func OpenWithDialect(dsn string) (*Dialect, *sql.DB, error) {
	d := DetectDSN(dsn)
	if d.Kind == DialectUnknown {
		return d, nil, fmt.Errorf("db.OpenWithDialect: unknown DSN format %q "+
			"(use sqlite:/path, postgres://user:pass@host/db, or bare /path)", dsn)
	}
	db, err := d.OpenDialect()
	if err != nil {
		return d, nil, err
	}
	return d, db, nil
}
