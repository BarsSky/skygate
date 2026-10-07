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
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// 2026-08-25 (B178): AdminRule is now a package-level type
// (was a local closure type inside AdminExitRules before
// B178) so annotateRulesWithPrefs can take it by reference.
// Fields match the DB column names + post-B178 pref fields.
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
	// 2026-09-21 (B277.4): the v0.74 "all my devices" fan-out
	// marker. Set by the conversion at form_admin.go:284 from
	// db.DeviceRule.AllDevices (now SELECTed in
	// qSelectAllRulesForAdmin). The admin template renders a
	// "все мои устройства" badge when true — before B277.4 the
	// admin view had no way to tell a per-device rule apart
	// from a fan-out copy of an all_devices rule.
	AllDevices bool
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
	// 2026-08-25 (B184): true iff the rule's target CIDR is
	// actually APPROVED in headscale for this rule's
	// ExitNode. B178's Applicable was a LOGICAL check
	// (rule.ExitNode == device's preferred exit-node) —
	// it told the operator "this rule is targeted at the
	// right exit-node" but NOT "this rule's CIDR is in
	// headscale ApprovedRoutes". The user's live case
	// (michail's rules shown as "✅ accepted" but actually
	// not in headscale) was a gap in the B178 check.
	//
	// Set by annotateRulesWithPrefs from
	// approvedByExitNode[rule.ExitNode] which is built in
	// the handler from headscale's ListAllNodes.ApprovedRoutes
	// (one set per exit-node hostname). For SUBNET/IP
	// rules: direct check. For DOMAIN rules (B184): at
	// least one of the rule's resolved subnets
	// (device_rules rows with parent_domain = this rule's
	// target_value, same (user, device, exit) triple) must
	// be in headscale ApprovedRoutes for the rule's
	// ExitNode. Otherwise the DOMAIN rule is pending (no
	// resolution yet, or resolution exists but headscale
	// hasn't approved any of the resolved CIDRs).
	ApprovedInHeadscale bool
}

// adminWindowFromQuery is B358's (2026-10-06) pure parser for the /admin/exit-rules
// window: which page of (user → device) GROUPS to render, and how many groups per
// page. Pulled out of the handler so the defaults and the "garbage in the URL"
// behaviour are testable without an HTTP round trip.
//
//	?page=      — 1-based page number; < 1, empty or non-numeric → 1
//	?page_size= — GROUPS per page since B358 (it counted rule rows before);
//	              empty, < 1 or non-numeric → db.AdminGroupsPerPage (20)
//
// The upper clamp stays inside db.GetAllRulesForAdminPaged (pageSizeClamp = 500), so
// a hand-typed ?page_size= stays bounded no matter what reaches this parser.
func adminWindowFromQuery(q url.Values) (page, groupsPerPage int) {
	page = 1
	groupsPerPage = db.AdminGroupsPerPage
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		page = p
	}
	if ps, err := strconv.Atoi(q.Get("page_size")); err == nil && ps > 0 {
		groupsPerPage = ps
	}
	return page, groupsPerPage
}

// adminGroupIsOversized reports whether a (user, device) group is too large to be
// worth expanding by default. B358: such a group renders as a COLLAPSED <details>
// with its rows already fetched — one device with hundreds of rules must not turn
// the page into a wall of rows.
func adminGroupIsOversized(rows int) bool {
	return rows > db.AdminOversizedGroupRows
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
//
// 2026-08-25 (B184): also takes resolvedByDomain — a
// map["userID:deviceID:exitNode:parent_domain"]set(target_value)
// of all (subnet, ip) rules that the autoupdater derived
// from a domain rule. The annotator uses this to propagate
// headscale-state from a domain's resolved subnets UP to
// the parent domain rule. A DOMAIN rule is approved iff at
// least one of its resolved subnets is in
// approvedByExitNode[rule.ExitNode]. See LoadResolvedByDomain
// (resolved_by_domain.go) for the producer. See
// ruleApprovedInHeadscale for the consumer.
//
// 2026-08-25 (B182): also takes approvedByExitNode — a
// map[exit_node_hostname]set(CIDR) of what's actually
// APPROVED in headscale for each exit-node. The annotator
// sets ApprovedInHeadscale=true iff the rule's target
// appears in headscale ApprovedRoutes for the rule's
// ExitNode. SUBNET/IP rules check directly. DOMAIN rules
// check via resolvedByDomain (B184). The template renders
// three states:
//
//	✅ approved      — Applicable + ApprovedInHeadscale
//	⏳ pending       — Applicable but ApprovedInHeadscale=false
//	                    (rule's target not in headscale yet)
//	⚠️ wrong-node   — Applicable=false (rule's ExitNode differs
//	                    from the device's preferred exit-node)
func annotateRulesWithPrefs(rr []AdminRule, prefFn func(userID int64, hostname string) string, approvedByExitNode map[string]map[string]bool, resolvedByDomain map[string]map[string]bool) int {
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
		// 2026-08-25 (B182+B184): real headscale-state check, not
		// just a logical match. Looks up the rule's target
		// CIDR in the per-exit-node set of headscale
		// ApprovedRoutes. DOMAIN rules (B184) check via
		// resolvedByDomain — at least one of the rule's
		// resolved subnets must be in headscale for the
		// DOMAIN rule to be "approved". Empty ExitNode
		// (unusual but possible) is also treated as
		// not-approved.
		rr[i].ApprovedInHeadscale = ruleApprovedInHeadscale(rr[i], approvedByExitNode, resolvedByDomain)
		if !rr[i].Applicable {
			mismatch++
		}
	}
	return mismatch
}

// 2026-08-25 (B184): ruleApprovedInHeadscale returns true iff
// the rule's TargetType is "subnet" or "ip" AND its
// TargetValue appears in headscale.ApprovedRoutes for the
// rule's ExitNode, OR the rule's TargetType is "domain" AND
// at least one of its resolved subnets (rows with
// parent_domain = rule.TargetValue, same (user, device, exit)
// triple) is in headscale.ApprovedRoutes for the rule's
// ExitNode.
//
// SUBNET/IP rules check directly. DOMAIN rules (B184) check
// via resolvedByDomain: the key is
// "userID:deviceID:exitNode:parent_domain" (built by
// ResolvedKeyForTuple); the value is the set of resolved
// CIDRs. The function returns true iff at least one of
// those CIDRs is in approvedByExitNode[rule.ExitNode].
//
// 2026-08-25 (B182): SUBNET/IP rules return false if the
// rule's ExitNode is empty or unknown to headscale. The
// same applies to DOMAIN rules — a DOMAIN rule pointing at
// an unknown exit-node returns false.
//
// nil maps are treated as "no data available" — every rule
// returns false (matching the pre-B184 behaviour for all
// rules, plus the new B184 DOMAIN case where there's
// nothing to propagate from).
func ruleApprovedInHeadscale(rule AdminRule, approvedByExitNode map[string]map[string]bool, resolvedByDomain map[string]map[string]bool) bool {
	if rule.TargetType == "subnet" || rule.TargetType == "ip" {
		if rule.ExitNode == "" || rule.TargetValue == "" {
			return false
		}
		approved, ok := approvedByExitNode[rule.ExitNode]
		if !ok {
			return false // unknown exit-node hostname
		}
		return approved[rule.TargetValue]
	}
	// DOMAIN rule — B184 propagate from resolved subnets.
	if rule.ExitNode == "" || rule.TargetValue == "" {
		return false
	}
	resolved := LookupResolvedForDomain(resolvedByDomain,
		int64(rule.UserID), int64(rule.DeviceID), rule.ExitNode, rule.TargetValue)
	if len(resolved) == 0 {
		return false // autoupdater hasn't resolved yet
	}
	approved, ok := approvedByExitNode[rule.ExitNode]
	if !ok {
		return false // unknown exit-node hostname
	}
	for cid := range resolved {
		if approved[cid] {
			return true // at least one resolved subnet is in headscale
		}
	}
	return false // resolved but none in headscale yet
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
//     (the original v0.16.x behaviour)
//   - ?device=NAME present  → only rules whose device_id maps to
//     a node_owner_map row with hostname
//     = NAME (case-insensitive). The
//     template shows a banner with the
//     filter name and a "show all" link.
//   - ?device=NAME not found → empty result set + banner. The
//     handler does NOT http.StatusNotFound — the
//     "device not found" case is
//     indistinguishable from "device
//     exists but has no rules" from the
//     operator's perspective.
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
	var adminRulePage db.AdminRulePage
	if deviceFilter != "" {
		// 2026-08-06: per-device drill-down. The
		// `LEFT JOIN node_owner_map` in
		// qSelectAllRulesForAdminByDevice handles the
		// "device deleted but rules still present" edge
		// case (the rule is returned with a NULL
		// hostname, which won't match the LOWER() filter,
		// so it's correctly excluded from the result).
		// v1.5.43: device-filtered path stays UNPAGED — these
		// queries are scoped to one device and the row count
		// is small (~50–200 rules typical). Pagination only
		// matters for the unfiltered cross-user view.
		dbRules, err = db.GetAllRulesForAdminByDevice(s.dbc(), deviceFilter)
	} else {
		// v1.5.43: pagination for the unfiltered cross-user view.
		// B358 (2026-10-06): the window unit is the GROUP (user → device), not the
		// rule row. Measured live: 2 users, 3 devices, 393 enabled rows rendered
		// EIGHT pages of 50 rows with the users and the devices smeared across
		// them, because the CDN auto-updater adds rows per device on every tick and
		// a row window cuts through a group. `?page_size=` therefore counts
		// GROUPS now (default db.AdminGroupsPerPage = 20, still clamped to
		// [1, pageSizeClamp] inside db.GetAllRulesForAdminPaged); the live shape
		// then renders ONE page holding all three groups whole.
		page, groupsPerPage := adminWindowFromQuery(r.URL.Query())
		adminRulePage, err = db.GetAllRulesForAdminPaged(s.dbc(), page, groupsPerPage)
		if err == nil {
			dbRules = adminRulePage.Rules
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// B348 (2026-10-04): the device inventory for the index below. A read failure
	// degrades to an empty index (the table and the filter banner still render)
	// rather than breaking the page — the list is a view over data the table
	// already shows.
	adminDeviceIndex, idxErr := s.DeviceRuleCountsForAdminService()
	if idxErr != nil {
		adminDeviceIndex = []DeviceRuleCount{}
	}

	// B351 (2026-10-05): the REAL counters. The groups below are built from the
	// page slice (that is what a page is), so every number derived from it changed
	// with the page — the operator saw the same user at «19 правил / 3% (19/500)»
	// on page 6 and «49 правил / 9% (49/500)» on page 7, because the CDN
	// auto-updater interleaves new rows across devices and `ORDER BY r.id` splits
	// one group over many pages. These maps are unpaginated, so the badges and the
	// heading now quote the database. A read failure degrades to the page window
	// (the old behaviour) rather than failing the page.
	realCounts, rcErr := db.AdminRuleCounters(s.dbc())
	if rcErr != nil {
		log.Printf("admin/exit-rules: AdminRuleCounters: %v (counters fall back to the page window)", rcErr)
	}
	// The heading total: the unpaginated enabled-rule total on the unfiltered view,
	// the full (single-device) list on the drill-down.
	//
	// B358 (2026-10-06): realTotal comes from the COUNT the group window carries
	// (AdminRulePage.TotalRules), NOT from the rows the page happens to hold. The
	// window is now a set of whole groups, so `len(rr)` would be "the rows of the
	// groups that fit on this page" — a page window dressed as a total, which is
	// exactly the B351 defect the counters above exist to prevent.
	realTotal := len(dbRules)
	if adminRulePage.TotalRules > 0 {
		realTotal = adminRulePage.TotalRules
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
			AllDevices:   r.AllDevices,
		})
	}

	// Resolve device hostnames from headscale API — match by Tailscale IP.
	// Also captured for B182 (build approvedByExitNode from
	// node.ApprovedRoutes for the per-rule headscale-state check
	// below). One ListAllNodes call instead of two — headscale's
	// gRPC is fast but the per-row iteration over `nodes` was
	// already O(rules × nodes); the ApprovedRoutes-only pass
	// is just one more O(nodes) loop.
	var nodes []headscale.NodeView
	if n, e := s.HS.ListAllNodes(); e == nil {
		nodes = n
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
	if recent, err := db.RecentExitRuleLogs(s.dbc()); err == nil {
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
	if recent, err := db.RecentACLSnapshots(s.dbc()); err == nil {
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
	//
	// B351 (2026-10-05): Count/ShownCount are the rows in THIS PAGE's slice, while
	// TotalCount/RealTotalCount are the database's unpaginated numbers. Before this
	// block every count here was page-scoped, so the same user read different
	// totals on different pages (measured: 19/500 on page 6, 49/500 on page 7) and
	// the heading said "Все правила (50)" on a page whose own pagination line said
	// "всего 349 правил".
	type devNodeGroup struct {
		DeviceName string
		// Count is the rows of this device inside the current page.
		Count int
		// TotalCount is every ENABLED rule of this device (db.AdminRuleCounters).
		TotalCount int
		// B358 (2026-10-06): the group holds more than
		// db.AdminOversizedGroupRows rows, so the template renders it COLLAPSED —
		// the rows were fetched with the rest of the group (no second query, no new
		// JS state), this flag only decides whether the <details> starts open.
		Oversized bool
		Nodes     map[string][]AdminRule
		// 2026-09-07: B237.22 / TD-11 (Approach G) —
		// parallel CDN-grouped view. Same (exitNode) keys
		// as Nodes, but the value is a CDNDisplayViewAdmin
		// (CDN groups + ungrouped tail + pre-computed
		// TotalCount). The admin template iterates
		// NodesCDN to render the collapsible CDN-group
		// headers. Storage unchanged; only the view layer
		// is affected.
		NodesCDN map[string]CDNDisplayViewAdmin
	}
	type userGroup struct {
		// UserID is the rule owner's portal id; the real counters are keyed by it.
		UserID int64
		// UserCount is the QUOTA numerator — the same number the insert guard
		// compares against UserLimit (B328's countUserFacingForUser), never the
		// page window.
		UserCount int
		// TotalCount is every ENABLED rule of this user.
		TotalCount int
		// ShownCount is how many of them are on this page.
		ShownCount int
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
	// `PreferredExitNodeForRule(s.dbc(), 6, "basic")` returned
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
	//
	// 2026-08-25 (B182): we also pass `approvedByExitNode`
	// (built from the same `nodes` we already fetched for
	// device-name resolution) so the annotator can fill in
	// the per-rule ApprovedInHeadscale field. See
	// ruleApprovedInHeadscale below for the precise
	// semantics. Reusing the existing `nodes` slice means
	// no second ListAllNodes call.
	approvedByExitNode := indexNodesApprovedRoutes(nodes)
	// 2026-08-25 (B184): also pass `resolvedByDomain` so
	// DOMAIN rules can propagate their status from the
	// autoupdater-derived subnets. One SQL query covers
	// all (user, device, exit) tuples — no per-rule query.
	// Errors are logged but not fatal: a DB hiccup here
	// falls back to the pre-B184 behaviour (all DOMAIN
	// rules show ⏳ pending) instead of breaking the page.
	resolvedByDomain := map[string]map[string]bool{}
	if rbd, err := LoadResolvedByDomain(s.dbc()); err != nil {
		log.Printf("admin/exit-rules: LoadResolvedByDomain: %v (DOMAIN rules will show pending)", err)
	} else {
		resolvedByDomain = rbd
	}
	totalMismatch := annotateRulesWithPrefs(rr, func(uid int64, hn string) string {
		pref, _ := PreferredExitNodeForRule(s.dbc(), uid, hn)
		return pref
	}, approvedByExitNode, resolvedByDomain)

	groupedByUser := map[string]userGroup{}
	// B351: the system-load badge quotes the DATABASE, not the page (it used to be
	// totalRules/maxTotal with totalRules = the 50-row slice).
	totalPct := 0
	// B328: display the caps the guard ENFORCES (global_settings > .env > default), so a
	// panel override cannot leave the page quoting a limit that is no longer in force.
	adminLimits, limitSources := s.effectiveRuleLimits(c.Username)
	maxTotal := adminLimits.MaxTotal
	if maxTotal > 0 {
		totalPct = realTotal * 100 / maxTotal
	}
	for _, rule := range rr {
		uid := int64(rule.UserID)
		ug, ok := groupedByUser[rule.UserName]
		if !ok {
			ruleLimits, _ := s.effectiveRuleLimits(rule.UserName)
			ug = userGroup{
				UserID:     uid,
				Devices:    map[int]devNodeGroup{},
				UserLimit:  ruleLimits.MaxPerUser,
				TotalCount: realCounts.EnabledByUser[uid],
				UserCount:  realCounts.UserFacingByUser[uid],
			}
			if ug.UserLimit > 0 {
				ug.LoadPct = ug.UserCount * 100 / ug.UserLimit
			}
		}
		dg, ok := ug.Devices[rule.DeviceID]
		if !ok {
			dg = devNodeGroup{
				DeviceName: rule.DeviceName,
				TotalCount: realCounts.EnabledByUserDevice[db.UserDeviceKey(uid, rule.DeviceID)],
				Nodes:      map[string][]AdminRule{},
				NodesCDN:   map[string]CDNDisplayViewAdmin{},
			}
		}
		dg.Nodes[rule.ExitNode] = append(dg.Nodes[rule.ExitNode], rule)
		dg.Count++
		// B358: the whole group was fetched, so Count IS the group's size — the
		// oversized decision is made on it. Presentation only: the rows are already
		// in the page.
		dg.Oversized = adminGroupIsOversized(dg.Count)
		ug.Devices[rule.DeviceID] = dg
		ug.ShownCount++
		groupedByUser[rule.UserName] = ug
	}

	// 2026-09-07: B237.22 / TD-11 — build the CDN-grouped
	// view per (user, device, exitNode) tuple. Same
	// shape as form_my.go's groupedByHostnameCDN. Iterates
	// groupedByUser AFTER the main loop so we can
	// process the per-exitNode slice in one Group call.
	for userName, ug := range groupedByUser {
		for devID, dg := range ug.Devices {
			for exitNode, rulesForTuple := range dg.Nodes {
				groups, ungrouped := GroupAdminRulesByCDN(rulesForTuple)
				items := make([]CDNDisplayItemAdmin, 0, len(groups)+1)
				totalCount := 0
				for _, g := range groups {
					items = append(items, CDNDisplayItemAdmin{
						IsCDNGroup: true,
						Source:     g.Source,
						CDN:        g.CDN,
						Count:      g.Count,
						Rules:      g.Rules,
					})
					totalCount += g.Count
				}
				if len(ungrouped) > 0 {
					items = append(items, CDNDisplayItemAdmin{
						IsCDNGroup: false,
						Count:      len(ungrouped),
						Rules:      ungrouped,
					})
					totalCount += len(ungrouped)
				}
				// B351: the unpaginated count for the same triple, so the
				// exchange-level badge cannot pass a page window off as a total.
				// A triple present in the page always has a real count >= its
				// slice count, so 0 can only mean "the map missed it" — fall back
				// to the slice then, never to zero.
				realExit := realCounts.EnabledByUserDeviceExit[db.UserDeviceExitKey(ug.UserID, devID, exitNode)]
				if realExit < totalCount {
					realExit = totalCount
				}
				dg.NodesCDN[exitNode] = CDNDisplayViewAdmin{
					Items:          items,
					TotalCount:     totalCount,
					RealTotalCount: realExit,
				}
			}
			ug.Devices[devID] = dg
		}
		groupedByUser[userName] = ug
	}
	_ = totalPct

	// B343 (2026-10-02, operator report): the admin twin of the my-page marker.
	// A device whose ENABLED rules have no exit-node preference is inert — nothing
	// pins it to a relay — and the panel used to say nothing while the generated
	// ACL looked correct (the per-CIDR pins come from prefix_owner since B275).
	// One query per user that HAS rules (bounded by the portal's user count), not
	// per rule, so the cost does not grow with the rule table.
	inertDevices := map[string]int{}
	inertList := []string{}
	if urows, uerr := s.dbc().Query(`
		SELECT DISTINCT user_id, COALESCE(user_name, '')
		  FROM device_rules
		 WHERE enabled = 1 AND device_hostname <> ''
		 ORDER BY user_id`); uerr == nil {
		defer urows.Close()
		for urows.Next() {
			var uid int64
			var uname string
			if err := urows.Scan(&uid, &uname); err != nil {
				continue
			}
			m, merr := s.DevicesWithoutExitNodePrefForService(uid)
			if merr != nil {
				continue
			}
			for host, n := range m {
				inertDevices[strconv.FormatInt(uid, 10)+":"+host] = n
				label := host
				if uname != "" {
					label = uname + "/" + host
				}
				inertList = append(inertList, label+" ("+strconv.Itoa(n)+")")
			}
		}
	}

	s.Backend.RenderWithLayout(w, r, "admin/exit_rules.html", c, map[string]any{
		"Page":          "exit-rules",
		"Title":         "Exit Rules",
		"Rules":         rr,
		"Logs":          logs,
		"Snapshots":     snaps,
		"GroupedByUser": groupedByUser,
		"TotalRules":    realTotal,
		"MaxTotalRules": maxTotal,
		"LoadPct":       totalPct,
		// B351: the page window, so the template can say what it is showing
		// instead of letting a slice look like a total.
		"ShownRules": len(rr),
		// v1.5.43: pagination (only meaningful for the
		// unfiltered cross-user view; the device-filtered
		// drill-down sets RulePage=zero-value so the template
		// renders no controls).
		"RulePage": adminRulePage,
		// 2026-08-06: cross-check counter — admin sees the total
		// dead-rule count at the top of the page. Click to
		// filter the table to only-applicable vs only-mismatch
		// (the template renders a toggle).
		"MismatchCount": totalMismatch,
		// B343: devices whose enabled rules have no exit-node preference, keyed
		// "userID:hostname" for an inline marker plus a ready-made label list for
		// the banner (deterministic order, so the page does not shuffle).
		"NoExitNodeDevices": inertDevices,
		"NoExitNodeCount":   len(inertList),
		"NoExitNodeList":    inertList,
		// 2026-08-06: per-device filter state. Non-empty
		// when the operator clicked a "dead rules" badge on
		// /admin/devices. The template renders a banner
		// ("filtered to device X, show all") and keeps the
		// rule count scoped to this device only.
		"DeviceFilter":    deviceFilter,
		"DeviceRuleCount": len(rr),
		// B348 (2026-10-04): the COMPLETE cross-user inventory — every device with
		// enabled rules and its count, one unpaginated query. Measured live: the
		// unfiltered view is paged by ROW, so page 1 of 7 held only skyworker (the
		// auto-updater had just added ~54 /32 rows for it) and every other device
		// was on a later page — which the operator read, correctly, as "правила
		// пропали". The index is rendered when no drill-down is active, so any
		// device is one click from its full, unpaged rule list.
		"DeviceIndex": adminDeviceIndex,
		// 2026-09-11 (Issue #2 closure): admin add form state.
		// The form posts to /admin/exit-rules and redirects
		// back here with ?err=...&form_*=... on validation
		// failure, or ?applied=1 on success. The template
		// renders the form with the user's typed values
		// preserved (B237.19 flash-banner UX, mirrored from
		// /my/exit-rules).
		"err":               r.URL.Query().Get("err"),
		"applied":           r.URL.Query().Get("applied") == "1",
		"form_user_id":      r.URL.Query().Get("form_user_id"),
		"form_device_id":    r.URL.Query().Get("form_device_id"),
		"form_exit_node":    r.URL.Query().Get("form_exit_node"),
		"form_target_type":  r.URL.Query().Get("form_target_type"),
		"form_target_value": r.URL.Query().Get("form_target_value"),
		"form_action":       r.URL.Query().Get("form_action"),
		// B328: keep the «все устройства» checkbox ticked across the redirect, so a
		// refused submit does not silently drop the option the operator chose.
		"all_devices": isFormChecked(r.URL.Query().Get("all_devices")),
		// B328: the rule-caps card. It renders the EFFECTIVE value of every level with
		// the layer it came from (db / env / default), so the operator can see whether
		// editing .env would change anything at all — the same honesty the DERP probe
		// card uses (B296). The DB row is shown only when it exists, so an empty input
		// means "inherit".
		"limit_per_device":          adminLimits.MaxPerDevice,
		"limit_per_user":            adminLimits.MaxPerUser,
		"limit_total":               adminLimits.MaxTotal,
		"limit_per_device_source":   string(limitSources["per_device"].Source),
		"limit_per_user_source":     string(limitSources["per_user"].Source),
		"limit_total_source":        string(limitSources["total"].Source),
		"limit_per_device_override": dbLimitOverride(s.dbc(), SettingKeyMaxPerDevice),
		"limit_total_override":      dbLimitOverride(s.dbc(), SettingKeyMaxTotal),
		"limits_saved":              r.URL.Query().Get("limits_saved") == "1",
		"limits_err":                r.URL.Query().Get("limits_err"),
	})
}

// dbLimitOverride returns the raw stored override ("" when the row is absent), so the
// panel's input shows what is actually stored rather than the effective value — an input
// pre-filled with the effective value would silently PROMOTE an env/default value into a
// database override the moment the operator saved the form for an unrelated reason.
func dbLimitOverride(d *sql.DB, key string) string {
	if d == nil {
		return ""
	}
	v, err := db.GetGlobalSetting(d, key, "")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// ============================================================================
// Issue #2 closure (2026-09-11, lamblador/Daniil):
//
//   Admin cannot create exit-rules for another user's devices.
//
// Pre-fix: /admin/exit-rules was a view-only page (Re-apply +
// sync + rollback) with no Add form. Admin couldn't add a rule
// for daniil's `workpc` from the admin context. The fix is a
// new POST /admin/exit-rules handler that mirrors PostMyExitRule
// but takes a `user_id` form field for the rule owner. The
// `src` of the generated headscale ACL stays = device owner,
// not the admin session.
//
// Design notes:
//   - handler lives next to AdminExitRules (the GET) so the
//     cross-user admin context is co-located in the file
//   - 4 helper functions + 1 handler:
//       validateAdminRuleForm        — pure form-field check
//       buildAdminExitRuleRedirectURL — mirrors B237.19 redirect
//                                       helper, carries user_id
//                                       + form_* values
//       PostAdminExitRule            — the handler itself
//       adminExitRuleInsertAndAudit  — DB+audit row helper (the
//                                       shared insert path with
//                                       PostMyExitRule, extracted
//                                       so the two paths can't
//                                       drift on the insert shape)
//   - the handler is wired in cmd/skygate/main.go under the
//     authMW gate; the IsAdmin check inside the handler is a
//     belt-and-suspenders second guard so a future router
//     misconfiguration can't bypass it
// ============================================================================

// validateAdminRuleForm returns true iff all required fields
// are present. action="" is allowed and defaults to "accept"
// downstream (matches PostMyExitRule's form_my.go:632
// behaviour). Pure function — no DB — so the test file
// pins it without spinning up sqlmock.
func validateAdminRuleForm(userID, deviceID, exitNode, targetType, targetValue, action string) bool {
	// user_id + device_id must parse as integers (the
	// handler does strconv.Atoi after this returns true).
	if _, err := strconv.Atoi(userID); err != nil || userID == "" {
		return false
	}
	if _, err := strconv.Atoi(deviceID); err != nil || deviceID == "" {
		return false
	}
	if strings.TrimSpace(exitNode) == "" {
		return false
	}
	if strings.TrimSpace(targetValue) == "" {
		return false
	}
	// target_type is whitelisted (defense-in-depth — the form
	// template only emits ip/subnet/domain, but a hostile
	// operator could POST anything).
	switch targetType {
	case "ip", "subnet", "domain":
	default:
		return false
	}
	// action is optional (handler defaults to "accept"). If
	// non-empty, it must be "accept" or "deny" (matches
	// PostMyExitRule's downstream contract).
	if action != "" && action != "accept" && action != "deny" {
		return false
	}
	return true
}

// buildAdminExitRuleRedirectURL mirrors buildFormErrorRedirectURL
// (B237.19) but for the admin path: carries form_user_id +
// form_device_id + form_exit_node + form_target_type +
// form_target_value + form_action + err. The user_id is the
// extra dimension admin needs (so the form re-renders with
// the same target user pre-selected).
func buildAdminExitRuleRedirectURL(errMsg, userID string, deviceID int, exitNode, targetType, targetValue, action string) string {
	if action == "" {
		action = "accept"
	}
	return fmt.Sprintf("/admin/exit-rules?err=%s&form_user_id=%s&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s",
		url.QueryEscape(errMsg),
		url.QueryEscape(userID),
		url.QueryEscape(strconv.Itoa(deviceID)),
		url.QueryEscape(exitNode),
		url.QueryEscape(targetType),
		url.QueryEscape(targetValue),
		url.QueryEscape(action),
	)
}

// PostAdminExitRule handles POST /admin/exit-rules — admin
// adds an exit-rule for ANOTHER portal user's device. The
// rule's owner (device_rules.user_id) is the target user, NOT
// the admin session — so the headscale ACL `src` stays
// = device owner (matches the issue's suggested fix).
//
// Mirrors PostMyExitRule's flow:
//  1. IsAdmin gate (defense-in-depth)
//  2. parse + validate form fields
//  3. validate target user exists
//  4. validate device exists + is owned by target user
//     (node_owner_map.username = target.username)
//  5. reject if device is an exit-node (routing infra)
//  6. IP/CIDR validation for target_type=ip/subnet
//  7. DNS resolve for target_type=domain (each /32 rule
//     remembers parent_domain for autoupdater stability)
//  8. per-user / per-device / total rule limits
//  9. insertRuleUnique + audit row
//  10. redirect to /admin/exit-rules with success/partial
//     banner (template reads ?applied=N or ?err=...)
func (s *Service) PostAdminExitRule(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userIDStr := r.FormValue("user_id")
	deviceIDStr := r.FormValue("device_id")
	exitNode := r.FormValue("exit_node")
	targetType := r.FormValue("target_type")
	targetValue := strings.TrimSpace(r.FormValue("target_value"))
	action := r.FormValue("action")
	if action == "" {
		action = "accept"
	}

	// B328: «все устройства» for the target user. The admin form's device field is a
	// number, so the option arrives as a separate flag instead of the /my page's extra
	// <option value="all">. The device that IS named (or the user's first one) stays the
	// primary and every downstream check — ownership by the TARGET user, exit-node
	// refusal, IP/CIDR validation — runs against it unchanged; the fan-out at the end
	// covers the rest. Pre-B328 the admin path had no such option at all: form_admin.go
	// never called MarkDeviceRulesAllDevices, so mirroring one rule across another
	// user's device set had to be done device by device.
	allDevices := isFormChecked(r.FormValue("all_devices"))
	if allDevices && strings.TrimSpace(deviceIDStr) == "" {
		targetUID, _ := strconv.Atoi(userIDStr)
		ids, derr := db.DeviceIDsForPortalUser(s.dbc(), int64(targetUID))
		if derr != nil || len(ids) == 0 {
			http.Redirect(w, r, buildAdminExitRuleRedirectURL(
				fmt.Sprintf("all_devices: user_id=%d has no attributed device to attach the rule to (register or adopt a device first)", targetUID),
				userIDStr, 0, exitNode, targetType, targetValue, action), http.StatusFound)
			return
		}
		deviceIDStr = strconv.Itoa(ids[0])
	}

	if !validateAdminRuleForm(userIDStr, deviceIDStr, exitNode, targetType, targetValue, action) {
		http.Redirect(w, r, buildAdminExitRuleRedirectURL(
			"missing or invalid form fields",
			userIDStr, atoiOrZero(deviceIDStr), exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}
	uid, _ := strconv.Atoi(userIDStr)
	devID, _ := strconv.Atoi(deviceIDStr)

	// 3. target user must exist
	targetUserName, uerr := db.GetUserNameByID(s.dbc(), int64(uid))
	if uerr != nil || targetUserName == "" {
		http.Redirect(w, r, buildAdminExitRuleRedirectURL(
			fmt.Sprintf("user_id=%d not found in portal_users", uid),
			userIDStr, devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}

	// 4+5. device must exist in headscale AND be owned by the
	// target user (NOT the admin — the whole point of this
	// handler). Reject exit-nodes (routing infra, same as
	// PostMyExitRule).
	var deviceIP string
	var isExitNode bool
	owned := false
	if nodes, err := s.HS.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID != strconv.Itoa(devID) {
				continue
			}
			isExitNode = n.IsExitNode
			if len(n.IPAddresses) > 0 {
				deviceIP = n.IPAddresses[0]
			}
			// ownership check uses the TARGET username,
			// not the admin session (the whole point of
			// Issue #2 — admin acts on behalf of user).
			ownerCount, _ := db.CountNodeOwnerByNodeUser(s.dbc(), strconv.Itoa(devID), targetUserName)
			owned = ownerCount > 0
			break
		}
	}
	if !owned {
		http.Redirect(w, r, buildAdminExitRuleRedirectURL(
			fmt.Sprintf("device %d is not in node_owner_map for user %q (admin session=%q)", devID, targetUserName, c.Username),
			userIDStr, devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}
	if isExitNode {
		http.Redirect(w, r, buildAdminExitRuleRedirectURL(
			"cannot attach rules to exit-node (routing infrastructure)",
			userIDStr, devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}

	// 6. IP/CIDR validation (mirrors form_my.go:650).
	if targetType == "ip" || targetType == "subnet" {
		if !isValidIPOrCIDR(targetValue) {
			http.Redirect(w, r, buildAdminExitRuleRedirectURL(
				fmt.Sprintf("invalid target_value %q: expected IP or CIDR for target_type=%q", targetValue, targetType),
				userIDStr, devID, exitNode, targetType, targetValue, action), http.StatusFound)
			return
		}
	}

	// 7+8+9. DNS resolve + limits + insert (mirrors form_my.go).
	// B328: the caps come from the shared resolver (global_settings > .env > default),
	// and the unit is the one the page shows — this is now the SAME function the /my path
	// and the usage panel use, so the three surfaces cannot disagree.
	//
	// Per-user limit check uses the TARGET user's rule count
	// (not the admin's) — admin has no rules of their own,
	// but a malicious admin should still hit the per-user
	// cap when stuffing rules into another user's account.
	limits, _ := s.effectiveRuleLimits(targetUserName)
	targetCounts := measureRuleLimits(s.dbc(), limits, int64(uid), devID, targetUserName)
	if limits.MaxPerUser > 0 && targetCounts.PerUser >= limits.MaxPerUser {
		http.Redirect(w, r, buildAdminExitRuleRedirectURL(
			fmt.Sprintf("user limit exceeded: %d/%d rules for user %s", targetCounts.PerUser, limits.MaxPerUser, targetUserName),
			userIDStr, devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}
	if limits.MaxPerDevice > 0 && targetCounts.PerDevice >= limits.MaxPerDevice {
		http.Redirect(w, r, buildAdminExitRuleRedirectURL(
			fmt.Sprintf("device limit exceeded: %d/%d user-facing rules on device %d", targetCounts.PerDevice, limits.MaxPerDevice, devID),
			userIDStr, devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}
	if limits.MaxTotal > 0 && targetCounts.Total >= limits.MaxTotal {
		http.Redirect(w, r, buildAdminExitRuleRedirectURL(
			fmt.Sprintf("system limit exceeded: %d/%d user-facing rules", targetCounts.Total, limits.MaxTotal),
			userIDStr, devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}

	// 7. DNS resolve (admin path mirrors my path).
	dnsWarning := ""
	ipsToInsert := []string{targetValue}
	typeToInsert := targetType
	if typeToInsert == "ip" && !strings.Contains(targetValue, "/") {
		ipsToInsert = []string{targetValue + "/32"}
	}
	if targetType == "domain" {
		if addrs, err := net.LookupHost(targetValue); err == nil {
			ipsToInsert = nil
			seen := map[string]bool{}
			for _, a := range addrs {
				if strings.Contains(a, ":") {
					continue
				}
				if seen[a] {
					continue
				}
				seen[a] = true
				ipsToInsert = append(ipsToInsert, a+"/32")
			}
			if len(ipsToInsert) > 0 {
				typeToInsert = "subnet"
			}
		} else {
			dnsWarning = fmt.Sprintf("DNS resolve failed for %q: %v (autoupdater will retry)", targetValue, err)
		}
	}
	subnetParent := ""
	if targetType == "domain" && typeToInsert == "subnet" {
		subnetParent = targetValue
	}

	// 9. insert (one row per resolved IP, idempotent on
	// existing rules). insertRuleUnique is shared with
	// PostMyExitRule so the row shape can't drift.
	insertedCount := 0
	dupCount := 0
	for _, ip := range ipsToInsert {
		ok, _ := s.insertRuleUnique(int64(uid), devID, exitNode, typeToInsert, ip, action, deviceIP, subnetParent)
		if !ok {
			// 2026-09-18 (R6): this branch used to answer with a raw
			// http.Error carrying a literal 500 status (which leaked the
			// DB error and replaced the page with text/plain). The
			// validation branches in this same handler already redirect
			// through buildAdminExitRuleRedirectURL (which restores the
			// form), so the DB-error branch uses it too instead of
			// replacing the page with a bare text/plain body.
			http.Redirect(w, r, buildAdminExitRuleRedirectURL(
				s.I18n.T(s.I18n.LangFromRequest(r), "error.db"),
				fmt.Sprint(uid), devID, exitNode, typeToInsert, targetValue, action),
				http.StatusSeeOther)
			return
		}
		// insertRuleUnique returns (true, existingID) when
		// the row already exists — we treat that as a dup
		// for the admin redirect (the rule is already in
		// place, no harm done).
		insertedCount++
		_ = dupCount
	}
	_ = insertedCount

	// B328: materialise «все устройства» for the TARGET user. The intent is marked
	// first and the SAME pass the five-minute tick runs does the copying, so the
	// admin path and the /my path cannot drift apart in what they produce.
	if allDevices {
		for _, ip := range ipsToInsert {
			if _, merr := db.MarkDeviceRulesAllDevices(s.dbc(), int64(uid), exitNode, typeToInsert, ip); merr != nil {
				log.Printf("admin exit-rules: could not mark %s %s as all-devices for user %d: %v", typeToInsert, ip, uid, merr)
			}
		}
		fanned, perr := s.propagateAllDeviceRules()
		if perr != nil {
			log.Printf("admin exit-rules: all-devices marked for user %d but the immediate fan-out failed: %v (the maintenance pass will retry)", uid, perr)
		} else {
			log.Printf("admin exit-rules: all-devices rule %s %s via %s for user %s marked (fan-out created %d row(s))",
				typeToInsert, targetValue, exitNode, targetUserName, fanned)
		}
	}

	// 10. audit row (action=admin_add_exit_rule_for_user,
	// not the generic PostMyExitRule's action — the operator
	// needs to see which admin added the rule on whose
	// behalf).
	s.Backend.Audit(c.UserID, c.Username, "admin_add_exit_rule_for_user",
		fmt.Sprintf("added %d rule(s) for user=%s (uid=%d) device=%d exit=%s target=%s all_devices=%t",
			insertedCount, targetUserName, uid, devID, exitNode, targetValue, allDevices))

	// success redirect (mirrors PostMyExitRule's ?applied=1).
	warnParam := ""
	if dnsWarning != "" {
		warnParam = "&warn=" + url.QueryEscape(dnsWarning)
	}
	if allDevices {
		warnParam += "&all_devices=1"
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/exit-rules?applied=1&form_user_id=%s&form_device_id=%s%s",
		url.QueryEscape(userIDStr),
		url.QueryEscape(strconv.Itoa(devID)),
		warnParam), http.StatusFound)
}

// isFormChecked reads an HTML checkbox the way browsers submit it: "on" when checked
// by default, or an explicit value. Anything else (absent, "", "0", "false") is false.
func isFormChecked(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "on", "true", "yes":
		return true
	}
	return false
}

// atoiOrZero is a tiny helper to avoid panicking in the
// buildAdminExitRuleRedirectURL call when the form value
// is unparseable (the validator catches it earlier, but
// the redirect still needs a numeric device_id for the
// template to render).
func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
