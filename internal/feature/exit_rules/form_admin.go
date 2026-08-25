// Package exit_rules — form_admin.go owns the admin
// cross-user view of all users' exit rules.
//
// refactor-v0.30 Phase B step 4e (2026-07-29): moved
// from internal/handlers/exit_rules_form_admin.go.
// Renders admin/exit_rules.html with cross-user
// hierarchical view (grouped by user -> device ->
// exit_node), recent logs, and ACL snapshot history.
//
// 2026-08-25 (B178): AdminRule now carries the preferred
// exit-node hostname + a per-rule "Applicable" flag, so the
// template can render a "dead rule" badge without doing a
// O(n*m) inner lookup. The pre-B178 design (a parallel
// []AnnotatedRule + a template inner range) had a Go-template
// `.`-rebind bug that always leaked the last annotated
// rule's PreferredHost into every visible row — live-verified
// for michail/basic (UserID=6, DeviceID=29) which
// `device_exit_node_prefs` pins to "emilia" but the rendered
// HTML was showing "karolina".
package exit_rules

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// 2026-08-25 (B178): AdminRule is now a package-level type
// (was a local closure type inside AdminExitRules before
// B178) so annotateRulesWithPrefs can take it by reference.
// Fields match the DB column names + post-B178 pref fields.
type AdminRule struct {
	ID            int
	UserID        int
	UserName      string
	DeviceID      int
	DeviceName    string
	DeviceIP      string
	ExitNode      string
	TargetType    string
	TargetValue   string
	Action        string
	ParentDomain  string
	CreatedAt     string
	// 2026-08-25 (B178): preferred exit-node hostname for
	// this (user, device) pair. Empty when no per-device /
	// per-user pref is set. The admin template renders a
	// ✅ when rule.ExitNode == PreferredHost (the rule
	// takes effect), or a ⚠️ + the preferred hostname
	// when they differ (the "dead rule" case).
	PreferredHost string
	// 2026-08-25 (B178): convenience — true iff the rule
	// is "live" given the device's preferred exit-node.
	// Same semantics as IsRuleApplicable(ExitNode,
	// PreferredHost). Template can use it directly.
	Applicable bool
}

// 2026-08-25 (B178): annotateRulesWithPrefs fills in the
// PreferredHost + Applicable fields for every rule in rr,
// in place, and returns the total number of "dead rules"
// (where Applicable=false).
//
// Why a helper, not inline in the handler: this is the
// regression-bearing code path (the original B178 bug was
// hidden in a template that did a Go-template `.`-rebind
// lookup; this function is the testable replacement). Pulling
// it out into a small pure-ish function makes the basic/
// karolina regression easy to pin with a unit test.
//
// The preferred-host lookup is taken as a callback (prefFn)
// so unit tests don't need a real DB — they pass a stub
// that returns whatever hostname the test wants.
func annotateRulesWithPrefs(rr []AdminRule, prefFn func(userID int64, hostname string) string) int {
	// Batch by (userID, hostname) — one lookup per unique
	// (user, device), not per rule. For 324 rules covering
	// 3 unique (user, host) pairs, that's 3 lookups instead
	// of 324.
	prefByUserHost := map[string]string{} // "userID:hostname" → preferred host
	for _, rule := range rr {
		hn := strings.ToLower(strings.TrimSpace(rule.DeviceName))
		if hn == "" || hn == "?" {
			continue
		}
		key := strconv.FormatInt(int64(rule.UserID), 10) + ":" + hn
		if _, ok := prefByUserHost[key]; ok {
			continue
		}
		prefByUserHost[key] = prefFn(int64(rule.UserID), hn)
	}
	mismatch := 0
	for i := range rr {
		hn := strings.ToLower(strings.TrimSpace(rr[i].DeviceName))
		pref := ""
		if hn != "" && hn != "?" {
			pref = prefByUserHost[strconv.FormatInt(int64(rr[i].UserID), 10)+":"+hn]
		}
		rr[i].PreferredHost = pref
		rr[i].Applicable = IsRuleApplicable(rr[i].ExitNode, pref)
		if !rr[i].Applicable {
			mismatch++
		}
	}
	return mismatch
}

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

	var rr []AdminRule
	for _, r := range dbRules {
		rr = append(rr, AdminRule{
			ID:           r.ID,
			UserID:       r.UserID,
			UserName:     r.UserName,
			DeviceID:     r.DeviceID,
			DeviceName:   r.DeviceName,
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

	// 2026-08-25 (B178): per-(user, device) preferred exit-node
	// pref lookup, annotating each rule with PreferredHost +
	// Applicable. The admin template uses these to flag "dead
	// rules" — rules whose exit_node_id doesn't match the
	// device's preferred exit-node.
	//
	// IMPORTANT: this MUST run BEFORE the groupedByUser build
	// below. The grouping loop COPIES each AdminRule into the
	// Nodes map (`dg.Nodes[rule.ExitNode] = append(..., rule)`),
	// so annotations set AFTER the grouping are lost — the
	// copies in groupedByUser still have empty PreferredHost.
	// The first deployment of B178 ran annotate AFTER grouping
	// and ALL 325 rules rendered with "No preferred exit-node
	// set" — the headscale resolution worked (DeviceName was
	// populated) and the prefFn returned the right values
	// (verified by B178-DBG log lines in skygate stderr), but
	// the template reads from groupedByUser which had the
	// unannotated copies.
	//
	// Pre-B178 architecture: built a parallel `[]AnnotatedRule`
	// slice + `groupedByUserAnnotated` map, passed both to the
	// template as `Rules` + `RulesAnnotated`, and the template
	// did an O(n*m) inner `range` to look up each rule's
	// annotation. That template was BROKEN due to a Go-template
	// `.`-rebind bug: inside the inner `{{range $ar :=
	// $.RulesAnnotated}}`, `.` was rebound to `$ar` (the inner
	// iteration), so `{{if eq $ar.ID .ID}}` was effectively
	// `{{if eq $ar.ID $ar.ID}}` — always true. The lookup
	// overwrote $pref on every iteration, ending up with the
	// LAST annotated rule's PreferredHost (skyworker/karolina)
	// for every rule on the page. Live-verified: the rendered
	// HTML showed "karolina" for basic's rules (UserID=6,
	// DeviceID=29) even though `device_exit_node_prefs` had
	// `michail/basic → tag:exit-emilia` and
	// `PreferredExitNodeForRule(s.DB, 6, "basic")` returned
	// "emilia" correctly.
	//
	// B178 fix: collapse the annotated slice into AdminRule
	// itself (PreferredHost + Applicable fields), drop the
	// inner template lookup, and let the template read
	// `.PreferredHost` directly. O(n) lookups total, no
	// template scope traps. The dead `groupedByUserAnnotated`
	// map is also removed — it was never used by the
	// template (the template reads `GroupedByUser`, which is
	// the unannotated form).
	totalMismatch := annotateRulesWithPrefs(rr, func(uid int64, hn string) string {
		pref, _ := PreferredExitNodeForRule(s.DB, uid, hn)
		return pref
	})

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
		"Page":           "exit-rules",
		"Title":          "Exit Rules",
		"Rules":          rr,
		"Logs":           logs,
		"Snapshots":      snaps,
		"GroupedByUser":  groupedByUser,
		"TotalRules":     totalRules,
		"MaxTotalRules":  maxTotal,
		"LoadPct":        totalPct,
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
		"DeviceFilter":    deviceFilter,
		"DeviceRuleCount": len(rr),
	})
}
