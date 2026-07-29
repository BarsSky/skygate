// Package exit_rules — form_admin.go owns the admin
// cross-user view of all users' exit rules.
//
// refactor-v0.30 Phase B step 4e (2026-07-29): moved
// from internal/handlers/exit_rules_form_admin.go.
// Renders admin/exit_rules.html with cross-user
// hierarchical view (grouped by user -> device ->
// exit_node), recent logs, and ACL snapshot history.
// Local types (AdminRule, devNodeGroup, userGroup)
// are defined inline where used.
package exit_rules

import (
	"net/http"
	"time"

	"skygate/internal/db"
)

// AdminExitRules renders the admin cross-user view.
// GET /admin/exit-rules
func (s *Service) AdminExitRules(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	// 2026-07-11: Этап 9 part 2 — SQL moved to db.GetAllRulesForAdmin
	dbRules, err := db.GetAllRulesForAdmin(s.DB)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	type AdminRule struct {
		ID           int
		UserID       int
		UserName     string
		DeviceID     int
		DeviceName   string
		DeviceIP     string
		ExitNode     string
		TargetType   string
		TargetValue  string
		Action       string
		ParentDomain string
		CreatedAt    string
	}
	var rr []AdminRule
	for _, r := range dbRules {
		rr = append(rr, AdminRule{
			ID:           r.ID,
			UserID:       r.UserID,
			UserName:     r.UserName,
			DeviceID:     r.DeviceID,
			DeviceIP:     r.DeviceIP,
			ExitNode:     r.ExitNodeID,
			TargetType:   r.TargetType,
			TargetValue:  r.TargetValue,
			Action:       r.Action,
			ParentDomain: r.ParentDomain,
			CreatedAt:    time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04"),
		})
	}

	// Resolve device hostnames from headscale API — match by Tailscale IP
	if nodes, e := s.HS.ListAllNodes(); e == nil {
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

	logs := []map[string]any{}
	if recent, err := db.RecentExitRuleLogs(s.DB); err == nil {
		for _, l := range recent {
			logs = append(logs, map[string]any{
				"version": l.Version,
				"action":  l.Action,
				"detail":  l.Detail,
				"time":    db.ExitRuleLogTime(l.CreatedAt),
			})
		}
	}

	snaps := []map[string]any{}
	if recent, err := db.RecentACLSnapshots(s.DB); err == nil {
		for _, s := range recent {
			success := false
			if s.AppliedSuccess.Valid && s.AppliedSuccess.Int64 == 1 {
				success = true
			}
			snaps = append(snaps, map[string]any{
				"version": s.Version,
				"by":      s.CreatedBy,
				"success": success,
				"error":   s.ErrorMsg,
				"time":    db.ExitRuleLogTime(s.CreatedAt),
			})
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
	maxTotal := 0
	if s.Cfg != nil {
		maxTotal = s.Cfg.MaxTotalRules
	}
	if maxTotal > 0 {
		totalPct = totalRules * 100 / maxTotal
	}
	for _, rule := range rr {
		ug, ok := groupedByUser[rule.UserName]
		if !ok {
			ug = userGroup{Devices: map[int]devNodeGroup{}, UserLimit: s.getMaxRulesForUser(rule.UserName)}
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

	s.Backend.RenderWithLayout(w, r, "admin/exit_rules.html", c, map[string]any{
		"Page":          "exit-rules",
		"Title":         "Exit Rules",
		"Rules":         rr,
		"Logs":          logs,
		"Snapshots":     snaps,
		"GroupedByUser": groupedByUser,
		"TotalRules":    totalRules,
		"MaxTotalRules": maxTotal,
		"LoadPct":       totalPct,
	})
}
