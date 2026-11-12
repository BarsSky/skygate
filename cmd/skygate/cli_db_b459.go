// B459 — the CLI subcommands must open the database through the DIALECT-AWARE
// opener.
//
// Measured live on the standby host (native, SQLite — the v1.5.4+ DEFAULT
// backend) on 2026-10-29: `skygate oidc-export`, `skygate init status` and the
// whole `skygate cluster …` family all called db.OpenDSN, which is the
// PostgreSQL-only opener (sql.Open("pgx", dsn) — its own doc comment still says
// "v1.3.0: skygate is PG-only", stale since v1.5.4). On a SQLite install every
// one of them died with
//
//	cannot parse `sqlite:/srv/skygate/skygate.db`: failed to parse as
//	keyword/value (invalid keyword/value)
//
// and the failure was invisible in the gate because it only reproduces on a
// non-PostgreSQL install. The worst instance was `oidc-export`: it treated the
// open error as a soft warning, exported the SKYGATE_OIDC_* env values and
// exited 0 — a "successful" export of a configuration the panel does not have,
// which deploy/skygate-apply-oidc.sh would then write into headscale.
//
// openCLIDB is the single entry point for an operator-on-the-box subcommand.
// db.OpenWithDialect is a strict superset of what db.OpenDSN did: it classifies
// the DSN and then runs the SAME migration chain for that dialect
// (openPostgresWith runs MigratePostgres, openSQLite runs MigrateSQLite), so no
// caller loses behaviour.
package main

import (
	"database/sql"

	"skygate/internal/config"
	"skygate/internal/db"
)

// openCLIDB opens the configured database through the dialect-aware opener.
func openCLIDB(cfg *config.Config) (*sql.DB, error) {
	_, d, err := db.OpenWithDialect(cfg.DBDSN)
	if err != nil {
		return nil, err
	}
	return d, nil
}
