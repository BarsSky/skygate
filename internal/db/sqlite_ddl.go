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
	"regexp"
	"strings"
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
// `device_ip TEXT NOT NULL DEFAULT ”`) to `table`, but only when the
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

// sqliteAddColumnRe pulls (table, column, definition-tail) out of an
// `ALTER TABLE <t> ADD COLUMN <c> <type ...>` statement. The optional
// IF [NOT] EXISTS fragments are accepted even though SQLite rejects them —
// accepting them here means such a statement goes through
// addColumnIfMissingSQLite (which generates its own ALTER from the parsed
// pieces), instead of failing with a syntax error that the old loops
// swallowed.
var sqliteAddColumnRe = regexp.MustCompile(
	`(?is)^\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\s+(.+)$`)

// execSQLiteDDL runs a batch of DDL statements from a SQLite migration.
//
// 2026-09-18 (§12.13 follow-up, plan §12.16): this file's migrations used to
// run their statements through ~18 ad-hoc loops of the shape
//
//	for _, q := range stmts {
//	    if _, err := d.Exec(q); err != nil {
//	        continue // "column exists", "already migrated", ...
//	    }
//	}
//
// Every failure was swallowed. For `ALTER TABLE ... ADD COLUMN` that hid two
// real defects (both found by running the chain on a real database, not by
// reading it):
//
//   - the statements were written with PostgreSQL's
//     `ADD COLUMN IF NOT EXISTS`, which is a SYNTAX ERROR in SQLite, and the
//     syntax error was read as "the column already exists" — so
//     exit_servers.{ssh_target,ssh_key_path,accept_routes} and
//     device_rules.device_ip simply never appeared on a fresh database;
//   - on a re-run, a genuine "duplicate column name" error was likewise
//     swallowed instead of being recognised as "already applied".
//
// This helper is the single chokepoint:
//
//   - ADD COLUMN statements go through addColumnIfMissingSQLite, which checks
//     PRAGMA table_info first and therefore IS idempotent, and which returns
//     real errors (invalid SQL is no longer mistaken for "column exists");
//   - every other statement (CREATE TABLE/INDEX IF NOT EXISTS, INSERT OR
//     IGNORE, ...) is executed and its error RETURNED — those are idempotent
//     by construction, so a failure means something is genuinely wrong and the
//     migration must stop rather than continue on a half-built schema.
func execSQLiteDDL(d *sql.DB, stmts []string) error {
	for _, q := range stmts {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if m := sqliteAddColumnRe.FindStringSubmatch(q); m != nil {
			// addColumnIfMissingSQLite's `ddl` argument is the FULL column
			// definition, name included (`ssh_port TEXT NOT NULL DEFAULT ''`) —
			// it only uses `column` for the PRAGMA check. Rebuild it from the
			// parsed pieces so the caller's `... IF NOT EXISTS` spelling (and
			// any quoting) cannot leak into the generated ALTER.
			ddl := m[2] + " " + strings.TrimSpace(m[3])
			if err := addColumnIfMissingSQLite(d, m[1], m[2], ddl); err != nil {
				return err
			}
			continue
		}
		if _, err := d.Exec(q); err != nil {
			return fmt.Errorf("ddl %q: %w", truncateDDL(q), err)
		}
	}
	return nil
}

// truncateDDL keeps error messages readable when a migration statement is a
// multi-line CREATE TABLE.
func truncateDDL(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if len(q) > 160 {
		return q[:160] + "…"
	}
	return q
}
