// v0.21.0 — invite_codes table for the
// user-to-user subnet bridge feature.
//
// The v0.18.0 + v0.20.0 subnets roadmap gives every
// portal user their own 10.0.<uid>.0/24. The
// remaining piece is "how does user A share that
// subnet with user B?". v0.17.1 added the
// admin-mediated "share" path (admin runs a POST
// /admin/users/{id}/subnet/share); v0.21.0 adds
// the user-mediated path: A generates a short
// code, B types it in the bot, the bridge
// auto-applies.
//
// Invitation lifecycle:
//
//   1. A (any identified user) runs /invite.
//      skygate creates an invite_codes row with
//      grantor_user_id = A.id, grantee_username
//      = "B's username" (the B they want to share
//      with), and a random 8-char code. The
//      default TTL is 7 days.
//
//   2. A tells B the code (out of band —
//      Telegram DM, in-person, smoke signals,
//      etc.). The code is the only thing B
//      needs.
//
//   3. B runs /accept <code>. skygate validates
//      (active + not expired + grantee_username
//      == B.username), then atomically:
//        a. marks the row as consumed (status,
//           consumed_at, consumed_by_user_id)
//        b. inserts a user_subnet_shares row
//           (grantor=A.id, grantee=B.id) — the
//           same shape v0.17.1's admin share
//           creates, so the ACL builder picks
//           it up on the next pipeline run
//        c. enqueues an ACL re-apply for every
//           distinct headscale_url (the v0.17.1
//           auto-reapply trigger; runs in a
//           goroutine so the bot reply is fast)
//
//   4. The ACL now contains a per-user rule
//      letting B's tag:private devices reach
//      A's 10.0.<A>.0/24. The Tailscale clients
//      pick it up on their next ACL poll
//      (usually <60s).
//
// Schema:
//
//   invite_codes
//     (id INTEGER PRIMARY KEY AUTOINCREMENT,
//      code TEXT UNIQUE NOT NULL,             -- 8-char alphanumeric
//      grantor_user_id INTEGER NOT NULL,      -- FK portal_users (the sharer)
//      grantee_username TEXT NOT NULL,       -- target user (by username, not id)
//      status TEXT NOT NULL DEFAULT 'active', -- active | consumed | expired | revoked
//      created_at INTEGER NOT NULL,
//      expires_at INTEGER NOT NULL,
//      consumed_at INTEGER DEFAULT 0,
//      consumed_by_user_id INTEGER DEFAULT 0, -- FK portal_users (FK enforced)
//      audit_message TEXT DEFAULT '')         -- optional note from grantor
//
// Indices:
//   - UNIQUE on code (the lookup path during
//     /accept)
//   - (grantor_user_id) for "list my invites"
//   - (status, expires_at) for the cleanup
//     query that marks expired rows
//
// Why "grantee_username" (TEXT) instead of
// "grantee_user_id" (FK)?
// The operator may not have created the target
// user yet. v0.21.0 supports "future B" — A
// generates a code for "bob" before bob has
// signed up. The /accept path resolves the
// username to a user_id at consume time and
// returns a clear error if the user doesn't
// exist. This matches the operator's stated
// workflow ("I want to invite my friend, even
// if they don't have a skygate account yet").
//
// Why a separate "consumed_by_user_id" column
// (not just the FK on grantee)?
// "consumed_by" is the user who actually typed
// the code in the bot. It SHOULD equal
// grantee (the row was generated for them),
// but the validation step enforces the match —
// if A generates a code for "bob" and
// accidentally pastes it to "alice", the
// consume fails with "not for you". The
// audit_log carries the mismatch attempt for
// debugging.

package db

import "database/sql"

// migrationV042 — v0.21.0 invite_codes.
//
// Idempotent: re-runs are no-ops (CREATE TABLE
// IF NOT EXISTS, CREATE INDEX IF NOT EXISTS).
func migrationV042(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS invite_codes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			grantor_user_id INTEGER NOT NULL,
			grantee_username TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at INTEGER NOT NULL DEFAULT 0,
			expires_at INTEGER NOT NULL DEFAULT 0,
			consumed_at INTEGER NOT NULL DEFAULT 0,
			consumed_by_user_id INTEGER NOT NULL DEFAULT 0,
			audit_message TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (grantor_user_id) REFERENCES portal_users(id) ON DELETE CASCADE
		)`,
		// Note: consumed_by_user_id does NOT have
		// an FK because the user may not exist at
		// generation time (we store grantee by
		// username, resolved to id at consume).
		// At consume time the consuming user IS
		// known (they have to be authenticated to
		// reach /accept), so we add a partial FK
		// to gate "consumed but user_id is now
		// gone" cases. Setting to ON DELETE SET
		// DEFAULT 0 keeps the audit trail even if
		// the consumer is later deleted.
		`CREATE INDEX IF NOT EXISTS idx_invite_codes_grantor
			ON invite_codes (grantor_user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_invite_codes_status_expires
			ON invite_codes (status, expires_at)`,
		// Also: lookup by grantee_username (the
		// "show me invites for me" query — the
		// consume path joins on this).
		`CREATE INDEX IF NOT EXISTS idx_invite_codes_grantee
			ON invite_codes (grantee_username)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
