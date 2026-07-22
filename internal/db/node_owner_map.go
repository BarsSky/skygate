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
//
// 2026-07-14: Этап 14 v10 — added Hostname. The bot's
// /my_nodes output now prefers "hostname (node_id) [tag]" over
// the bare node_id, so users recognise their device by the name
// they set in `tailscale up --hostname=` (or via the
// Headplane node-edit UI). Hostname is backfilled from headscale
// during backfillNodeOwnership (see handlers_node_ownership.go)
// and is optional — empty string means "not yet looked up" and
// the bot falls back to the raw node_id.
type NodeOwner struct {
	NodeID          string
	HeadscaleUserID int64
	Username        string
	Tag             string
	TaggedByUserID  int64
	TaggedAt        int64
	Hostname        string
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
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0),
		        COALESCE(hostname, '')
		   FROM node_owner_map
		  WHERE node_id = $1`, nodeID,
	).Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt, &n.Hostname)
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
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0),
		        COALESCE(hostname, '')
		   FROM node_owner_map
		  WHERE username = $1
		  ORDER BY tag, node_id`, username,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeOwner{}
	for rows.Next() {
		var n NodeOwner
		if err := rows.Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt, &n.Hostname); err != nil {
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
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0),
		        COALESCE(hostname, '')
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
		if err := rows.Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt, &n.Hostname); err != nil {
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
		        COALESCE(tag, ''), COALESCE(tagged_by_user_id, 0), COALESCE(tagged_at, 0),
		        COALESCE(hostname, '')
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
		if err := rows.Scan(&n.NodeID, &n.HeadscaleUserID, &n.Username, &n.Tag, &n.TaggedByUserID, &n.TaggedAt, &n.Hostname); err != nil {
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
	// Production schema for node_owner_map (live DBs):
	//   node_id INTEGER PRIMARY KEY,
	//   headscale_user_id INTEGER NOT NULL,
	//   username TEXT NOT NULL,
	//   tag TEXT NOT NULL,
	//   tagged_by_user_id INTEGER,    -- nullable, no DEFAULT
	//   tagged_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
	// No `user_id` column. The v0.25 test schema added one
	// (NOT NULL no DEFAULT) which is why an earlier draft
	// of this query tried to back-fill it; production
	// doesn't have the column, so the simpler query is
	// what actually runs on real installs.
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
		`INSERT INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (node_id) DO NOTHING`,
		nodeID, headscaleUserID, username, tag, taggedByUserID,
	)
	return err
}

// InsertIgnoreNodeOwnerWithHostname is the same as
// InsertIgnoreNodeOwner but also stores the headscale hostname
// (the friendly `tailscale up --hostname=...` name). Used by
// the lazy backfill so the bot's /my_nodes can show
// "hostname (node_id) [tag]" instead of the bare node_id.
//
// 2026-07-14: Этап 14 v10.
func InsertIgnoreNodeOwnerWithHostname(d dbExec, nodeID string, headscaleUserID int64, username, tag, hostname string, taggedByUserID int64) error {
	_, err := d.Exec(
		`INSERT INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id, hostname)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (node_id) DO NOTHING`,
		nodeID, headscaleUserID, username, tag, taggedByUserID, hostname,
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
		    SET tag = $1, tagged_by_user_id = $2, tagged_at = strftime('%s','now')
		  WHERE node_id = $3 AND (tag = '' OR tag = 'tag:untagged')`,
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

// UpdateNodeOwnerTag sets a new tag for an existing row, keyed by
// node_id. The (username, headscale_user_id, hostname) columns
// are preserved — only tag and tagged_by_user_id are changed.
//
// 2026-07-15: Этап 14 v13 (v0.10.13) — fixes the v0.10.11
// regression where admin-tagged devices kept tag:untagged in
// node_owner_map (PostAdminNodeTag's old guard skipped the
// update when origUserName was "tagged-devices"). The bot now
// self-heals on read via SyncTagsFromHeadscale; this helper is
// the source-of-truth fix so the row never gets stale in the
// first place.
//
// Returns ErrNodeOwnerNotFound if no row exists for nodeID —
// the caller (PostAdminNodeTag) is fine to ignore this; the
// bot-side SyncTagsFromHeadscale will pick up the new tag on
// the next read.
func UpdateNodeOwnerTag(d *sql.DB, nodeID, tag string, taggedByUserID int64) error {
	res, err := d.Exec(
		`UPDATE node_owner_map
		    SET tag = $1, tagged_by_user_id = $2, tagged_at = strftime('%s','now')
		  WHERE node_id = $3`,
		tag, taggedByUserID, nodeID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNodeOwnerNotFound
	}
	return nil
}

// DeleteNodeOwnersByUser removes EVERY row whose username matches.
// Called by the admin user-delete cascade
// (handlers_admin_users.go:PostAdminUserDelete) so a deleted user
// doesn't leave orphan rows that future /my/devices backfills
// would resurrect via the temporal fallback.
func DeleteNodeOwnersByUser(d dbExec, username string) error {
	_, err := d.Exec(`DELETE FROM node_owner_map WHERE username = $1`, username)
	return err
}

// BackfillEmptyHostnames updates node_owner_map.hostname for every
// row whose hostname is currently the empty string AND whose
// node_id appears as a key in the map. Rows with a non-empty
// hostname are left alone (we never overwrite a known value —
// the headscale copy could have drifted and we trust the most
// recent successful backfill).
//
// 2026-07-15: Этап 14 v13 — added to power lazy backfill in the
// bot's /my_nodes and /nodes commands. The migration v0.34 added
// the hostname column, but the operator-driven backfill only runs
// from /admin/devices — if a user opened the bot before visiting
// /admin/devices, every row in node_owner_map had an empty
// hostname and the bot's "hostname (node_id)" formatting silently
// fell back to the bare node_id (see screenshot in the v0.10.12
// release notes). Now the bot's read paths self-heal: on the
// first /my_nodes or /nodes after startup, if any visible row
// has an empty hostname, we call ListAllNodes, build the map,
// and call this helper.
//
// Returns the number of rows updated, for tests and operator
// visibility. An error from Exec is returned as-is; the caller
// is expected to log it but not fail the bot reply (the user
// still gets the read they asked for, just with bare node_ids).
func BackfillEmptyHostnames(d *sql.DB, hostnameByNodeID map[string]string) (int, error) {
	if len(hostnameByNodeID) == 0 {
		return 0, nil
	}
	var updated int
	for nodeID, hn := range hostnameByNodeID {
		if hn == "" {
			continue
		}
		res, err := d.Exec(
			`UPDATE node_owner_map
			    SET hostname = $1
			  WHERE node_id = $2 AND (hostname = '' OR hostname IS NULL)`,
			hn, nodeID,
		)
		if err != nil {
			return updated, err
		}
		if n, err := res.RowsAffected(); err == nil {
			updated += int(n)
		}
	}
	return updated, nil
}

// AnyHostnameEmpty reports whether any of the given NodeOwners
// has an empty Hostname. Used by the bot's read paths to decide
// whether to trigger the lazy backfill (avoids the headscale
// round-trip when nothing needs updating). O(n) over the slice;
// a few dozen rows is negligible compared to the API call it
// saves.
func AnyHostnameEmpty(owners []NodeOwner) bool {
	for _, o := range owners {
		if o.Hostname == "" {
			return true
		}
	}
	return false
}

// AnyTagStale reports whether any of the given NodeOwners has a
// tag that disagrees with the headscale-side tag (the headscale
// map is keyed by node_id; values are the live headscale tag,
// e.g. "tag:private" or "" if headscale has no tag). A row
// whose DB tag is "tag:untagged" but whose headscale tag is
// "tag:private" is stale — the admin tagged the node in
// headscale but the row didn't get updated (the old
// PostAdminNodeTag guard skipped it when origUserName was
// "tagged-devices"). The bot's /my_nodes and /nodes use this
// to trigger a lazy tag sync.
//
// "headscale has no tag" is treated as "don't touch" — the
// portal-side row is the source of truth in that case (admin
// override, or a node that's been de-tagged in headscale but
// kept in skygate). This avoids overwriting legitimate admin
// state with a headscale-side change that might be transient.
func AnyTagStale(owners []NodeOwner, headscaleTagByNodeID map[string]string) bool {
	for _, o := range owners {
		want := headscaleTagByNodeID[o.NodeID]
		if want == "" {
			continue
		}
		if want == "tag:untagged" {
			continue
		}
		if o.Tag != want {
			return true
		}
	}
	return false
}

// SyncTagsFromHeadscale updates node_owner_map.tag for every row
// whose DB tag disagrees with the given headscale map. Only the
// tag is changed; the (username, headscale_user_id) columns are
// preserved so a node whose headscale ownership link was lost
// (reassigned to the synthetic "tagged-devices" user after a
// tag was applied) keeps its original portal owner in skygate.
//
// 2026-07-15: Этап 14 v13 (v0.10.13). The bot's /my_nodes and
// /nodes call this when AnyTagStale reports drift. The first
// /my_nodes or /nodes after admin tags a device in headscale
// picks up the new tag; subsequent calls are a fast indexed
// read.
//
// Returns the number of rows updated, for tests + operator
// visibility. An error from Exec is returned as-is; the caller
// is expected to log it but not fail the bot reply (the user
// still gets the read they asked for, just with the old tags
// until the next sync).
func SyncTagsFromHeadscale(d *sql.DB, headscaleTagByNodeID map[string]string) (int, error) {
	if len(headscaleTagByNodeID) == 0 {
		return 0, nil
	}
	var updated int
	for nodeID, want := range headscaleTagByNodeID {
		if want == "" || want == "tag:untagged" {
			// Headscale has no tag → leave the portal-side
			// state alone. See AnyTagStale for the reasoning.
			continue
		}
		res, err := d.Exec(
			`UPDATE node_owner_map
			    SET tag = $1
			  WHERE node_id = $2 AND tag != $3`,
			want, nodeID, want,
		)
		if err != nil {
			return updated, err
		}
		if n, err := res.RowsAffected(); err == nil {
			updated += int(n)
		}
	}
	return updated, nil
}

// SyncNodeInfo is the input shape for SyncNodesFromHeadscale.
// We use a plain struct (not headscale.NodeView) to keep the
// db package free of the headscale import (which would create
// a cycle: headscale → db is fine, db → headscale is not).
// The admin handler translates headscale.NodeView to this
// shape before calling.
type SyncNodeInfo struct {
	ID       string
	Hostname string
	Tag      string
	Username string
	HSUserID int64
	// TaggedBy is the skygate user_id that initiated the
	// sync (0 = system sync, used by the v0.14.0 background
	// path that doesn't have a user context). The v0.14.0
	// admin button passes the clicker's user id; the bot's
	// /sync_nodes path also passes 0.
	TaggedBy int64
}

// SyncNodesFromHeadscale is a v0.14.0 full sync that INSERTs
// missing rows (in addition to updating drifted ones). The
// original SyncTagsFromHeadscale only UPDATEs — that's enough
// for the common case where the bot's /my_nodes is reading
// from node_owner_map after the operator tagged a device via
// /admin/devices (which inserts the row). It is NOT enough
// for the case where the operator tagged a relay directly via
// the headscale CLI: in that path, no row is ever written to
// node_owner_map, and the bot's /exit_nodes (which reads the
// cache) reports "no exit-nodes found" even though headscale
// is happy with three relays.
//
// SyncNodesFromHeadscale upserts each input row into
// node_owner_map. It runs as a one-off admin action (the
// "Sync from headscale" button on /admin/devices) and as the
// v0.13.0+ background monitor's per-tick complement. Returns
// the count of rows inserted (new nodes) and updated (drifted
// tags).
//
// 2026-07-15: v0.14.0 — replaces the v0.10.13 SyncTagsFromHeadscale
// partial sync for the admin-triggered path. The bot's
// /my_nodes + /nodes auto-heal path (AnyTagStale +
// SyncTagsFromHeadscale) is kept for cheap read-time
// reconciliation; this function is the full rebuild.
//
// Nodes that exist in node_owner_map but NOT in headscale
// (the operator deleted them in headscale, or the headscale
// API returned a transient empty list) are NOT touched —
// we leave them as-is for the audit log. A future v0.14.x
// could add a "prune missing nodes" mode behind a flag.
func SyncNodesFromHeadscale(d *sql.DB, nodes []SyncNodeInfo) (inserted, updated int, err error) {
	for _, n := range nodes {
		if n.Tag == "" || n.Tag == "tag:untagged" {
			// Headscale has no tag → leave the portal-side
			// state alone. (Same rule as SyncTagsFromHeadscale.)
			continue
		}
		// Distinguish insert vs update: an explicit EXISTS
		// check before the upsert. The naive "rowsAffected
		// = 1 means I changed something" pattern conflates
		// "row was new" with "tag drifted", and the upsert's
		// internal timestamp stamp means we can't use
		// tagged_at to decide afterwards either. A cheap
		// primary-key lookup is the cleanest fix.
		var existed int
		row := d.QueryRow(`SELECT COUNT(*) FROM node_owner_map WHERE node_id = $1`, n.ID)
		if serr := row.Scan(&existed); serr != nil {
			return inserted, updated, serr
		}
		// Upsert: INSERT if missing, UPDATE only the tag if present.
		// The username + headscale_user_id from the headscale view
		// are taken as the source of truth on INSERT (no portal
		// owner can exist for a row that doesn't exist); on
		// UPDATE, the existing portal-side owner is preserved
		// (a node whose headscale ownership was reassigned to
		// tagged-devices keeps its portal owner).
		//
		// No `user_id` column in this INSERT — production's
		// node_owner_map doesn't have one (see UpsertNodeOwner's
		// comment for the full schema). The
		// hostname-included-on-update clause is a v0.14.1
		// improvement so a row that had empty hostname gets
		// backfilled on the next auto-sync tick.
		host := n.Hostname
		if host == "" {
			host = n.ID
		}
		_, err := d.Exec(
			`INSERT INTO node_owner_map
				(node_id, hostname, headscale_user_id, username, tag, tagged_by_user_id, tagged_at)
			VALUES ($1, $2, $3, $4, $5, $6, strftime('%s','now'))
			ON CONFLICT(node_id) DO UPDATE SET
				tag = excluded.tag,
				hostname = excluded.hostname,
				tagged_at = strftime('%s','now')
			WHERE node_owner_map.tag != excluded.tag`,
			n.ID, host, n.HSUserID, n.Username, n.Tag, n.TaggedBy,
		)
		if err != nil {
			return inserted, updated, err
		}
		// Counters. The existence check above tells us
		// whether the row was new (existed=0) or pre-existing
		// (existed=1). The upsert may have been a no-op
		// (tag was already correct, row unchanged) — we
		// don't count that case.
		if existed == 0 {
			inserted++
		} else {
			updated++
		}
	}
	return inserted, updated, nil
}
