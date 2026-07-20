package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/db"
)




// DeviceRule is the handlers-package alias of db.DeviceRule. The
// db layer is the canonical source; this alias lets templates and
// other handlers code use the unqualified name without an import.
//
// 2026-07-11: Этап 9 part 2 — the struct moved to internal/db
// (where the SQL that fills it lives). UserID widened to int64 to
// match the Go-native SQLite INTEGER type and auth.Claims.UserID.
type DeviceRule = db.DeviceRule



// 2026-07-07: issue #5 — dedup protection.
// Returns:
//   (true, existingID) — rule already existed; do not re-insert.
//   (true, 0)          — new rule inserted successfully.
//   (false, 0)         — DB error.
//
// 2026-07-11: Этап 9 part 2 — the SELECT-then-INSERT pattern is now
// composed of db.FindDeviceRuleID + db.AppendDeviceRule so the SQL
// strings live in queries.go. Behaviour is unchanged.
func (a *App) insertRuleUnique(userID int64, deviceID int, exitNode, targetType, targetValue, action, deviceIP string) (bool, int) {
	existingID, err := db.FindDeviceRuleID(a.DB, userID, deviceID, exitNode, targetType, targetValue)
	if err == nil {
		return true, existingID
	}
	if !errors.Is(err, db.ErrNotFound) {
		return false, 0
	}
	// not found → insert. Set parent_domain = target_value for domain rules so
	// autoupdater can track them and UI can show "auto" badge.
	parentDomain := ""
	if targetType == "domain" {
		parentDomain = targetValue
	}
	newID, err := db.AppendDeviceRule(a.DB, userID, deviceID, exitNode, targetType, targetValue, action, deviceIP, parentDomain)
	if err != nil {
		return false, 0
	}
	return true, int(newID)
}

func (a *App) getDeviceRules(userID int64) ([]DeviceRule, error) {
	// 2026-07-11: Этап 9 part 2 — moved to db.GetDeviceRulesForUser.
	// The DeviceName field still needs a headscale IP-to-hostname
	// lookup, which is App-level, so that part stays here.
	rr, err := db.GetDeviceRulesForUser(a.DB, userID)
	if err != nil {
		return nil, err
	}
	// Resolve device hostnames from headscale API — match by Tailscale IP
	if nodes, e := a.HS.ListAllNodes(); e == nil {
		for i := range rr {
			if rr[i].DeviceIP == "" {
				continue
			}
			for _, n := range nodes {
				found := false
				for _, ip := range n.IPAddresses {
					if ip == rr[i].DeviceIP {
						hn := n.GivenName
						if hn == "" {
							hn = n.Hostname
						}
						rr[i].DeviceName = hn
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}
	}
	return rr, nil
}

func (a *App) getUserDevices(userID int) ([]map[string]any, error) {
	// 2026-07-11: Этап 9 part 2 — SQL moved to db.GetUserDevicesForUser
	// (which returns rows that we still scan here for the shape
	// /my/exit-rules wants). The HS fallback for "user has no rows
	// in the devices table yet" stays in this method because it
	// requires a.HS.
	rows, err := a.DB.Query(db.QSelectUserDevices, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dd []map[string]any
	for rows.Next() {
		var id int
		var hn string
		var ls sql.NullInt64
		if err := rows.Scan(&id, &hn, &ls); err != nil {
			return nil, err
		}
		m := map[string]any{"id": id, "hostname": hn}
		if ls.Valid {
			m["last_seen"] = time.Unix(ls.Int64, 0).Format("2006-01-02 15:04")
		}
		dd = append(dd, m)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if len(dd) == 0 {
		if nodes, err := a.HS.NodeList(); err == nil {
			for _, n := range nodes {
				dd = append(dd, map[string]any{"id": n["id"], "hostname": n["hostname"], "is_hs": true})
			}
		}
	}
	return dd, nil
}

// GenerateACL builds valid headscale 0.29 HuJSON.
// ACL controls ACCESS (not routing). Exit-node selection is client-side.
// When exit rules exist, per-device rules are added for audit/restriction,
// but routing is controlled via the route setup script (see GenerateRouteSetupScript).
//
// 2026-07-13: Этап 11 part 2b — this method is now a thin
// wrapper around internal/acl.GenerateACL, which is a free
// function so the telegram bot (which has no *App reference)
// can reuse the same logic. Body and behaviour are unchanged.
//
// 2026-07-20: v0.19.0 — passes a.HSGlobal() so the
// `exitnode.skygate-subnet-<user>` DNS records are
// populated (GenerateACLForPlane needs the headscale
// client to look up exit-node IPs).
func (a *App) GenerateACL() (string, error) {
	return acl.GenerateACL(a.DB, a.HSGlobal())
}

// saveACLSnapshot persists one acl_snapshots row and returns the
// new version. 2026-07-13: Этап 11 part 2b — now a thin wrapper
// around internal/acl.SaveACLSnapshot. The App.Notifier is passed
// as the Alerter (it satisfies the interface implicitly via
// SendAlert). When App.Notifier is nil the free function skips
// the alert, matching the previous behaviour.
func (a *App) saveACLSnapshot(config, username string) int {
	return acl.SaveACLSnapshot(a.DB, config, username, a.Notifier)
}

// HTML form handlers split across:
//   exit_rules_form_my.go        — GetMyExitRules, PostMyExitRule, PostDeleteExitRule
//   exit_rules_form_admin.go     — AdminExitRules
//   exit_rules_form_rollback.go  — PostAdminRollbackACL
// --- JSON API for AI assistant integration ---

// apiRule is the JSON structure for rule creation/listing.
type apiRule struct {
	ID          int    `json:"id,omitempty"`
	DeviceID    int    `json:"device_id"`
	DeviceName  string `json:"device_name,omitempty"`
	ExitNode    string `json:"exit_node"`
	TargetType  string `json:"target_type"`  // "ip", "subnet", "domain"
	TargetValue string `json:"target_value"`
	Action      string `json:"action"`        // "accept" or "deny"
	DeviceIP    string `json:"device_ip,omitempty"`
}

// REST API handlers moved to exit_rules_api.go.
// (GetExitRulesAPI, PostExitRulesAPI, GetExitRulesAPIHelp)
// GetAdminNodesLoad renders the admin node load dashboard.
// GET /admin/exit-rules/nodes
func (a *App) GetAdminNodesLoad(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Collect per-exit-node metrics
	type NodeLoad struct {
		Name           string
		ApprovedRoutes int
		AvailableRoutes int
		RuleCount      int
		LastSync       string
		LoadPct        int
	}
	var nodes []NodeLoad
	maxPerNode := a.Cfg.MaxRulesPerDevice * 5 // heuristic: total rules / 5 nodes
	if maxPerNode == 0 { maxPerNode = 1000 }
	// Get distinct exit_nodes from device_rules
	// 2026-07-11: Этап 9 part 2 — moved to db.ListDistinctExitNodesWithRules
	exitNodeNames, _ := db.ListDistinctExitNodesWithRules(a.DB)
	exitNodeSet := map[string]bool{}
	for _, n := range exitNodeNames {
		exitNodeSet[n] = true
	}
	// Also add known exit_servers
	// 2026-07-12: Этап 10 part 5 — moved to db.ListEnabledExitServerHostnames.
	// BUG FIX in passing: the previous inline query was
	//   `SELECT name FROM exit_servers WHERE enabled=1`
	// which referenced a `name` column that has never existed in any
	// migration (the table has `hostname`). The result was being
	// silently dropped (`serverRows, _ := a.DB.Query(...)`), so the
	// dashboard never showed admin-curated exit-nodes that had no
	// device_rules. ListEnabledExitServerHostnames queries the right
	// column and surfaces any error to the caller.
	if names, err := db.ListEnabledExitServerHostnames(a.DB); err == nil {
		for _, n := range names {
			exitNodeSet[n] = true
		}
	}
	for name := range exitNodeSet {
		nl := NodeLoad{Name: name}
		// 2026-07-11: Этап 9 part 2 — moved to db.CountRulesForExitNode
		nl.RuleCount, _ = db.CountRulesForExitNode(a.DB, name)
		// Get from headscale
		// Find node by hostname
		if allNodes, err := a.HS.ListAllNodes(); err == nil {
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
		ts, _ := db.LastSyncForExitNode(a.DB, name)
		if ts > 0 {
			nl.LastSync = time.Unix(ts, 0).Format("2006-01-02 15:04:05")
		} else {
			nl.LastSync = "никогда"
		}
		nodes = append(nodes, nl)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].LoadPct > nodes[j].LoadPct })
	totalRules := 0
	for _, n := range nodes { totalRules += n.RuleCount }
	loadPct := 0
	if a.Cfg != nil && a.Cfg.MaxTotalRules > 0 {
		loadPct = totalRules * 100 / a.Cfg.MaxTotalRules
	}
	a.renderWithLayout(w, r, "admin/exit_rules_nodes.html", c, map[string]any{
		"Page":         "exit-rules-nodes",
		"Title":        "Node Load",
		"Nodes":        nodes,
		"TotalRules":   totalRules,
		"MaxTotalRules": a.Cfg.MaxTotalRules,
		"LoadPct":      loadPct,
	})
}

