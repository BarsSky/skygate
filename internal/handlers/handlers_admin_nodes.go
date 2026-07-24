package handlers

// handlers_admin_nodes.go — extracted from handlers.go.
// Admin devices page: list of all nodes across portal users, plus the
// tag/untag actions that apply headscale tags. Kept separate because these
// handlers reach into the headscale admin API (TagNode/UntagNode via
// CLI fallback) rather than the per-user portal flow in handlers.go.

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

func (a *App) GetAdminDevices(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	// 2026-07-15: v0.12.0 — admin pages always use the global
	// headscale (HSGlobal). Per-user routing on /admin/devices
	// would be ambiguous ("show devices of which user?"); the
	// admin view is the operator's-eye view of the primary
	// control plane.
	users, _ := a.HSGlobal().ListUsers()
	allNodes, _ := a.HSGlobal().ListAllNodes()
	// 2026-07-24: v0.28.0 — admin page surfaces the
	// per-device ACL tag (tag:dev-<user>-<hostname>) for
	// every node in node_owner_map. The list is small
	// (one row per device, currently ~13), and a flat list
	// keeps the template lookup O(1) via a map.
	devTags, _ := db.GetPerUserDeviceTags(a.DB, "")
	devTagMap := make(map[string]string, len(devTags))
	for _, t := range devTags {
		// Key by hostname — admin/devices.html iterates
		// Nodes (headscale view) and looks up
		// DevTagMap[.Hostname]. Hostname is unique across
		// the tailnet (Tailscale rejects duplicates), so
		// the map is 1:1.
		devTagMap[t.Hostname] = t.Tag
	}
	a.renderWithLayout(w, r, "admin/devices.html", c, map[string]any{
		"Nodes":        allNodes,
		"Users":        users,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
		"DevTagMap":    devTagMap,
	})
}

// PostAdminDevicesSyncFromHeadscale is the v0.14.0
// "Sync from headscale" button on /admin/devices. Admin-only.
// Calls db.SyncNodesFromHeadscale to INSERT any missing
// rows + UPDATE drifted tags. This is the operator's
// escape hatch when:
//   1. They tagged a relay directly in headscale (the bot's
//      /exit_nodes then reports "no nodes found" until
//      this button is clicked).
//   2. The bot's per-tick auto-heal in commands_user.go is
//      off (e.g. SKYGATE_BOT_AUTO_HEAL_TAGS=false), so the
//      cache is stale.
//
// 2026-07-15: v0.14.0 — also wired to the bot's /sync_nodes
// admin command (same DB call, different entry point).
func (a *App) PostAdminDevicesSyncFromHeadscale(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	nodes, err := a.HSGlobal().ListAllNodes()
	if err != nil {
		http.Error(w, "headscale list failed: "+err.Error(), 500)
		return
	}
	var syncInfos []db.SyncNodeInfo
	for _, n := range nodes {
		// Pick the first non-empty tag for the row. headscale
		// returns a slice; we treat "tag:exit-node" as the
		// most specific (it's what the bot reads) and fall
		// back to whatever else is set.
		tag := ""
		for _, t := range n.Tags {
			if t == headscale.TagPublicTag || t == headscale.TagPrivateTag {
				continue
			}
			tag = t
			break
		}
		if tag == "" {
			for _, t := range n.Tags {
				if t != "" {
					tag = t
					break
				}
			}
		}
		var hsUID int64
		if n.UserID != "" {
			if v, perr := strconv.ParseInt(n.UserID, 10, 64); perr == nil {
				hsUID = v
			}
		}
		syncInfos = append(syncInfos, db.SyncNodeInfo{
			ID:       n.ID,
			Hostname: n.Hostname,
			Tag:      tag,
			Username: n.UserName,
			HSUserID: hsUID,
			TaggedBy: c.UserID,
		})
	}
	ins, upd, err := db.SyncNodesFromHeadscale(a.DB, syncInfos)
	if err != nil {
		http.Error(w, "sync failed: "+err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "node_sync_from_headscale",
		fmt.Sprintf("inserted=%d updated=%d", ins, upd))
	// Redirect to /admin/devices with a flash that the
	// template renders as the success banner. We use the
	// dedicated ?ok=... query param that PostAdminNodeTag
	// / PostAdminNodeUntag already use (the page reads it).
	http.Redirect(w, r, fmt.Sprintf(
		"/admin/devices?ok=%s", url.QueryEscape(
			fmt.Sprintf("Sync from headscale: %d inserted, %d updated", ins, upd))), http.StatusSeeOther)
}

// PostAdminNodeTag adds a headscale tag to a node.
func (a *App) PostAdminNodeTag(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", 400)
		return
	}
	tag := r.FormValue("tag")
	if tag == "" {
		tag = headscale.TagPublicTag
	}

	var origUserID, origUserName string
	if nodes, err := a.HSGlobal().ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == strconv.FormatInt(nodeID, 10) {
				origUserID = n.UserID
				origUserName = n.UserName
				break
			}
		}
	}

	if err := a.HSGlobal().TagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	// 2026-07-15: Этап 14 v13 — v0.10.13 fix. The old guard
	// "origUserName != \"tagged-devices\"" skipped the node_owner_map
	// update for nodes whose headscale ownership was reassigned to
	// the synthetic tagged-devices user (which happens automatically
	// when any tag is applied to a node in headscale). The result
	// was that admin-tagged devices kept their old tag:untagged
	// row in skygate, so the bot's /nodes (which reads from
	// node_owner_map) showed the wrong tag. The bot now self-heals
	// on read via db.SyncTagsFromHeadscale, but we also fix the
	// source here: when the origUserName is "tagged-devices" we
	// look up the existing row in node_owner_map (by node_id) and
	// UPDATE only the tag, leaving username + headscale_user_id
	// alone so a portal-side owner link is preserved.
	if origUserID != "" && origUserName != "" {
		nodeIDStr := strconv.FormatInt(nodeID, 10)
		var hsUID int64
		if n, err := strconv.ParseInt(origUserID, 10, 64); err == nil {
			hsUID = n
		}
		if origUserName == "tagged-devices" {
			// Preserve the existing portal-side owner. The new
			// tag is the source of truth (admin just set it on
			// headscale), the username + headscale_user_id stay
			// as they were.
			_ = db.UpdateNodeOwnerTag(a.DB, nodeIDStr, tag, c.UserID)
		} else {
			_ = db.UpsertNodeOwner(a.DB, nodeIDStr, hsUID, origUserName, tag, c.UserID)
		}
	}

	a.HSGlobal().InvalidateCache()
	a.audit(c.UserID, c.Username, "node_tag", fmt.Sprintf("node=%d tag=%s owner=%s", nodeID, tag, origUserName))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}

func (a *App) PostAdminNodeUntag(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := extractIDFromPath(r.URL.Path)
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad node id", 400)
		return
	}
	tag := r.FormValue("tag")
	if tag == "" {
		tag = headscale.TagPublicTag
	}
	if err := a.HSGlobal().UntagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// 2026-07-12: Этап 10 part 4 — moved to db.DeleteNodeOwnerByNodeTag.
	_ = db.DeleteNodeOwnerByNodeTag(a.DB, strconv.FormatInt(nodeID, 10), tag)

	a.HSGlobal().InvalidateCache()
	a.audit(c.UserID, c.Username, "node_untag", fmt.Sprintf("node=%d tag=%s", nodeID, tag))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}
