// Package db — node_owner_map helpers.
//
// Этап 10 part 4 (2026-07-12): moves the 17 raw SQL strings spread
// across internal/handlers/*.go, internal/telegram/commands_*.go,
// and cmd/skygate/main.go into typed helpers. Before this file, the
// SQL was duplicated in 8+ places (SELECT node_id WHERE username=?
// alone appeared in 5 files) and schema drift was a real risk —
// every column change to node_owner_map meant hunting the same
// string in 5+ places.
//
// The helpers are split by intent (read / write / upgrade) so each
// call site reads like a description of the operation rather than a
// raw SQL string. Wrapping is minimal: a typed NodeOwner struct, a
// few "rows → slice" helpers, and write functions that match the
// existing per-call-site behaviour exactly so the refactor is
// byte-for-byte equivalent at runtime.
//
// What this file does NOT do:
//   - schema changes (those live in migrations_v0.25/v0.28/v0.29)
//   - the lazy backfill logic in handlers_node_ownership.go — that
//     stays in the handlers package because it depends on the
//     headscale client. The helpers in this file are the SQL-level
//     primitives it builds on.
//   - cross-user ownership checks — those are the caller's
//     responsibility (typically handlers.MyUserCanUseDevice).

package db

import (
	"database/sql"
	"errors"
)

// ErrNodeOwnerNotFound is returned by GetNodeOwner when no row
// matches. Used by handlers that need a "yes/no" rather than a
// "count > 0" check (e.g. /admin/devices/:id/taged).
var ErrNodeOwnerNotFound = errors.New("db: node_owner_map: no row")

// NodeOwner is the typed view of one row in node_owner_map.
// node_id is the primary key (headscale's per-node id, stored as
// TEXT even when the source is numeric). tag is the headscale tag
// that headscale currently reports for the node — the
// denormalization lets /my/devices and /admin/devices read
// node→user and node→tag from a single SELECT.
type NodeOwner struct {
	NodeID         string
	HeadscaleUserID int64
	Username       string
	Tag            string
	TaggedByUserID int64
	TaggedAt       int64
}

// dbExec is the small subset of *sql.DB / *sql.Tx that every
// write helper needs. Letting helpers accept either lets callers
// stay inside a transaction (cmd/skygate/main.go runs the startup
// tag:public backfill in a tx) without forcing every call site to
// use a separate "in-tx" variant.
//
// Read helpers take *sql.DB directly because no caller currently
// needs a read in a transaction; if that ever changes they can be
// widened the same way.
type dbExec interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// GetNodeOwner returns the single row for nodeID, or
// ErrNodeOwnerNotFound. The table is keyed on node_id, so at most
// one row can match. Used by /admin/devices/:id/tag to check
// "is this node already mapped to someone?" before re-tagging.
func GetNodeOwner(d *sql.DB, nodeID string) (*NodeOwner, error) {
	var n NodeOwner
	err := d.QueryRow(
		`SELECT node_id, COALESCE(headscale_user_id, 0), COALESCE(username, ''),
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0)
		   FROM node_owner_map
		  WHERE node_id = ?`, nodeID,
	).Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNodeOwnerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// ListNodeOwnerNodeIDsByUsername returns the set of node_ids the
// named user owns. Used by /my/devices, /my/exit-rules, and the
// dashboard — all of which want a fast "is this node mine?" check
// or a list to render.
//
// Empty slice (not nil) when no rows match, so callers can
// `for _, n := range ListNodeOwnerNodeIDsByUsername(...)` without
// nil-checks.
func ListNodeOwnerNodeIDsByUsername(d *sql.DB, username string) ([]string, error) {
	rows, err := d.Query(qSelectNodeOwnerByUsername, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListNodeOwnersByUsername returns the full rows for the user
// (node_id + tag). /my/devices needs the tag to render the device
// list with the right pill, so this is the helper that powers the
// /my/devices page now (replacing the inline query that also
// joined to headscale.NodeView).
//
// Empty slice (not nil) when no rows match.
func ListNodeOwnersByUsername(d *sql.DB, username string) ([]NodeOwner, error) {
	rows, err := d.Query(
		`SELECT node_id, COALESCE(headscale_user_id, 0), COALESCE(username, ''),
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0)
		   FROM node_owner_map
		  WHERE username = ?
		  ORDER BY tag, node_id`, username,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeOwner{}
	for rows.Next() {
		var n NodeOwner
		if err := rows.Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListAllNodeOwners returns every row in node_owner_map, grouped
// by tag then username. Powers the telegram /nodes admin command
// (cross-user view). The order matches /admin/devices so an
// operator scanning the bot sees the same layout as the web UI.
func ListAllNodeOwners(d *sql.DB) ([]NodeOwner, error) {
	rows, err := d.Query(
		`SELECT node_id, COALESCE(headscale_user_id, 0), COALESCE(username, ''),
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0)
		   FROM node_owner_map
		  ORDER BY COALESCE(tag, ''), COALESCE(username, ''), node_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeOwner{}
	for rows.Next() {
		var n NodeOwner
		if err := rows.Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListExitNodeOwners returns only the rows tagged 'tag:exit-node'.
// The original query (telegram/commands_phase3.go) LEFT JOINed
// devices for last_seen/online — that's a presentation concern,
// not a row-shape one, so the join stays in the caller. The
// helper just gives the rows.
func ListExitNodeOwners(d *sql.DB) ([]NodeOwner, error) {
	rows, err := d.Query(
		`SELECT node_id, COALESCE(headscale_user_id, 0), COALESCE(username, ''),
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0)
		   FROM node_owner_map
		  WHERE tag = 'tag:exit-node'
		  ORDER BY COALESCE(username, ''), node_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeOwner{}
	for rows.Next() {
		var n NodeOwner
		if err := rows.Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CountNodeOwnerByNodeUser returns the number of rows that match
// (node_id, username). 0 or 1 in practice (node_id is the PK), but
// the original code used COUNT(*) so the call-site semantics don't
// change. Used by exit_rules_form_my.go's device-ownership check
// before adding a rule.
func CountNodeOwnerByNodeUser(d *sql.DB, nodeID, username string) (int, error) {
	var n int
	err := d.QueryRow(qCountNodeOwnerByNodeUser, nodeID, username).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// UpsertNodeOwner inserts a new row or replaces an existing one for
// nodeID. Used by the admin /admin/devices/:id/taged handler when
// a node is re-tagged (admin override path). The previous row
// (if any) is dropped; the new row carries the new tag, the admin
// who tagged it, and a fresh tagged_at.
//
// We use INSERT OR REPLACE here (not INSERT OR IGNORE) because
// the admin is explicitly telling us "this node now belongs to X
// with tag Y" — silently keeping the old row would be wrong.
func UpsertNodeOwner(d dbExec, nodeID string, headscaleUserID int64, username, tag string, taggedByUserID int64) error {
	_, err := d.Exec(qInsertOrReplaceNodeOwner, nodeID, headscaleUserID, username, tag, taggedByUserID)
	return err
}

// InsertIgnoreNodeOwner inserts a row if and only if no row for
// nodeID exists yet. Used by the lazy backfill in
// handlers_node_ownership.go: when the backfill decides "this node
// is mine", we want a row to appear — but if an admin already set
// tag:public for the same node, INSERT OR IGNORE respects the
// existing row (preserves admin intent).
func InsertIgnoreNodeOwner(d dbExec, nodeID string, headscaleUserID int64, username, tag string, taggedByUserID int64) error {
	_, err := d.Exec(
		`INSERT OR IGNORE INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id)
			VALUES (?, ?, ?, ?, ?)`,
		nodeID, headscaleUserID, username, tag, taggedByUserID,
	)
	return err
}

// UpgradeStaleNodeOwnerToPrivate upgrades any row for nodeID whose
// tag is empty or 'tag:untagged' to the given tag (in practice
// 'tag:private'). This is the second half of the backfill
// INSERT+UPDATE pattern added in commit b9e9a60 to fix the
// SKYWORKER disappearance: the INSERT puts the row in if missing,
// the UPDATE bumps any stale empty/untagged row to the new tag.
//
// The WHERE clause is intentionally narrow: rows with tag:public
// or tag:exit-node (set by an admin) are NOT touched. The point
// is to upgrade OUR old "tag:untagged" rows, not to clobber
// admin-set tags.
func UpgradeStaleNodeOwnerToPrivate(d dbExec, nodeID, newTag string, taggedByUserID int64) error {
	_, err := d.Exec(
		`UPDATE node_owner_map
		    SET tag = ?, tagged_by_user_id = ?, tagged_at = strftime('%s','now')
		  WHERE node_id = ? AND (tag = '' OR tag = 'tag:untagged')`,
		newTag, taggedByUserID, nodeID,
	)
	return err
}

// DeleteNodeOwnerByID removes the (node_id, username) pair. Used
// by the backfill GC pass: when a node that node_owner_map claims
// the user owns no longer exists in headscale, drop the row.
func DeleteNodeOwnerByID(d dbExec, nodeID, username string) error {
	_, err := d.Exec(qDeleteNodeOwnerByID, nodeID, username)
	return err
}

// DeleteNodeOwnerByNodeTag removes the row matching
// (node_id, tag). Used by /admin/devices/:id/untag to undo a
// specific tag application. If the row had only that tag, the
// whole row is dropped; if the row has no tag column at all
// (legacy schema) the DELETE is a no-op.
//
// We use the (node_id, tag) tuple because the same node can have
// multiple rows in older versions of the schema (one per tag).
// Today's schema is keyed on node_id only, but the WHERE clause
// is still safe (matches 0 or 1 row).
func DeleteNodeOwnerByNodeTag(d dbExec, nodeID, tag string) error {
	_, err := d.Exec(qDeleteNodeOwnerByNodeTag, nodeID, tag)
	return err
}

// DeleteNodeOwnersByUser removes EVERY row whose username matches.
// Called by the admin user-delete cascade
// (handlers_admin_users.go:PostAdminUserDelete) so a deleted user
// doesn't leave orphan rows that future /my/devices backfills
// would resurrect via the temporal fallback.
func DeleteNodeOwnersByUser(d dbExec, username string) error {
	_, err := d.Exec(`DELETE FROM node_owner_map WHERE username = ?`, username)
	return err
}
