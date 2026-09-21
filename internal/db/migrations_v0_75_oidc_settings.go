// migrations_v0_75_oidc_settings.go — v0.75 (B-oidc-setup,
// 2026-09-21): oidc_settings table — DB-backed OIDC config so
// the operator can enable / configure / disable OIDC from the
// admin web UI, not by editing env vars + restarting the
// container.
//
// WHY (2026-09-21): the pre-v0.75 OIDC config lived in 4 env
// vars (SKYGATE_OIDC_ISSUER, _CLIENT_ID, _CLIENT_SECRET,
// _KEY_DIR) read at boot. The /admin/oidc page rendered
// them read-only — operators had to SSH into the host and
// edit .env (or restart the docker container) to change any
// value. A "first-time setup" flow (paste issuer, click
// Enable) didn't exist. The user reported:
//
//	"сейчас в вебинтерфейсе отсутствует какой либо понятный
//	 и удобный механизм включения также как и флаг в env
//	 чтобы развернуть skygate уже сразу с OIDC — нужен
//	 удобный интерфейс для администратора"
//
// V075 stores the effective OIDC config in a single-row table;
// the boot sequence reads the DB row first and falls back to
// env vars only when no DB row exists (preserves the legacy
// env-only deployments). The admin UI writes to the DB.
//
// The id column is a synthetic 1 — the CHECK enforces at-most-
// one-row so concurrent form submits can't create two rows.
// SQLite + PG both support the partial UNIQUE index; on
// SQLite the WHERE clause requires an index, on PG it's a
// partial-index hint. Both backends accept the same DDL.
//
// The table is additive — an existing deployment without an
// oidc_settings row continues to read its env vars until the
// operator opens /admin/oidc, fills the form, and saves. The
// row is created with id=1 on first save; the migration only
// installs the empty schema.
package db

import (
	"database/sql"
	"fmt"
)

const oidcSettingsTableSQLiteSQL = `CREATE TABLE IF NOT EXISTS oidc_settings (
	id INTEGER PRIMARY KEY DEFAULT 1,
	enabled INTEGER NOT NULL DEFAULT 0,
	issuer TEXT NOT NULL DEFAULT '',
	client_id TEXT NOT NULL DEFAULT '',
	client_secret TEXT NOT NULL DEFAULT '',
	redirect_uris TEXT NOT NULL DEFAULT '',
	key_dir TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0,
	CHECK (id = 1)
)`

const oidcSettingsTablePGSQL = `CREATE TABLE IF NOT EXISTS oidc_settings (
	id INTEGER PRIMARY KEY DEFAULT 1,
	enabled BOOLEAN NOT NULL DEFAULT FALSE,
	issuer TEXT NOT NULL DEFAULT '',
	client_id TEXT NOT NULL DEFAULT '',
	client_secret TEXT NOT NULL DEFAULT '',
	redirect_uris TEXT NOT NULL DEFAULT '',
	key_dir TEXT NOT NULL DEFAULT '',
	created_at BIGINT NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL DEFAULT 0,
	CONSTRAINT oidc_settings_singleton CHECK (id = 1)
)`

// migrateV075PG adds oidc_settings on PostgreSQL.
func migrateV075PG(d *sql.DB) error {
	if _, err := d.Exec(oidcSettingsTablePGSQL); err != nil {
		return fmt.Errorf("v0.75 oidc_settings table: %w", err)
	}
	return nil
}

// migrateV075SQLite adds oidc_settings on SQLite through the
// single DDL chokepoint (execSQLiteDDL handles ADD COLUMN
// routing through addColumnIfMissingSQLite for SQLite's lack of
// IF NOT EXISTS on column-add).
func migrateV075SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{oidcSettingsTableSQLiteSQL}); err != nil {
		return fmt.Errorf("v0.75 oidc_settings (sqlite): %w", err)
	}
	return nil
}
