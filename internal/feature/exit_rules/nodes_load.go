// Package exit_rules — nodes_load.go owns the admin
// "Node Load" dashboard.
//
// refactor-v0.30 Phase B step 4g (2026-07-29): moved from
// internal/handlers/exit_rules.go. The remaining handlers
// in that file (insertRuleUnique, getDeviceRules,
// getUserDevices, saveACLSnapshot, generateACL,
// getMaxRulesForUser, apiRule struct) had already moved
// to store.go / api.go in step 4e+4f. GetAdminNodesLoad
// was the last hold-out because it was the only handler
// in exit_rules.go that hadn't been extracted to a
// dedicated file.
//
// Admin-only. Renders admin/exit_rules_nodes.html with
// per-exit-node rule counts + load percentages.
package exit_rules

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"skygate/internal/db"
)

// GetAdminNodesLoad renders the admin "Node Load"
// dashboard. Admin-only. Renders
// admin/exit_rules_nodes.html with per-exit-node rule
// counts + load percentages + last-sync timestamps.
//
// GET /admin/exit-rules/nodes
func (s *Service) GetAdminNodesLoad(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Collect per-exit-node metrics
	type NodeLoad struct {
		Name            string
		ApprovedRoutes  int
		AvailableRoutes int
		RuleCount       int
		LastSync        string
		LoadPct         int
	}
	var nodes []NodeLoad
	maxPerNode := 0
	if s.Cfg != nil {
		maxPerNode = s.Cfg.MaxRulesPerDevice * 5 // heuristic: total rules / 5 nodes
	}
	if maxPerNode == 0 {
		maxPerNode = 1000
	}
	// Get distinct exit_nodes from device_rules
	// 2026-07-11: Этап 9 part 2 — moved to db.ListDistinctExitNodesWithRules
	exitNodeNames, _ := db.ListDistinctExitNodesWithRules(s.DB)
	exitNodeSet := map[string]bool{}
	for _, n := range exitNodeNames {
		exitNodeSet[n] = true
	}
	// Also add known exit_servers.
	// 2026-07-12: Этап 10 part 5 — moved to db.ListEnabledExitServerHostnames.
	// BUG FIX in passing: the previous inline query was
	//   `SELECT name FROM exit_servers WHERE enabled=1`
	// which referenced a `name` column that has never existed in any
	// migration (the table has `hostname`). The result was being
	// silently dropped (`serverRows, _ := a.DB.Query(...)`), so the
	// dashboard never showed admin-curated exit-nodes that had no
	// device_rules. ListEnabledExitServerHostnames queries the right
	// column and surfaces any error to the caller.
	if names, err := db.ListEnabledExitServerHostnames(s.DB); err == nil {
		for _, n := range names {
			exitNodeSet[n] = true
		}
	}
	for name := range exitNodeSet {
		nl := NodeLoad{Name: name}
		// 2026-07-11: Этап 9 part 2 — moved to db.CountRulesForExitNode
		nl.RuleCount, _ = db.CountRulesForExitNode(s.DB, name)
		// Get from headscale
		// Find node by hostname
		if allNodes, err := s.HS.ListAllNodes(); err == nil {
			for _, n := range allNodes {
				if strings.EqualFold(n.Hostname, name) || strings.EqualFold(n.GivenName, name) {
					nl.AvailableRoutes = len(n.AvailableRoutes)
					// ApprovedRoutes not in NodeView — show 0 or call separate API
					nl.ApprovedRoutes = nl.AvailableRoutes // approximation
					break
				}
			}
		}
		nl.LoadPct = nl.RuleCount * 100 / maxPerNode
		// Last sync: find most recent log
		ts, _ := db.LastSyncForExitNode(s.DB, name)
		if ts > 0 {
			nl.LastSync = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
		} else {
			nl.LastSync = "никогда"
		}
		nodes = append(nodes, nl)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].LoadPct > nodes[j].LoadPct })
	totalRules := 0
	for _, n := range nodes {
		totalRules += n.RuleCount
	}
	maxTotal := 0
	if s.Cfg != nil {
		maxTotal = s.Cfg.MaxTotalRules
	}
	loadPct := 0
	if maxTotal > 0 {
		loadPct = totalRules * 100 / maxTotal
	}
	s.Backend.RenderWithLayout(w, r, "admin/exit_rules_nodes.html", c, map[string]any{
		"Page":          "exit-rules-nodes",
		"Title":         "Node Load",
		"Nodes":         nodes,
		"TotalRules":    totalRules,
		"MaxTotalRules": maxTotal,
		"LoadPct":       loadPct,
	})
}
