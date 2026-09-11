// internal/db/open_pg.go — PostgreSQL connection setup (B-mod-sqlite-pg-bidi).
//
// Thin wrapper around the pre-existing OpenPostgres function in
// driver_postgres.go. The original OpenPostgres is kept (it's the
// public entry point used by cmd/skygate/main.go + tests). This
// shadow file just adapts the signature to the Dialect.OpenDialect
// convention: takes the DSN string, returns *sql.DB.
//
// Migration: openPostgres delegates to OpenPostgres (which runs
// MigratePostgres inside Open — the v0.33.1 fix). The migration
// tool (Task 4) calls MigratePostgres explicitly so the conversion
// destination is fully migrated before data copy.
package db

import (
	"database/sql"
)

// openPostgres opens a PostgreSQL connection. Delegates to the
// pre-existing OpenPostgres (which sets pool sizing + runs the
// migration chain on every Open).
//
// Returns the same errors as OpenPostgres:
//   - bad DSN → "pgx open: ..."
//   - network/auth fail → "pgx ping: ..."
//   - migration fail → "pgx migrate: ..."
func openPostgres(dsn string) (*sql.DB, error) {
	return OpenPostgres(dsn)
}
