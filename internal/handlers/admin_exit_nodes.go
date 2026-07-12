package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
)

type ExitNodeInfo struct {
	NodeID       string   `json:"node_id"`
	Hostname     string   `json:"hostname"`
	TailscaleIP  string   `json:"tailscale_ip"`
	SSHTarget    string   `json:"ssh_target"`
	SSHKeyPath   string   `json:"ssh_key_path"`
	Enabled      bool     `json:"enabled"`
	Routes       []string `json:"routes"`
	RouteCount   int      `json:"route_count"`
	SyncStatus   string   `json:"sync_status"`
	Description  string   `json:"description"`
	AcceptRoutes int      `json:"accept_routes"` // -1=false, 0=unset, 1=true
}

func (a *App) AdminExitNodes(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	a.ensureExitServers()

	// 2026-07-12: Этап 10 part 5 — moved to db.ListExitServers. The
	// row shape matches ExitNodeInfo 1:1 except the auto-increment id
	// (which the web UI doesn't render) and the headscale enrichment
	// (which happens below from a.HS.ListAllNodes()).
	dbRows, err := db.ListExitServers(a.DB)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var nodes []ExitNodeInfo
	for _, e := range dbRows {
		n := ExitNodeInfo{
			NodeID:       e.NodeID,
			Hostname:     e.Hostname,
			TailscaleIP:  e.TailscaleIP,
			SSHTarget:    e.SSHTarget,
			SSHKeyPath:   e.SSHKeyPath,
			Enabled:      e.Enabled,
			Description:  e.Description,
			AcceptRoutes: e.AcceptRoutes,
		}
		nodes = append(nodes, n)
	}

	if hsNodes, err := a.HS.ListAllNodes(); err == nil {
		for i := range nodes {
			for _, hn := range hsNodes {
				nid, _ := strconv.Atoi(nodes[i].NodeID)
				hnID, _ := strconv.Atoi(hn.ID)
				if nid == hnID {
					if nodes[i].TailscaleIP == "" && len(hn.IPAddresses) > 0 {
						nodes[i].TailscaleIP = hn.IPAddresses[0]
					}
					nodes[i].Routes = hn.AvailableRoutes
					nodes[i].RouteCount = len(hn.AvailableRoutes)
					if nodes[i].Hostname == "" {
						nodes[i].Hostname = hn.GivenName
					}
					break
				}
			}
		}
	}

	ruleRows, _ := a.DB.Query("SELECT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet')")
	if ruleRows != nil {
		defer ruleRows.Close()
		expectedRoutes := map[string]int{}
		for ruleRows.Next() {
			var node, target string
			if ruleRows.Scan(&node, &target) == nil {
				expectedRoutes[node]++
			}
		}
		for i := range nodes {
			expected := expectedRoutes[nodes[i].Hostname]
			if expected > 0 && nodes[i].RouteCount != expected {
				nodes[i].SyncStatus = fmt.Sprintf("mismatch: have %d, want %d", nodes[i].RouteCount, expected)
			} else if expected > 0 {
				nodes[i].SyncStatus = "synced"
			}
		}
	}

	a.renderWithLayout(w, r, "admin/exit_nodes.html", c, map[string]any{
		"Page":       "exit-nodes",
		"Title":      "Exit Nodes",
		"Nodes":      nodes,
		"SSHKeyPath": a.SSHKeyPath,
	})
}

func (a *App) PostAdminExitNodesAdd(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	nodeID := strings.TrimSpace(r.FormValue("node_id"))
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	sshTarget := strings.TrimSpace(r.FormValue("ssh_target"))
	sshKey := strings.TrimSpace(r.FormValue("ssh_key_path"))
	desc := strings.TrimSpace(r.FormValue("description"))
	if nodeID == "" || hostname == "" {
		http.Error(w, "node_id and hostname required", 400)
		return
	}
	acceptRoutes := 0
	switch strings.TrimSpace(r.FormValue("accept_routes")) {
	case "true":
		acceptRoutes = 1
	case "false":
		acceptRoutes = -1
	}
	// 2026-07-12: Этап 10 part 5 — moved to db.UpsertExitServer.
	if err := db.UpsertExitServer(a.DB, nodeID, hostname, sshTarget, sshKey, desc, acceptRoutes); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "exit_node_add", fmt.Sprintf("node=%s ssh=%s", hostname, sshTarget))
	http.Redirect(w, r, "/admin/exit-nodes?added=1", http.StatusFound)
}

func (a *App) PostAdminExitNodesDelete(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	nodeID := r.FormValue("node_id")
	if nodeID == "" {
		http.Error(w, "node_id required", 400)
		return
	}
	// 2026-07-12: Этап 10 part 5 — moved to db.DeleteExitServerByNodeID.
	if err := db.DeleteExitServerByNodeID(a.DB, nodeID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "exit_node_delete", nodeID)
	http.Redirect(w, r, "/admin/exit-nodes?deleted=1", http.StatusFound)
}

func (a *App) PostAdminExitNodesSync(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, `{"error":"forbidden"}`, 403)
		return
	}
	result := a.SyncAdvertisedRoutes()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (a *App) ensureExitServers() {
	nodes, err := a.HS.ListAllNodes()
	if err != nil {
		return
	}
	for _, n := range nodes {
		isExit := false
		for _, t := range n.Tags {
			if strings.Contains(t, "exit-node") {
				isExit = true
				break
			}
		}
		if isExit || len(n.AvailableRoutes) > 0 {
			// 2026-07-12: Этап 10 part 5 — moved to db.InsertIgnoreExitServerOnDiscovery.
			// INSERT OR IGNORE so an admin's manual row (possibly
			// enabled=0) is preserved.
			db.InsertIgnoreExitServerOnDiscovery(a.DB, n.ID, n.GivenName, strings.Join(n.IPAddresses, ","))
		}
	}
}
