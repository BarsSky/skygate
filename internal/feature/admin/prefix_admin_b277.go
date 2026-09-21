// prefix_admin_b277.go — B277: the prefix-assignment operator surface.
//
// B275.1 shipped one row per prefix with a relay <select> and a Save button, which
// was honest but unusable: the table held every prefix it had ever seen (live: 1655
// rows, 1497 of them claimed by no rule at all), the operator's real question is
// "which relay serves Cloudflare / this device / everything", and answering it meant
// hundreds of individual clicks.
//
// B277 adds the three things that question needs:
//
//  1. the dead rows are pruned by the engine (prefixowner.Prune), so the table
//     describes the network instead of its history;
//  2. the rows are GROUPED — by owner relay, by domain/CDN, or by device — with the
//     counts that matter (prefixes, devices, problems) and a bulk pin per group;
//  3. a GLOBAL override ("every prefix through <relay>") that is one setting rather
//     than a mass write, applies to prefixes that appear later, and is overridden by
//     a manual per-prefix pin.
//
// Every mutation re-applies the ACL. That is not a nicety: prefixowner.Assign keeps a
// manual row it finds, so a manual pin produces no "changed" count on the next pass —
// without this the pin would sit in the database while headscale kept serving the old
// relay (the B276 failure mode, one layer up).
package admin

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/acl"
	"skygate/internal/headscale"
	"skygate/internal/prefixowner"
)

// PrefixGroup is one rendered group of assignment rows.
type PrefixGroup struct {
	// Key is the value a bulk action is addressed to (relay name, domain label,
	// device hostname). Label is what the operator reads (they can differ: the
	// device group's label carries the owner name as well).
	Key   string
	Label string
	Rows  []PrefixOwnerRow
	// Counts for the header: how many prefixes, how many of them are drifted, how
	// many the operator has pinned by hand, and how many distinct devices claim them.
	Total   int
	Problem int
	Manual  int
	Devices int
	// Detail is a short human sentence for the header (e.g. "2 устройства: basic,
	// olesya" or "cloudflare через karolina").
	Detail string
}

// PrefixAdminView is everything the prefix card needs.
type PrefixAdminView struct {
	Groups    []PrefixGroup
	Stats     PrefixDriftStats
	GroupBy   string
	Relays    []string
	Force     string
	OnlyDrift bool
	// ActivePrefixes is how many rows the table holds after the prune — the number
	// the operator's actions actually cover.
	ActivePrefixes int
}

// prefixClaimInfo is what the grouping needs to know about one prefix: which domains
// it came from, which devices and users claim it.
type prefixClaimInfo struct {
	Domains map[string]bool
	Devices map[int]bool
	Users   map[string]bool
}

// loadPrefixClaims maps every claimed prefix to the domains/devices/users that claim
// it. One query, because the grouping runs on every page render.
func (s *Service) loadPrefixClaims() map[string]*prefixClaimInfo {
	out := map[string]*prefixClaimInfo{}
	rows, err := s.dbc().Query(
		`SELECT target_value, COALESCE(parent_domain,''), COALESCE(device_id,0), COALESCE(user_name,'')
		   FROM device_rules
		  WHERE enabled = 1 AND target_type IN ('ip','subnet')
		  GROUP BY target_value, COALESCE(parent_domain,''), COALESCE(device_id,0), COALESCE(user_name,'')`)
	if err != nil {
		log.Printf("[exit-nodes] prefix claims read failed: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var prefix, domain, user string
		var deviceID int
		if err := rows.Scan(&prefix, &domain, &deviceID, &user); err != nil {
			continue
		}
		info := out[prefix]
		if info == nil {
			info = &prefixClaimInfo{Domains: map[string]bool{}, Devices: map[int]bool{}, Users: map[string]bool{}}
			out[prefix] = info
		}
		if domain != "" {
			info.Domains[domain] = true
		}
		if deviceID != 0 {
			info.Devices[deviceID] = true
		}
		if user != "" {
			info.Users[user] = true
		}
	}
	return out
}

// domainGroupLabel turns a rule's parent_domain into the label an operator thinks in.
// The CDN resolver stores one row per (domain, CIDR) with `cdn:<provider>:<domain>`
// (B183 keeps that row through the dedup), so "cdn:cloudflare:discord.com" is really
// "cloudflare" — and everything else is the domain itself.
func domainGroupLabel(parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return ""
	}
	if strings.HasPrefix(parent, "cdn:") {
		parts := strings.SplitN(strings.TrimPrefix(parent, "cdn:"), ":", 2)
		if len(parts) > 0 && parts[0] != "" {
			return strings.ToLower(parts[0])
		}
	}
	return strings.ToLower(parent)
}

// deviceLabels maps node ids to the hostname the operator recognises.
func (s *Service) deviceLabels() map[int]string {
	out := map[int]string{}
	rows, err := s.dbc().Query(`SELECT node_id, COALESCE(hostname,''), COALESCE(username,'') FROM node_owner_map`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID, host, user string
		if err := rows.Scan(&nodeID, &host, &user); err != nil {
			continue
		}
		id, cerr := strconv.Atoi(strings.TrimSpace(nodeID))
		if cerr != nil || id == 0 {
			continue
		}
		if host == "" {
			host = "node " + nodeID
		}
		if user != "" {
			host += " (" + user + ")"
		}
		out[id] = host
	}
	return out
}

// buildPrefixGroups arranges the rows into the groups of the requested mode
// (owner | domain | device). Unknown modes fall back to owner, so a hand-edited URL
// still renders.
func (s *Service) buildPrefixGroups(rows []PrefixOwnerRow, claims map[string]*prefixClaimInfo, mode string) []PrefixGroup {
	if mode != "domain" && mode != "device" {
		mode = "owner"
	}
	labels := map[int]string{}
	if mode == "device" {
		labels = s.deviceLabels()
	}
	byKey := map[string]*PrefixGroup{}
	order := []string{}
	for _, r := range rows {
		info := claims[r.Prefix]
		keys := []string{}
		switch mode {
		case "owner":
			keys = []string{r.ExitNode}
		case "domain":
			seen := map[string]bool{}
			if info != nil {
				for d := range info.Domains {
					if l := domainGroupLabel(d); l != "" && !seen[l] {
						seen[l] = true
						keys = append(keys, l)
					}
				}
			}
			if len(keys) == 0 {
				keys = []string{""}
			}
		case "device":
			if info != nil {
				for id := range info.Devices {
					keys = append(keys, strconv.Itoa(id))
				}
			}
			if len(keys) == 0 {
				keys = []string{"0"}
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			g := byKey[key]
			if g == nil {
				g = &PrefixGroup{Key: key, Label: prefixGroupLabel(mode, key, labels)}
				byKey[key] = g
				order = append(order, key)
			}
			g.Rows = append(g.Rows, r)
			g.Total++
			if !r.Advertised || r.Unserved || len(r.AlsoBy) > 0 {
				g.Problem++
			}
			if r.Source == "manual" {
				g.Manual++
			}
			if info != nil {
				g.Devices += len(info.Devices)
			}
		}
	}
	out := make([]PrefixGroup, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		g.Detail = prefixGroupDetail(mode, g)
		out = append(out, *g)
	}
	// Biggest groups first: the operator is usually looking for the CDN-sized ones.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// prefixGroupLabel renders the heading for one group key.
func prefixGroupLabel(mode, key string, deviceLabels map[int]string) string {
	switch mode {
	case "owner":
		if key == "" {
			return "(без владельца)"
		}
		return key
	case "domain":
		if key == "" {
			return "(без домена)"
		}
		return key
	default:
		if key == "0" {
			return "(устройство неизвестно)"
		}
		id, err := strconv.Atoi(key)
		if err == nil {
			if l := deviceLabels[id]; l != "" {
				return l
			}
		}
		return "node " + key
	}
}

func prefixGroupDetail(mode string, g *PrefixGroup) string {
	parts := []string{}
	if g.Manual > 0 {
		parts = append(parts, fmt.Sprintf("%d закреплено вручную", g.Manual))
	}
	if g.Problem > 0 {
		parts = append(parts, fmt.Sprintf("%d проблемных", g.Problem))
	}
	if mode != "device" && g.Devices > 0 {
		parts = append(parts, fmt.Sprintf("%d устройств(а)", g.Devices))
	}
	return strings.Join(parts, ", ")
}

// loadPrefixAdminView composes the whole prefix card: the rows, their flags, the
// current claims, the groups of the requested mode and the drift summary.
//
// `onlyDrift` hides the healthy rows (the checkbox on the page); it never changes what
// a bulk action covers — the bulk handlers re-resolve the group from the database, so
// the operator cannot pin "the 12 rows I can see" and accidentally skip the rest.
func (s *Service) loadPrefixAdminView(groupBy string, onlyDrift bool) PrefixAdminView {
	rows, stats := s.loadPrefixOwnerRows()
	view := PrefixAdminView{Stats: stats, GroupBy: groupBy, OnlyDrift: onlyDrift}
	if groupBy != "domain" && groupBy != "device" {
		view.GroupBy = "owner"
	}
	view.ActivePrefixes = len(rows)
	if onlyDrift {
		drifted := make([]PrefixOwnerRow, 0, len(rows))
		for _, r := range rows {
			if !r.Advertised || r.Unserved || len(r.AlsoBy) > 0 || r.Source == "manual" {
				drifted = append(drifted, r)
			}
		}
		rows = drifted
	}
	claims := s.loadPrefixClaims()
	view.Groups = s.buildPrefixGroups(rows, claims, view.GroupBy)
	seen := map[string]bool{}
	for _, r := range rows {
		if r.ExitNode != "" && !seen[r.ExitNode] {
			seen[r.ExitNode] = true
			view.Relays = append(view.Relays, r.ExitNode)
		}
	}
	sort.Strings(view.Relays)
	view.Force = prefixowner.ForceRelay(s.dbc())
	return view
}

// PostAdminExitPrefixOwnerBulk pins every prefix of one group to one relay (B277).
// An empty relay hands the whole group back to the engine.
//
// Group keys are resolved the same way the page renders them, from the CURRENT claims
// and table — so what the operator clicked on is what gets written, even if the rule
// set changed between the render and the click.
func (s *Service) PostAdminExitPrefixOwnerBulk(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	relay := strings.TrimSpace(r.FormValue("relay"))
	mode := strings.TrimSpace(r.FormValue("group_by"))
	key := strings.TrimSpace(r.FormValue("group_key"))
	if key == "" && mode != "domain" && mode != "device" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("group key is empty"), http.StatusSeeOther)
		return
	}

	rows, _ := s.loadPrefixOwnerRows()
	claims := s.loadPrefixClaims()
	groups := s.buildPrefixGroups(rows, claims, mode)
	var prefixes []string
	for _, g := range groups {
		if g.Key != key {
			continue
		}
		for _, row := range g.Rows {
			prefixes = append(prefixes, row.Prefix)
		}
	}
	if len(prefixes) == 0 {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("the group has no prefixes left — reload the page"), http.StatusSeeOther)
		return
	}
	pinned := 0
	for _, prefix := range prefixes {
		if err := prefixowner.SetManual(s.dbc(), prefix, relay); err != nil {
			log.Printf("[exit-nodes] bulk SetManual(%s, %s): %v", prefix, relay, err)
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
			return
		}
		pinned++
	}
	action := "prefix_owner_pin_bulk"
	result := "→ " + relay
	if relay == "" {
		action = "prefix_owner_auto_bulk"
		result = "→ авто"
	}
	detail := fmt.Sprintf("group_by=%s group=%s prefixes=%d %s", mode, key, pinned, result)
	s.Backend.Audit(c.UserID, c.Username, action, detail)
	s.finishPrefixChange(w, r, c.Username, "bulk "+detail,
		fmt.Sprintf("%d префикс(ов) группы «%s» %s", pinned, key, result))
}

// PostAdminExitPrefixOwnerMulti pins an arbitrary selection of prefixes (B277 — the
// multi-checkbox surface). The operator checks rows across groups and submits one
// form, instead of repeating the bulk action per group.
//
// Selected prefixes are intersected with the current rows, so a stale form (a prefix
// that disappeared between render and submit) cannot resurrect a dead row. An empty
// selection refuses with an error — no silent no-op. As with the per-group form,
// relay="" hands the selection back to the engine.
func (s *Service) PostAdminExitPrefixOwnerMulti(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	relay := strings.TrimSpace(r.FormValue("relay"))
	ids := r.Form["ids"]
	if len(ids) == 0 {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("ничего не выбрано — отметьте префиксы чекбоксами"), http.StatusSeeOther)
		return
	}

	// Intersect with the CURRENT rows. A prefix that no longer exists in the table
	// (it was pruned, or never existed in this DB) is skipped silently — the operator
	// would otherwise need to reload the page between render and submit. De-duplicate
	// while we're at it so the audit log shows N, not 3×N for a triple-checked row.
	rows, _ := s.loadPrefixOwnerRows()
	known := make(map[string]bool, len(rows))
	for _, row := range rows {
		known[row.Prefix] = true
	}
	seen := make(map[string]bool, len(ids))
	var prefixes []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || !known[id] {
			continue
		}
		seen[id] = true
		prefixes = append(prefixes, id)
	}
	if len(prefixes) == 0 {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("выбранные префиксы уже не в таблице — перезагрузите страницу"), http.StatusSeeOther)
		return
	}
	pinned := 0
	for _, prefix := range prefixes {
		if err := prefixowner.SetManual(s.dbc(), prefix, relay); err != nil {
			log.Printf("[exit-nodes] multi SetManual(%s, %s): %v", prefix, relay, err)
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
			return
		}
		pinned++
	}
	action := "prefix_owner_pin_multi"
	result := "→ " + relay
	if relay == "" {
		action = "prefix_owner_auto_multi"
		result = "→ авто"
	}
	detail := fmt.Sprintf("selected=%d prefixes=%d %s", len(ids), pinned, result)
	s.Backend.Audit(c.UserID, c.Username, action, detail)
	s.finishPrefixChange(w, r, c.Username, "multi "+detail,
		fmt.Sprintf("%d префикс(ов) выбрано, %d применено %s", len(ids), pinned, result))
}

// PostAdminExitNodePrefixForce sets (or clears) the global override (B277): every
// prefix that is not manually pinned is served by this relay.
func (s *Service) PostAdminExitNodePrefixForce(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	relay := strings.TrimSpace(r.FormValue("relay"))
	if relay != "" {
		nodes, err := s.HSGlobalFn().ListAllNodes()
		if err != nil {
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("cannot list exit nodes: "+err.Error()), http.StatusSeeOther)
			return
		}
		known := false
		for _, n := range nodes {
			if strings.EqualFold(n.Hostname, relay) || strings.EqualFold(n.GivenName, relay) {
				known = true
				relay = n.Hostname
				break
			}
		}
		if !known {
			// A typo here would take the whole tailnet's egress away, so refuse it
			// loudly instead of storing a relay nobody serves.
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("unknown exit node: "+relay), http.StatusSeeOther)
			return
		}
	}
	if err := prefixowner.SetForceRelay(s.dbc(), relay); err != nil {
		log.Printf("[exit-nodes] SetForceRelay(%q): %v", relay, err)
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(s.I18n.T(s.I18n.LangFromRequest(r), "error.db")), http.StatusSeeOther)
		return
	}
	action := "prefix_owner_force"
	msg := "все префиксы через " + relay
	if relay == "" {
		action = "prefix_owner_force_clear"
		msg = "глобальное закрепление снято (снова распределение движком)"
	}
	s.Backend.Audit(c.UserID, c.Username, action, "relay="+relay)
	s.finishPrefixChange(w, r, c.Username, action+" relay="+relay, msg)
}

// finishPrefixChange re-applies the ACL after a prefix mutation and redirects with the
// outcome. prefixowner.Assign keeps a manual row it finds, so the next sync pass
// reports no change for a manual pin — without this explicit apply the pin would live
// in the database while headscale kept serving the old relay.
func (s *Service) finishPrefixChange(w http.ResponseWriter, r *http.Request, username, detail, okMsg string) {
	var alerter acl.Alerter
	if s.Notifier != nil {
		alerter = s.Notifier
	}
	viaFlag := false
	if s.Cfg != nil {
		viaFlag = s.Cfg.ACLWithViaEnabled
	}
	results := acl.ApplyACLForAllPlanes(s.dbc(),
		func(string) *headscale.Client { return s.HSGlobalFn() },
		alerter, username, "prefix assignment: "+detail, viaFlag)
	for _, res := range results {
		if res.Err != nil {
			log.Printf("[exit-nodes] ACL re-apply after a prefix change failed: %v", res.Err)
			http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("назначение сохранено, но ACL не применился: "+res.Err.Error()), http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/admin/exit-nodes?ok="+url.QueryEscape(okMsg+"; ACL пересобран"), http.StatusSeeOther)
}
