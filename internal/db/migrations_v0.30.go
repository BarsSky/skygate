package db

import "database/sql"

// migrateV030 (2026-07-13): per-user default device + default exit_node.
//
// Adds two TEXT columns to portal_users, both defaulting to ''. The
// empty string is the "no default" sentinel and matches the helper
// return convention (Get* returns "" when unset) so callers don't
// have to special-case NULL.
//
// What these columns carry:
//   default_device_node_id   — the headscale node_id the user has
//                              picked as their default for /add_rule
//                              (and any other "which device?"
//                              shortcut in the future). Stored as
//                              node_id (TEXT, the headscale primary
//                              key) rather than the integer
//                              device_rules.device_id because the
//                              telegram bot has the node_id
//                              (from /my_nodes) and not the int.
//   default_exit_node_id     — the headscale node_id of the exit-node
//                              the user has picked as their default.
//                              Same storage rationale.
//
// Why columns (not a user_prefs table): symmetry with `theme`
// (also a column on portal_users), and the data shape is fixed
// (two strings per user). A generic key/value table would be
// over-engineering for two fixed-shape prefs and would also make
// the /my/devices "set as default" UI (future work) need a
// JSON-string pivot or a per-key subquery.
//
// The migration is idempotent in the SQLite sense: the ALTER TABLE
// statements may fail with "duplicate column" on a DB that already
// has them, which is fine (the migrate() loop in db.go ignores
// errors from these statements — see the v0.21/v0.22 pattern).
func migrateV030(d *sql.DB) error {
	stmts := []string{
		`ALTER TABLE portal_users ADD COLUMN default_device_node_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE portal_users ADD COLUMN default_exit_node_id    TEXT NOT NULL DEFAULT ''`,
	}
	for _, q := range stmts {
		// ALTER TABLE ADD COLUMN returns an error if the column
		// already exists; that's the "migration already applied"
		// signal and we swallow it (same idiom as v0.21/v0.22).
		_, _ = d.Exec(q)
	}
	return nil
}
