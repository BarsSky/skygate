package handlers

import (
	"net/http"
)

// ---------- DASHBOARD ----------

// TailnetMetrics is a small summary of the tailnet for the dashboard hero.
// For admin: shows the whole tailnet. For users: shows their own devices
// and only the public/exit nodes they're allowed to see.
type TailnetMetrics struct {
	TotalNodes     int
	OnlineNodes    int
	ExitNodesCount int
	UsersCount     int
	ActiveDERP     string
	// User-scoped metrics (populated when called with a username)
	MyTotalNodes     int
	MyOnlineNodes    int
	MyExitNodesCount int
	// MyPreauthKeys is a 3-way split (used/active/expired). Empty
	// when not a per-user call.
	MyPreauthKeys PreauthKeyStats
}

func (a *App) computeTailnetMetrics(myUsername string, myUserID int64) TailnetMetrics {
	m := TailnetMetrics{}
	nodes, _ := a.HS.ListAllNodes()
	m.TotalNodes = len(nodes)
	for _, n := range nodes {
		if n.Online {
			m.OnlineNodes++
		}
		if n.IsExitNode {
			m.ExitNodesCount++
		}
	}
	// Per-user metrics: for non-admin users, count nodes via node_owner_map
	// (same source /my/devices uses) rather than n.UserName, because
	// headscale reassigns tagged nodes to a synthetic "tagged-devices"
	// user and the live user_id link is lost. The backfill that runs in
	// /my/devices also fires from here, so the dashboard sees the same
	// set the moment the user lands on the page.
	if myUserID != 0 {
		a.backfillNodeOwnership(a.DB, nodes, myUserID, myUsername)
	}
	if myUsername != "" {
		// Use a set of node IDs the user owns, sourced from node_owner_map.
		owned := map[string]bool{}
		rows, _ := a.DB.Query(`SELECT node_id FROM node_owner_map WHERE username=?`, myUsername)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var nid string
				if err := rows.Scan(&nid); err == nil {
					owned[nid] = true
				}
			}
		}
		// Plus any node still showing the live user name (untagged nodes).
		for _, n := range nodes {
			if n.UserName == myUsername {
				owned[n.ID] = true
			}
		}
		for _, n := range nodes {
			if !owned[n.ID] {
				continue
			}
			m.MyTotalNodes++
			if n.Online {
				m.MyOnlineNodes++
			}
			if n.IsExitNode {
				m.MyExitNodesCount++
			}
		}
	}
	users, _ := a.HS.ListUsers()
	m.UsersCount = len(users)
	// Preauth split is per-user; admins see zero (their own key history
	// is admin tooling, not a per-user metric).
	if myUserID != 0 {
		m.MyPreauthKeys = a.countMyPreAuthKeys(myUserID, nodes)
	}
	m.ActiveDERP = "waw" // could be parsed from netcheck but kept simple here
	return m
}

func (a *App) GetDashboard(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Look up the headscale username for this portal user (may be empty for
	// brand-new users who haven't registered a device yet).
	var hsUserName string
	_ = a.DB.QueryRow(`SELECT username FROM portal_users WHERE id=?`, c.UserID).Scan(&hsUserName)
	// Admins see whole-tailnet metrics; users see only their own.
	scope := ""
	if !c.IsAdmin && hsUserName != "" {
		scope = hsUserName
	}
	a.renderWithLayout(w, r, "dashboard.html", c, map[string]any{
		"TailnetMetrics": a.computeTailnetMetrics(scope, c.UserID),
	})
}
