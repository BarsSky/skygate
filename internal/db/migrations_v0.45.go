// v0.28.1 — per-user preferred exit-node (and the
// build-up to per-device preferred exit-node).
//
// 2026-07-24: v0.28.0 introduced tag:dev-<user>-<device>
// so per-device rules are IP-independent. But the exit-node
// the user actually USES is still controlled at the
// headscale client level: `tailscale set --exit-node=<name>`
// picks any advertised exit-node regardless of what the
// ACL allows.
//
// headscale 0.29.0-beta.4+ (we run 0.29.2) supports `via`
// in `grants[]` — a per-grant route filter that says
// "this traffic MUST travel through one of these exit-
// nodes / subnet-routers". With `via`, the operator can
// pin workstation-3 to relay-1, workstation-1 to relay-3, etc. —
// headscale rejects packets that would take a different
// route, regardless of what the client tries to do.
//
// 2026-07-24: v0.28.1 is the first step. We:
//   1. Add a user_exit_node_prefs table — one row per
//      user, holds the user's preferred exit-node tag.
//      Set by the user on /my/exit-nodes ("Set as my
//      preferred") or by the admin on
//      /admin/users/{id}/subnet (dropdown).
//   2. Add a buildExitNodeList helper that reads the
//      current exit-nodes from headscale and emits the
//      canonical "tag:exit-<hostname>" tags. The list
//      is small (3 today: relay-1, relay-2, relay-3)
//      and is computed every GenerateACL call. If headscale
//      ever reports a node with tag:exit-node AND tag:public
//      (the canonical exit-node signature), it's added to
//      the available list.
//   3. New GenerateACLWithVia replaces the v0.28.0 policy
//      with a hybrid that adds per-user `grants[]` rules
//      with `via: ["<user's preferred exit-node tag>"]`.
//      Catch-all `* → autogroup:internet:*` stays in
//      `acls[]` as the legacy fallback (so users without
//      a preferred exit-node still get exit-node access via
//      any of the available exit-nodes).
//
// Future v0.29.x: per-device exit-node preferences extend
// user_exit_node_prefs to a (user_id, device_id, tag) row.
// The via field already supports per-device src
// (tag:dev-<user>-<device>), so the only schema change
// needed is a UNIQUE(user_id, device_id) index. Skipped
// in v0.28.1 — the per-user table covers the operator's
// current ask and the per-device variant can ship as a
// small follow-up without breaking this migration.

package db

import (
	"database/sql"
)

// ExitNodePref is one row of user_exit_node_prefs.
// One row per user (PK on user_id) — the user can have
// at most one preferred exit-node at a time. To change
// the preference, the user / admin UPDATEs the row.
//
// 2026-07-24: v0.28.1.
// 2026-07-25: v0.28.5 — added ViaEnabled. When true,
// the ACL builder emits `via=[]` in the per-user grant,
// pinning the user to ExitNodeTag. When false, the
// per-user grant has dst=autogroup:internet with NO via
// (the user can use any exit-node — needed for older
// Tailscale clients like Android that reject via in
// the policy).
type ExitNodePref struct {
	UserID       int64
	Username     string
	ExitNodeTag  string
	UpdatedAt    int64
	SetByUserID  int64
	ViaEnabled   bool
}

// GetUserExitNodePref returns the user's preferred exit-node
// tag, or "" if the user has no preference set. Joins
// portal_users for the username so the template can render
// "<username>'s preferred exit-node: <tag>" without an
// extra round-trip.
//
// 2026-07-24: v0.28.1.
// 2026-07-25: v0.28.5 — also returns ViaEnabled so the
// caller can show the strict-pinning state in the UI.
func GetUserExitNodePref(d *sql.DB, userID int64) (ExitNodePref, error) {
	var p ExitNodePref
	var viaEnabled int
	err := d.QueryRow(`
		SELECT p.user_id, pu.username, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM user_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 WHERE p.user_id = ?`, userID,
	).Scan(&p.UserID, &p.Username, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID, &viaEnabled)
	if err == sql.ErrNoRows {
		return p, nil
	}
	if err == nil {
		p.ViaEnabled = viaEnabled != 0
	}
	return p, err
}

// SetUserExitNodePref upserts the user's preferred exit-node.
// Pass exitNodeTag = "" to clear the preference (the row is
// deleted, not set to empty — keeps the table small and
// makes "has preference?" a simple SELECT EXISTS check).
//
// setByUserID is recorded in the row so the admin can see
// "alice set this herself" vs. "admin set this for alice"
// on /admin/users/{id}/subnet.
//
// 2026-07-25: v0.28.5 — added viaEnabled param. When true,
// the ACL builder emits via=[] in the per-user grant. When
// false (the new default for fresh rows), the per-user
// grant has dst=autogroup:internet with no via (Android-
// friendly). Existing rows are migrated to via_enabled=1
// (backwards compat with v0.28.1-v0.28.4 — the operator
// has to explicitly flip to 0 to un-pin).
func SetUserExitNodePref(d *sql.DB, userID int64, exitNodeTag string, setByUserID int64, viaEnabled bool) error {
	if exitNodeTag == "" {
		_, err := d.Exec(`DELETE FROM user_exit_node_prefs WHERE user_id = ?`, userID)
		return err
	}
	viaInt := 0
	if viaEnabled {
		viaInt = 1
	}
	_, err := d.Exec(`
		INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		VALUES (?, ?, ?, strftime('%s','now'), ?)
		ON CONFLICT(user_id) DO UPDATE SET
			exit_node_tag = excluded.exit_node_tag,
			set_by_user_id = excluded.set_by_user_id,
			updated_at = excluded.updated_at,
			via_enabled = excluded.via_enabled`,
		userID, exitNodeTag, setByUserID, viaInt)
	return err
}

// ListAllUserExitNodePrefs returns every row, joined with
// portal_users for the username. Used by GenerateACLWithVia
// to build the per-user `via` set in one pass. The result
// is small (4-10 rows in production) so we don't worry
// about pagination.
//
// 2026-07-25: v0.28.5 — also returns ViaEnabled. The ACL
// builder uses this to decide whether to emit via=[] in
// the per-user grant (via_enabled=true) or skip the via
// (via_enabled=false — the user has a "default" exit-node
// for the UI but no policy-level pinning).
func ListAllUserExitNodePrefs(d *sql.DB) ([]ExitNodePref, error) {
	rows, err := d.Query(`
		SELECT p.user_id, pu.username, p.exit_node_tag, p.updated_at, p.set_by_user_id, p.via_enabled
		  FROM user_exit_node_prefs p
		  JOIN portal_users pu ON pu.id = p.user_id
		 ORDER BY pu.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExitNodePref
	for rows.Next() {
		var p ExitNodePref
		var viaEnabled int
		if err := rows.Scan(&p.UserID, &p.Username, &p.ExitNodeTag, &p.UpdatedAt, &p.SetByUserID, &viaEnabled); err != nil {
			return nil, err
		}
		p.ViaEnabled = viaEnabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func migrateV045(d *sql.DB) error {
	// 2026-07-24: v0.28.1 — user_exit_node_prefs table.
	// One row per user (PK on user_id). Cascade on user
	// delete is intentional: a deleted user has no
	// preference. exit_node_tag is the headscale tag
	// (e.g. "tag:exit-relay-1") — NOT the hostname, NOT
	// the node id. The tag is what `grants[].via` takes.
	const q = `CREATE TABLE IF NOT EXISTS user_exit_node_prefs (
		user_id INTEGER NOT NULL PRIMARY KEY,
		exit_node_tag TEXT NOT NULL,
		set_by_user_id INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		FOREIGN KEY (user_id) REFERENCES portal_users(id) ON DELETE CASCADE
	)`
	if _, err := d.Exec(q); err != nil {
		return err
	}
	return nil
}
