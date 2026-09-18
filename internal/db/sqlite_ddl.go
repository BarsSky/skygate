// internal/db/sqlite_ddl.go — SQLite DDL helpers for the migration chain.
//
// WHY THIS EXISTS (2026-09-18 audit)
// ----------------------------------
// migrations_sqlite.go is a script-generated reverse-port of the PG
// migration chain, and several of its statements were copied verbatim from
// PostgreSQL:
//
//	ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS ssh_target TEXT ...
//
// SQLite has NO `IF NOT EXISTS` clause for ADD COLUMN — that is a syntax
// error. Worse, the port wrapped those statements in error-swallowing
// loops:
//
//	d.Exec(q)                                  // ignore errors (column may exist)
//	if _, err := d.Exec(q); err != nil { continue }
//	if err != nil { return nil }               // "column exists"
//
// so the syntax error was interpreted as "the column already exists" and
// the ALTER never ran. Proven by probing a fresh :memory: database: the
// SQLite schema was missing
//
//	exit_servers.ssh_target
//	exit_servers.ssh_key_path
//	exit_servers.accept_routes
//	device_rules.device_ip
//
// which is what broke exit-node sync and per-device IP rules on a SQLite
// deployment.
//
// The fix is to make idempotency EXPLICIT: ask the catalog whether the
// column is there (PRAGMA table_info) instead of issuing an invalid
// statement and hoping the error means the right thing. A genuine failure
// now propagates instead of being swallowed.
package db

import (
	"database/sql"
	"fmt"
)

// sqliteColumnExists reports whether `column` is present in `table`.
//
// Table and column names are interpolated (PRAGMA does not accept bound
// parameters), so callers MUST pass hardcoded literals — every current
// caller passes string constants from the migration chain. Never pass
// user input here.
func sqliteColumnExists(d *sql.DB, table, column string) bool {
	rows, err := d.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, ctype      string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	// A scan error here means we could not read the schema; reporting
	// "absent" makes the caller attempt the ALTER, which then surfaces the
	// real error. That is the opposite of the old swallow-and-continue.
	return false
}

// addColumnIfMissingSQLite adds `ddl` (a full column definition such as
// `device_ip TEXT NOT NULL DEFAULT ''`) to `table`, but only when the
// column is not already there. Idempotent, and it RETURNS errors.
//
// Table/column names must be hardcoded literals (see sqliteColumnExists).
func addColumnIfMissingSQLite(d *sql.DB, table, column, ddl string) error {
	if sqliteColumnExists(d, table, column) {
		return nil
	}
	if _, err := d.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, ddl)); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
