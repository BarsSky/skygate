// migrations_v0_76_monitor_events.go — v0.76 (B305, 2026-09-23):
// monitor_events — the operator's monitoring inbox.
//
// WHY: the operator asked for «поле с уведомлениями куда будут приходить все
// сообщения разной важности с мониторинга skygate». Before this table every
// monitoring signal lived somewhere different and most of them lived nowhere:
// exit-node health went to Telegram only (and only on a calm-mode crossing), tag
// reconciliation failures went to a metric + an audit row, system-test failures
// were rendered once and forgotten, DB-health degradation was a badge on one page.
// Nothing could answer "what is wrong with this install right now?" in one place,
// and nothing survived a page reload or a Telegram outage.
//
// The table is deliberately SEPARATE from the per-user `notifications` table
// (V059, B157):
//   * those rows are per-user and disappear with the user; these are per-INSTALL
//     and belong to the operator's view of the system;
//   * a monitoring event needs DEDUPLICATION (a recurring failure must update one
//     row with repeats/last_seen, not flood the list) and a RESOLVED state, which
//     a per-user inbox has no concept of;
//   * an event can exist before any user is known (boot-time checks), and the
//     per-user table has a NOT NULL foreign key to portal_users.
//
// fingerprint is the dedup key (`<source>:<subject>:<kind>`), UNIQUE so the
// upsert collapses recurrences. The pair (state, last_seen) is indexed for the
// page's default view ("open, newest first").
//
// Additive: an existing install gets an empty table; nothing reads it unless a
// producer reports an event, and the v0.76 migration only installs the schema.
package db

import (
	"database/sql"
	"fmt"
)

const monitorEventsTableSQLiteSQL = `CREATE TABLE IF NOT EXISTS monitor_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	fingerprint TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	subject TEXT NOT NULL DEFAULT '',
	severity TEXT NOT NULL DEFAULT 'info',
	state TEXT NOT NULL DEFAULT 'open',
	title TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	link TEXT NOT NULL DEFAULT '',
	first_seen INTEGER NOT NULL DEFAULT 0,
	last_seen INTEGER NOT NULL DEFAULT 0,
	repeats INTEGER NOT NULL DEFAULT 1,
	acked_at INTEGER NOT NULL DEFAULT 0,
	acked_by TEXT NOT NULL DEFAULT '',
	resolved_at INTEGER NOT NULL DEFAULT 0
)`

const monitorEventsTablePGSQL = `CREATE TABLE IF NOT EXISTS monitor_events (
	id BIGSERIAL PRIMARY KEY,
	fingerprint TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	subject TEXT NOT NULL DEFAULT '',
	severity TEXT NOT NULL DEFAULT 'info',
	state TEXT NOT NULL DEFAULT 'open',
	title TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	link TEXT NOT NULL DEFAULT '',
	first_seen BIGINT NOT NULL DEFAULT 0,
	last_seen BIGINT NOT NULL DEFAULT 0,
	repeats INTEGER NOT NULL DEFAULT 1,
	acked_at BIGINT NOT NULL DEFAULT 0,
	acked_by TEXT NOT NULL DEFAULT '',
	resolved_at BIGINT NOT NULL DEFAULT 0
)`

const monitorEventsFingerprintIndexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_monitor_events_fingerprint
	ON monitor_events(fingerprint)`

const monitorEventsStateIndexSQL = `CREATE INDEX IF NOT EXISTS idx_monitor_events_state_seen
	ON monitor_events(state, last_seen DESC)`

// migrateV076PG adds monitor_events on PostgreSQL.
func migrateV076PG(d *sql.DB) error {
	for _, stmt := range []string{
		monitorEventsTablePGSQL,
		monitorEventsFingerprintIndexSQL,
		monitorEventsStateIndexSQL,
	} {
		if _, err := d.Exec(stmt); err != nil {
			return fmt.Errorf("v0.76 monitor_events: %w", err)
		}
	}
	return nil
}

// migrateV076SQLite adds monitor_events on SQLite through the single DDL
// chokepoint (execSQLiteDDL routes ADD COLUMN through addColumnIfMissingSQLite;
// CREATE TABLE/INDEX here are already idempotent).
func migrateV076SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{
		monitorEventsTableSQLiteSQL,
		monitorEventsFingerprintIndexSQL,
		monitorEventsStateIndexSQL,
	}); err != nil {
		return fmt.Errorf("v0.76 monitor_events (sqlite): %w", err)
	}
	return nil
}
