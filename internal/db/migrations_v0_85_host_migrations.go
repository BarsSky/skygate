// migrations_v0_85_host_migrations.go — v0.85 (B482): a migration RUN becomes a row, not a
// procedure in someone's notes.
//
// # WHY
//
// The 2026-11-15/16 cutover from an agent VM to a VPS was performed by hand, step by step, and
// the knowledge lived in a chat log: which files the panel actually consists of, that headscale's
// noise key and SKYGATE_SECRET_KEY must be copied verbatim, that OIDC re-registration drops tags,
// that derper verifies the full chain, that a DERP upgrade cannot ride HTTP/2. The operator asked
// for that analysis to become a wizard in the panel («Поля Администратора»), where the answers
// are questions with a choice or an input, and the application is automatic.
//
// A wizard needs memory: the answers must survive a page reload and a panel restart, and every
// step that ran must leave a record of what it did — otherwise a failed half-migration is
// indistinguishable from an untouched host, which is exactly the failure mode the cutover hit.
//
// TWO TABLES
//
//	host_migrations       one row per attempt: the answers and the run's state;
//	host_migration_steps one row per step of the plan: what ran, the output tail, the verdict.
//
// The step rows are written when the plan is generated (so the UI can show the whole plan before
// anything executes) and updated as it runs. `UNIQUE (run_id, step_key)` is the idempotency
// anchor: re-generating a plan for the same run rewrites rows instead of duplicating them.
//
// # SECRETS
//
// The SSH password and a pasted private key are NOT columns of this table by accident: they are
// stored encrypted (`secret_enc`, AES-256-GCM via db.EncryptForColumn and SKYGATE_SECRET_KEY) and
// are write-only — the render path never fills the field back in, so a stored credential cannot
// be read out of the HTML. The DSN of a target PostgreSQL travels the same way (`db_dsn_enc`).
//
// Both chains, in the shape the rest of this package uses: PostgreSQL gets BIGSERIAL + BIGINT
// epoch columns, SQLite gets INTEGER PRIMARY KEY AUTOINCREMENT and goes through execSQLiteDDL.
package db

import (
	"database/sql"
	"fmt"
)

const hostMigrationsPG = `
CREATE TABLE IF NOT EXISTS host_migrations (
    id              BIGSERIAL PRIMARY KEY,
    created_by      BIGINT  NOT NULL DEFAULT 0,
    created_by_name TEXT    NOT NULL DEFAULT '',
    state           TEXT    NOT NULL DEFAULT 'draft',
    mode            TEXT    NOT NULL DEFAULT 'copy',
    target_host     TEXT    NOT NULL DEFAULT '',
    target_port     INTEGER NOT NULL DEFAULT 22,
    target_user     TEXT    NOT NULL DEFAULT 'root',
    auth_kind       TEXT    NOT NULL DEFAULT 'key_path',
    auth_ref        TEXT    NOT NULL DEFAULT '',
    secret_enc      TEXT    NOT NULL DEFAULT '',
    db_backend      TEXT    NOT NULL DEFAULT 'same',
    db_dsn_enc      TEXT    NOT NULL DEFAULT '',
    new_hostname    TEXT    NOT NULL DEFAULT '',
    dns_mode        TEXT    NOT NULL DEFAULT 'manual',
    old_host_mode   TEXT    NOT NULL DEFAULT 'keep',
    answers_json    TEXT    NOT NULL DEFAULT '{}',
    created_at      BIGINT  NOT NULL DEFAULT 0,
    updated_at      BIGINT  NOT NULL DEFAULT 0,
    started_at      BIGINT  NOT NULL DEFAULT 0,
    finished_at     BIGINT  NOT NULL DEFAULT 0,
    summary         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS host_migrations_created_idx ON host_migrations (created_at);
CREATE TABLE IF NOT EXISTS host_migration_steps (
    id          BIGSERIAL PRIMARY KEY,
    run_id      BIGINT  NOT NULL,
    phase       TEXT    NOT NULL DEFAULT '',
    ordinal     INTEGER NOT NULL DEFAULT 0,
    step_key    TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT '',
    kind        TEXT    NOT NULL DEFAULT 'auto',
    step_where  TEXT    NOT NULL DEFAULT 'remote',
    status      TEXT    NOT NULL DEFAULT 'pending',
    required    INTEGER NOT NULL DEFAULT 1,
    command     TEXT    NOT NULL DEFAULT '',
    output      TEXT    NOT NULL DEFAULT '',
    verify      TEXT    NOT NULL DEFAULT '',
    started_at  BIGINT  NOT NULL DEFAULT 0,
    finished_at BIGINT  NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS host_migration_steps_run_key_uniq
    ON host_migration_steps (run_id, step_key);
CREATE INDEX IF NOT EXISTS host_migration_steps_run_idx
    ON host_migration_steps (run_id, ordinal);`

const hostMigrationsSQLiteStmt1 = `CREATE TABLE IF NOT EXISTS host_migrations (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    created_by      INTEGER NOT NULL DEFAULT 0,
    created_by_name TEXT    NOT NULL DEFAULT '',
    state           TEXT    NOT NULL DEFAULT 'draft',
    mode            TEXT    NOT NULL DEFAULT 'copy',
    target_host     TEXT    NOT NULL DEFAULT '',
    target_port     INTEGER NOT NULL DEFAULT 22,
    target_user     TEXT    NOT NULL DEFAULT 'root',
    auth_kind       TEXT    NOT NULL DEFAULT 'key_path',
    auth_ref        TEXT    NOT NULL DEFAULT '',
    secret_enc      TEXT    NOT NULL DEFAULT '',
    db_backend      TEXT    NOT NULL DEFAULT 'same',
    db_dsn_enc      TEXT    NOT NULL DEFAULT '',
    new_hostname    TEXT    NOT NULL DEFAULT '',
    dns_mode        TEXT    NOT NULL DEFAULT 'manual',
    old_host_mode   TEXT    NOT NULL DEFAULT 'keep',
    answers_json    TEXT    NOT NULL DEFAULT '{}',
    created_at      INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER NOT NULL DEFAULT 0,
    finished_at     INTEGER NOT NULL DEFAULT 0,
    summary         TEXT    NOT NULL DEFAULT ''
);`

const hostMigrationsSQLiteStmt2 = `CREATE INDEX IF NOT EXISTS host_migrations_created_idx
    ON host_migrations (created_at);`

const hostMigrationsSQLiteStmt3 = `CREATE TABLE IF NOT EXISTS host_migration_steps (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id      INTEGER NOT NULL,
    phase       TEXT    NOT NULL DEFAULT '',
    ordinal     INTEGER NOT NULL DEFAULT 0,
    step_key    TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT '',
    kind        TEXT    NOT NULL DEFAULT 'auto',
    step_where  TEXT    NOT NULL DEFAULT 'remote',
    status      TEXT    NOT NULL DEFAULT 'pending',
    required    INTEGER NOT NULL DEFAULT 1,
    command     TEXT    NOT NULL DEFAULT '',
    output      TEXT    NOT NULL DEFAULT '',
    verify      TEXT    NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
);`

const hostMigrationsSQLiteStmt4 = `CREATE UNIQUE INDEX IF NOT EXISTS host_migration_steps_run_key_uniq
    ON host_migration_steps (run_id, step_key);`

const hostMigrationsSQLiteStmt5 = `CREATE INDEX IF NOT EXISTS host_migration_steps_run_idx
    ON host_migration_steps (run_id, ordinal);`

// migrateV085PG creates the migration-run tables on PostgreSQL.
func migrateV085PG(d *sql.DB) error {
	if _, err := d.Exec(hostMigrationsPG); err != nil {
		return fmt.Errorf("v0.85 host_migrations: %w", err)
	}
	return nil
}

// migrateV085SQLite creates the migration-run tables on SQLite through the single DDL chokepoint.
func migrateV085SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{
		hostMigrationsSQLiteStmt1,
		hostMigrationsSQLiteStmt2,
		hostMigrationsSQLiteStmt3,
		hostMigrationsSQLiteStmt4,
		hostMigrationsSQLiteStmt5,
	}); err != nil {
		return fmt.Errorf("v0.85 host_migrations (sqlite): %w", err)
	}
	return nil
}
