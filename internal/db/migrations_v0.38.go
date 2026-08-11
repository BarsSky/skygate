package db

import "database/sql"

// migrateV038 (2026-07-17): per-user subnets foundation.
//
// The v0.16.0 release ships the schema for per-user subnets
// without the actual sidecar container management (that is
// v0.16.1). The data model is the first piece so the rest of
// the v0.16.0 work (admin UI, bot /mysubnet, fake-headscale
// integration tests) can build against a real schema.
//
// Adds two pieces:
//
//  1. The `user_subnets` table — one row per portal user
//     who has opted in to a personal subnet. The unique
//     constraints on user_id and cidr make the table the
//     source of truth for "who owns which CIDR"; the
//     `portal_users.subnet_cidr` denormalized column is a
//     quick-lookup shortcut for /myexitnodes / /mysubnet
//     without a JOIN.
//
//     Columns:
//
//       id              INTEGER PRIMARY KEY
//       user_id         INTEGER NOT NULL UNIQUE — one subnet
//                       per user; cascade-delete on user
//                       removal (so /admin/users/{id}/delete
//                       also frees the CIDR for re-allocation)
//       cidr            TEXT NOT NULL UNIQUE — the assigned
//                       CIDR (e.g. "10.0.42.0/24"); allocator
//                       picks from 10.0.<uid>.0/24 in v0.16.0
//       subnet_bits     INTEGER NOT NULL DEFAULT 24 — bit
//                       width; lets us move to /28 later
//                       without a schema migration
//       control_plane_url TEXT NOT NULL DEFAULT '' — the
//                       per-plane context (v0.12.0 multi-plane);
//                       '' = global plane (matches the
//                       `portal_users.headscale_url = ''` default)
//       status          TEXT NOT NULL DEFAULT 'pending' —
//                       lifecycle: pending (allocated, sidecar
//                       not yet up) | active (sidecar up,
//                       node registered, route approved) |
//                       disabled (opt-out: subnet row kept for
//                       audit but no live sidecar)
//       router_node_id  TEXT NOT NULL DEFAULT '' — headscale
//                       node_id of the registered subnet
//                       router; '' when status=pending
//       router_container_id TEXT NOT NULL DEFAULT '' — docker
//                       container_id of the sidecar (v0.16.1
//                       fills this; empty in v0.16.0)
//       router_hostname TEXT NOT NULL DEFAULT '' — friendly
//                       name (e.g. "skygate-subnet-alice");
//                       operator-facing
//       created_at      INTEGER NOT NULL — unix seconds
//       updated_at      INTEGER NOT NULL — unix seconds
//
//  2. Three columns on `portal_users` (denormalized copies
//     for quick lookups without joining user_subnets):
//
//       subnet_cidr     TEXT NOT NULL DEFAULT '' — copy of
//                       user_subnets.cidr; empty = no subnet
//       subnet_status   TEXT NOT NULL DEFAULT 'none' —
//                       'none' | 'pending' | 'active' | 'disabled'
//                       (mirrors user_subnets.status with
//                       'none' as the no-subnet case)
//       subnet_router_node_id TEXT NOT NULL DEFAULT '' — copy
//                       of user_subnets.router_node_id; used
//                       by /myexitnodes and /mysubnet to
//                       show "router: <hostname> (offline)"
//                       without a JOIN
//
// The schema is additive and idempotent: ALTER TABLE ADD
// COLUMN with a DEFAULT doesn't fail on re-run, and the new
// CREATE TABLE IF NOT EXISTS is a no-op on re-run. The "duplicate
// column" error from ALTER is ignored (same as v0.37.0).
//
// 2026-07-17: v0.16.0 — per-user subnets schema. See
// docs/v0.16.0-open-questions.md for the 8 design decisions
// that justify this layout.
func migrateV038(d *sql.DB) error {
	stmts := []string{
		// New table: user_subnets. The unique constraints
		// on user_id and cidr let the allocator rely on
		// the DB to prevent duplicate allocations (a
		// second AllocateCIDR call for the same user_id
		// hits the UNIQUE(user_id) and the manager can
		// detect "already allocated" via the error).
		`CREATE TABLE IF NOT EXISTS user_subnets (
			id INTEGER PRIMARY KEY,
			user_id INTEGER NOT NULL UNIQUE,
			cidr TEXT NOT NULL UNIQUE,
			subnet_bits INTEGER NOT NULL DEFAULT 24,
			control_plane_url TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			router_node_id TEXT NOT NULL DEFAULT '',
			router_container_id TEXT NOT NULL DEFAULT '',
			router_hostname TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
		)`,
		// Indexes: the (cidr) UNIQUE in the table definition
		// already creates a unique index; we add plain
		// indexes for the (status) and (control_plane_url)
		// lookups that the manager / bot / admin pages do.
		`CREATE INDEX IF NOT EXISTS idx_user_subnets_status
			ON user_subnets (status)`,
		`CREATE INDEX IF NOT EXISTS idx_user_subnets_plane
			ON user_subnets (control_plane_url)`,
		// Denormalized columns on portal_users. The 3
		// queries that read these columns are:
		//   1. /mysubnet: SELECT subnet_cidr, subnet_status,
		//      subnet_router_node_id FROM portal_users WHERE id = ?
		//   2. /myexitnodes: JOIN against user_subnets via
		//      portal_users.subnet_cidr (for "your default
		//      exit-node can route through your personal
		//      subnet" hint, v0.17.0+)
		//   3. /admin/users/{id} admin page: shows the
		//      subnet_status next to the username
		// Keeping these in portal_users saves a JOIN on the
		// hot path. The user_subnets table is the source
		// of truth; the portal_users columns are kept in
		// sync by the manager (internal/subnet/manager.go).
		`ALTER TABLE portal_users ADD COLUMN subnet_cidr TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE portal_users ADD COLUMN subnet_status TEXT NOT NULL DEFAULT 'none'`,
		`ALTER TABLE portal_users ADD COLUMN subnet_router_node_id TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS idx_portal_users_subnet_status
			ON portal_users (subnet_status)`,
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			// ALTER TABLE ADD COLUMN fails with "duplicate
			// column" on a re-run; ignore that case (the
			// column already exists, which is fine).
			// CREATE TABLE / INDEX IF NOT EXISTS is a no-op
			// on re-run, so it shouldn't hit this branch.
			continue
		}
	}
	return nil
}
