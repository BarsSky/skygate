//go:build postgres

// open_pg_pg.go — Postgres variant of openPostgres. Compiled when
// the `postgres` build tag is set.
package db

import "database/sql"

// openPostgres is the lowercase wrapper used by OpenDSN. It's
// just OpenPostgres from driver_postgres.go.
func openPostgres(dsn string) (*sql.DB, error) {
	return OpenPostgres(dsn)
}
