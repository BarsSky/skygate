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
		_, _ = a.DB.Exec(`INSERT OR REPLACE INTO node_owner_map
			(node_id, headscale_user_id, username, tag, tagged_by_user_id)
			VALUES (?, ?, ?, ?, ?)`,
			nodeID, origUserID, origUserName, tag, c.UserID)
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
	_, _ = a.DB.Exec(`DELETE FROM node_owner_map WHERE node_id=? AND tag=?`, nodeID, tag)

	a.HS.InvalidateCache()
	a.audit(c.UserID, c.Username, "node_untag", fmt.Sprintf("node=%d tag=%s", nodeID, tag))
	http.Redirect(w, r, "/admin/devices", http.StatusFound)
}
