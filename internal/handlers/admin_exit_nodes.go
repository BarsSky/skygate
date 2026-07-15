package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	// 2026-07-15: v0.13.0 — health monitor fields. Populated
	// from exit_node_health (the snapshot table updated by
	// the background monitor) and matched on NodeID. Empty
	// strings / false mean "no snapshot yet" — the page
	// renders a "—" placeholder.
	Online               bool      `json:"online"`
	LastSeen             string    `json:"last_seen"`
	LastSeenAgo          string    `json:"last_seen_ago"`
	State                string    `json:"state"`
	Healthy              bool      `json:"healthy"`
	LastCheckAt          time.Time `json:"last_check_at"`
	HasExitTag           bool      `json:"has_exit_tag"`
	AdvertisedRoutesOK   bool      `json:"advertised_routes_ok"`
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

	// 2026-07-15: v0.13.0 — overlay the health-monitor
	// snapshot on each row (matched by node_id). The snapshot
	// may not exist yet (monitor hasn't ticked, or this node
	// was added after the last tick); the template renders
	// "—" placeholders in that case.
	healthRows, _ := db.ListExitNodeHealth(a.DB)
	healthByID := make(map[string]db.ExitNodeHealth, len(healthRows))
	now := time.Now().UTC()
	for _, h := range healthRows {
		healthByID[h.NodeID] = h
	}
	healthyCount := 0
	for i := range nodes {
		h, ok := healthByID[nodes[i].NodeID]
		if !ok {
			continue
		}
		nodes[i].Online = h.Online
		nodes[i].LastSeen = h.LastSeen
		nodes[i].State = h.State
		nodes[i].Healthy = h.Healthy
		nodes[i].LastCheckAt = h.LastCheckAt
		nodes[i].HasExitTag = h.HasExitTag
		nodes[i].AdvertisedRoutesOK = h.AdvertisedRoutesOK
		if !h.LastSeenParsed.IsZero() {
			nodes[i].LastSeenAgo = humanizeDuration(now.Sub(h.LastSeenParsed))
		}
		if h.Healthy {
			healthyCount++
		}
	}

	a.renderWithLayout(w, r, "admin/exit_nodes.html", c, map[string]any{
		"Page":         "exit-nodes",
		"Title":        "Exit Nodes",
		"Nodes":        nodes,
		"SSHKeyPath":   a.SSHKeyPath,
		"HealthyCount": healthyCount,
		"TotalCount":   len(nodes),
		"MonitorRunning": a.ExitNodeMonitor != nil,
	})
}

// humanizeDuration formats a time.Duration as a short
// human-readable string ("3s", "2m 14s", "1h 5m", "2d 3h").
// Used by /admin/exit-nodes to render the "last seen X ago"
// column without pulling moment.js / dayjs. Negative inputs
// are treated as "0s" (the monitor's clock skew can produce
// these on a clock-adjusting laptop).
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}

// PostAdminExitNodesHealthNow (v0.13.0) is the "Run health
// check now" button on /admin/exit-nodes. Admin-only. Calls
// ExitNodeMonitor.CheckNow synchronously (the monitor's
// internal mutex serialises concurrent admin clicks) and
// redirects back to /admin/exit-nodes so the operator sees
// the fresh state. The background goroutine is unaffected
// (it runs on its own ticker, not through CheckNow).
//
// If the monitor is disabled
// (SKYGATE_EXIT_NODE_CHECK_INTERVAL=off) or hasn't been
// wired (e.g. running unit tests), the handler shows a
// flash error instead of crashing.
func (a *App) PostAdminExitNodesHealthNow(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if a.ExitNodeMonitor == nil {
		a.redirectWithFlash(w, r, "", "Exit-node monitor is disabled (SKYGATE_EXIT_NODE_CHECK_INTERVAL=off)")
		return
	}
	if err := a.ExitNodeMonitor.CheckNow(r.Context()); err != nil {
		a.redirectWithFlash(w, r, "", "Health check failed: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "exit_node_health_now", "")
	a.redirectWithFlash(w, r, "Health check completed.", "")
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
