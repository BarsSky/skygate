// File: internal/db/migrations_v0_72_admin_primary.go
//
// v0.72 (B264) — portal_users.is_primary: the immutable primary admin.
//
// B264 lets an administrator grant AND revoke the `admin` role for
// other portal users from /admin/users, while the primary
// (bootstrap/root) admin account stays immutable — it can never be
// demoted, deleted or renamed.
//
// Before V072 the only role column was `portal_users.is_admin`, and the
// admin-user-sync contract (scripts/check_b_admin_user_sync.sh,
// contract A) asserted "exactly ONE portal_users row with is_admin=1".
// That invariant is what made admin delegation impossible: promoting a
// second user would have failed the contract, and there was no way to
// tell "the canonical admin" apart from "an admin a colleague granted".
//
// V072 introduces the marker that separates the two:
//
//   - `is_primary INTEGER NOT NULL DEFAULT 0` — at most ONE row has
//     is_primary=1. That row is the bootstrap admin named by
//     SKYGATE_ADMIN_USER and is immutable from the UI.
//   - A partial UNIQUE index (`WHERE is_primary = 1`) makes "at most
//     one primary" a database-enforced invariant instead of a
//     convention. Partial indexes are supported by PostgreSQL and by
//     SQLite 3.8+, so both chains get the same guarantee.
//
// Backfill semantics (the interesting part)
// -----------------------------------------
// The migration is dialect-split because it needs bound parameters and
// dialect-specific idempotency helpers:
//
//  1. Add the column (IF NOT EXISTS on PG; addColumnIfMissingSQLite via
//     execSQLiteDDL on SQLite).
//  2. Mark the row whose username equals the configured bootstrap admin
//     as primary. The configured name is read from SKYGATE_ADMIN_USER
//     (default "admin") — the migration table has no access to
//     internal/config, and this mirrors the default in
//     config.getenv("SKYGATE_ADMIN_USER", "admin"). The row is also
//     forced to is_admin=1, because "primary" and "admin" are the same
//     account by definition and the drift banner (users_sync_banner.go)
//     already treats that username as should-be-admin.
//  3. FALLBACK: if step 2 matched nothing — a deployment whose
//     SKYGATE_ADMIN_USER was changed, or whose portal row was renamed
//     out of band — mark the LOWEST-ID is_admin=1 row as primary. That
//     preserves the pre-V072 "the canonical admin is the original
//     bootstrap row" semantics, and guarantees the
//     "exactly one primary whenever any admin exists" invariant that
//     the renegotiated contract A asserts. On a deployment with no
//     admins at all the migration leaves is_primary=0 everywhere;
//     bootstrapAdmin (cmd/skygate/main.go) sets it when the admin row
//     is created, and re-asserts it on every later boot that has
//     SKYGATE_ADMIN_PASS set.
//  4. Defensive dedupe: keep only the lowest-id primary row if a
//     hand-edited database already has several. Without this the
//     UNIQUE index creation below would abort the whole chain.
//  5. Create the partial UNIQUE index last, so the invariants it
//     enforces already hold.
//
// Idempotency: every statement is a no-op on a second run (column
// exists, UPDATE sets the same value, index IF NOT EXISTS).
package db

import (
	"database/sql"
	"os"
	"strings"
)

// portalUsersOnePrimaryIndex is the partial UNIQUE index that enforces
// "at most one primary admin". Both dialects support the syntax; the
// SQLite chain also runs it through execSQLiteDDL (which passes
// non-ADD-COLUMN statements straight through and returns real errors).
const portalUsersOnePrimaryIndex = `CREATE UNIQUE INDEX IF NOT EXISTS portal_users_one_primary_uniq ON portal_users(is_primary) WHERE is_primary = 1`

// portalPrimaryBackfillUsername returns the bootstrap admin username the
// V072 backfill looks for: SKYGATE_ADMIN_USER, or "admin" when unset.
//
// Deliberately duplicated from internal/config (getenv("SKYGATE_ADMIN_USER",
// "admin")): the migration chain is registered as plain func(d *sql.DB)
// error values and has no access to the loaded config. Keeping the
// default in sync with internal/config/config.go is the only coupling.
func portalPrimaryBackfillUsername() string {
	name := strings.TrimSpace(os.Getenv("SKYGATE_ADMIN_USER"))
	if name == "" {
		return "admin"
	}
	return name
}

func migrateV072PG(d *sql.DB) error {
	// 1. Column.
	if _, err := d.Exec(
		`ALTER TABLE portal_users ADD COLUMN IF NOT EXISTS is_primary INTEGER NOT NULL DEFAULT 0`,
	); err != nil {
		return err
	}
	// 2. Username-match backfill.
	if _, err := d.Exec(
		`UPDATE portal_users SET is_primary = 1, is_admin = 1 WHERE username = $1 AND is_primary = 0`,
		portalPrimaryBackfillUsername(),
	); err != nil {
		return err
	}
	// 3. Fallback: lowest-id admin becomes primary when nothing matched.
	var primaries int
	if err := d.QueryRow(`SELECT COUNT(*) FROM portal_users WHERE is_primary = 1`).Scan(&primaries); err != nil {
		return err
	}
	if primaries == 0 {
		if _, err := d.Exec(
			`UPDATE portal_users SET is_primary = 1 WHERE is_primary = 0 AND id = (SELECT MIN(id) FROM portal_users WHERE is_admin = 1)`,
		); err != nil {
			return err
		}
	}
	// 4. Dedupe (only reachable on a hand-edited database).
	if _, err := d.Exec(
		`UPDATE portal_users SET is_primary = 0 WHERE is_primary = 1 AND id <> (SELECT MIN(id) FROM portal_users WHERE is_primary = 1)`,
	); err != nil {
		return err
	}
	// 5. Partial UNIQUE index, last.
	if _, err := d.Exec(portalUsersOnePrimaryIndex); err != nil {
		return err
	}
	return nil
}

func migrateV072SQLite(d *sql.DB) error {
	// 1. Column — MUST go through the SQLite DDL chokepoint: SQLite has
	// no `ADD COLUMN IF NOT EXISTS`, and execSQLiteDDL routes ADD COLUMN
	// through addColumnIfMissingSQLite (PRAGMA table_info first).
	if err := execSQLiteDDL(d, []string{
		`ALTER TABLE portal_users ADD COLUMN IF NOT EXISTS is_primary INTEGER NOT NULL DEFAULT 0`,
	}); err != nil {
		return err
	}
	// 2. Username-match backfill. modernc.org/sqlite binds `$N` by Go
	// argument ordinal, but this function is SQLite-only, so `?` is the
	// unambiguous spelling.
	if _, err := d.Exec(
		`UPDATE portal_users SET is_primary = 1, is_admin = 1 WHERE username = ? AND is_primary = 0`,
		portalPrimaryBackfillUsername(),
	); err != nil {
		return err
	}
	// 3. Fallback: lowest-id admin becomes primary when nothing matched.
	var primaries int
	if err := d.QueryRow(`SELECT COUNT(*) FROM portal_users WHERE is_primary = 1`).Scan(&primaries); err != nil {
		return err
	}
	if primaries == 0 {
		if _, err := d.Exec(
			`UPDATE portal_users SET is_primary = 1 WHERE is_primary = 0 AND id = (SELECT MIN(id) FROM portal_users WHERE is_admin = 1)`,
		); err != nil {
			return err
		}
	}
	// 4. Dedupe (only reachable on a hand-edited database).
	if _, err := d.Exec(
		`UPDATE portal_users SET is_primary = 0 WHERE is_primary = 1 AND id <> (SELECT MIN(id) FROM portal_users WHERE is_primary = 1)`,
	); err != nil {
		return err
	}
	// 5. Partial UNIQUE index, last.
	if err := execSQLiteDDL(d, []string{portalUsersOnePrimaryIndex}); err != nil {
		return err
	}
	return nil
}
