package db

import "database/sql"

func migrateV020(d *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS exit_servers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			node_id TEXT NOT NULL UNIQUE,
			hostname TEXT NOT NULL,
			tailscale_ip TEXT NOT NULL DEFAULT '',
			description TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS device_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			device_id INTEGER NOT NULL,
			exit_node_id TEXT NOT NULL,
			target_type TEXT NOT NULL DEFAULT 'domain',
			target_value TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			created_at INTEGER DEFAULT (strftime('%s','now')),
			FOREIGN KEY (user_id) REFERENCES portal_users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS acl_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version INTEGER NOT NULL,
			config TEXT NOT NULL,
			created_by TEXT NOT NULL,
			applied_success INTEGER DEFAULT NULL,
			error_msg TEXT DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`CREATE TABLE IF NOT EXISTS exit_rule_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version INTEGER NOT NULL,
			action TEXT NOT NULL,
			detail TEXT DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func migrateV021(d *sql.DB) error {
	queries := []string{
		`ALTER TABLE device_rules ADD COLUMN action TEXT NOT NULL DEFAULT 'accept'`,
		`CREATE TABLE IF NOT EXISTS global_settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at INTEGER DEFAULT (strftime('%s','now'))
		)`,
		`INSERT INTO global_settings (key, value) VALUES ('exit_policy', 'allow_all') ON CONFLICT (key) DO NOTHING`,
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			// ALTER TABLE ADD COLUMN may fail if column exists — ignore
			continue
		}
	}
	return nil
}

func migrateV022(d *sql.DB) error {
	// 2026-07-11: Этап 9 part 2 — the original 2026-07-09 statement had
	// `DEFAULT ` with no value, which is a syntax error. The function
	// ignored the error so the migration silently no-op'd, leaving
	// device_rules without device_ip on fresh DBs. Fixed.
	_, err := d.Exec("ALTER TABLE device_rules ADD COLUMN device_ip TEXT NOT NULL DEFAULT ''")
	if err != nil { return nil } // column exists
	return nil
}

func migrateV023(d *sql.DB) error {
	// 2026-07-09: refactor v0.6.0 — original statement had a syntax error
	// (`DEFAULT ,` with no value) which made the table creation silently
	// fail — the function was invoked without an err check so the migration
	// was a no-op. Production tables were created out-of-band and exist
	// today, but fresh deployments lost this table. Fixed below.
	const q = `CREATE TABLE IF NOT EXISTS personal_api_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		label TEXT NOT NULL DEFAULT '',
		last_used_at INTEGER DEFAULT 0,
		created_at INTEGER DEFAULT (strftime('%s','now')),
		FOREIGN KEY (user_id) REFERENCES portal_users(id)
	)`
	if _, err := d.Exec(q); err != nil {
		return err
	}
	return nil
}


func migrateV024(d *sql.DB) error {
	queries := []string{
		"ALTER TABLE exit_servers ADD COLUMN ssh_target TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE exit_servers ADD COLUMN ssh_key_path TEXT NOT NULL DEFAULT ''",
	}
	for _, q := range queries {
		d.Exec(q) // ignore errors (column may exist)
	}
	return nil
}
