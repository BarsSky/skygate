package handlers

// exit_rules_form_admin.go — admin view of all users' exit rules.
// - AdminExitRules (GET /admin/exit-rules)
//
// Renders admin/exit_rules.html with cross-user hierarchical view
// (grouped by user -> device -> exit_node), recent logs, and ACL
// snapshot history. Local types (AdminRule, devNodeGroup, userGroup)
// are defined inline where used.

import (
	"net/http"
)



func (a *App) AdminExitRules(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	rows, err := a.DB.Query("SELECT r.id, r.user_id, r.device_id, r.exit_node_id, r.target_type, r.target_value, r.action, COALESCE(r.parent_domain,''), r.created_at, r.enabled, COALESCE(r.device_ip,'') as device_ip, COALESCE(u.username,'?') as user_name FROM device_rules r LEFT JOIN portal_users u ON u.id = r.user_id ORDER BY r.id")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer rows.Close()

	type AdminRule struct {
		ID          int
		UserID      int
		UserName    string
		DeviceID    int
		DeviceName  string
		DeviceIP    string
		ExitNode    string
		TargetType  string
		TargetValue string
		Action      string
		ParentDomain string
		CreatedAt   string
	}
	var rr []AdminRule
	for rows.Next() {
		var r AdminRule
		var en int
		if err := rows.Scan(&r.ID, &r.UserID, &r.DeviceID, &r.ExitNode, &r.TargetType, &r.TargetValue, &r.Action, &r.ParentDomain, &r.CreatedAt, &en, &r.DeviceIP, &r.UserName); err != nil {
			continue
		}
		rr = append(rr, r)
	}

	// Resolve device hostnames from headscale API — match by Tailscale IP
	if nodes, e := a.HS.ListAllNodes(); e == nil {
		for i := range rr {
			if rr[i].DeviceIP == "" {
				rr[i].DeviceName = "?"
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
			if rr[i].DeviceName == "" {
				rr[i].DeviceName = "?"
			}
		}
	}

	logRows, _ := a.DB.Query("SELECT version, action, detail, created_at FROM exit_rule_logs ORDER BY id DESC LIMIT 20")
	var logs []map[string]any
	if logRows != nil {
		defer logRows.Close()
		for logRows.Next() {
			var v int
			var a, d, ts string
			if err := logRows.Scan(&v, &a, &d, &ts); err == nil {
				logs = append(logs, map[string]any{"version": v, "action": a, "detail": d, "time": ts})
			}
		}
	}

	snapRows, _ := a.DB.Query("SELECT version, created_by, applied_success, error_msg, created_at FROM acl_snapshots ORDER BY version DESC LIMIT 10")
	var snaps []map[string]any
	if snapRows != nil {
		defer snapRows.Close()
		for snapRows.Next() {
			var v, success int
			var by, errMsg, ts string
			if err := snapRows.Scan(&v, &by, &success, &errMsg, &ts); err == nil {
				snaps = append(snaps, map[string]any{"version": v, "by": by, "success": success == 1, "error": errMsg, "time": ts})
			}
		}
	}

	// 2026-07-07: hierarchical grouping by user -> device -> exit_node
	type devNodeGroup struct {
		DeviceName string
		Count      int
		Nodes      map[string][]AdminRule
	}
	type userGroup struct {
		UserCount  int
		TotalCount int
		UserLimit  int
		LoadPct    int
		Devices    map[int]devNodeGroup
	}
	groupedByUser := map[string]userGroup{}
	totalRules := len(rr)
	totalPct := 0
	if a.Cfg != nil && a.Cfg.MaxTotalRules > 0 {
		totalPct = totalRules * 100 / a.Cfg.MaxTotalRules
	}
	for _, rule := range rr {
		ug, ok := groupedByUser[rule.UserName]
		if !ok {
			ug = userGroup{Devices: map[int]devNodeGroup{}, UserLimit: a.getMaxRulesForUser(rule.UserName)}
		}
		dg, ok := ug.Devices[rule.DeviceID]
		if !ok {
			dg = devNodeGroup{DeviceName: rule.DeviceName, Nodes: map[string][]AdminRule{}}
		}
		dg.Nodes[rule.ExitNode] = append(dg.Nodes[rule.ExitNode], rule)
		dg.Count++
		ug.Devices[rule.DeviceID] = dg
		ug.UserCount++
		ug.TotalCount++
		if ug.UserLimit > 0 {
			ug.LoadPct = ug.UserCount * 100 / ug.UserLimit
		}
		groupedByUser[rule.UserName] = ug
	}
	_ = totalPct

	a.renderWithLayout(w, r, "admin/exit_rules.html", c, map[string]any{
		"Page":          "exit-rules",
		"Title":         "Exit Rules",
		"Rules":         rr,
		"Logs":          logs,
		"Snapshots":     snaps,
		"GroupedByUser": groupedByUser,
		"TotalRules":    totalRules,
		"MaxTotalRules": a.Cfg.MaxTotalRules,
		"LoadPct":       totalPct,
	})
}
