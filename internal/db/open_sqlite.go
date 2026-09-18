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
	// 2026-09-18 (B261.1): strip an explicit "sqlite:" scheme prefix.
	//
	// "sqlite:/var/lib/skygate/skygate.db" is exactly what
	// deploy/install-{debian,rh,bare}.sh write into
	// /etc/skygate/skygate.env (resolve_db_type) and what
	// config.resolveDBDSN() hands back, and DetectDSN classifies it as
	// DialectSQLite. But the driver (modernc.org/sqlite) does NOT know
	// the "sqlite:" scheme: it treats the whole string as a filename,
	// so SQLite tries to create a *relative* file whose name contains a
	// colon, fails with SQLITE_CANTOPEN, and the driver reports it as
	// the thoroughly misleading
	// "unable to open database file: out of memory (14)".
	//
	// Net effect pre-fix: a native SQLite install could never start —
	// db.OpenDSNWithRetry burned its 5 attempts over ~40s and then
	// log.Fatalf'd. Only ":memory:" (every test) and bare paths worked,
	// which is why the whole test suite stayed green while the shipped
	// installer config was broken.
	//
	// Normalising here (rather than at each call site) covers the web
	// server, the CLI subcommands and the db-migrate converter, since
	// they all reach SQLite through this function.
	dsn = strings.TrimSpace(dsn)
	if strings.HasPrefix(strings.ToLower(dsn), "sqlite:") {
		dsn = dsn[len("sqlite:"):]
	}

	var db *sql.DB
	if dsn == "" || dsn == ":memory:" {
		// In-memory DSN — modernc.org accepts ":memory:" directly
		// OR "file::memory:?cache=shared". The bare ":memory:" form
		// gives a private in-memory DB per *sql.DB (no shared cache).
		var err error
		db, err = sql.Open("sqlite", ":memory:")
		if err != nil {
			return nil, fmt.Errorf("sqlite open :memory:: %w", err)
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite ping :memory:: %w", err)
		}
	} else {
		// File DSN — normalise to modernc.org "file:" URI form and
		// append the three required PRAGMAs. If the DSN already has
		// a "?" we use "&" as the separator; otherwise we use "?".
		// (The "sqlite:" prefix was already stripped above, so the
		// only remaining forms are "file:..." and bare paths.)
		finalDSN := dsn
		if !strings.HasPrefix(strings.ToLower(finalDSN), "file:") {
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

		var err error
		db, err = sql.Open("sqlite", finalDSN)
		if err != nil {
			return nil, fmt.Errorf("sqlite open %q: %w", dsn, err)
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite ping %q: %w", dsn, err)
		}
	}

	// Register the backend so BackendOf(db) returns BackendSQLite
	// (not empty). The migration tracking + conversion tool rely
	// on BackendOf to dispatch DDL fragments.
	registerBackend(db, BackendSQLite)
	return db, nil
}
