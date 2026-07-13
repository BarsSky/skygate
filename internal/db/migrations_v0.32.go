package db

import "database/sql"

// migrateV032 (2026-07-13): shared (cross-instance) rate-limit
// store for the Telegram bot.
//
// Replaces the in-memory loginAttempts map in
// internal/telegram/commands_login.go. The in-memory version
// was reset on every container restart AND didn't survive
// multi-instance deploys (each replica kept its own counter).
// The new design is a tiny (key, action, ts) table; the bot
// INSERTs one row per attempt and the "is this chat over the
// limit?" check is one SELECT with a 60s WHERE clause. Stale
// rows are pruned by a background sweeper that runs on every
// 1000th attempt (cheap because of idx_telegram_rate_limit_prune).
//
// Why SQLite and not Redis: skygate is single-instance by
// design (per AGENTS.md + the existing per-instance rate-limit
// note in ratelimit/ratelimit.go). Adding Redis would be a
// new infrastructure dependency for a problem that SQLite
// already solves correctly and atomically. If/when the project
// moves to multi-instance, the same SQL works against any
// shared store that the driver can speak to — only the
// driver's connection string changes.
//
// Schema:
//   key       TEXT NOT NULL  — "<scope>:<id>", e.g. "login:555"
//                              where 555 is the chat_id. The
//                              scope prefix lets future
//                              commands share the table
//                              (e.g. "addrule:555", "restart:1")
//                              without colliding.
//   action    TEXT NOT NULL  — sub-action within the scope.
//                              For /login this is empty
//                              string (the scope is enough);
//                              for more nuanced rate-limits
//                              ("5 /min total + 1/sec burst")
//                              the action would distinguish.
//                              Reserved for future use.
//   ts        INTEGER NOT NULL — unix seconds, the attempt time.
//
// Indexes:
//   idx_telegram_rate_limit_prune ON (ts) — every PruneSweep
//     runs DELETE WHERE ts < cutoff; the index keeps that
//     cheap.
//   idx_telegram_rate_limit_lookup ON (key, ts) — the per-attempt
//     SELECT (count rows for this key in the last 60s) reads
//     through this.
func migrateV032(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS telegram_rate_limit (
			key    TEXT NOT NULL,
			action TEXT NOT NULL DEFAULT '',
			ts     INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_rate_limit_lookup
			ON telegram_rate_limit(key, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_rate_limit_prune
			ON telegram_rate_limit(ts)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
