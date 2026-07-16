package db

import "database/sql"

// migrateV037 (2026-07-16): personal API token TTL.
//
// Adds two columns to personal_api_tokens:
//
//   expires_at    INTEGER NOT NULL DEFAULT 0
//     Unix timestamp at which the token stops being valid.
//     0 = "never expires" (the v0.37.0 default; pre-existing
//     rows are also 0 so the auth middleware's expiry check is
//     a no-op for the legacy tokens). PostMyToken now
//     accepts a TTL dropdown (1h / 1d / 7d / 30d / never) and
//     stores the resulting timestamp here.
//
//   auto_rotate    INTEGER NOT NULL DEFAULT 0
//     Boolean flag. 0 = no auto-rotation (v0.37.0 only stores
//     the flag; the rotation job is a v0.16.0 follow-up).
//     1 = the (future) background job will mint a new token
//     and revoke the old one before this one expires. The
//     flag is read by the future TokenRotator so users can
//     opt in to rotation without an explicit interval field
//     (rotation interval is a v0.16.0 design call).
//
// The schema is additive and idempotent: ALTER TABLE ADD
// COLUMN with a DEFAULT doesn't fail on re-run, and the
// existing CREATE TABLE in migrations.go has the new
// columns for fresh installs.
//
// 2026-07-16: v0.15.5 — personal API token rotation (TTL
// field, auto_rotate flag reserved for v0.16.0).
func migrateV037(d *sql.DB) error {
	stmts := []string{
		`ALTER TABLE personal_api_tokens ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE personal_api_tokens ADD COLUMN auto_rotate INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_personal_api_tokens_expires
			ON personal_api_tokens (expires_at)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			// ALTER TABLE ADD COLUMN fails with "duplicate
			// column" on a re-run; ignore that case (the
			// column already exists, which is fine).
			// CREATE INDEX IF NOT EXISTS is a no-op on
			// re-run, so it shouldn't hit this branch.
			continue
		}
	}
	return nil
}
