package db

// 2026-07-22: v0.27.0 — PostgreSQL migration chain (proof of concept).
//
// migratePostgres() in db.go dispatches to migrateV025PG() as the
// first port. The remaining versions (V0.20 / V0.21 / ... / V0.43)
// will be ported incrementally in Phase 1.3 of the v0.27.0 plan
// (see docs/v0.27.0-postgres-ha.md).
//
// Conversion rules applied (see docs/v0.27.0-postgres-ha.md §1.2 for
// the full table):
//
//   SQLite                              PostgreSQL
//   ----------------------------------  ----------------------------------------
//   INTEGER PRIMARY KEY AUTOINCREMENT   BIGSERIAL PRIMARY KEY
//   INTEGER NOT NULL                    BIGINT NOT NULL
//   TEXT                                TEXT (unchanged)
//   strftime('%s','now')                EXTRACT(EPOCH FROM now())::bigint
//   FOREIGN KEY (col) REFERENCES tab    FOREIGN KEY (col) REFERENCES tab (same)
//   CREATE INDEX IF NOT EXISTS          CREATE INDEX IF NOT EXISTS (same)
//
// We keep the `created_at BIGINT` convention (Unix epoch seconds) for
// PG to stay aligned with the SQLite schema. Switching to TIMESTAMPTZ
// would force a refactor of every query that filters on created_at
// (which is most of them); the BIGINT path is a clean 1:1 port.
import "database/sql"

func migrateV025PG(d *sql.DB) error {
	queries := []string{
		// portal_users (root table; everything else FKs to it).
		`CREATE TABLE IF NOT EXISTS portal_users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			is_admin BIGINT NOT NULL DEFAULT 0,
			headscale_user_id BIGINT,
			created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM now())::bigint,
			theme TEXT NOT NULL DEFAULT 'linear'
		)`,
		// preauth_keys
		`CREATE TABLE IF NOT EXISTS preauth_keys (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL,
			key TEXT NOT NULL UNIQUE,
			headscale_preauth_id TEXT DEFAULT '',
			reusable BIGINT NOT NULL DEFAULT 0,
			used BIGINT NOT NULL DEFAULT 0,
			expires_at BIGINT DEFAULT 0,
			created_at BIGINT DEFAULT EXTRACT(EPOCH FROM now())::bigint,
			CONSTRAINT fk_preauth_keys_user FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
		)`,
		// devices
		`CREATE TABLE IF NOT EXISTS devices (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT NOT NULL,
			hostname TEXT NOT NULL,
			node_id TEXT DEFAULT '',
			headscale_node_id TEXT DEFAULT '',
			ip_addresses TEXT DEFAULT '',
			os TEXT DEFAULT '',
			last_seen BIGINT DEFAULT 0,
			online BIGINT DEFAULT 0,
			created_at BIGINT DEFAULT EXTRACT(EPOCH FROM now())::bigint,
			CONSTRAINT fk_devices_user FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
		)`,
		// audit_log
		`CREATE TABLE IF NOT EXISTS audit_log (
			id BIGSERIAL PRIMARY KEY,
			user_id BIGINT DEFAULT 0,
			username TEXT DEFAULT '',
			action TEXT NOT NULL,
			detail TEXT DEFAULT '',
			ip_address TEXT DEFAULT '',
			created_at BIGINT DEFAULT EXTRACT(EPOCH FROM now())::bigint
		)`,
		// node_owner_map
		`CREATE TABLE IF NOT EXISTS node_owner_map (
			node_id TEXT PRIMARY KEY,
			headscale_user_id BIGINT NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			tag TEXT NOT NULL DEFAULT '',
			tagged_by_user_id BIGINT NOT NULL DEFAULT 0,
			tagged_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM now())::bigint,
			hostname TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, q := range queries {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
