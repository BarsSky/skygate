package db

import "database/sql"

// migrateV049 (v0.32.19, 2026-08-03): migration integrity tracking.
//
// Adds the applied_migrations table that records, for every
// migration that has been applied, the SHA-256 of the migration
// body. On every Open() / restart, the migrator checks every
// recorded checksum against the current in-binary SQL body of
// the matching migration. A mismatch means the migration was
// modified after being applied (e.g. a developer fixed a typo
// in an old migration) — a latent bug because the existing DB
// already has the pre-fix schema and the new code never re-runs.
//
// Initial mode is SOFT: a mismatch produces a warning log line
// but does NOT prevent skygate from starting. After one release
// cycle of observation (v0.32.20+), the mode flips to HARD: a
// mismatch is a fatal error and the operator must restore the
// previous migration body and rebuild. The hard mode is
// opt-in earlier via the SKYGATE_MIGRATION_INTEGRITY=hard
// env var.
//
// The table is intentionally tiny (one row per migration,
// ~120 bytes). Index on (version) is implicit (PRIMARY KEY).
//
// 2026-08-03: v0.32.19 — migration integrity (soft mode).
func migrateV049(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS applied_migrations (
			version     INTEGER PRIMARY KEY,
			sha256      TEXT    NOT NULL,
			source_file TEXT    NOT NULL DEFAULT '',
			applied_at  INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
			first_seen  TEXT    NOT NULL DEFAULT ''
		)`,
		// The table is small and PRIMARY KEY on version is enough.
		// No secondary indexes needed.
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
