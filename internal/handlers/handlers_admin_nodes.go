package handlers

// handlers_admin_nodes.go — extracted from handlers.go.
// Admin devices page: list of all nodes across portal users, plus the
// tag/untag actions that apply headscale tags. Kept separate because these
// handlers reach into the headscale admin API (TagNode/UntagNode via
// CLI fallback) rather than the per-user portal flow in handlers.go.

import (
	"fmt"
	"net/http"
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
	a.renderWithLayout(w, r, "admin/devices.html", c, map[string]any{
		"Nodes": allNodes,
		"Users": users,
	})
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
