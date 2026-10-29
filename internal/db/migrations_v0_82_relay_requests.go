// migrations_v0_82_relay_requests.go — v0.82 (B440): a user may ASK for another
// exit node, and the operator decides per user and globally.
//
// WHY. After B275 the carrier of a prefix is the `prefix_owner` table, and after
// B439 the row says which relay carries it. But a user who wants their traffic
// moved had no way to ask: the only lever was the operator editing the assignment by
// hand (`/admin/exit-nodes`), and the operator's decision *what a user may ask for*
// lived in nobody's memory. The operator asked for exactly this (verbatim, 2026-10-29):
//
//	«необходимо чтобы пользователь мог делать запрос на смену exit node для правила и у
//	 администратора имелась возможность выставить политику поведения на такие запросы —
//	 к примеру от такого то пользователя всегда разрешать запрос на другой нод от
//	 такого то нет и общая политика и оповещать о таких запросах администратора в
//	 уведомлениях и телеграмме»
//
// Two tables:
//
//	exit_relay_requests          — one ask: who, for which device, from which relay to
//	                               which, over which prefixes, and what was decided.
//	                               `prefixes` is a SPACE-separated snapshot (the table
//	                               they came from is recomputed every tick, so the row
//	                               must remember what the ask was ABOUT).
//	exit_relay_policies          — the policy: scope `global` or `user:<username>`,
//	                               mode `auto_approve` | `require_approval` | `deny`.
//	                               A user row overrides the global one; with neither,
//	                               the default is `require_approval` (a request is never
//	                               silently applied).
//
// Both chains, in the same money-path shape the rest of this package uses: PostgreSQL
// gets BIGSERIAL + TIMESTAMPTZ-as-INTEGER, SQLite gets INTEGER PRIMARY KEY AUTOINCREMENT
// and goes through execSQLiteDDL.
package db

import (
	"database/sql"
	"fmt"
)

const relayRequestsPG = `
CREATE TABLE IF NOT EXISTS exit_relay_requests (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT  NOT NULL,
    username        TEXT    NOT NULL DEFAULT '',
    device_hostname TEXT    NOT NULL DEFAULT '',
    from_relay      TEXT    NOT NULL DEFAULT '',
    to_relay        TEXT    NOT NULL,
    prefixes        TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'pending',
    note            TEXT    NOT NULL DEFAULT '',
    decided_by      TEXT    NOT NULL DEFAULT '',
    decided_at      BIGINT  NOT NULL DEFAULT 0,
    created_at      BIGINT  NOT NULL DEFAULT 0,
    updated_at      BIGINT  NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS exit_relay_requests_status_idx ON exit_relay_requests (status, created_at);
CREATE INDEX IF NOT EXISTS exit_relay_requests_user_idx   ON exit_relay_requests (user_id, created_at);

CREATE TABLE IF NOT EXISTS exit_relay_policies (
    scope      TEXT PRIMARY KEY,
    mode       TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL DEFAULT 0
);`

const relayRequestsSQLiteStmt1 = `CREATE TABLE IF NOT EXISTS exit_relay_requests (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL,
    username        TEXT    NOT NULL DEFAULT '',
    device_hostname TEXT    NOT NULL DEFAULT '',
    from_relay      TEXT    NOT NULL DEFAULT '',
    to_relay        TEXT    NOT NULL,
    prefixes        TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'pending',
    note            TEXT    NOT NULL DEFAULT '',
    decided_by      TEXT    NOT NULL DEFAULT '',
    decided_at      INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL DEFAULT 0
);`

const relayRequestsSQLiteStmt2 = `CREATE INDEX IF NOT EXISTS exit_relay_requests_status_idx ON exit_relay_requests (status, created_at);`

const relayRequestsSQLiteStmt3 = `CREATE INDEX IF NOT EXISTS exit_relay_requests_user_idx   ON exit_relay_requests (user_id, created_at);`

const relayRequestsSQLiteStmt4 = `CREATE TABLE IF NOT EXISTS exit_relay_policies (
    scope      TEXT PRIMARY KEY,
    mode       TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);`

// migrateV082PG creates the two relay-request tables on PostgreSQL.
func migrateV082PG(d *sql.DB) error {
	if _, err := d.Exec(relayRequestsPG); err != nil {
		return fmt.Errorf("v0.82 exit_relay_requests/policies: %w", err)
	}
	return nil
}

// migrateV082SQLite creates them on SQLite through the single DDL chokepoint. Each
// statement is its OWN element: the chokepoint runs one Exec per element, and a
// multi-statement string would silently execute only the first one (measured: the
// table appeared, the policies table and both indexes did not).
func migrateV082SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{
		relayRequestsSQLiteStmt1,
		relayRequestsSQLiteStmt2,
		relayRequestsSQLiteStmt3,
		relayRequestsSQLiteStmt4,
	}); err != nil {
		return fmt.Errorf("v0.82 exit_relay_requests/policies (sqlite): %w", err)
	}
	return nil
}
