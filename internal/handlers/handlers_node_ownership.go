package handlers

import (
	"database/sql"
	"log"
	"strconv"
	"time"

	"skygate/internal/headscale"

	dbpkg "skygate/internal/db"
)

// firstTagOrFallback returns the node's first tag, or "tag:untagged"
// if the node has no tags. Used to populate node_owner_map.tag for
// rows that come from strategies that don't otherwise carry a tag
// (specifically the temporal fallback in C, which fires for both
// tagged and untagged nodes).
//
// Moved from handlers_derp.go during Этап 8 — only used by
// backfillNodeOwnership, so it lives here.
func firstTagOrFallback(n headscale.NodeView) string {
	if len(n.Tags) > 0 {
		return n.Tags[0]
	}
	return "tag:untagged"
}

// backfillNodeOwnership walks all nodes, and for any node whose headscale
// preAuthKey matches one of this portal user's preauth_keys, inserts a row
// in node_owner_map (idempotent via INSERT OR IGNORE).
//
// Why this exists:
//   - When a user issues a preauth key via /my/devices, we save the
//     headscale ID in preauth_keys.headscale_preauth_id.
//   - When that key is later consumed by a Tailscale client, the resulting
//     node reports its origin via the headscale API (node.preAuthKey.id).
//   - If the node then gets a tag applied (e.g. tag:private by ACL),
//     headscale reassigns ownership to a synthetic "tagged-devices" user
//     and the live user_id link is lost.
//   - This backfill reconstructs the link from the persisted key, so the
//     node shows up under the original owner in /my/devices and on the
//     user dashboard. Safe to call on every /my/devices load - the IGNORE
//     makes it a no-op once the snapshot exists.
//
// Garbage collection: this function also reconciles the snapshot against
// current reality. If a node that node_owner_map claims the user owns no
// longer exists in headscale (deleted, expired, reaped), the orphan row
// is removed. Without this, a user who deletes their device would keep
// seeing it on the dashboard forever - the original symptom of the
// michail "0/0" report. The flip side is that a transient headscale API
// hiccup could drop a row; the next successful /my/devices load will
// re-backfill it from preAuthKey, so the blast radius is one page load.
//
// Two strategies, applied in order, first match wins:
//
//   A. Strict join on n.PreAuthKeyID == preauth_keys.headscale_preauth_id.
//      Works for keys whose headscale_preauth_id was captured at issue
//      time. This is the original path from v0.3.9 - fast and accurate,
//      but vulnerable to API response shape changes (a preauth key issued
//      when the response field name shifted will not have a stored
//      headscale_preauth_id, and the node will not match here).
//
//   C. Temporal fallback. If (A) failed AND the node has a non-empty
//      CreatedAt AND the user has at least one preauth key created
//      within 1 hour BEFORE the node's CreatedAt, we attribute the node
//      to that key's owner. The 1-hour window is a safety margin: a
//      user can't physically generate a preauth key, ship it to a remote
//      device, and have that device register with headscale faster
//      than that. If a key was created within the window, it's
//      effectively the only plausible cause. This recovers ownership
//      for keys whose headscale_preauth_id was never captured (the
//      michail case: 5/7 keys have NULL headscale_preauth_id because
//      the API stopped populating that field on the day they were
//      generated).
//
// Safety: BOTH strategies skip nodes whose current headscale user
// belongs to a *different* portal user. A node that headscale has
// reassigned to "tagged-devices" still has user=tagged-devices there
// (we never override that), and nodes still in someone's namespace
// (user != "tagged-devices") keep their live link. We only insert
// snapshot rows for nodes that headscale has effectively orphaned
// OR for nodes that the user plausibly owns via temporal correlation.
func (a *App) backfillNodeOwnership(db *sql.DB, nodes []headscale.NodeView, portalUserID int64, portalUsername string) {
	if portalUserID == 0 || portalUsername == "" {
		return
	}
	// Build a set of currently-live node IDs.
	live := map[string]bool{}
	for _, n := range nodes {
		live[n.ID] = true
	}
	// GC pass: drop snapshot rows for nodes that no longer exist in
	// headscale. Restricted to rows that this portal user owns, so a
	// row owned by a different portal user (and pointing at the same
	// node id, possible if a node was re-tagged under someone else)
	// is left alone.
	snapRows, err := db.Query(`SELECT node_id FROM node_owner_map WHERE username=?`, portalUsername)
	if err == nil {
		var orphans []string
		for snapRows.Next() {
			var nid string
			if err := snapRows.Scan(&nid); err == nil && !live[nid] {
				orphans = append(orphans, nid)
			}
		}
		snapRows.Close()
		for _, nid := range orphans {
			_, _ = db.Exec(`DELETE FROM node_owner_map WHERE node_id=? AND username=?`, nid, portalUsername)
		}
	}
	// Preload this user's preauth keys once.
	// 2026-07-11: Этап 10 part 3 — SELECT moved to db.ListPreauthKeysByUser.
	// We use the full row even though only (ID, HeadscalePreauthID,
	// CreatedAt) feed the temporal-match logic. The full struct keeps
	// the helper single-purpose; the unused fields are zero-cost.
	paks, err := dbpkg.ListPreauthKeysByUser(db, portalUserID)
	if err != nil {
		return
	}
	// Look up the headscale user IDs that other portal users own,
	// so we can detect "this node is currently in someone else's
	// namespace" and refuse to steal it. A node whose n.UserID maps
	// to a different portal user is theirs, not ours.
	otherOwners := map[string]bool{}
	if portalUserID != 0 {
		// 2026-07-11: Этап 10 part 1 — moved to db.GetOtherHSUserIDs
		// (uses a.DB because `db` here is the local *sql.DB param
		// and shadows the db package import)
		ids, _ := dbpkg.GetOtherHSUserIDs(a.DB, portalUserID)
		for _, hid := range ids {
			if hid != "" {
				otherOwners[hid] = true
			}
		}
	}
	// Track nodes we've already snapshotted in this pass so a node
	// doesn't get two snapshot rows (e.g. matching (A) AND (C)).
	inserted := map[string]bool{}
	for _, n := range nodes {
		if inserted[n.ID] {
			continue
		}
		// Refuse to steal a node that headscale currently has in
		// another portal user's namespace. tagged-devices is a
		// synthetic user created by headscale for tag-bearing
		// nodes, NOT a portal user, so it doesn't appear in
		// otherOwners and is fair game for snapshot rows.
		if n.UserID != "" && otherOwners[n.UserID] {
			continue
		}
		var matchedTag string
		// Strategy A: strict join on headscale_preauth_id.
		if n.PreAuthKeyID != "" {
			for _, p := range paks {
				if p.HeadscalePreauthID != "" && p.HeadscalePreauthID == n.PreAuthKeyID {
					matchedTag = firstTagOrFallback(n)
					break
				}
			}
		}
		// Strategy C: temporal fallback. Node has CreatedAt, and
		// one of this user's preauth keys was created within the
		// 1-hour window before the node.
		if matchedTag == "" && n.CreatedAt != "" {
			if nodeAt, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
				bestKey := int64(0)
				bestDelta := time.Duration(0)
				for _, p := range paks {
					keyAt := time.Unix(p.CreatedAt, 0)
					delta := nodeAt.Sub(keyAt)
					// Preauth key must be created BEFORE the node
					// (delta >= 0), and within 1 hour. The user
					// can issue a key, send it to a device, and
					// have the device register - but not faster
					// than ~minute for a remote network, and we
					// want a wide enough window to absorb clock
					// skew, retries, slow SSH tunnels, etc.
					if delta < 0 || delta > time.Hour {
						continue
					}
					if bestKey == 0 || delta < bestDelta {
						bestKey = p.ID
						bestDelta = delta
					}
				}
				if bestKey != 0 {
					// 2026-07-10: bug fix — when the match came through a skygate-issued preauth
					// key, the node must have been registered BY our user. Default to
					// tag:private (so the user only sees their own devices in Tailscale).
					// Previously firstTagOrFallback(n) returned tag:untagged for
					// headscale-tagless nodes — UI showed tag:private locally but
					// headscale had no tag. Admins can still set tag:public manually
					// via /admin/devices/taged (PostAdminNodeTag).
					matchedTag = "tag:private"
				}
			}
		}
		if matchedTag == "" {
			continue
		}
		if matchedTag == "tag:private" {
			// 2026-07-10: bug fix — sync DB and headscale. If we already
			// have a stale "tag:untagged" row from an older build (or empty
			// tag), upgrade to tag:private. Skip rows that already carry
			// tag:public (admin-assigned exit-node tag).
			_, _ = db.Exec(`UPDATE node_owner_map SET tag=?, tagged_by_user_id=?, tagged_at=strftime('%s','now')
				WHERE node_id=? AND (tag = '' OR tag = 'tag:untagged')`,
				matchedTag, portalUserID, n.ID)
		} else {
			_, _ = db.Exec(`INSERT OR IGNORE INTO node_owner_map
				(node_id, headscale_user_id, username, tag, tagged_by_user_id)
				VALUES (?, ?, ?, ?, ?)`,
				n.ID, portalUserID, portalUsername, matchedTag, portalUserID)
		}
		// Push tag:private to headscale if matched. Safe for empty/untagged rows.
		// Idempotent: skip if the node already carries tag:private — otherwise every
		// /my/devices load would do an HTTP roundtrip to headscale per device,
		// AND call InvalidateCache() which forces the next /my/devices load to
		// re-fetch everything (the bug that was making the page take ~2s).
		if matchedTag == "tag:private" && a != nil && a.HS != nil {
			hasPrivate := false
			for _, t := range n.Tags {
				if t == "tag:private" {
					hasPrivate = true
					break
				}
			}
			if !hasPrivate {
				if nodeIDInt, err := strconv.ParseInt(n.ID, 10, 64); err == nil {
					if err := a.HS.TagNode(nodeIDInt, "tag:private"); err != nil {
						log.Printf("warn: auto-tag node %s: %v", n.ID, err)
					}
				}
			}
		}
		inserted[n.ID] = true
		// Mark the preauth key as used if headscale has a node attached to it.
		// 2026-07-11: Этап 10 part 3 — UPDATE moved to db.MarkPreauthKeyUsedByHSID.
		// Best-effort (helper returns error, we log + continue). The
		// helper is a no-op for empty headscaleID, so the n.PreAuthKeyID
		// != "" guard is technically redundant but kept for symmetry
		// with the original inline code and as a fast-path skip.
		if n.PreAuthKeyID != "" {
			if err := dbpkg.MarkPreauthKeyUsedByHSID(db, n.PreAuthKeyID); err != nil {
				log.Printf("warn: mark key %s used: %v", n.PreAuthKeyID, err)
			}
		}
	}
}
