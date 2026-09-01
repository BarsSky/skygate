// driver_postgres.go — PostgreSQL backend (v1.3.0+)
//
// As of v1.3.0, skygate is PG-only. There is no build tag — the
// pgx driver is always registered via the `_ "github.com/jackc/
// pgx/v5/stdlib"` import below, and MigratePostgres is the
// canonical migration entry point.
//
// The PG migration functions live in migrations_pg.go (also no
// build tag) so the helper is reachable in unit tests.

package db

import (
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// OpenPostgres opens a PostgreSQL connection. The dsn is the
// standard libpq URL form:
//
//	postgres://skygate:<password>@<host>:5432/skygate?sslmode=disable
//
// Pool sizing follows the small-Go-HTTP-service defaults: 10 open /
// 5 idle. Operators tune via the DSN's `pool_max_conns` parameter
// (pgx-native).
//
// 2026-08-04: v0.33.1 — auto-migrate on open. Pre-fix, OpenPostgres
// returned a bare *sql.DB without running MigratePostgres, so the
// v0.33.0 tables (headscale_acl_rules + system_tests_runs) were
// only created when the operator manually ran
// `cmd/apply_pg_migrations`. On the live VM the cutover happened
// before v0.33.0 was deployed, so the manual apply picked up
// everything up to v0.49 (no v0.50, no v0.51) — and the
// /admin/headscale/acl page returned http.StatusInternalServerError "relation
// headscale_acl_rules does not exist" until the operator
// triggered a deploy. Calling MigratePostgres() here makes
// the PG path symmetric with the SQLite Open() → migrate(conn)
// path: every container start re-applies the idempotent
// migration chain. New operators don't have to know about
// the standalone `apply_pg_migrations` tool.
func OpenPostgres(dsn string) (*sql.DB, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("pgx open: %w", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("pgx ping: %w", err)
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	if err := MigratePostgres(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("pgx migrate: %w", err)
	}
	registerBackend(conn, BackendPostgres)
	return conn, nil
}

// MigratePostgres runs every PG migration function in version
// order. Returns the first error encountered; the migration state
// is whatever the DB was left in (PG transactions make this
// atomic per-function, not per-call).
//
// V025 runs first because V020+ have FOREIGN KEY → portal_users
// (which V025 creates).
func MigratePostgres(d *sql.DB) error {
	// Set lock_timeout so concurrent migrators fail fast instead of
	// deadlocking. 5s is generous; live migrations finish in
	// well under a second on a fresh DB.
	if _, err := d.Exec(`SET lock_timeout = '5s'`); err != nil {
		return fmt.Errorf("SET lock_timeout: %w", err)
	}
	for _, fn := range []func(*sql.DB) error{
		migrateV025PG, migrateV020PG, migrateV021PG, migrateV022PG,
		migrateV023PG, migrateV024PG, migrateV026PG, migrateV027PG,
		migrateV028PG, migrateV029PG, migrateV030PG, migrateV031PG,
		migrateV032PG, migrateV033PG, migrateV034PG, migrateV035PG,
		migrateV036PG, migrateV037PG, migrateV038PG, migrateV039PG,
		migrateV041PG, migrateV042PG, migrateV043PG, migrateV044PG,
		migrateV045PG, migrateV046PG, migrateV047PG,
		migrateV048PG, migrateV049PG, migrateV050PG, migrateV051PG,
		migrateV053PG, migrateV054PG, migrateV055PG, migrateV056PG,
		migrateV057PG, migrateV058PG, migrateV059PG,
		migrateV060PG, migrateV061PG, migrateV062PG, migrateV063PG,
		migrateV064PG,
	} {
		if err := fn(d); err != nil {
			return fmt.Errorf("migration: %w", err)
		}
	}
	return nil
}
