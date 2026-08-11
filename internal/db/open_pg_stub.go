//go:build !postgres

// open_pg_stub.go — default build (no `postgres` tag). The
// openPostgres function is a stub that returns an error so
// OpenDSN fails fast and clearly if the operator tries to use
// a postgres:// DSN without rebuilding with the right tag.
package db

import (
	"database/sql"
	"fmt"
)

func openPostgres(dsn string) (*sql.DB, error) {
	return nil, fmt.Errorf("postgres backend not compiled in (rebuild with -tags postgres)")
}
