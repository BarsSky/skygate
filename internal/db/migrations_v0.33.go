package db

import "database/sql"

// migrateV033 (2026-07-14): per-chat language preference for
// the Telegram bot.
//
// Этап 14 v5 — bot i18n. Each telegram_bindings row gets a
// `lang` column (default 'en') so the bot can answer in
// Russian or English per chat, not per deployment. The
// column is denormalised on purpose: looking up the language
// is a single indexed read in the same query that already
// returns the binding row for the dispatcher, so the bot's
// hot path is unchanged. An alternative (join via
// portal_users.lang) would be cleaner schema-wise but it
// would either require a new column on portal_users
// (telegram and web would share a lang — not what the user
// asked for; the web UI's lang comes from the lang cookie
// and should be independent) or a JOIN in the binding
// SELECT (more expensive and a refactor in five files).
//
// We pick denormalisation. The value is updated in two
// places:
//   1. UpsertTelegramBinding (passes through from /login).
//   2. SetTelegramBindingLang (the /lang command, and the
//      auto-detect path in notify.go that reads
//      message.from.language_code on first bind).
//
// Why 'en' is the default and not 'ru': the user population
// on the public web is international, and an explicit /lang
// ru is a one-step opt-in. The Telegram client itself sends
// language_code on every update, so an unidentified chat
// gets the right language in /start's welcome via the
// message-level detect; the binding-level default only
// kicks in after a /login (which is when we have a row to
// write into), and the user can override at any time.
func migrateV033(d *sql.DB) error {
	stmts := []string{
		`ALTER TABLE telegram_bindings ADD COLUMN lang TEXT NOT NULL DEFAULT 'en'`,
	}
	for _, q := range stmts {
		// ALTER TABLE ADD COLUMN with a DEFAULT is idempotent
		// in our model: if the column already exists, the
		// statement returns an error and we move on. Fresh
		// DBs get the column with the default; existing DBs
		// silently no-op. Same pattern as the v0.21 ALTER
		// for device_rules.action.
		if _, err := d.Exec(q); err != nil {
			// Column already exists — that's the expected
			// "already migrated" path. Continue.
			continue
		}
	}
	return nil
}
