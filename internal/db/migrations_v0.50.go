package db

import "database/sql"

// migrateV050 (v0.33.0, 2026-08-04): headscale_acl_rules table.
//
// Stores the rules that skygate has added to the headscale ACL
// policy via /admin/headscale/acl (the v0.33.0 Network Access
// Manager). Each row corresponds to one entry in policy.acls.
//
// Why we need this table:
//
//   The current headscale policy may contain rules that the
//   operator added OUTSIDE skygate (via headscale CLI, headplane
//   UI, or direct DB edit). When the operator adds a rule via
//   skygate, we must NOT clobber their manual edits — we
//   read the current policy, append the new rule, write back,
//   and remember which rules are ours so we can update or
//   remove them later.
//
//   Without a persistent record, after a restart skygate would
//   have no way to know which rules it added vs which were
//   external — and an attempted "remove" might accidentally
//   delete a manually-authored rule.
//
// Schema (v0.33.0):
//
//   id                   TEXT PK    skygate-{nanoid}    stable ID for audit
//   rule_json            TEXT       full headscale rule object
//   label                TEXT       human description (audit + UI)
//   created_at           INTEGER    strftime('%s','now') at insert
//   created_by_user_id   INTEGER    FK portal_users.id
//   enabled              INTEGER    1=active in policy, 0=disabled
//
// The `enabled` flag allows "soft delete": when the operator
// removes a rule, we flip enabled=0 but keep the row for
// audit. The policy write only includes enabled=1 rules.
//
// We also store a SHA-256 of the rule_json (`fingerprint`)
// to enable O(1) deduplication: before adding a rule, check
// if an existing skygate rule has the same fingerprint.
//
// 2026-08-04: v0.33.0 — Network Access Manager.
func migrateV050(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS headscale_acl_rules (
			id                  TEXT    PRIMARY KEY,
			rule_json           TEXT    NOT NULL,
			fingerprint         TEXT    NOT NULL,
			label               TEXT    NOT NULL DEFAULT '',
			created_at          INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			created_by_user_id  INTEGER NOT NULL DEFAULT 0,
			enabled             INTEGER NOT NULL DEFAULT 1,
			UNIQUE(fingerprint, enabled)
		)`,
		// Index for the "list enabled rules" hot path.
		`CREATE INDEX IF NOT EXISTS idx_headscale_acl_enabled
			ON headscale_acl_rules(enabled, created_at DESC)`,
		// No FK to portal_users: the row must survive even if
		// the admin user is later deleted (audit trail). The
		// `created_by_user_id` is a snapshot, not a live link.
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			return err
		}
	}
	return nil
}
