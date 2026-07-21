// v0.22.0 — mesh (shared network) tables.
//
// The v0.21.0 invite feature lets user A bridge to user B
// via a one-shot code. The v0.22.0 mesh feature generalizes
// that: a mesh is a NAMED group of users whose personal
// subnets are all mutually visible to each other. Like
// radmin VPN's "shared network" — A creates a mesh, B and C
// join, and after that A↔B, A↔C, B↔C subnets are all
// reachable within the mesh.
//
// The v0.17.1 one-directional share is the underlying
// primitive: each membership (mesh_id, user_id) becomes
// the equivalent of N one-directional shares (user M_i
// → user M_j for every pair of members). The ACL builder
// (GenerateACLForPlane) reads mesh membership at render
// time and extends the per-user dst list with the CIDRs
// of all other mesh members — same shape as the v0.17.1
// share rows, just generated automatically.
//
// Schema:
//
//   meshes
//     (id INTEGER PRIMARY KEY,
//      code TEXT UNIQUE NOT NULL,           -- 8-char alphanumeric
//                                            -- (same alphabet as invite_codes)
//      name TEXT NOT NULL,                  -- human-readable
//                                            -- ("office-net", "lab")
//      creator_user_id INTEGER NOT NULL,    -- FK portal_users
//      status TEXT NOT NULL DEFAULT 'active',
//                                            -- active | dissolved
//      created_at INTEGER NOT NULL,
//      dissolved_at INTEGER NOT NULL DEFAULT 0,
//      FOREIGN KEY (creator_user_id)
//        REFERENCES portal_users(id) ON DELETE CASCADE)
//
//   mesh_members
//     (mesh_id INTEGER NOT NULL,
//      user_id INTEGER NOT NULL,
//      joined_at INTEGER NOT NULL DEFAULT 0,
//      PRIMARY KEY (mesh_id, user_id),
//      FOREIGN KEY (mesh_id) REFERENCES meshes(id) ON DELETE CASCADE,
//      FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE)
//
// Why "code" on meshes (not just "name")?
// Users join a mesh by code (one-shot DM, like invite
// codes). The name is a human-readable label the creator
// picks ("office-net"); the code is the auth token a
// joiner types in /mesh join. Same pattern as invite_codes:
// name is metadata, code is the secret.
//
// Why "status" + "dissolved_at" instead of DELETE?
// Dissolving a mesh is a destructive operation that
// affects everyone's ACL. We want the row to stick
// around (with status='dissolved' + dissolved_at=now)
// for audit + for the admin /admin/meshes view (which
// shows the lifetime of every mesh). DELETE would lose
// the audit trail.
//
// Why a separate mesh_members table (not a JSON column
// on meshes)?
// The ACL builder needs a JOINable structure to look up
// "all members of mesh X". A JSON column would force a
// per-mesh JSON parse in the hot path (one per member per
// ACL render). A separate table with the (mesh_id, user_id)
// PK is one indexed lookup per membership, which scales
// linearly with the number of meshes × members — fine for
// the expected volume (low hundreds over the deployment
// lifetime).

package db

import "database/sql"

// migrationV043 — v0.22.0 meshes + mesh_members.
//
// Idempotent: re-runs are no-ops (CREATE TABLE IF NOT
// EXISTS, CREATE INDEX IF NOT EXISTS).
func migrationV043(d *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS meshes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT '',
			creator_user_id INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at INTEGER NOT NULL DEFAULT 0,
			dissolved_at INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY (creator_user_id) REFERENCES portal_users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS mesh_members (
			mesh_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			joined_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (mesh_id, user_id),
			FOREIGN KEY (mesh_id) REFERENCES meshes(id) ON DELETE CASCADE,
			FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
		)`,
		// Lookups:
		//   - "which meshes does user U belong to" → idx_mesh_members_user
		//   - "which users are in mesh M" → idx_mesh_members_mesh (PK already covers)
		//   - "find a mesh by its code" → UNIQUE on meshes.code (PK-like)
		`CREATE INDEX IF NOT EXISTS idx_mesh_members_user
			ON mesh_members (user_id)`,
	}
	for _, s := range stmts {
		if _, err := d.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
