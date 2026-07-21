package db

import "database/sql"

// migrateV035 (2026-07-15): per-user headscale control plane.
//
// 2026-07-15: Этап 14 v18 (v0.12.0) — Skygate-as-shell step 2:
// pluggable headscale. The single headscale client that today
// comes from HEADSCALE_URL / HEADSCALE_API_KEY in .env is the
// only control plane Skygate knows about. v0.12.0 lets each
// portal_users row carry its own (headscale_url,
// headscale_api_key) override so the same Skygate portal can
// serve users on different control planes — a US operator with
// us-east / eu-west headscale instances, a multi-tenant shop
// with one Skygate per customer tailnet, or a migration where
// users move between control planes one at a time.
//
// Two new columns on portal_users:
//
//   headscale_url        TEXT  (default '') — non-empty
//                                  means "this user is bound
//                                  to <this> headscale instead
//                                  of the global default".
//                                  Empty = use the global
//                                  client (a.HS, the existing
//                                  behaviour, preserves
//                                  backward compat).
//
//   headscale_api_key_enc TEXT  (default '') — encrypted
//                                  headscale API key for the
//                                  override. Encrypted with
//                                  AES-GCM keyed by
//                                  SKYGATE_SECRET_KEY (see
//                                  secrets.go's
//                                  EncryptForColumn /
//                                  DecryptForColumn).
//                                  Empty = no override (use
//                                  the global HEADSCALE_API_KEY
//                                  from env).
//
// Why encrypted: a leaked skygate.db would otherwise expose
// every portal user's headscale API key in plain text, which
// is a write-capable credential on each user's control plane.
// AES-GCM with a 32-byte server-side key brings the threat
// model down to "attacker has the DB AND the key file".
//
// Why not a separate `control_planes` table with FK from
// portal_users: every user is on exactly one plane, and the
// override is rare (most users use the global default). One
// nullable (url, key) pair per row is the simplest schema
// that captures that. A future v0.13.0+ release can split
// this into a proper planes table if multiple operators ask
// for it (see "Open questions" in docs/skygate-as-shell.md).
func migrateV035(d *sql.DB) error {
	stmts := []string{
		`ALTER TABLE portal_users ADD COLUMN headscale_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE portal_users ADD COLUMN headscale_api_key_enc TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_portal_users_hs_url ON portal_users (headscale_url) WHERE headscale_url != ''`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			// Column / index already exists — that's the
			// "already migrated" path. Continue.
			continue
		}
	}
	return nil
}
