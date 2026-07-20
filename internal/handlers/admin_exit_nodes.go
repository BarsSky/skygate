package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
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
	// 2026-07-17: v0.18.1 — raw headscale-side state. The
	// "Tag as exit-node" / "Untag" buttons need to know
	// whether the node already has tag:exit-node and
	// whether it advertises 0.0.0.0/0 + ::/0 (the
	// exit-node bases). Without these the template
	// can't decide which button to render.
	Tags                []string `json:"tags"`
	AdvertisesV4Default bool     `json:"advertises_v4_default"`
	AdvertisesV6Default bool     `json:"advertises_v6_default"`
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
					// 2026-07-17: v0.18.1 — surface the
					// raw headscale tags + exit-node-base
					// advertising state so the template
					// can render the "Tag as exit-node"
					// / "Untag" buttons correctly.
					nodes[i].Tags = hn.Tags
					for _, r := range hn.AvailableRoutes {
						if r == "0.0.0.0/0" {
							nodes[i].AdvertisesV4Default = true
						}
						if r == "::/0" {
							nodes[i].AdvertisesV6Default = true
						}
					}
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
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
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
// We redirect to /admin/exit-nodes directly (not via the
// shared redirectWithFlash helper, which is hard-coded to
// /admin/telegram) so a successful run lands the operator
// back on the page they were just on.
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
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Exit-node monitor is disabled (SKYGATE_EXIT_NODE_CHECK_INTERVAL=off)"), http.StatusSeeOther)
		return
	}
	if err := a.ExitNodeMonitor.CheckNow(r.Context()); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Health check failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	a.audit(c.UserID, c.Username, "exit_node_health_now", "")
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Health check completed."), http.StatusSeeOther)
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

// PostAdminExitNodeTagAsExitNode is the v0.18.1 "Tag as
// exit-node" button on /admin/exit-nodes. It replaces the
// operator's two manual `docker exec headscale headscale
// nodes ...` invocations with a single click:
//
//  1. Approves the exit-node bases (0.0.0.0/0, ::/0) on
//     the headscale side via the CLI. We approve ONLY the
//     two base routes, not the full availableRoutes set
//     (karolina has 200+ subnets that the operator does
//     NOT want auto-approved).
//  2. Tags the node with `tag:exit-node`. The ACL
//     already includes `* → tag:exit-node:*` so the new
//     node immediately starts accepting tailnet traffic.
//
// Both steps go through the same docker-exec headscale
// CLI that the operator used to run by hand. The handler
// refuses to act if:
//   - the node doesn't have 0.0.0.0/0 AND ::/0 advertised
//     (operator hasn't run `tailscale set --advertise-exit-node` yet)
//   - the node is already tagged with `tag:exit-node`
//     (idempotency: this handler is for the
//     "tag" half of the workflow, not the "untag")
//
// PostAdminExitNodeUntagAsExitNode (below) handles the
// reverse — removing tag:exit-node from a node.
func (a *App) PostAdminExitNodeTagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := r.FormValue("node_id")
	if idStr == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node_id required"), http.StatusSeeOther)
		return
	}
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("bad node id"), http.StatusSeeOther)
		return
	}

	// Find the node and verify it has the exit-node
	// bases advertised. We refuse to tag a node that
	// hasn't advertised 0.0.0.0/0+::/0 (the operator
	// must run `tailscale set --advertise-exit-node`
	// first — that's the "I want this to be an exit-node"
	// gate). This is also why the button is only rendered
	// in the template for nodes that have these routes
	// advertised; the server-side check is defense in
	// depth in case the operator crafts a POST by hand.
	allNodes, err := a.HSGlobal().ListAllNodes()
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("list nodes: "+err.Error()), http.StatusSeeOther)
		return
	}
	var target *headscale.NodeView
	for i := range allNodes {
		if allNodes[i].ID == idStr {
			target = &allNodes[i]
			break
		}
	}
	if target == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("node not found"), http.StatusSeeOther)
		return
	}
	hasV4, hasV6 := false, false
	for _, r := range target.AvailableRoutes {
		if r == "0.0.0.0/0" {
			hasV4 = true
		}
		if r == "::/0" {
			hasV6 = true
		}
	}
	if !hasV4 || !hasV6 {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"node does not advertise 0.0.0.0/0 + ::/0 yet — run `tailscale set --advertise-exit-node` on the relay first"), http.StatusSeeOther)
		return
	}

	// Idempotency: if the node already has tag:exit-node,
	// skip the TagNode call. The button is hidden in this
	// case but we re-check here.
	for _, t := range target.Tags {
		if t == "tag:exit-node" {
			http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(
				fmt.Sprintf("%s is already tagged as exit-node", target.Hostname)), http.StatusSeeOther)
			return
		}
	}

	// Step 1: approve the exit-node bases. We approve
	// ONLY 0.0.0.0/0 and ::/0 (not the full availableRoutes)
	// to avoid accidentally approving karolina's 200+
	// subnets.
	approved, err := a.HSGlobal().ApproveRoutesForNodeID(nodeID, []string{"0.0.0.0/0", "::/0"})
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("approve-routes: "+err.Error()), http.StatusSeeOther)
		return
	}

	// Step 2: tag with tag:exit-node. The ACL already
	// allows `* → tag:exit-node:*`, so the node starts
	// accepting traffic immediately on the next ACL
	// poll by the Tailscale client (usually <60s).
	if err := a.HSGlobal().TagNode(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("tag: "+err.Error()), http.StatusSeeOther)
		return
	}

	a.HSGlobal().InvalidateCache()
	a.audit(c.UserID, c.Username, "exit_node_tag",
		fmt.Sprintf("node=%s id=%d approved_routes=%d tag=tag:exit-node",
			target.Hostname, nodeID, approved))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(
		fmt.Sprintf("%s is now tagged as exit-node (%d routes approved)",
			target.Hostname, approved)), http.StatusSeeOther)
}

// PostAdminExitNodeUntagAsExitNode is the v0.18.1
// "Untag" button on /admin/exit-nodes. Removes
// `tag:exit-node` from a node. Useful when the
// operator wants to demote a relay back to a
// regular node (e.g. the relay is going down for
// maintenance and they don't want tailnet clients
// to pick it as an exit-node).
//
// The handler does NOT touch the approved routes —
// those stay as-is. To remove the routes too, the
// operator has to run `docker exec headscale headscale
// nodes approve-routes -i <id> -r "" --force` (or
// similar); we don't expose that from the UI because
// route removal is rarely wanted.
func (a *App) PostAdminExitNodeUntagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	idStr := r.FormValue("node_id")
	nodeID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("bad node id"), http.StatusSeeOther)
		return
	}

	// UntagNode preserves the other tags (replaces the
	// full tag list, leaving the others in place). If
	// the node was tagged only with tag:exit-node, it
	// falls back to tag:private so headscale keeps at
	// least one tag (the headscale CLI rejects empty
	// tag sets).
	if err := a.HSGlobal().UntagNode(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("untag: "+err.Error()), http.StatusSeeOther)
		return
	}
	a.HSGlobal().InvalidateCache()
	a.audit(c.UserID, c.Username, "exit_node_untag",
		fmt.Sprintf("node_id=%d tag=tag:exit-node", nodeID))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Removed tag:exit-node from node."), http.StatusSeeOther)
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
