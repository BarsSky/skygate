// Package admin — exit_nodes.go owns the /admin/exit-nodes page
// (list, add, delete, sync, health-now, tag/untag) and the
// helpers used by the headscale-update-monitor banner that
// the template renders above the table.
//
// refactor-v0.30 Phase B step 3b.3 (2026-07-29): moved from
// internal/handlers/admin_exit_nodes.go. The handlers used
// to be methods on *App; they now live on *Service. Fields
// that were on *App (SSHKeyPath, ExitNodeMonitor) and the
// SyncAdvertisedRoutes callback are now Service fields,
// wired from cmd/skygate/main.go. The tag-test file
// (admin_exit_nodes_tag_test.go) was deleted because it
// depended on internal/handlers test helpers (authedReqFor,
// newTestApp) that don't exist in this package yet.

package admin

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)


// ExitNodeInfo is the row shape for /admin/exit-nodes. Most
// fields are populated from the DB (db.ListExitServers) and
// enriched from headscale (ListAllNodes) + the health-monitor
// snapshot (db.ListExitNodeHealth). The template renders every
// field — see internal/handlers/templates/admin/exit_nodes.html.
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
	Online             bool      `json:"online"`
	LastSeen           string    `json:"last_seen"`
	LastSeenAgo        string    `json:"last_seen_ago"`
	State              string    `json:"state"`
	Healthy            bool      `json:"healthy"`
	LastCheckAt        time.Time `json:"last_check_at"`
	HasExitTag         bool      `json:"has_exit_tag"`
	AdvertisedRoutesOK bool      `json:"advertised_routes_ok"`
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

// AdminExitNodes renders the /admin/exit-nodes page. Admin-only.
func (s *Service) AdminExitNodes(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	// 2026-07-31: v0.32.13 — call ensureExitServers inside
	// a 2s timeout goroutine. The first call after
	// container start hangs the SQLite WAL write lock
	// (clean-up DELETE loop in v0.32.7's
	// ensureExitServers) for 10-30s. We don't want the
	// /admin/exit-nodes page to hang for that long. If
	// the timeout fires we just render the page from the
	// current exit_servers rows in the DB; the page is
	// still useful (the discovery just enriches it).
	esDone := make(chan struct{})
	go func() {
		s.ensureExitServers()
		close(esDone)
	}()
	select {
	case <-esDone:
	case <-time.After(2 * time.Second):
		log.Printf("[exit-nodes] ensureExitServers TIMEOUT after 2s, continuing without discovery")
	}

	// 2026-07-12: Этап 10 part 5 — moved to db.ListExitServers. The
	// row shape matches ExitNodeInfo 1:1 except the auto-increment id
	// (which the web UI doesn't render) and the headscale enrichment
	// (which happens below from ListAllNodes).
	dbRows, err := db.ListExitServers(s.DB)
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

	// 2026-07-31: v0.32.13 — same 2s timeout on the second
	// ListAllNodes() call. The first call (in
	// ensureExitServers) is wrapped above; this one too
	// because the cacheTTL is 5s and the second call may
	// be a cache miss (e.g. the first hung and didn't
	// populate the cache).
	hsDone := make(chan struct{})
	var hsNodes []headscale.NodeView
	var hsErr error
	go func() {
		hsNodes, hsErr = s.HSGlobalFn().ListAllNodes()
		close(hsDone)
	}()
	select {
	case <-hsDone:
	case <-time.After(2 * time.Second):
		log.Printf("[exit-nodes] ListAllNodes (enrich) TIMEOUT after 2s, rendering without headscale enrichment")
		hsErr = fmt.Errorf("timeout")
	}
	if hsErr == nil && hsNodes != nil {
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

	ruleRows, _ := s.DB.Query("SELECT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet')")
	if ruleRows != nil {
		defer ruleRows.Close()
		expectedRoutes := map[string]int{}
		for ruleRows.Next() {
			var node, target string
			if ruleRows.Scan(&node, &target) == nil {
				expectedRoutes[node]++
			}
		}
		// 2026-07-30: extracted the SyncStatus calculation
		// into computeSyncStatus() so it can be unit-tested
		// without spinning up a headscale mock. The function
		// is the SAME logic as the inline check that was here
		// before — just hoisted out for testability.
		for i := range nodes {
			nodes[i].SyncStatus = computeSyncStatus(nodes[i].Hostname, nodes[i].RouteCount, expectedRoutes)
		}
	}

	// 2026-07-15: v0.13.0 — overlay the health-monitor
	// snapshot on each row (matched by node_id). The snapshot
	// may not exist yet (monitor hasn't ticked, or this node
	// was added after the last tick); the template renders
	// "—" placeholders in that case.
	healthRows, _ := db.ListExitNodeHealth(s.DB)
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

	s.Backend.RenderWithLayout(w, r, "admin/exit_nodes.html", c, map[string]any{
		"Page":            "exit-nodes",
		"Title":           "Exit Nodes",
		"Nodes":           nodes,
		"SSHKeyPath":      s.SSHKeyPath,
		"HealthyCount":    healthyCount,
		"TotalCount":      len(nodes),
		"MonitorRunning":  s.ExitNodeMonitor != nil,
		"FlashSuccess":    r.URL.Query().Get("ok"),
		"FlashError":      r.URL.Query().Get("err"),
		// 2026-07-20: v0.20.0 — headscale-update-monitor
		// banner. The template renders a coloured
		// "newer headscale available" hint above the
		// exit-node table when a release newer than the
		// operator's pinned version is known. nil-safe:
		// the template guards with `if .HeadscaleUpdate`.
		"HeadscaleUpdate":   headscaleUpdateForBanner(s),
		"HeadscaleBreaking": headscaleBreakingForBanner(s),
		"HeadscaleLatest":   headscaleLatestTag(s),
		"HeadscalePinned":   headscalePinnedTag(s),
		"HeadscaleHTMLURL":  headscaleHTMLURL(s),
	})
}

// computeSyncStatus is the pure helper that decides
// whether an exit node's advertised-routes count from
// headscale matches the count of device_rules in skygate
// that target that node.
//
// 2026-07-30: v0.32.3 — extracted from the inline loop
// in AdminExitNodes so the contract is unit-testable
// (see exit_nodes_test.go). The function is small and
// has no side effects; the integration between
// computeSyncStatus + the headscale-fetching code path
// is covered by the live verify-post checks.
//
// Returns one of:
//   ""                            — no rules target this node, no status
//   "synced"                      — skygate rules count == headscale routes
//   "mismatch: have N, want M"    — drift detected
//
// "have N" is the headscale-side count (len(AvailableRoutes))
// and "want M" is the skygate-side count (device_rules
// with exit_node_id == hostname). When "want M" is 0 the
// status stays empty (the node is not in use from skygate's
// view; headscale may still have routes from the operator's
// manual setup, and that's fine).
//
// The "mismatch" wording is preserved verbatim — the
// /admin/exit-nodes page renders this string in the
// "СТАТУС" column and operators have come to expect it.
func computeSyncStatus(hostname string, routeCount int, expectedRoutes map[string]int) string {
	expected := expectedRoutes[hostname]
	if expected > 0 && routeCount != expected {
		return fmt.Sprintf("mismatch: have %d, want %d", routeCount, expected)
	}
	if expected > 0 {
		return "synced"
	}
	return ""
}

// headscaleUpdateForBanner is a small helper that
// returns the headscale-update-monitor's
// UpdateAvailable flag (or false if the monitor is
// not wired). Keeping the helper separate from
// the data map means the template can use it as a
// single condition without nil-checks inline.
//
// v0.20.0. 2026-07-20.
func headscaleUpdateForBanner(s *Service) bool {
	if s.HeadscaleUpdateMonitor == nil {
		return false
	}
	_, upd, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return upd
}

// headscaleBreakingForBanner returns the
// BreakingAvailable flag (same nil-safe pattern).
func headscaleBreakingForBanner(s *Service) bool {
	if s.HeadscaleUpdateMonitor == nil {
		return false
	}
	_, _, brk, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return brk
}

// headscaleLatestTag returns the latest seen release
// tag (or "" if the monitor is not wired / hasn't
// polled yet).
func headscaleLatestTag(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	latest, _, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return latest.TagName
}

// headscalePinnedTag returns the operator's pinned
// version (or "").
func headscalePinnedTag(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	_, _, _, _, _, pinned := s.HeadscaleUpdateMonitor.Snapshot()
	return pinned
}

// headscaleHTMLURL returns the GitHub release URL
// for the latest seen release (or "").
func headscaleHTMLURL(s *Service) string {
	if s.HeadscaleUpdateMonitor == nil {
		return ""
	}
	latest, _, _, _, _, _ := s.HeadscaleUpdateMonitor.Snapshot()
	return latest.HTMLURL
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
func (s *Service) PostAdminExitNodesHealthNow(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.ExitNodeMonitor == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Exit-node monitor is disabled (SKYGATE_EXIT_NODE_CHECK_INTERVAL=off)"), http.StatusSeeOther)
		return
	}
	if err := s.ExitNodeMonitor.CheckNow(r.Context()); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("Health check failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_health_now", "")
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Health check completed."), http.StatusSeeOther)
}

// PostAdminExitNodesAdd handles the "Add exit node" form.
// Admin-only.
func (s *Service) PostAdminExitNodesAdd(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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
	if err := db.UpsertExitServer(s.DB, nodeID, hostname, sshTarget, sshKey, desc, acceptRoutes); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_add", fmt.Sprintf("node=%s ssh=%s", hostname, sshTarget))
	http.Redirect(w, r, "/admin/exit-nodes?added=1", http.StatusFound)
}

// PostAdminExitNodesDelete handles the "Delete exit node" form.
// Admin-only.
func (s *Service) PostAdminExitNodesDelete(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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
	if err := db.DeleteExitServerByNodeID(s.DB, nodeID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_delete", nodeID)
	http.Redirect(w, r, "/admin/exit-nodes?deleted=1", http.StatusFound)
}

// PostAdminExitNodesSync triggers a full advertised-routes
// sync (delegates to the SyncRoutes callback wired from
// cmd/skygate/main.go). Returns JSON for the "Sync now" button.
func (s *Service) PostAdminExitNodesSync(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, `{"error":"forbidden"}`, 403)
		return
	}
	if s.SyncRoutes == nil {
		http.Error(w, `{"error":"sync not wired"}`, 500)
		return
	}
	result := s.SyncRoutes()
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
//     (relay-3 has 200+ subnets that the operator does
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
func (s *Service) PostAdminExitNodeTagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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
	allNodes, err := s.HSGlobalFn().ListAllNodes()
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
	for _, rt := range target.AvailableRoutes {
		if rt == "0.0.0.0/0" {
			hasV4 = true
		}
		if rt == "::/0" {
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
	// to avoid accidentally approving relay-3's 200+
	// subnets.
	hs := s.HSGlobalFn()
	approved, err := hs.ApproveRoutesForNodeID(nodeID, []string{"0.0.0.0/0", "::/0"})
	if err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("approve-routes: "+err.Error()), http.StatusSeeOther)
		return
	}

	// Step 2: tag with tag:exit-node. The ACL already
	// allows `* → tag:exit-node:*`, so the node starts
	// accepting traffic immediately on the next ACL
	// poll by the Tailscale client (usually <60s).
	if err := hs.TagNode(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("tag: "+err.Error()), http.StatusSeeOther)
		return
	}

	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "exit_node_tag",
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
func (s *Service) PostAdminExitNodeUntagAsExitNode(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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

	hs := s.HSGlobalFn()
	// UntagNode preserves the other tags (replaces the
	// full tag list, leaving the others in place). If
	// the node was tagged only with tag:exit-node, it
	// falls back to tag:private so headscale keeps at
	// least one tag (the headscale CLI rejects empty
	// tag sets).
	if err := hs.UntagNode(nodeID, "tag:exit-node"); err != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("untag: "+err.Error()), http.StatusSeeOther)
		return
	}
	hs.InvalidateCache()
	s.Backend.Audit(c.UserID, c.Username, "exit_node_untag",
		fmt.Sprintf("node_id=%d tag=tag:exit-node", nodeID))
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape("Removed tag:exit-node from node."), http.StatusSeeOther)
}

// ensureExitServers walks every headscale node and INSERT
// OR IGNOREs a row in exit_servers for any node that either
// (a) has an exit-node tag, or (b) advertises any routes.
// The "OR IGNORE" preserves the operator's manual row
// (possibly with enabled=0) so the discovery pass can't
// accidentally re-enable a node the operator disabled.
//
// 2026-07-31: v0.32.7 — exclude subnet-routers. Pre-fix
// `ensureExitServers` also matched any node that advertises
// any routes (condition b), which incorrectly included
// per-user subnet-routers (e.g. skygate-subnet-admin with
// tag:subnet-router advertising 10.0.1.0/24). The subnet-router
// is a LAN bridge for the tailnet, not an exit-node — it
// doesn't route traffic to the internet, doesn't have the
// tag:exit-* role, and shouldn't appear on /admin/exit-nodes.
// The fix: also skip nodes whose tags contain
// `tag:subnet-router` (and the `tag:dev-*` family which is
// the per-device v0.28.0 marker for user devices — those
// don't belong on an exit-node admin page either). A
// `tag:public`-only node with subnet routes is still
// included (public-tagged nodes are the relays that may
// legitimately advertise both 0.0.0.0/0 and a /32 set).
// shouldIncludeAsExitServer is the pure filter extracted from
// ensureExitServers (v0.32.7). Returns true if a node with
// the given tags + available-route count should appear on
// /admin/exit-nodes.
//
// Exclusion rules (added 2026-07-31, v0.32.7):
//   - tag:subnet-router → false (it's a LAN bridge, not an exit)
//   - tag:dev-*        → false (per-user device v0.28.0 marker)
//
// Inclusion rules:
//   - any tag:exit-* tag → true
//   - has 1+ advertised route → true
//
// 2026-07-31: extracted from ensureExitServers so the filter
// logic is unit-testable without a live headscale.
func shouldIncludeAsExitServer(tags []string, availableRouteCount int) bool {
	hasExitTag := false
	isSubnetRouter := false
	isPerUserDevice := false
	for _, t := range tags {
		if strings.Contains(t, "exit-node") {
			hasExitTag = true
		}
		if t == "tag:subnet-router" {
			isSubnetRouter = true
		}
		if strings.HasPrefix(t, "tag:dev-") {
			isPerUserDevice = true
		}
	}
	if isSubnetRouter || isPerUserDevice {
		return false
	}
	return hasExitTag || availableRouteCount > 0
}

func (s *Service) ensureExitServers() {
	nodes, err := s.HSGlobalFn().ListAllNodes()
	if err != nil {
		return
	}
	// Index nodes by ID for the cleanup pass below.
	nodeByID := make(map[string]headscale.NodeView, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	// Step 1: insert any node that should be in
	// exit_servers. The "OR IGNORE" preserves the
	// operator's manual row (possibly with enabled=0) so
	// the discovery pass can't accidentally re-enable a
	// node the operator disabled.
	for _, n := range nodes {
		if shouldIncludeAsExitServer(n.Tags, len(n.AvailableRoutes)) {
			db.InsertIgnoreExitServerOnDiscovery(s.DB, n.ID, n.GivenName, strings.Join(n.IPAddresses, ","))
		}
	}
	// Step 2 (v0.32.7): clean up rows that the pre-fix
	// filter would have included but the new one excludes
	// (e.g. skygate-subnet-admin with tag:subnet-router
	// that was inserted into exit_servers before the
	// v0.32.7 fix tightened the filter). Without this,
	// the stale row would keep showing up on
	// /admin/exit-nodes even after the new code excludes
	// it from inserts.
	//
	// We only delete rows whose headscale node still exists
	// (node_id is in our current node list) and now fails
	// the filter. Rows for nodes that disappeared from
	// headscale are operator artifacts (e.g. the user
	// deleted the node from the tailnet) — leave those
	// alone; the operator can `kill` them via the
	// /admin/exit-nodes page or directly in the DB.
	rows, _ := s.DB.Query("SELECT id, node_id FROM exit_servers")
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			var nid string
			if err := rows.Scan(&id, &nid); err != nil {
				continue
			}
			n, ok := nodeByID[nid]
			if !ok {
				continue // node gone from headscale — leave row
			}
			if shouldIncludeAsExitServer(n.Tags, len(n.AvailableRoutes)) {
				continue // still qualifies — keep row
			}
			// Node no longer qualifies — delete the row.
			// Best-effort: errors are logged but not fatal
			// (the next page load will retry).
			if _, err := s.DB.Exec("DELETE FROM exit_servers WHERE id = ?", id); err != nil {
				s.Backend.Audit(0, "skygate", "exit_server_cleanup_failed",
					fmt.Sprintf("node_id=%s id=%d: %v", nid, id, err))
			}
		}
	}
}
