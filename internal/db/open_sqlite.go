// internal/db/open_sqlite.go — SQLite connection setup (B-mod-sqlite-pg-bidi).
//
// Pure-Go SQLite via modernc.org/sqlite (no CGO, Windows+Linux compatible).
// Driver name "sqlite" is registered by the blank import in dialect.go.
//
// Three PRAGMAs are set on every open to make SQLite usable for skygate:
//
//   - foreign_keys=ON: SQLite has FKs OFF by default per-connection.
//     Without this, the migration chain's FOREIGN KEY clauses are
//     no-ops and the conversion tool's FK-dep topo sort is meaningless.
//   - journal_mode=WAL: write-ahead logging for concurrent readers +
//     one writer. skygate has many HTTP handler goroutines reading +
//     occasional writes; WAL is the only journal mode that allows
//     readers to proceed while a writer holds the lock.
//   - busy_timeout=2000: 2-second timeout on SQLITE_BUSY (writer
//     contention). Default is 0 (= instant fail). The skygate HTTP
//     handlers would otherwise crash with "database is locked" under
//     load.
//
// The PRAGMAs are passed as DSN query parameters (modernc.org/sqlite's
// `_pragma=...` syntax) so they apply per-connection automatically —
// no manual "PRAGMA foreign_keys=ON" exec after Open. This is the
// modernc.org convention documented at
// https://pkg.go.dev/modernc.org/sqlite#Driver.Open.
package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// openSQLite opens a *sql.DB connection for the given SQLite DSN.
// The DSN may be any form DetectDSN accepts (sqlite:/path, file: URI,
// bare path, :memory:).
//
// On :memory: the connection is per-*sql.DB — closing the DB drops
// the database. This matches the test-mode semantics callers expect
// from `:memory:` (Task 1 test: `OpenWithDialect(":memory:")` →
// SELECT 1 → close).
//
// On a file DSN, modernc.org/sqlite creates the file (and any missing
// parent directories? — NO, parent dirs must exist; openSQLite returns
// an error if the parent directory is missing). The first open creates
// an empty SQLite DB; MigrateSQLite (Task 2) then applies the schema.
func openSQLite(dsn string) (*sql.DB, error) {
	if dsn == "" || dsn == ":memory:" {
		// In-memory DSN — modernc.org accepts ":memory:" directly
		// OR "file::memory:?cache=shared". The bare ":memory:" form
		// gives a private in-memory DB per *sql.DB (no shared cache).
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, fmt.Errorf("sqlite open :memory:: %w", err)
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite ping :memory:: %w", err)
		}
		return db, nil
	}

	// File DSN — normalise to modernc.org "file:" URI form and append
	// the three required PRAGMAs. If the DSN already has a "?" we use
	// "&" as the separator; otherwise we use "?".
	finalDSN := dsn
	if !strings.HasPrefix(strings.ToLower(finalDSN), "file:") &&
		!strings.HasPrefix(strings.ToLower(finalDSN), "sqlite:") {
		// Bare path — convert to file: URI.
		finalDSN = "file:" + finalDSN
	}

	// Append PRAGMAs unless the DSN already has _pragma= (caller
	// supplied their own). We don't try to merge; if the caller
	// supplies their own, we trust them.
	if !strings.Contains(finalDSN, "_pragma=") {
		sep := "?"
		if strings.Contains(finalDSN, "?") {
			sep = "&"
		}
		finalDSN += sep + "_pragma=foreign_keys(1)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=busy_timeout(2000)"
	}

	db, err := sql.Open("sqlite", finalDSN)
	if err != nil {
		return nil, fmt.Errorf("sqlite open %q: %w", dsn, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite ping %q: %w", dsn, err)
	}
	return db, nil
}
