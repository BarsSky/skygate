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
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// AdminExitRules renders the admin cross-user view.
// GET /admin/exit-rules[?device=NAME]
//
// 2026-08-06: ?device=NAME filter for the per-device "dead rules"
// drill-down from /admin/devices. The /admin/devices page shows
// a per-device dead-rule count badge (added in v0.33.1.17); the
// badge links to /admin/exit-rules?device=NAME and this handler
// filters the view to that device's rules only.
//
// Behaviour:
//   - no query param        → all rules across all users
//                             (the original v0.16.x behaviour)
//   - ?device=NAME present  → only rules whose device_id maps to
//                             a node_owner_map row with hostname
//                             = NAME (case-insensitive). The
//                             template shows a banner with the
//                             filter name and a "show all" link.
//   - ?device=NAME not found → empty result set + banner. The
//                             handler does NOT http.StatusNotFound — the
//                             "device not found" case is
//                             indistinguishable from "device
//                             exists but has no rules" from the
//                             operator's perspective.
func (s *Service) AdminExitRules(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Read the filter FIRST (before any DB work) so we can
	// pass it to the data map for the template banner.
	deviceFilter := strings.TrimSpace(r.URL.Query().Get("device"))
	// 2026-07-11: Этап 9 part 2 — SQL moved to db.GetAllRulesForAdmin
	var dbRules []db.DeviceRule
	var err error
	if deviceFilter != "" {
		// 2026-08-06: per-device drill-down. The
		// `LEFT JOIN node_owner_map` in
		// qSelectAllRulesForAdminByDevice handles the
		// "device deleted but rules still present" edge
		// case (the rule is returned with a NULL
		// hostname, which won't match the LOWER() filter,
		// so it's correctly excluded from the result).
		dbRules, err = db.GetAllRulesForAdminByDevice(s.DB, deviceFilter)
	} else {
		dbRules, err = db.GetAllRulesForAdmin(s.DB)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	// 2026-08-06: per-(user, device) preferred exit-node pref
	// lookup. The admin template uses this to flag "dead rules"
	// — rules whose exit_node_id doesn't match the device's
	// preferred exit-node. Cross-user view; we batch by
	// (user_id, hostname) so each rule's preferred hostname
	// is O(1).
	prefByUserHost := map[string]string{} // "userID:hostname" → preferred host
	for _, rule := range rr {
		key := strconv.FormatInt(int64(rule.UserID), 10) + ":" + strings.ToLower(rule.DeviceName)
		if _, ok := prefByUserHost[key]; ok {
			continue
		}
		pref, _ := PreferredExitNodeForRule(s.DB, int64(rule.UserID), rule.DeviceName)
		prefByUserHost[key] = pref
	}
	// Annotate each rule with PreferredHost + Applicable. The
	// template renders a warning icon for Applicable=false.
	type AnnotatedRule struct {
		AdminRule
		PreferredHost string
		Applicable    bool
	}
	annotated := make([]AnnotatedRule, 0, len(rr))
	totalMismatch := 0
	for _, r := range rr {
		key := strconv.FormatInt(int64(r.UserID), 10) + ":" + strings.ToLower(r.DeviceName)
		pref := prefByUserHost[key]
		ok := IsRuleApplicable(r.ExitNode, pref)
		if !ok {
			totalMismatch++
		}
		annotated = append(annotated, AnnotatedRule{
			AdminRule:     r,
			PreferredHost: pref,
			Applicable:    ok,
		})
	}
	// Rebuild the hierarchical view with the annotated rules so
	// the template can read .Applicable on each row.
	groupedByUserAnnotated := map[string]map[int]devNodeGroup{}
	for _, ar := range annotated {
		ug, ok := groupedByUserAnnotated[ar.UserName]
		if !ok {
			ug = map[int]devNodeGroup{}
		}
		dg, ok := ug[ar.DeviceID]
		if !ok {
			dg = devNodeGroup{DeviceName: ar.DeviceName, Nodes: map[string][]AdminRule{}}
		}
		dg.Nodes[ar.ExitNode] = append(dg.Nodes[ar.ExitNode], ar.AdminRule)
		dg.Count++
		ug[ar.DeviceID] = dg
		groupedByUserAnnotated[ar.UserName] = ug
	}
	_ = groupedByUserAnnotated // (the GroupedByUser below is the legacy form; template can use either)

	s.Backend.RenderWithLayout(w, r, "admin/exit_rules.html", c, map[string]any{
		"Page":          "exit-rules",
		"Title":         "Exit Rules",
		"Rules":         rr,
		"RulesAnnotated": annotated,
		"Logs":          logs,
		"Snapshots":     snaps,
		"GroupedByUser": groupedByUser,
		"TotalRules":    totalRules,
		"MaxTotalRules": maxTotal,
		"LoadPct":       totalPct,
		// 2026-08-06: cross-check counter — admin sees the total
		// dead-rule count at the top of the page. Click to
		// filter the table to only-applicable vs only-mismatch
		// (the template renders a toggle).
		"MismatchCount": totalMismatch,
		// 2026-08-06: per-device filter state. Non-empty
		// when the operator clicked a "dead rules" badge on
		// /admin/devices. The template renders a banner
		// ("filtered to device X, show all") and keeps the
		// rule count scoped to this device only.
		"DeviceFilter":   deviceFilter,
		"DeviceRuleCount": len(rr),
	})
}
