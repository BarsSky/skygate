// v0.28.5 — explicit opt-in for the per-user / per-device
// `via` constraint.
//
// 2026-07-25: v0.28.3 closed the catch-all bypass by making
// `* → autogroup:internet` restricted to `tag:public`. Every
// user could now reach the internet through their per-user
// grant, with via=[<preferred>]. The per-user via is enforced
// at the headscale packet filter — it pins the user's
// exit-node choice to their preferred node.
//
// Problem: headscale's `via` is implemented in the
// Tailscale client, and older Tailscale clients (notably
// the Android client) handle `via` inconsistently. Some
// versions reject the entire policy when they see a
// grant with `via` they don't understand, blocking ALL
// traffic (not just exit-node routing) for that client.
// The operator reported on 2026-07-25 11:41 MSK:
// "на андроид устройствах в принципе теперь нет доступа
// через любой exit node надо поправить".
//
// Solution: make `via` opt-in. The default is OFF (no
// constraint) — the per-user grant still has
// `dst=autogroup:internet` so the user CAN reach the
// internet, but no `via` is emitted, so the user can pick
// any exit-node. The per-user / per-device prefs still
// exist (they're the "default exit-node" hint shown in
// the UI), but the strict pinning is opt-in via the new
// `via_enabled` flag.
//
// Schema:
//   user_exit_node_prefs.via_enabled INTEGER NOT NULL
//     DEFAULT 0
//   device_exit_node_prefs.via_enabled INTEGER NOT NULL
//     DEFAULT 0
//
// Existing rows from v0.28.1 / v0.28.4 are migrated to
// via_enabled=1 (preserves the prior "always pinned"
// behavior — the operator has to explicitly flip to
// 0 to un-pin). New rows default to 0 (the safe side).
//
// The migration runs:
//   UPDATE user_exit_node_prefs SET via_enabled = 1
//   UPDATE device_exit_node_prefs SET via_enabled = 1
// (after the column is added, with the default 0 set
// in the schema). The UPDATE is a no-op on a fresh
// install where the tables are empty, but on a live
// deploy it preserves the existing pinning.

package db

import (
	"database/sql"
)

// AddViaEnabledUserPrefSQL is the SQL ALTER for the user
// table. Exposed so the migration helper can be tested in
// isolation.
//
// 2026-07-25: v0.28.5.
const addViaEnabledUserPrefSQL = `ALTER TABLE user_exit_node_prefs ADD COLUMN via_enabled INTEGER NOT NULL DEFAULT 0`

// AddViaEnabledDevicePrefSQL is the SQL ALTER for the device
// table. Same default (0 = opt-out). Existing rows are
// backfilled to 1 by migrateV047 below.
//
// 2026-07-25: v0.28.5.
const addViaEnabledDevicePrefSQL = `ALTER TABLE device_exit_node_prefs ADD COLUMN via_enabled INTEGER NOT NULL DEFAULT 0`

func migrateV047(d *sql.DB) error {
	// ALTER TABLE — IF NOT EXISTS for the column would
	// require a more elaborate SQLite pragma (the
	// IF NOT EXISTS clause for ADD COLUMN was added
	// in SQLite 3.35.0). headscale 0.29's docker
	// image ships with SQLite 3.45+, so the clause
	// is supported. The pragma_query_only fallback
	// (try/catch) keeps the migration idempotent
	// even on older SQLite versions.
	//
	// CRITICAL: the backfill UPDATE only runs if the
	// ALTER actually created the column in THIS run
	// (i.e., this is the very first time the migration
	// runs on a deploy that previously didn't have
	// the column). Otherwise, every skygate restart
	// would clobber operator-set via_enabled=0 back
	// to 1. The "freshlyAdded" flag tracks whether
	// THIS run was the one that created the column.
	freshlyAdded := true
	if _, err := d.Exec(addViaEnabledUserPrefSQL); err != nil {
		// "duplicate column name: via_enabled" — already
		// migrated in a previous run. Don't backfill.
		if !isSQLiteDuplicateColumnError(err) {
			return err
		}
		freshlyAdded = false
	}
	if _, err := d.Exec(addViaEnabledDevicePrefSQL); err != nil {
		if !isSQLiteDuplicateColumnError(err) {
			return err
		}
		// Both columns added in this run? Set false.
		// If user ALTER was the duplicate and device
		// ALTER also was duplicate, both columns
		// pre-existed.
		if freshlyAdded {
			freshlyAdded = false
		}
	}
	// Backfill ONLY on the first-time migration. On
	// every subsequent startup, skip the UPDATE so
	// the operator's via_enabled=0 (un-pinned) is
	// preserved across restarts.
	if !freshlyAdded {
		return nil
	}
	// Backfill existing rows: every row that was
	// created under v0.28.1 / v0.28.4 had the
	// pinning enforced. Preserve that behavior on
	// upgrade (the operator has to explicitly flip
	// the flag to un-pin). The UPDATE is a no-op on
	// a fresh install (column defaults to 0).
	if _, err := d.Exec(`UPDATE user_exit_node_prefs SET via_enabled = 1 WHERE via_enabled = 0`); err != nil {
		return err
	}
	if _, err := d.Exec(`UPDATE device_exit_node_prefs SET via_enabled = 1 WHERE via_enabled = 0`); err != nil {
		return err
	}
	return nil
}

// isSQLiteDuplicateColumnError returns true iff the error
// is a SQLite "duplicate column" error. Used by
// migrateV047 to make the migration idempotent.
//
// 2026-07-25: v0.28.5.
func isSQLiteDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// SQLite error text: "duplicate column name: via_enabled"
	return contains(msg, "duplicate column name")
}

// contains is a tiny helper to avoid pulling in
// strings.Contains just for one substring check.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
