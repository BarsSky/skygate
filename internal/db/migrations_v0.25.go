package db

import "database/sql"

// 2026-07-09: refactor v0.6.0 — schema bootstrap fix.
//
// Historically portal_users was created by a migration that was lost, or
// was bootstrapped manually before the project adopted an explicit
// migration chain. As a result, fresh deployments (and unit tests) would
// lack the root table that everything else references.
//
// migrateV025 is idempotent (CREATE TABLE IF NOT EXISTS + matching schema
// to the live database's portal_users layout) and is the canonical source
// of the table on greenfield deployments.
//
// Live installations whose portal_users was created with a different
// column set are safe — CREATE TABLE IF NOT EXISTS returns early.
func migrateV025(d *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS portal_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			is_admin INTEGER NOT NULL DEFAULT 0,
			headscale_user_id INTEGER,
			created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			theme TEXT NOT NULL DEFAULT 'linear'
		)`,
		`CREATE TABLE IF NOT EXISTS preauth_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			key TEXT NOT NULL UNIQUE,
			headscale_preauth_id TEXT DEFAULT '',
			reusable INTEGER NOT NULL DEFAULT 0,
			used INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER DEFAULT 0,
			created_at INTEGER DEFAULT (strftime('%s','now')),
			FOREIGN KEY (user_id) REFERENCES portal_users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS devices (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			hostname TEXT NOT NULL,
			node_id TEXT DEFAULT '',
			headscale_node_id TEXT DEFAULT '',
			ip_addresses TEXT DEFAULT '',
			os TEXT DEFAULT '',
			last_seen INTEGER DEFAULT 0,
			online INTEGER DEFAULT 0,
			created_at INTEGER DEFAULT (strftime('%s','now')),
			FOREIGN KEY (user_id) REFERENCES portal_users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER DEFAULT 0,
			username TEXT DEFAULT '',
			action TEXT NOT NULL,
			detail TEXT DEFAULT '',
			ip_address TEXT DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS node_owner_map (
			node_id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			attributed_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
