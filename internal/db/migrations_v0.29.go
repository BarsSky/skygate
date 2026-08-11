package db

import "database/sql"

// migrateV029 (2026-07-12): Telegram chat_id → portal_user bindings.
//
// Before V029 the bot was implicitly admin-only: a single chat_id was
// configured in global_settings (telegram.chat_id) and the polling
// loop dispatched every command to admin-scope handlers. There was no
// way for a regular portal user to interact with the bot.
//
// V029 introduces telegram_bindings so a chat can be mapped to a
// portal user. The bot's dispatch path now:
//   1. reads the inbound chat_id from the update
//   2. looks it up in telegram_bindings (or treats it as admin if it
//      matches the configured telegram.chat_id, for backward compat)
//   3. routes the command through a user-aware dispatcher
//      (see internal/telegram/commands.go:HandleCommand)
//
// Schema:
//   chat_id           INTEGER PRIMARY KEY  (Telegram's per-chat id;
//                                          negative for groups, positive
//                                          for direct DMs)
//   portal_user_id    INTEGER NOT NULL     (FK to portal_users.id)
//   is_admin          INTEGER NOT NULL DEFAULT 0  (denormalized for fast
//                                          permission check; copied from
//                                          portal_users.is_admin at bind
//                                          time so a revocation doesn't
//                                          need to recompute every reply)
//   bound_at          INTEGER NOT NULL DEFAULT (strftime('%s','now'))
//   bound_by_user_id  INTEGER NOT NULL DEFAULT 0  (admin who created
//                                          the binding, 0 for system)
//
// CASCADE on portal_user_id is left to the admin user-delete path
// (handlers_admin_users.go:PostAdminUserDelete already wipes
// preauth_keys, device_rules, etc.; a DeleteTelegramBindingsByUserID
// helper is provided for that path).
func migrateV029(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS telegram_bindings (
			chat_id          INTEGER PRIMARY KEY,
			portal_user_id   INTEGER NOT NULL,
			is_admin         INTEGER NOT NULL DEFAULT 0,
			bound_at         INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			bound_by_user_id INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_telegram_bindings_user
			ON telegram_bindings(portal_user_id)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
