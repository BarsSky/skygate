package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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

	rows, err := a.DB.Query("SELECT id, node_id, hostname, tailscale_ip, ssh_target, ssh_key_path, enabled, COALESCE(description,''), accept_routes FROM exit_servers ORDER BY hostname")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	var nodes []ExitNodeInfo
	for rows.Next() {
		var n ExitNodeInfo
		var en, id int
		if err := rows.Scan(&id, &n.NodeID, &n.Hostname, &n.TailscaleIP, &n.SSHTarget, &n.SSHKeyPath, &en, &n.Description, &n.AcceptRoutes); err != nil {
			continue
		}
		n.Enabled = en == 1
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

	a.renderWithLayout(w, "admin/exit_nodes.html", c, map[string]any{
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
	_, err := a.DB.Exec("INSERT INTO exit_servers (node_id, hostname, ssh_target, ssh_key_path, description, accept_routes) VALUES (?,?,?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET hostname=excluded.hostname, ssh_target=excluded.ssh_target, ssh_key_path=excluded.ssh_key_path, description=excluded.description, accept_routes=excluded.accept_routes",
		nodeID, hostname, sshTarget, sshKey, desc, acceptRoutes)
	if err != nil {
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
	a.DB.Exec("DELETE FROM exit_servers WHERE node_id = ?", nodeID)
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
			a.DB.Exec("INSERT OR IGNORE INTO exit_servers (node_id, hostname, tailscale_ip) VALUES (?,?,?)",
				n.ID, n.GivenName, strings.Join(n.IPAddresses, ","))
		}
	}
}
