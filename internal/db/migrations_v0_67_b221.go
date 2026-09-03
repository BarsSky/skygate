// v1.5.0+ (B221) — structured target on audit_log.
//
// Phase 4.1 of docs/internal/cluster-management.md
// ("Generic audit log for all admin actions").
//
// Background
// ----------
// audit_log has been the B0-era write-once
// append-only log of every admin action since
// v0.25. The schema (id, user_id, username, action,
// detail, ip_address, created_at) captured WHAT
// happened + WHO did it, but NOT WHICH ENTITY
// was changed. The "target" was buried in the
// `detail` text field as a freeform string like
// "hostname=foo" or "invite_id=abc" — hard to
// query, hard to join, and inconsistent across
// call sites (some pass target info, some don't).
//
// In contrast, the B195 `cluster_audit` table has
// a proper `target_node_id` column — and the
// /admin/audit unified view (B207) already shows
// it for cluster events. The gap: audit_log rows
// always show target="" because the column doesn't
// exist on that table.
//
// This migration adds the missing columns so
// every audit row has a structured (type, id)
// reference. The pair mirrors the pattern already
// used in cluster_audit (target_node_id) +
// elsewhere in skygate (Telegram message entities,
// etc.) — one freeform-text id field per entity
// type, with a `target_type` discriminator the
// operator can read in the /admin/audit view.
//
// Schema after this migration
// -----------------------------
// audit_log:
//   id          BIGSERIAL PRIMARY KEY
//   user_id     INTEGER DEFAULT 0
//   username    TEXT DEFAULT ''
//   action      TEXT NOT NULL
//   detail      TEXT DEFAULT ''
//   ip_address  TEXT DEFAULT ''
//   created_at  INTEGER DEFAULT (EXTRACT(EPOCH FROM now())::bigint)
//   target_type TEXT DEFAULT ''     <-- NEW (B221)
//   target_id   TEXT DEFAULT ''     <-- NEW (B221)
//
// Backward compat
// ---------------
// Both new columns default to '' (empty string), so
// pre-existing rows get an empty target_type +
// empty target_id — visible in the /admin/audit view
// as "—" (no target) instead of the previous "all
// audit_log rows show empty target" bug. The B221
// writers (PostAdminClusterNodeAdd, etc.) populate
// both fields for new rows. Old call sites that
// haven't been migrated yet still work — the
// existing `db.AppendAuditLog(d, userID, username,
// action, detail)` signature is preserved (calls
// the new AppendAuditLogWithTarget with empty
// target), no caller is broken.
//
// Idempotency
// -----------
// ADD COLUMN IF NOT EXISTS — both columns are safe
// to add to a table that already has them (the
// migration is then a no-op). The default
// expressions in the ADD COLUMN IF NOT EXISTS
// clause must match the production schema, so
// re-running on a fresh DB after a previous
// partial run is fine.
//
// Concurrency
// -----------
// ADD COLUMN on a small audit_log table (< 100k
// rows even on a heavy-traffic skygate-staging)
// takes < 1s with a metadata lock; no long-running
// transactions are blocked. The skygate process can
// keep writing to audit_log during the migration
// (Postgres auto-acquires the ACCESS EXCLUSIVE lock
// only briefly).
package db

import (
	"database/sql"
)

// migrateV067PG — v1.5.0+ (B221) —
//   audit_log.target_type + audit_log.target_id.
//
// Idempotent: ADD COLUMN IF NOT EXISTS is a no-op
// when the column already exists. Re-running this
// migration on a DB that was bootstrapped before
// B221 (or that already had this migration applied
// via `skygate migrate up`) is safe.
func migrateV067PG(d *sql.DB) error {
	stmts := []string{
		// target_type: discriminator (e.g.
		// "cluster_node", "cluster_invite",
		// "cluster_database", "device",
		// "acl", "user"). Empty for the
		// pre-B221 rows.
		`ALTER TABLE audit_log
			ADD COLUMN IF NOT EXISTS target_type TEXT DEFAULT ''`,
		// target_id: the entity id (hostname for
		// cluster_node, invite_id for
		// cluster_invite, cluster_id for
		// cluster_database, user_id for
		// portal_users, etc.). Empty for
		// pre-B221 rows.
		`ALTER TABLE audit_log
			ADD COLUMN IF NOT EXISTS target_id TEXT DEFAULT ''`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
