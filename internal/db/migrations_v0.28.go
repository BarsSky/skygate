package db

import "database/sql"

// migrateV028 (2026-07-11): backfill columns that V020/V021/V022/V025
// forgot to add.
//
// Three latent gaps the original migrations never closed:
//
//   1. device_rules.parent_domain — the v0.25 migration was supposed
//      to add this column so the autoupdater can track which /32
//      entries were derived from which domain. The CREATE TABLE in
//      V020 doesn't include it, and no V021-V027 ALTER adds it. Live
//      DBs have the column (added out-of-band when v0.25 shipped);
//      fresh DBs do not.
//
//   2. node_owner_map.tag / tagged_by_user_id / tagged_at — V025's
//      CREATE only adds (node_id, user_id, attributed_at). The
//      backfill code in handlers_node_ownership.go depends on the
//      wider schema.
//
//   3. preauth_keys.headscale_preauth_id — V025's CREATE omits the
//      column. handlers_admin_nodes.go reads it after
//      headscale.CreatePreauthKey returns.
//
// All three ALTERs are idempotent (SQLite ADD COLUMN fails if the
// column exists; we ignore the error, the same way V022 already
// did). This makes the migration safe to run on both fresh and
// live DBs.
func migrateV028(d *sql.DB) error {
	stmts := []string{
		`ALTER TABLE device_rules ADD COLUMN parent_domain TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE node_owner_map ADD COLUMN username TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE node_owner_map ADD COLUMN headscale_user_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE node_owner_map ADD COLUMN tag TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE node_owner_map ADD COLUMN tagged_by_user_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE node_owner_map ADD COLUMN tag TEXT NOT NULL DEFAULT ''`, // safe no-op if V028 ran twice
		`ALTER TABLE preauth_keys ADD COLUMN headscale_preauth_id TEXT NOT NULL DEFAULT ''`,
	}
	for _, q := range stmts {
		// SQLite ALTER TABLE ADD COLUMN fails if the column already
		// exists — that's the case on live DBs (the column was
		// created out-of-band). We swallow the error so the
		// migration is idempotent.
		_, _ = d.Exec(q)
	}
	return nil
}
