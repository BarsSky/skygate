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
	users, _ := a.HS.ListUsers()
	allNodes, _ := a.HS.ListAllNodes()
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
	if nodes, err := a.HS.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == strconv.FormatInt(nodeID, 10) {
				origUserID = n.UserID
				origUserName = n.UserName
				break
			}
		}
	}

	if err := a.HS.TagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if origUserID != "" && origUserName != "" && origUserName != "tagged-devices" {
		// 2026-07-12: Этап 10 part 4 — moved to db.UpsertNodeOwner.
		// nodeID is int64 here (from headscale.NodeView.ID parsed
		// via strconv above); UpsertNodeOwner wants the string form
		// (matches the column type TEXT). origUserID is a string
		// headscale user id; we best-effort parse it to int64 for
		// the headscale_user_id column.
		var hsUID int64
		if n, err := strconv.ParseInt(origUserID, 10, 64); err == nil {
			hsUID = n
		}
		_ = db.UpsertNodeOwner(a.DB, strconv.FormatInt(nodeID, 10), hsUID, origUserName, tag, c.UserID)
	}

	a.HS.InvalidateCache()
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
	if err := a.HS.UntagNode(nodeID, tag); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// 2026-07-12: Этап 10 part 4 — moved to db.DeleteNodeOwnerByNodeTag.
	_ = db.DeleteNodeOwnerByNodeTag(a.DB, strconv.FormatInt(nodeID, 10), tag)

	a.HS.InvalidateCache()
	a.audit(c.UserID, c.Username, "node_untag", fmt.Sprintf("node=%d tag=%s", nodeID, tag))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}
