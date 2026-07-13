package db

import "database/sql"

// migrateV031 (2026-07-13): Telegram login-by-key + strict-mode support.
//
// Two pieces land here together because they ship as one feature
// (Этап 12 — full bot access control):
//
//  1. telegram_login_tokens — one-time tokens the user generates in
//     /my/telegram and pastes into the bot via /login <token>. The
//     bot consumes the token and UPSERTs the chat_id → portal_user
//     binding into telegram_bindings. Tokens carry:
//       token            TEXT PRIMARY KEY  (the key the user pastes)
//       portal_user_id   INTEGER NOT NULL  (which portal user this key
//                                         authorises — set at
//                                         generation time, immutable)
//       created_at       INTEGER NOT NULL  (audit + rate-limit window)
//       expires_at       INTEGER NOT NULL  (now + TTL; rejected past this)
//       used_at          INTEGER           (0 = unused; set on consume)
//       used_by_chat_id  INTEGER           (which chat consumed it —
//                                         helps with "did I paste it
//                                         into the right bot?" diagnosis)
//       request_ip       TEXT              (IP of the web request that
//                                         generated the key — audit only,
//                                         never displayed to the user)
//
//  2. The two new keys land via global_settings, not a new column
//     on a typed table, because:
//       - strict_mode is a single boolean (1/0 in DB) that the bot
//         reads on every message (hot-swap); the existing
//         global_settings key/value shape fits.
//       - login_token_ttl is operator-tunable but rarely changed;
//         a global_settings row is enough and saves a migration
//         next time we add another operator-tunable.
//     The /admin/telegram page writes both; the /my/telegram page
//         reads login_token_ttl to render the countdown.
//
// CASCADE on portal_user_id is enforced by the application layer
// (handlers_admin_users.go:PostAdminUserDelete calls
// DeleteTelegramLoginTokensByUserID in the same transaction that
// wipes preauth_keys, device_rules, etc.). No FK declaration: the
// pragma_foreign_keys = OFF default in this codebase means a real
// ON DELETE CASCADE wouldn't fire anyway, and the existing pattern
// (see v0.29) is "application-level cascade in the delete handler".
func migrateV031(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS telegram_login_tokens (
			token            TEXT PRIMARY KEY,
			portal_user_id   INTEGER NOT NULL,
			created_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			expires_at       INTEGER NOT NULL,
			used_at          INTEGER NOT NULL DEFAULT 0,
			used_by_chat_id  INTEGER NOT NULL DEFAULT 0,
			request_ip       TEXT    NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_login_tokens_user
			ON telegram_login_tokens(portal_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_login_tokens_expiry
			ON telegram_login_tokens(expires_at)`,
		// Default strict_mode = 0 (off) so the migration doesn't
		// surprise existing single-admin-chat deploys. New deploys
		// that want strict mode flip it via /admin/telegram.
		// We use INSERT OR IGNORE so re-running the migration is a
		// no-op (the row already exists from a previous run).
		`INSERT OR IGNORE INTO global_settings(key, value, updated_at)
			VALUES ('telegram.strict_mode', '0', strftime('%s','now'))`,
		// Default token TTL = 300s (5 min). Operators can tune via
		// /admin/telegram UI or by editing the global_settings row
		// directly. Stored as a string to keep the schema uniform
		// (all global_settings values are TEXT).
		`INSERT OR IGNORE INTO global_settings(key, value, updated_at)
			VALUES ('telegram.login_token_ttl_seconds', '300', strftime('%s','now'))`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
