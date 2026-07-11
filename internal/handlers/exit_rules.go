package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)




type DeviceRule struct {
	ID           int
	UserID       int
	DeviceID     int
	DeviceName   string
	ExitNodeID   string
	TargetType   string
	TargetValue  string
	Action       string
	DeviceIP     string
	Enabled      bool
	ParentDomain string
}



// 2026-07-07: issue #5 — dedup protection.
// Returns:
//   (true, existingID) — rule already existed; do not re-insert.
//   (true, 0)          — new rule inserted successfully.
//   (false, 0)         — DB error.
func (a *App) insertRuleUnique(userID int64, deviceID int, exitNode, targetType, targetValue, action, deviceIP string) (bool, int) {
	var existingID int
	err := a.DB.QueryRow(
		"SELECT id FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type=? AND target_value=? LIMIT 1",
		userID, deviceID, exitNode, targetType, targetValue).Scan(&existingID)
	if err == nil {
		return true, existingID
	}
	// not found → insert. Set parent_domain = target_value for domain rules so
	// autoupdater can track them and UI can show "auto" badge.
	parentDomain := ""
	if targetType == "domain" {
		parentDomain = targetValue
	}
	res, err := a.DB.Exec(
		"INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		userID, deviceID, exitNode, targetType, targetValue, action, deviceIP, parentDomain)
	if err != nil {
		return false, 0
	}
	newID, _ := res.LastInsertId()
	return true, int(newID)
}

func scanRules(rows *sql.Rows) ([]DeviceRule, error) {
	var rr []DeviceRule
	for rows.Next() {
		var r DeviceRule
		var en int
		var pd string
		if err := rows.Scan(&r.ID, &r.UserID, &r.DeviceID, &r.ExitNodeID, &r.TargetType, &r.TargetValue, &r.Action, &r.DeviceIP, &en, &pd); err != nil {
			return nil, err
		}
		r.Enabled = en == 1
		r.ParentDomain = pd
		rr = append(rr, r)
	}
	return rr, rows.Err()
}

func (a *App) getDeviceRules(userID int) ([]DeviceRule, error) {
	rows, err := a.DB.Query("SELECT d.id, d.user_id, d.device_id, d.exit_node_id, d.target_type, d.target_value, COALESCE(d.action,'accept') as action, COALESCE(d.device_ip,'') as device_ip, d.enabled, COALESCE(d.parent_domain,'') as parent_domain FROM device_rules d WHERE d.user_id = ? ORDER BY d.id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rr, err := scanRules(rows)
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
	rows, err := a.DB.Query("SELECT id, hostname, last_seen FROM devices WHERE user_id = ? ORDER BY hostname", userID)
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
	if len(dd) == 0 {
		if nodes, err := a.HS.NodeList(); err == nil {
			for _, n := range nodes {
				dd = append(dd, map[string]any{"id": n["id"], "hostname": n["hostname"], "is_hs": true})
			}
		}
	}
	return dd, rows.Err()
}

// GenerateACL builds valid headscale 0.29 HuJSON.
// ACL controls ACCESS (not routing). Exit-node selection is client-side.
// When exit rules exist, per-device rules are added for audit/restriction,
// but routing is controlled via the route setup script (see GenerateRouteSetupScript).
func (a *App) GenerateACL() (string, error) {
	// Build per-user ACL. Each portal user gets one rule that lets their
	// own devices reach each other; no user can see another user's
	// tag:private devices. Public/exit-node rules at the bottom let
	// everyone reach internet through the exit-nodes.
	rows, err := a.DB.Query(`SELECT target_type, target_value, action, COALESCE(device_ip, '') as device_ip FROM device_rules WHERE enabled = 1`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	type ruleEntry struct {
		deviceIP string
		target   string
		action   string
	}
	var entries []ruleEntry
	for rows.Next() {
		var tt, tv, action, dip string
		if err := rows.Scan(&tt, &tv, &action, &dip); err != nil {
			return "", err
		}
		if tt == "subnet" || tt == "ip" {
			entries = append(entries, ruleEntry{deviceIP: dip, target: tv, action: action})
		}
	}

	// Build the list of headscale user identities from portal_users.
	// tagOwners requires user@domain form. We hard-code the headscale
	// base_domain ("tsnet.skynas.ru") for now — it is the only deployment.
	const baseDomain = "tsnet.skynas.ru"
	userRows, err := a.DB.Query(`SELECT username FROM portal_users ORDER BY id`)
	if err != nil {
		return "", err
	}
	defer userRows.Close()
	var identities []string
	for userRows.Next() {
		var uname string
		if err := userRows.Scan(&uname); err == nil && uname != "" {
			identities = append(identities, uname+"@"+baseDomain)
		}
	}
	if identities == nil {
		identities = []string{}
	}

	var sb strings.Builder
	sb.WriteString("{\n  \"acls\": [\n")

	// Per-user rule: user can reach their OWN devices only.
	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		}
		sb.WriteString("    { \"action\": \"accept\", \"src\": [\"" + idn + "\"], \"dst\": [\"" + idn + ":*\"] }")
	}

	// Informational/audit per-device exit-rules (DNS, telegram IPs, etc).
	// These come AFTER the per-user rules so that the per-user "self-only"
	// rule wins for actual user traffic. The exit-rule targets are still
	// reachable because nothing filters them out by user identity — they
	// are routed via the per-device * 100.64.0.0/10 lookup at the SRC IP.
	for _, e := range entries {
		src := "\"*\""
		if e.deviceIP != "" {
			src = fmt.Sprintf("\"%s\"", e.deviceIP)
		}
		sb.WriteString(",\n    { \"action\": \"" + e.action + "\", \"src\": [" + src + "], \"dst\": [\"" + e.target + ":*\"] }")
	}

	// tag:public (shared exit-nodes) and tag:exit-node are visible to
	// everyone so users can pick an exit-node and others can see status
	// servers if needed.
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"tag:public:*\"] }")
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"tag:exit-node:*\"] }")

	// Internet egress: each device can reach the internet directly (Tailscale
	// uses the device\u0027s own routing when no exit-node is selected).
	sb.WriteString(",\n    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"*:*\"] }")

	sb.WriteString("\n  ],\n")

	// tagOwners: every portal user is an owner of tag:private for their own
	// devices. tag:public and tag:exit-node remain admin-only (skyadmin)
	// because those usually correspond to shared infra decisions.
	sb.WriteString("  \"tagOwners\": {\n")
	sb.WriteString("    \"tag:public\": [\"skyadmin@" + baseDomain + "\"]\n")
	if len(identities) > 1 {
		sb.WriteString(",\n    \"tag:private\": [" + strings.Join(quoteAll(identities), ",") + "]\n")
	} else {
		sb.WriteString(",\n    \"tag:private\": [\"" + (identities[0]) + "\"]\n")
	}
	sb.WriteString("  },\n")

	// groups: one per portal user so future per-group rules can reference them.
	sb.WriteString("  \"groups\": {\n")
	for i, idn := range identities {
		if i > 0 {
			sb.WriteString(",\n")
		}
		// group:skyadmin etc.
		parts := strings.SplitN(idn, "@", 2)
		groupName := "group:" + parts[0]
		sb.WriteString("    \"" + groupName + "\": [\"" + idn + "\"]")
	}
	sb.WriteString("\n  },\n")

	sb.WriteString("  \"ssh\": [\n")
	sb.WriteString("    {\n")
	sb.WriteString("      \"action\": \"accept\",\n")
	sb.WriteString("      \"src\": [\"tag:private\", \"skyadmin@" + baseDomain + "\"],\n")
	sb.WriteString("      \"dst\": [\"tag:exit-node\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    }\n")
	sb.WriteString("  ]\n")

	sb.WriteString("}")
	return sb.String(), nil
}

func quoteAll(ss []string) []string {
	res := make([]string, len(ss))
	for i, s := range ss {
		res[i] = strconv.Quote(s)
	}
	return res
}

func (a *App) saveACLSnapshot(config, username string) int {
	var maxVer int
	a.DB.QueryRow("SELECT COALESCE(MAX(version),0) FROM acl_snapshots").Scan(&maxVer)
	ver := maxVer + 1
	a.DB.Exec("INSERT INTO acl_snapshots (version, config, created_by, applied_success) VALUES (?, ?, ?, 1)", ver, config, username)
	if a.Notifier != nil {
		go a.Notifier.SendTelegram(fmt.Sprintf("🛡️ ACL #%d by %s\nLength: %d bytes", ver, username, len(config)))
	}
	return ver
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
	rows, _ := a.DB.Query("SELECT DISTINCT exit_node_id FROM device_rules WHERE enabled=1 AND exit_node_id != ''")
	exitNodeSet := map[string]bool{}
	if rows != nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil { exitNodeSet[n] = true }
		}
		rows.Close()
	}
	// Also add known exit_servers
	serverRows, _ := a.DB.Query("SELECT name FROM exit_servers WHERE enabled=1")
	if serverRows != nil {
		for serverRows.Next() {
			var n string
			if serverRows.Scan(&n) == nil { exitNodeSet[n] = true }
		}
		serverRows.Close()
	}
	for name := range exitNodeSet {
		nl := NodeLoad{Name: name}
		a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE enabled=1 AND exit_node_id=?", name).Scan(&nl.RuleCount)
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
		var lastSync time.Time
		a.DB.QueryRow("SELECT COALESCE(MAX(created_at), '1970-01-01') FROM exit_rule_logs WHERE action='sync' AND detail LIKE ?", "%"+name+"%").Scan(&lastSync)
		if !lastSync.IsZero() && lastSync.Year() > 2000 {
			nl.LastSync = lastSync.Format("2006-01-02 15:04:05")
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

