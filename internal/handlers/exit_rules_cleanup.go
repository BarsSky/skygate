package handlers

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
)

// CleanupPlan summarises what CleanupRulesAnalyze found and what would
// change if CleanupRulesApply were run.
type CleanupPlan struct {
	TotalRules     int      // rows in device_rules
	DistinctDevIDs int      // unique device_id values before cleanup
	DistinctHosts  int      // unique hostname groups after analysis
	ResolvedIPs    int      // rules whose device_ip would be backfilled
	MergedIDs      int      // rules whose device_id would be reassigned
	Groups         []CleanupHostnameGroup
	StaleDevIDs    []int // device_ids absent from headscale
	DeviceIDToHost map[int]string
}

// CleanupHostnameGroup is one hostname bucket.
type CleanupHostnameGroup struct {
	Hostname  string // "" if no hostname could be resolved
	Canonical int    // device_id kept as the canonical row for this hostname
	AllDevIDs []int  // every device_id observed under this hostname
	Rules     int    // rule count
}

// CleanupRulesAnalyze runs the analysis without mutating DB. Safe to
// call repeatedly.
func (a *App) CleanupRulesAnalyze() (*CleanupPlan, error) {
	if a.DB == nil {
		return nil, fmt.Errorf("db not initialised")
	}
	if a.HS == nil {
		return nil, fmt.Errorf("headscale client not initialised")
	}

	nodes, err := a.HS.ListAllNodes()
	if err != nil {
		return nil, fmt.Errorf("ListAllNodes: %w", err)
	}

	// Build lookup tables from headscale.
	type ni struct {
		id, ip string
	}
	nameToNode := map[string]ni{}
	idToName := map[string]string{}
	ipToName := map[string]string{}
	for _, n := range nodes {
		name := n.GivenName
		if name == "" {
			name = n.Hostname
		}
		if name == "" {
			continue
		}
		ip := firstNonEmptyIP(n.IPAddresses)
		nameToNode[name] = ni{id: n.ID, ip: ip}
		idToName[n.ID] = name
		if ip != "" {
			ipToName[ip] = name
		}
	}

	// Read all rules.
	rows, err := a.DB.Query(`
		SELECT id, user_id, device_id, COALESCE(device_ip,''), exit_node_id
		FROM device_rules`)
	if err != nil {
		return nil, fmt.Errorf("query device_rules: %w", err)
	}
	defer rows.Close()

	type ruleRow struct {
		id, userID, deviceID int
		deviceIP             string
		exitNode             string
	}
	var rules []ruleRow
	for rows.Next() {
		var r ruleRow
		if err := rows.Scan(&r.id, &r.userID, &r.deviceID, &r.deviceIP, &r.exitNode); err == nil {
			rules = append(rules, r)
		}
	}

	// device_id -> hostname. Headscale node id is stable.
	devIDToHost := map[int]string{}
	for _, r := range rules {
		if _, seen := devIDToHost[r.deviceID]; seen {
			continue
		}
		devIDToHost[r.deviceID] = idToName[fmt.Sprintf("%d", r.deviceID)]
	}

	// Bucket rules by hostname.
	byHost := map[string][]ruleRow{}
	for _, r := range rules {
		hn := devIDToHost[r.deviceID]
		if hn == "" && r.deviceIP != "" {
			if name, ok := ipToName[r.deviceIP]; ok {
				hn = name
			}
		}
		byHost[hn] = append(byHost[hn], r)
	}

	// user -> set of resolved hostnames. We use this to disambiguate
	// rules with unknown hostname: if a user has exactly one resolved
	// hostname, their unknown rules likely belong to the same physical
	// device (eg skyworker re-registered under a new headscale node id
	// and the old rules still carry the previous id).
	userToHosts := map[int]map[string]int{}
	for hn, rs := range byHost {
		if hn == "" {
			continue
		}
		for _, r := range rs {
			if userToHosts[r.userID] == nil {
				userToHosts[r.userID] = map[string]int{}
			}
			userToHosts[r.userID][hn]++
		}
	}

	// Re-bucket rules: assign unknown hostname buckets to the single
	// resolved hostname for that user (when unambiguous).
	rebucketed := map[string][]ruleRow{}
	for hn, rs := range byHost {
		if hn == "" {
			// All rules in this bucket share user_id, by construction.
			var uid int
			if len(rs) > 0 {
				uid = rs[0].userID
			}
			if userToHosts[uid] != nil && len(userToHosts[uid]) == 1 {
				for k := range userToHosts[uid] {
					rebucketed[k] = append(rebucketed[k], rs...)
				}
			} else {
				rebucketed[""] = append(rebucketed[""], rs...)
			}
		} else {
			rebucketed[hn] = append(rebucketed[hn], rs...)
		}
	}
	byHost = rebucketed

	groups := make([]CleanupHostnameGroup, 0, len(byHost))
	resolvedIPs := 0
	mergedIDs := 0
	stale := map[int]bool{}

	for hn, rs := range byHost {
		distinct := map[int]bool{}
		for _, r := range rs {
			distinct[r.deviceID] = true
		}
		ids := make([]int, 0, len(distinct))
		for d := range distinct {
			ids = append(ids, d)
		}
		sort.Ints(ids)

		// Canonical: highest id that maps to this host and still exists
		// in headscale; otherwise highest id of the bucket.
		canonical := ids[len(ids)-1]
		for i := len(ids) - 1; i >= 0; i-- {
			if devIDToHost[ids[i]] == hn && hn != "" {
				canonical = ids[i]
				break
			}
		}

		// Count merged ids and resolved IPs.
		for _, r := range rs {
			if r.deviceID != canonical {
				mergedIDs++
			}
			if r.deviceIP == "" && hn != "" {
				if n, ok := nameToNode[hn]; ok && n.ip != "" {
					resolvedIPs++
				}
			}
			if devIDToHost[r.deviceID] == "" && hn == "" {
				stale[r.deviceID] = true
			}
		}

		groups = append(groups, CleanupHostnameGroup{
			Hostname:  hn,
			Canonical: canonical,
			AllDevIDs: ids,
			Rules:     len(rs),
		})
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Hostname == groups[j].Hostname {
			return groups[i].Canonical < groups[j].Canonical
		}
		return groups[i].Hostname < groups[j].Hostname
	})

	staleList := make([]int, 0, len(stale))
	for d := range stale {
		staleList = append(staleList, d)
	}
	sort.Ints(staleList)

	distinctHosts := 0
	for _, g := range groups {
		if g.Hostname != "" {
			distinctHosts++
		}
	}

	return &CleanupPlan{
		TotalRules:     len(rules),
		DistinctDevIDs: len(devIDToHost),
		DistinctHosts:  distinctHosts,
		ResolvedIPs:    resolvedIPs,
		MergedIDs:      mergedIDs,
		Groups:         groups,
		StaleDevIDs:    staleList,
		DeviceIDToHost: devIDToHost,
	}, nil
}

// CleanupRulesApply performs the merge in a single transaction.
// Idempotent: a second run produces 0 changes.
func (a *App) CleanupRulesApply() (*CleanupPlan, error) {
	plan, err := a.CleanupRulesAnalyze()
	if err != nil {
		return nil, err
	}
	if plan.MergedIDs == 0 && plan.ResolvedIPs == 0 {
		return plan, nil
	}

	nodes, err := a.HS.ListAllNodes()
	if err != nil {
		return nil, err
	}
	nameToIP := map[string]string{}
	for _, n := range nodes {
		name := n.GivenName
		if name == "" {
			name = n.Hostname
		}
		if name == "" {
			continue
		}
		if ip := firstNonEmptyIP(n.IPAddresses); ip != "" {
			nameToIP[name] = ip
		}
	}

	tx, err := a.DB.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	// 1) Backfill device_ip per hostname.
	for hn, ip := range nameToIP {
		var dids []int
		for did, name := range plan.DeviceIDToHost {
			if name == hn {
				dids = append(dids, did)
			}
		}
		if len(dids) == 0 {
			continue
		}
		args := make([]any, 0, 1+len(dids))
		args = append(args, ip)
		ph := make([]string, len(dids))
		for i, did := range dids {
			args = append(args, did)
			ph[i] = "?"
		}
		q := "UPDATE device_rules SET device_ip = $1 WHERE (device_ip = '' OR device_ip IS NULL) AND device_id IN (" + strings.Join(ph, ",") + ")"
		if _, err := tx.Exec(q, args...); err != nil {
			return nil, fmt.Errorf("backfill device_ip for host=%s: %w", hn, err)
		}
	}

	// 2) Merge device_ids per hostname.
	for _, g := range plan.Groups {
		if g.Hostname == "" {
			continue
		}
		if len(g.AllDevIDs) <= 1 {
			continue
		}
		var others []int
		for _, did := range g.AllDevIDs {
			if did != g.Canonical {
				others = append(others, did)
			}
		}
		if len(others) == 0 {
			continue
		}
		args := make([]any, 0, 1+len(others))
		args = append(args, g.Canonical)
		ph := make([]string, len(others))
		for i, did := range others {
			args = append(args, did)
			ph[i] = "?"
		}
		q := "UPDATE device_rules SET device_id = $1 WHERE device_id IN (" + strings.Join(ph, ",") + ")"
		if _, err := tx.Exec(q, args...); err != nil {
			return nil, fmt.Errorf("merge device_ids to %d: %w", g.Canonical, err)
		}
		log.Printf("cleanup: merged device_ids %v -> %d (host=%s)", others, g.Canonical, g.Hostname)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return a.CleanupRulesAnalyze()
}

// AdminCleanupRules GET renders the analysis page (no mutation).
func (a *App) AdminCleanupRules(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	plan, err := a.CleanupRulesAnalyze()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.renderWithLayout(w, r, "admin/exit_rules_cleanup.html", c, map[string]any{
		"Page":      "admin/exit-rules-cleanup",
		"Title":     "Cleanup exit rules",
		"Plan":      plan,
		"RunResult": "",
	})
}

// AdminCleanupRulesApply POST applies the cleanup.
func (a *App) AdminCleanupRulesApply(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	plan, err := a.CleanupRulesApply()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	a.audit(c.UserID, c.Username, "exit_rules_cleanup",
		fmt.Sprintf("merged=%d resolved_ips=%d", plan.MergedIDs, plan.ResolvedIPs))
	a.renderWithLayout(w, r, "admin/exit_rules_cleanup.html", c, map[string]any{
		"Page":      "admin/exit-rules-cleanup",
		"Title":     "Cleanup exit rules",
		"Plan":      plan,
		"RunResult": "applied",
	})
}

// firstNonEmptyIP returns the first IPv4 address from the slice, or the
// first address if none are IPv4.
func firstNonEmptyIP(ips []string) string {
	for _, ip := range ips {
		if ip != "" && !strings.Contains(ip, ":") {
			return ip
		}
	}
	for _, ip := range ips {
		if ip != "" {
			return ip
		}
	}
	return ""
}