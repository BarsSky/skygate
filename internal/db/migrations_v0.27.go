package db

import "database/sql"

// migrateV027 (2026-07-11): Telegram alert ring buffer.
//
// Telegram bot now has /ack <id> so the admin can dismiss alerts from
// their phone. To make /ack addressable, every alert we send is
// recorded in telegram_alerts; the rowid becomes the id we prefix
// to the outgoing message and the id /ack looks up.
//
// We keep a hard cap (500 rows) so the table can't grow unbounded
// under chatty triggers; prune is fired-and-forgotten on each insert
// because exact bound is not important — older acked rows are useless
// anyway and a 500-row window covers any reasonable recent activity.
//
// A row is "open" while acked_at=0 and "acked" otherwise. The /ack
// command flips acked_at/acked_by in place (idempotent: re-acking is
// a no-op the second time, since UPDATE only matches acked_at=0).
func migrateV027(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS telegram_alerts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			body TEXT NOT NULL,
			sent_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			acked_at INTEGER NOT NULL DEFAULT 0,
			acked_by TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_alerts_unacked
			ON telegram_alerts(id) WHERE acked_at = 0`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
