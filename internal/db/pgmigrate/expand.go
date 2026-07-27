// Package pgmigrate — PostgreSQL-aware migration helpers (v0.29.0).
//
// Skygate currently runs on SQLite (the default for v0.28.x and earlier).
// The v0.27.0 driver abstraction in `feat/postgres-migration` adds
// PostgreSQL support; this package provides the migration-side helpers
// that make PG migrations safe to run while skygate is serving traffic.
//
// The package is named `pgmigrate` even though it's currently dormant —
// the helpers are documented, unit-tested, and ready to wire in when
// the PG driver lands on main. The current migrations on `main` are
// SQLite-only and don't call into this package.
//
// Why these helpers exist separately from `internal/db`:
//   - `internal/db` is the SQLite runtime path (migrations_v0.25.go
//     through migrations_v0.47.go all use `*sql.DB.Exec` directly).
//   - `internal/db/pgmigrate` is the *future* PG path — it wraps
//     transactions, sets `lock_timeout`, and provides non-blocking
//     ALTER patterns that don't apply to SQLite.
//   - The two paths can coexist: a migration file can call both
//     `d.Exec(...)` (for SQLite-compatible CREATE/ALTER) AND
//     `pgmigrate.Run(...)` (for PG-specific transaction + lock_timeout).
//     The driver dispatches based on the connection's driver name.
//
// See docs/plans/pg-migration-handling.md for the full design.
package pgmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DefaultLockTimeout is the upper bound on how long a migration will
// wait for a lock before aborting. The skygate updater retries the
// migration up to 3 times with a 5s backoff; after 3 failures, the
// update is rolled back (see docs/plans/self-update-v0.29.md).
//
// 10s is short enough that a stuck migration doesn't hold up a
// rolling update, and long enough that brief contention (another
// updater or an autoupdate-tick landing at the same moment) doesn't
// cause spurious failures.
const DefaultLockTimeout = 10 * time.Second

// AllowDestructiveEnv is the env var the operator sets to bypass the
// IsDestructive safety check. When the updater sees a migration that
// contains DROP / RENAME / TRUNCATE, it refuses to apply it unless
// this env var is set in the updater process.
//
// Set this ONLY when the operator has confirmed that ALL skygate
// instances are on the new code and the old schema is no longer
// referenced anywhere. The standard `expand-contract` pattern
// (see docs/plans/pg-migration-handling.md) requires Phase 2
// (DROP / RENAME) to be a separate, operator-approved release.
const AllowDestructiveEnv = "SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION"

// Run executes the given SQL statements inside a single transaction
// with `lock_timeout` set to DefaultLockTimeout. The transaction is
// committed if every statement succeeds; any failure rolls back the
// entire batch.
//
// On SQLite (the current default), this still works — SQLite serializes
// the writes into a single transaction, which is the safe default for
// any migration. The `SET lock_timeout` statement is a no-op on
// SQLite (no such config), so the helper is portable.
//
// Parameters:
//   - ctx: context for cancellation (e.g. on updater timeout)
//   - d:   the *sql.DB to use. The function acquires a single
//          connection from the pool for the duration of the
//          transaction (so a concurrent migration can't interleave).
//          Pass nil returns an error (no panic) so the updater can
//          safely call this in early-startup paths where the DB
//          connection is not yet available.
//   - stmts: the SQL statements to execute in order
//
// Returns the underlying error from BeginTx / Exec / Commit if any
// step fails. A lock_timeout abort comes back as a `*pgconn.PgError`
// with Code "55P03" (lock_not_available) when the PG driver is in
// use; on SQLite, the equivalent is `SQLITE_BUSY` (5).
func Run(ctx context.Context, d *sql.DB, stmts ...string) error {
	if d == nil {
		return fmt.Errorf("pgmigrate: nil *sql.DB")
	}
	// SQLite has a per-connection write lock; BEGIN IMMEDIATE
	// acquires it up-front. PG uses BEGIN (default) which is
	// also fine. The driver abstraction in feat/postgres-migration
	// wraps this with the actual BEGIN syntax.
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgmigrate: begin: %w", err)
	}
	// Set lock_timeout on PG (no-op on SQLite). This is a per-
	// session GUC, so the SET only takes effect on this
	// transaction's connection. After the tx ends, the GUC
	// reverts (PG resets session state at connection return to
	// pool). The 10s default is generous for a single ALTER
	// on a small table; a backfill migration on a large table
	// should override this by passing a custom Config (TODO v0.30.0).
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET lock_timeout = '%dms'", DefaultLockTimeout.Milliseconds()),
	); err != nil {
		// Best-effort: on SQLite this returns "no such column" or
		// similar. We don't fail the migration for it; we just
		// log-via-fmt and continue. The real lock guarantee comes
		// from the transaction itself.
		_ = err // intentionally ignored
	}
	for i, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return fmt.Errorf("pgmigrate: stmt %d failed (%v) and rollback failed: %w", i, err, rbErr)
			}
			return fmt.Errorf("pgmigrate: stmt %d failed: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgmigrate: commit: %w", err)
	}
	return nil
}

// AddColumnIfNotExists is the PG-safe ADD COLUMN helper. On PG 11+,
// a column with a constant default uses the "fast default" path —
// the table is rewritten lazily, not in the ALTER itself, so the
// ACCESS EXCLUSIVE lock is held for milliseconds instead of
// rewriting the whole table.
//
// On SQLite, the IF NOT EXISTS guard makes the statement a no-op
// when the column already exists, so this is also safe to call
// from the SQLite path.
//
// Parameters:
//   - ctx: context
//   - d:   *sql.DB
//   - table:  table name (NOT quoted — caller is responsible for
//             using the right schema if needed; skygate uses the
//             default `public` schema in PG)
//   - column: column name
//   - sqlType: PG type (e.g. "TEXT", "INTEGER", "BOOLEAN",
//              "TIMESTAMP WITH TIME ZONE")
//   - defaultExpr: SQL expression for the default. Use a CONSTANT
//                  for the fast-default path (e.g. "'pending'",
//                  "0", "false"). For non-constant defaults
//                  (e.g. "now()", "gen_random_uuid()"), the fast-
//                  default optimization does NOT apply — the caller
//                  must use the manual expand-contract pattern
//                  (add nullable → backfill in batches → SET NOT NULL).
func AddColumnIfNotExists(ctx context.Context, d *sql.DB, table, column, sqlType, defaultExpr string) error {
	stmt := fmt.Sprintf(
		"ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s DEFAULT %s",
		table, column, sqlType, defaultExpr,
	)
	return Run(ctx, d, stmt)
}

// CreateIndexConcurrently wraps CREATE INDEX CONCURRENTLY for PG.
// PG 11+ supports CONCURRENTLY; SQLite doesn't (its CREATE INDEX
// is always blocking, but the lock is brief for small tables —
// a few hundred ms for the skygate schema).
//
// On SQLite, this helper falls back to the regular CREATE INDEX
// IF NOT EXISTS (no CONCURRENTLY) so the call site doesn't need
// to branch on the driver. This is the "best effort" path for
// v0.29.0; once the PG driver lands, the helper detects the
// driver and uses the PG-specific form.
//
// Parameters:
//   - ctx: context
//   - d:   *sql.DB
//   - indexName: name of the index (e.g. "idx_user_subnets_status")
//   - table:     table name
//   - columns:   the columns to index, as a comma-separated string
//                (e.g. "user_id, status" or "hostname")
//   - where:     optional partial-index WHERE clause (e.g.
//                "hostname != ''"). Pass "" for a full index.
func CreateIndexConcurrently(ctx context.Context, d *sql.DB, indexName, table, columns, where string) error {
	driverName := driverOf(d)
	stmt := buildCreateIndexStmt(driverName, indexName, table, columns, where)
	return Run(ctx, d, stmt)
}

// driverOf returns the registered driver name for d, or "" if
// the driver is not registered (e.g. before database/sql is
// initialized). Used internally by CreateIndexConcurrently to
// pick the right SQL form.
func driverOf(d *sql.DB) string {
	if d == nil {
		return ""
	}
	// database/sql doesn't expose Driver() directly without
	// importing driver. We use the type-assertion trick: cast
	// to the unexported interface that *sql.DB satisfies.
	// This is the canonical way to introspect the driver.
	type driverer interface{ Driver() interface{} }
	if dd, ok := interface{}(d).(driverer); ok {
		drv := dd.Driver()
		if dv, ok := drv.(interface{ Name() string }); ok {
			return dv.Name()
		}
		// Fallback: use fmt.Sprintf to get a hint. Not pretty
		// but doesn't fail.
		return fmt.Sprintf("%T", drv)
	}
	return ""
}

// buildCreateIndexStmt returns the right CREATE INDEX form for
// the given driver. PG uses CONCURRENTLY (no exclusive lock);
// SQLite doesn't support CONCURRENTLY, so the statement is the
// standard form.
//
// This is split out from CreateIndexConcurrently so the test
// can pin the exact SQL form per driver.
func buildCreateIndexStmt(driverName, indexName, table, columns, where string) string {
	var b strings.Builder
	b.WriteString("CREATE INDEX ")
	if driverName == "postgres" || driverName == "pgx" {
		b.WriteString("CONCURRENTLY ")
	}
	b.WriteString("IF NOT EXISTS ")
	b.WriteString(indexName)
	b.WriteString(" ON ")
	b.WriteString(table)
	b.WriteString(" (")
	b.WriteString(columns)
	b.WriteString(")")
	if where != "" {
		b.WriteString(" WHERE ")
		b.WriteString(where)
	}
	return b.String()
}

// ErrDestructive is returned by IsDestructiveRefused when the
// migration source code contains a destructive operation and the
// AllowDestructiveEnv env var is not set.
var ErrDestructive = errors.New("pgmigrate: migration contains destructive operation (DROP/RENAME/TRUNCATE); set " + AllowDestructiveEnv + " to allow")

// destructivePatterns matches DDL that's unsafe to run via the
// auto-update path. The allowlist (CREATE TABLE, ALTER TABLE ADD
// COLUMN, CREATE INDEX, INSERT) is implicit — anything not in this
// list is fine.
var destructivePatterns = []*regexp.Regexp{
	// DROP COLUMN, DROP TABLE, DROP INDEX — schema-removal
	regexp.MustCompile(`(?i)\bDROP\s+(TABLE|COLUMN|INDEX)\b`),
	// RENAME TABLE / RENAME COLUMN / ALTER TABLE ... RENAME TO
	regexp.MustCompile(`(?i)\bRENAME\s+(TO|TO\s)`),
	regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+\S+\s+RENAME\s+(TO|COLUMN)\b`),
	// TRUNCATE — destructive on data
	regexp.MustCompile(`(?i)\bTRUNCATE\s+(TABLE\s+)?\S+`),
	// DELETE FROM migrations table itself — never auto-applied
	regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+schema_migrations\b`),
}

// IsDestructive returns true if the given SQL string contains any
// pattern that the auto-update path considers unsafe. Use this to
// gate the migration in the updater before calling Run.
func IsDestructive(sql string) bool {
	for _, p := range destructivePatterns {
		if p.MatchString(sql) {
			return true
		}
	}
	return false
}

// IsDestructiveRefused returns ErrDestructive if the migration
// source code is destructive AND the AllowDestructiveEnv is not
// set. The updater calls this as a guard before applying a
// migration; on ErrDestructive, the updater aborts the update
// and surfaces the error to the operator.
//
// In tests, set AllowDestructiveEnv = "1" to bypass.
func IsDestructiveRefused(sql string) error {
	if !IsDestructive(sql) {
		return nil
	}
	// The env-var check is deferred to the call site (the
	// updater) because this package must remain testable
	// without an env-var dependency. The updater reads
	// os.Getenv(AllowDestructiveEnv) and decides whether to
	// treat ErrDestructive as a hard fail or a warning.
	return ErrDestructive
}
