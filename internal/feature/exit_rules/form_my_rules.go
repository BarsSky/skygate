// Package exit_rules — form_my_rules.go owns the user-facing
// GET /my/exit-rules page handler.
//
// Split out of form_my.go (2026-10-08, PURE MOVE — the body below is
// byte-identical to what form_my.go carried before the split): the
// handler is 866 lines and was the bulk of that file. Everything that
// stayed behind (isValidIPOrCIDR, PostMyExitRule, PostDeleteExitRule,
// PostMyExitRulesApplyPreferred and the redirect-URL builders) is
// unchanged in form_my.go.
//
//   - GetMyExitRules (GET /my/exit-rules, also handles ?script=
//     download via GenerateRouteSetupScript)
package exit_rules

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// GetMyExitRules serves the user-facing /my/exit-rules
// page. Also handles the ?script= download (delegates to
// GenerateRouteSetupScript for the per-OS bash/.cmd body).
//
// v1.5.43: pagination. The previous version called
// s.getDeviceRules(c.UserID) which loaded EVERY enabled rule
// for the user in one round-trip. For users with 1500+ rules
// (active admins managing many domains across many devices), the
// per-host + CDN-grouped render is O(N²) on row count — 3-5s page
// load, >500KB HTML. The page now reads `?page=N&page_size=M`
// (defaults: page=1, page_size=50, max 500) and renders one
// slice per request. The /my/exit-rules?script= download path
// is unchanged — it needs the FULL rule set for the per-device
// routescript generation, so it bypasses pagination.
func (s *Service) GetMyExitRules(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	// Route setup script download (unpaged — the script needs
	// every rule for the device to emit the right ip route add
	// commands). Pagination is a display-side concern.
	if r.URL.Query().Get("script") != "" {
		devStr := r.URL.Query().Get("device_id")
		devID, _ := strconv.Atoi(devStr)
		os := r.URL.Query().Get("os")
		if os == "" {
			os = "linux"
		}
		restore := r.URL.Query().Get("restore") == "1"
		script, err := s.GenerateRouteSetupScript(int(c.UserID), devID, os, restore)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Build filename with device name if specified
		fname := "skygate-routes"
		if restore {
			fname = "skygate-routes-restore"
		}
		if devID > 0 {
			if nodes, _ := s.HS.ListAllNodes(); nodes != nil {
				for _, n := range nodes {
					if n.ID == strconv.Itoa(devID) {
						hn := n.GivenName
						if hn == "" {
							hn = n.Hostname
						}
						fname += "-" + hn
						break
					}
				}
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if os == "windows" {
			w.Header().Set("Content-Disposition", "attachment; filename="+fname+".bat")
		} else {
			w.Header().Set("Content-Disposition", "attachment; filename="+fname+".sh")
		}
		w.Write([]byte(script))
		return
	}

	// v1.5.43: pagination. Read ?page=&page_size= (defaults: page=1,
	// page_size=50, clamped to [1, 500]). The total unpaged row count
	// goes into RulePage.Total so the template can render "Showing
	// X–Y of Z" + the prev/next controls.
	page := 1
	pageSize := 50
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	if ps, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil && ps > 0 {
		pageSize = ps
	}
	rulePage, _ := db.GetDeviceRulesForUserPaged(s.dbc(), c.UserID, page, pageSize)
	rules := rulePage.Rules
	// B279 (v1.5.46): the paged helper deliberately does not touch
	// DeviceName (it needs a headscale round-trip — see
	// db.GetDeviceRulesForUserPaged's doc), and this handler switched to
	// it in v1.5.43 without adding the enrichment back. Every rule then
	// grouped under the raw device_id ("2" instead of "workpc").
	s.enrichDeviceNames(rules)

	// B276.1: label the rows that were saved for "все мои устройства". The copies
	// the fan-out created are individual rows, so without this the user cannot tell
	// a rule that follows their new laptop from one that does not.
	if keys := s.allDeviceRuleKeys(c.UserID); len(keys) > 0 {
		for i := range rules {
			rules[i].AllDevices = keys[allDeviceRuleKey(rules[i].ExitNodeID, rules[i].TargetType, rules[i].TargetValue)]
		}
	}

	var devices []map[string]any
	if nodes, e := s.HS.ListAllNodes(); e == nil {
		// 2026-07-11: bug fix — even admin sees only their own devices in the
		// user-facing form. Cross-user view lives at /admin/exit-rules. The
		// filter applies uniformly regardless of IsAdmin so the "device"
		// dropdown can't be abused to assign rules to another user's device.
		userNodes := map[int]bool{}
		snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), c.Username)
		for _, nid := range snapIDs {
			if n, err := strconv.Atoi(nid); err == nil {
				userNodes[n] = true
			}
		}
		for _, n := range nodes {
			nid, _ := strconv.Atoi(n.ID)
			if !userNodes[nid] {
				continue
			}
			// 2026-07-11: bug fix — exit-nodes are routing infrastructure
			// (tag:exit-node, name starts with "exit-", or advertises
			// 0.0.0.0/0). They belong in the "exit node" dropdown (target
			// side), never the "device" dropdown (source side) where a
			// user-facing rule would be attached.
			if n.IsExitNode {
				continue
			}
			hn := n.GivenName
			if hn == "" {
				hn = n.Hostname
			}
			devices = append(devices, map[string]any{"id": n.ID, "hostname": hn})
		}
	}
	if devices == nil {
		devices = []map[string]any{}
	}

	var exitServers []map[string]any
	if nodes, err := s.HS.ListExitNodes(); err == nil {
		for _, n := range nodes {
			exitServers = append(exitServers, map[string]any{"hostname": n.Hostname})
		}
	}
	if exitServers == nil {
		exitServers = []map[string]any{}
	}

	// Build per-device route info — match by hostname (resolved from IP)
	deviceRoutes := map[string][]db.DeviceRule{} // hostname -> rules
	hasRoutes := map[string]bool{}               // hostname -> has IP/subnet rules
	for _, rl := range rules {
		name := rl.DeviceName
		if name == "" {
			name = fmt.Sprintf("device-%d", rl.DeviceID)
		}
		deviceRoutes[name] = append(deviceRoutes[name], rl)
		if rl.TargetType == "ip" || rl.TargetType == "subnet" {
			hasRoutes[name] = true
		}
	}

	// Enrich devices with rule counts
	type DeviceInfo struct {
		ID           string
		Hostname     string
		RuleCount    int
		UserFacing   int // 2026-07-09: user-facing count (excludes /32 from autoupdater)
		HasRoutes    bool
		MaxForDevice int // 2026-07-09: per-device limit (MaxRulesPerDevice)
		// 2026-08-06: preferred exit-node hostname for this device
		// (empty if no per-device or per-user pref is set). The
		// template uses this to render a warning on rules that
		// don't match the preference.
		PreferredExitNode string
		// i18n: pre-rendered hint templates the JS uses to display per-device
		// usage at the current usage level. %d/%d (%d%%) gets replaced with
		// used/max/pct in the browser.
		HintOK     string
		HintWarn   string
		HintDanger string
	}
	var deviceInfos []DeviceInfo
	// B328: the number shown next to each device (`cyborg (1/500)`) must be the number
	// the guard ENFORCES. Reading Cfg directly here is how the page and the guard could
	// disagree — and the operator's report was literally a page showing 1/500 next to a
	// refusal quoting 500/500.
	displayLimits, _ := s.effectiveRuleLimits(c.Username)
	maxPerDeviceLimit := displayLimits.MaxPerDevice
	lang := ""
	if s.I18n != nil {
		lang = s.I18n.LangFromRequest(r)
	}
	for _, d := range devices {
		hn := fmt.Sprint(d["hostname"])
		info := DeviceInfo{
			ID:           fmt.Sprint(d["id"]),
			Hostname:     hn,
			RuleCount:    len(deviceRoutes[hn]),
			HasRoutes:    hasRoutes[hn],
			MaxForDevice: maxPerDeviceLimit,
		}
		// 2026-08-06: preferred exit-node hostname for this device
		// (per-device pref > per-user pref > ""). The template
		// renders a warning on rules whose exit_node doesn't match.
		// Best-effort: a DB error here must not break the page.
		if pref, perr := PreferredExitNodeForRule(s.dbc(), c.UserID, hn); perr == nil {
			info.PreferredExitNode = pref
		}
		// The i18n hints (HintOK/HintWarn/HintDanger) are
		// used by the browser-side JS. When I18n is nil
		// (e.g. unit tests) the hints are empty strings
		// and the JS falls back to its default copy.
		if s.I18n != nil {
			info.HintOK = s.I18n.T(lang, "exit_rules.usage_ok")
			info.HintWarn = s.I18n.T(lang, "exit_rules.usage_warn")
			info.HintDanger = s.I18n.T(lang, "exit_rules.usage_danger")
		}
		// Count user-facing rules for THIS device (excludes autoupdater /32).
		did, _ := strconv.Atoi(info.ID)
		if did > 0 {
			// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledNonSubnetRulesForUserDevice
			info.UserFacing, _ = db.CountEnabledNonSubnetRulesForUserDevice(s.dbc(), c.UserID, did)
		}
		deviceInfos = append(deviceInfos, info)
	}
	if deviceInfos == nil {
		deviceInfos = []DeviceInfo{}
	}

	// Overall HasRoutes for backward compat
	anyRoutes := len(hasRoutes) > 0

	// B348 (2026-10-04): DEVICE-first navigation, resolved BEFORE the grouping
	// below (every group map is built from `rules`, so a filter applied later
	// would change the slice and leave the rendered groups untouched — the exact
	// kind of half-applied change that made the page lie in the first place).
	//
	// Measured live: with 227 rules and a 50-row window, page 1 held ONLY
	// skyworker's rows (the auto-updater had just added ~54 /32 rows for it), so a
	// device with no rows in the window — cyborg, right after the operator added a
	// rule for it — had no group at all and looked like it had no rules.
	//
	// Priority: an explicit ?device=<host> wins; otherwise, on the post-save
	// landing, the device that was just saved. Either way the device's rules are
	// shown UNPAGED (a device is bounded by the per-device cap), and the row is
	// matched by device id as well as by the displayed name, because the page
	// groups by DeviceName (a headscale lookup) while the drill-down link carries
	// the denormalised device_hostname.
	deviceFilter := strings.TrimSpace(r.URL.Query().Get("device"))
	if deviceFilter == "" && r.URL.Query().Get("applied") != "" {
		wantID := r.URL.Query().Get("form_device_id")
		for _, di := range deviceInfos {
			if wantID != "" && di.ID == wantID && di.Hostname != "" {
				deviceFilter = di.Hostname
				break
			}
		}
	}
	if deviceFilter != "" {
		wanted := map[int]bool{}
		for _, di := range deviceInfos {
			if equalFoldASCII(di.Hostname, deviceFilter) {
				if id, cerr := strconv.Atoi(di.ID); cerr == nil {
					wanted[id] = true
				}
			}
		}
		if all, aerr := db.GetDeviceRulesForUser(s.dbc(), c.UserID); aerr == nil {
			only := filterRulesByDevice(all, deviceFilter, wanted)
			rulePage = db.RulePage{Rules: only, Total: len(only), Page: 1, PageSize: len(only)}
			rules = rulePage.Rules
			s.enrichDeviceNames(rules)
		}
	}
	// The complete inventory (one unpaginated query) so a device can never be
	// invisible merely because its rows fall outside the current window.
	deviceIndex, indexErr := s.DeviceRuleCountsForUserService(c.UserID)
	if indexErr != nil {
		deviceIndex = []DeviceRuleCount{}
	}

	// 2026-07-07: issue #12 — hierarchical view
	// Group rules by device_id -> exit_node. B276.2 (2026-09-21):
	// all_devices=true rules go into a SEPARATE map (allDevicesByExitNode)
	// — the per-host section no longer renders the fan-out copies, the
	// ALL-DEVICES section owns them.
	deviceNames := map[int]string{}
	grouped := map[int]map[string][]db.DeviceRule{}
	allDevicesByExitNode := map[string][]db.DeviceRule{}
	for _, r := range rules {
		if r.AllDevices {
			allDevicesByExitNode[r.ExitNodeID] = append(allDevicesByExitNode[r.ExitNodeID], r)
			continue
		}
		dn := deviceNames[r.DeviceID]
		if dn == "" {
			dn = fmt.Sprint(r.DeviceName)
			if dn == "" {
				dn = fmt.Sprint(r.DeviceID)
			}
			deviceNames[r.DeviceID] = dn
		}
		if grouped[r.DeviceID] == nil {
			grouped[r.DeviceID] = map[string][]db.DeviceRule{}
		}
		grouped[r.DeviceID][r.ExitNodeID] = append(grouped[r.DeviceID][r.ExitNodeID], r)
	}

	// 2026-07-09: GroupedByHostname collapses rules from the SAME logical
	// device that were accidentally recorded under multiple headscale node
	// ids. node IDs are monotonically increasing and never re-used: when a
	// node gets re-provisioned (eg tagged, re-keyed, brand-new host) the
	// replacement arrives under a new id, but pre-existing rules still
	// carry the OLD id. The hierarchical view used to render those as two
	// identical sections ("workstation-1" twice). GroupedByHostname reroutes
	// the template over (hostname -> exitNode -> []rules), so device_id=1
	// and device_id=9 (both workstation-1) collapse into one section.
	// B276.2: all_devices rules are skipped here too — they live in
	// allDevicesByExitNodeCDN (rendered as the ALL-DEVICES section).
	groupedByHostname := map[string]map[string][]db.DeviceRule{}
	for _, r := range rules {
		if r.AllDevices {
			continue
		}
		hn := deviceNames[r.DeviceID]
		if groupedByHostname[hn] == nil {
			groupedByHostname[hn] = map[string][]db.DeviceRule{}
		}
		groupedByHostname[hn][r.ExitNodeID] = append(groupedByHostname[hn][r.ExitNodeID], r)
	}

	// 2026-09-07: B237.22 / TD-11 (Approach G) — UI-only
	// CDN-grouping. Each (host, exitNode) tuple's rules are
	// pre-grouped into CDN groups (parent_domain markers) +
	// ungrouped (manual) rules. The template iterates this
	// instead of the raw []db.DeviceRule slice. See
	// cdn_group.go + cdn_group_test.go for the helper.
	//
	// Why a parallel structure instead of changing
	// groupedByHostname's value type: form_my.go's iteration
	// ordering is sensitive (the count badge on the
	// exitNode-level <details> reads `len $rules`). The
	// CDN-grouped view also needs to know the per-group
	// rule count separately, so a parallel structure with
	// the same (host, exitNode) key is cleaner than
	// overloading the existing one.
	groupedByHostnameCDN := map[string]map[string]CDNDisplayView{}
	for hn, byExit := range groupedByHostname {
		groupedByHostnameCDN[hn] = map[string]CDNDisplayView{}
		for exitNode, rulesForTuple := range byExit {
			// Convert db.DeviceRule → RuleRow (the
			// projection cdn_group.go operates on). All
			// fields the my-template reads (ID,
			// TargetType, TargetValue, ParentDomain,
			// Action, AllDevices) are in RuleRow —
			// AllDevices is the B276.1 «все мои
			// устройства» badge marker; dropping it here
			// crashes /my/exit-rules with "can't evaluate
			// field AllDevices in type exit_rules.RuleRow".
			rows := make([]RuleRow, len(rulesForTuple))
			for i, r := range rulesForTuple {
				rows[i] = RuleRow{
					ID:           int64(r.ID),
					UserID:       int64(r.UserID),
					DeviceID:     r.DeviceID,
					ExitNode:     r.ExitNodeID,
					TargetType:   r.TargetType,
					TargetValue:  r.TargetValue,
					ParentDomain: r.ParentDomain,
					Action:       r.Action,
					AllDevices:   r.AllDevices,
				}
			}
			groups, ungrouped := GroupRulesByCDN(rows)
			items := make([]CDNDisplayItem, 0, len(groups)+1)
			totalCount := 0
			for _, g := range groups {
				items = append(items, CDNDisplayItem{
					IsCDNGroup: true,
					Source:     g.Source,
					CDN:        g.CDN,
					Count:      g.Count,
					Rules:      g.Rules,
				})
				totalCount += g.Count
			}
			if len(ungrouped) > 0 {
				items = append(items, CDNDisplayItem{
					IsCDNGroup: false,
					Count:      len(ungrouped),
					Rules:      ungrouped,
				})
				totalCount += len(ungrouped)
			}
			groupedByHostnameCDN[hn][exitNode] = CDNDisplayView{
				Items:      items,
				TotalCount: totalCount,
			}
		}
	}

	// B276.2 (2026-09-21): the ALL-DEVICES section. all_devices=true
	// rules are fanned out across the user's devices in device_rules —
	// each row carries a different device_id but identical
	// (exit_node, target_type, target_value, parent_domain). For the
	// display we de-duplicate by that natural key and remember how
	// many devices each fan-out covered, so the section can render one
	// logical rule with a "применено к N устройств(ам)" badge instead of
	// N identical rows.
	allDevicesByExitNodeCDN := map[string]CDNDisplayView{}
	for exitNode, rulesForTuple := range allDevicesByExitNode {
		// Collapse to one entry per (target_type, target_value, parent_domain).
		type fanKey struct {
			tt, tv, pd string
		}
		fanGroups := map[fanKey]*struct {
			row         RuleRow
			deviceCount int
		}{}
		for _, r := range rulesForTuple {
			k := fanKey{r.TargetType, r.TargetValue, r.ParentDomain}
			e := fanGroups[k]
			if e == nil {
				e = &struct {
					row         RuleRow
					deviceCount int
				}{
					row: RuleRow{
						ID:           int64(r.ID),
						UserID:       int64(r.UserID),
						DeviceID:     r.DeviceID,
						ExitNode:     r.ExitNodeID,
						TargetType:   r.TargetType,
						TargetValue:  r.TargetValue,
						ParentDomain: r.ParentDomain,
						Action:       r.Action,
						AllDevices:   true,
					},
				}
				fanGroups[k] = e
			}
			e.deviceCount++
		}
		// Build CDNDisplayItems in a stable order: sort by TargetValue.
		keys := make([]fanKey, 0, len(fanGroups))
		for k := range fanGroups {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].tv != keys[j].tv {
				return keys[i].tv < keys[j].tv
			}
			return keys[i].pd < keys[j].pd
		})
		rows := make([]RuleRow, 0, len(keys))
		fanOutTotal := 0
		for _, k := range keys {
			e := fanGroups[k]
			rows = append(rows, e.row)
			fanOutTotal += e.deviceCount
		}
		// CDN-group via the existing helper (so cloudflare/fastly/etc.
		// bundles render as their own collapsible headers inside the
		// ALL-DEVICES section, identical to the per-host layout).
		groups, ungrouped := GroupRulesByCDN(rows)
		items := make([]CDNDisplayItem, 0, len(groups)+1)
		totalCount := 0
		for _, g := range groups {
			items = append(items, CDNDisplayItem{
				IsCDNGroup:  true,
				Source:      g.Source,
				CDN:         g.CDN,
				Count:       g.Count,
				Rules:       g.Rules,
				FanOutCount: fanOutTotal / len(g.Rules), // average per row (each row covers fanOutTotal devices)
			})
			totalCount += g.Count
		}
		if len(ungrouped) > 0 {
			items = append(items, CDNDisplayItem{
				IsCDNGroup:  false,
				Count:       len(ungrouped),
				Rules:       ungrouped,
				FanOutCount: fanOutTotal / len(ungrouped),
			})
			totalCount += len(ungrouped)
		}
		allDevicesByExitNodeCDN[exitNode] = CDNDisplayView{
			Items:       items,
			TotalCount:  totalCount,
			FanOutTotal: fanOutTotal,
		}
	}

	// Total rules count (all enabled).
	// B328: the ceiling shown here is the one the guard ENFORCES (db > env > default),
	// not the raw config field — otherwise a panel override would leave the system-load
	// badge computing a percentage against a limit that is no longer in force.
	maxTotal := displayLimits.MaxTotal
	totalRules := 0
	if maxTotal > 0 {
		// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledRules
		totalRules, _ = db.CountEnabledRules(s.dbc())
	}
	loadPct := 0
	if maxTotal > 0 {
		loadPct = totalRules * 100 / maxTotal
	}

	// 2026-07-07: issue #5 — query params for dedup notification
	duplicate := r.URL.Query().Get("duplicate") == "1"
	// 2026-09-07 (B237.19): form-error flash from
	// the POST handler. The handler used to call
	// http.Error which rendered a giant plain-text
	// page; now it redirects back with ?err=<msg>
	// and the template renders this as a flash
	// banner above the form. The user's form values
	// are preserved via the form_* query params
	// (also written by the handler).
	errMsg := r.URL.Query().Get("err")
	// B123: `existing` was renamed to `target` for clarity (it's the
	// value the user tried to add, NOT the existing rule). `existing_id`,
	// `blocking_ip`, `parent_domain` carry the details needed to point
	// the user at the rule that already covers the target. Back-compat:
	// if an old ?existing= link slips through, treat it as the target.
	target := r.URL.Query().Get("target")
	if target == "" {
		target = r.URL.Query().Get("existing")
	}
	existingIDStr := r.URL.Query().Get("existing_id")
	// B123: parse once here so the template can do
	// {{if gt .existing_id 0}} directly (no per-render strconv).
	existingIDInt, _ := strconv.Atoi(existingIDStr)
	blockingIP := r.URL.Query().Get("blocking_ip")
	parentDomain := r.URL.Query().Get("parent_domain")
	partial := r.URL.Query().Get("partial") == "1"
	// B328: the spread action reports its result through the URL (see the render map).
	spreadDevices, _ := strconv.Atoi(r.URL.Query().Get("spread_devices"))
	spreadCreated, _ := strconv.Atoi(r.URL.Query().Get("spread_created"))

	// 2026-07-06: form persistence (issue #1) — после добавления правила
	// сохраняем введённые значения в URL, чтобы форма не сбрасывалась.
	formDeviceID := r.URL.Query().Get("form_device_id")
	formExitNode := r.URL.Query().Get("form_exit_node")
	formTargetType := r.URL.Query().Get("form_target_type")
	formTargetValue := r.URL.Query().Get("form_target_value")
	formAction := r.URL.Query().Get("form_action")
	if formTargetType == "" {
		formTargetType = "ip"
	}
	if formAction == "" {
		formAction = "accept"
	}

	// 2026-07-09: per-user and per-device usage counters (user-facing only,
	// excludes /32 from autoupdater). Shown in the UI so the user sees
	// their personal limit, not just the system-wide MaxTotalRules.
	userFacingCount := 0
	if c.UserID > 0 {
		// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledNonSubnetRulesForUser
		userFacingCount, _ = db.CountEnabledNonSubnetRulesForUser(s.dbc(), c.UserID)
	}
	maxPerUser := displayLimits.MaxPerUser

	// 2026-07-09: per-device breakdown — shows count per device_id so the
	// UI can label each device with its own quota.
	type DeviceUsage struct {
		DeviceID int
		Count    int
	}
	var deviceUsageList []DeviceUsage
	// 2026-07-11: Этап 9 part 2 — moved to db.CountRulesByDeviceForUser
	deviceCounts, qerr := db.CountRulesByDeviceForUser(s.dbc(), c.UserID)
	if qerr == nil {
		for devID, count := range deviceCounts {
			deviceUsageList = append(deviceUsageList, DeviceUsage{DeviceID: devID, Count: count})
		}
	}
	deviceUsage := map[int]int{}
	for _, du := range deviceUsageList {
		deviceUsage[du.DeviceID] = du.Count
	}

	// Update deviceInfos with the aggregated deviceUsage (avoids N queries in template).
	for i := range deviceInfos {
		did, _ := strconv.Atoi(deviceInfos[i].ID)
		deviceInfos[i].UserFacing = deviceUsage[did]
	}

	// 2026-08-06: count "dead rules" — rules whose exit_node_id
	// doesn't match the device's preferred exit-node. A non-zero
	// count triggers a warning banner on the page (template:
	// exit_rules.preferred_mismatch_banner).
	mismatchCount := 0
	// Build (hostname → preferred) once for the rules loop.
	prefByHostname := map[string]string{}
	for _, di := range deviceInfos {
		prefByHostname[di.Hostname] = di.PreferredExitNode
	}
	// B277.4: user-level preferred is the fallback when the device
	// has no per-device pref. Read once here (used by the status
	// loop below AND by the page-level banner / button) instead
	// of two separate reads. The earlier code only read it for
	// the banner; the status loop's "no per-device pref" path
	// silently fell back to "" even when a user-level preferred
	// existed, masking genuine drift in the banner count.
	userPreferred, _ := db.GetUserExitNodePref(s.dbc(), c.UserID)
	userPreferredHost := TagToHostname(userPreferred.ExitNodeTag)
	// 2026-08-25 (B182): fetch headscale's ApprovedRoutes per
	// exit-node so the template can distinguish a rule that
	// "matches the preferred exit-node" (B178's green ✅) from
	// a rule that "matches AND is actually approved in
	// headscale" (B182's stronger green ✅). The user's live
	// case was: rules shown as ✅ accepted in /my/exit-rules
	// but the actual CIDRs were never pushed to headscale —
	// B178's logical check passed because the exit-node
	// matched, but headscale had no record of the CIDR.
	// B-pending-write (v1.5.42, 2026-09-21): the rule's exit_node_id is
	// ALWAYS `n.Hostname` (the value the operator picked from the
	// /my/exit-rules dropdown — see form_my.go:170). Pre-fix this
	// loop keyed the map by `GivenName` first with a `Hostname`
	// fallback, which broke the look-up whenever headscale returned
	// a node with `GivenName != Hostname` (the typical case for
	// nodes with a MagicDNS suffix — e.g. GivenName="node.tail-scale.ts.net",
	// Hostname="node"). The map was indexed under the wrong key,
	// `approvedByExitNode[r.ExitNodeID]` looked up an empty entry,
	// and every rule appeared stuck in `pending` even after
	// SyncAdvertisedRoutes had successfully pushed the routes to
	// headscale. Fix: index the SAME set under BOTH `GivenName` and
	// `Hostname` so a rule written against either key resolves
	// correctly. Same shape used in form_admin.go (see B182 fix).
	approvedByExitNode := map[string]map[string]bool{}
	if nodes, e := s.HS.ListAllNodes(); e == nil {
		approvedByExitNode = indexNodesApprovedRoutes(nodes)
	}
	// 2026-08-25 (B184): also load the resolved-subnets map
	// so DOMAIN rules can propagate their headscale-state
	// from the autoupdater-derived subnets. Same one-query
	// load pattern as /admin/exit-rules. DB error is logged
	// but not fatal: it falls back to the pre-B184 behaviour
	// (DOMAIN rules show ⏳ pending) instead of breaking
	// the page.
	resolvedByDomain := map[string]map[string]bool{}
	if rbd, err := LoadResolvedByDomain(s.dbc()); err != nil {
		log.Printf("my/exit-rules: LoadResolvedByDomain: %v (DOMAIN rules will show pending)", err)
	} else {
		resolvedByDomain = rbd
	}
	// statusByRuleID maps rule.ID → "approved" | "pending"
	// | "wrong" | "auto_pending" | "no_preferred". The template reads this to
	// render the ✅/⏳/⚠️ badge. DOMAIN rules (B184) are "approved"
	// iff at least one of their resolved subnets is in headscale
	// ApprovedRoutes for the rule's ExitNode. Otherwise "pending"
	// (autoupdater hasn't resolved, or resolved but headscale
	// hasn't approved any of the resolved CIDRs).
	//
	// B277.4 (2026-09-21):
	//   - new "auto_pending" status for rules with exit_node_id=""
	//     (engine-auto): the engine will pick a healthy relay on the
	//     next tick, so we DO NOT count them as a mismatch.
	//   - "effective preferred" falls back to user-level
	//     userPreferredHost when the device has no per-device
	//     pref. Pre-B277.4 the device's "no per-device pref" path
	//     silently counted the rule as "no_preferred" even when
	//     a user-level preferred existed — the banner would miss
	//     genuine drift between the rule's empty exit_node and
	//     the user's preferred.
	statusByRuleID := map[int]string{}
	for _, r := range rules {
		hn := deviceNames[r.DeviceID]
		// Effective preferred: per-device overrides user-level,
		// empty stays empty.
		pref := prefByHostname[hn]
		if pref == "" {
			pref = userPreferredHost
		}
		if pref == "" {
			statusByRuleID[r.ID] = "no_preferred"
			continue
		}
		// Auto rules (exit_node_id="") defer to the engine; the
		// engine is preferred-aware (B275) so it'll route through
		// `pref` if it's healthy. NOT counted as a mismatch.
		if r.ExitNodeID == "" {
			statusByRuleID[r.ID] = "auto_pending"
			continue
		}
		if !IsRuleApplicable(r.ExitNodeID, pref) {
			statusByRuleID[r.ID] = "wrong"
			mismatchCount++
			continue
		}
		// Applicable — check headscale state.
		if r.TargetType == "subnet" || r.TargetType == "ip" {
			if approved, ok := approvedByExitNode[r.ExitNodeID]; ok && approved[r.TargetValue] {
				statusByRuleID[r.ID] = "approved"
			} else {
				statusByRuleID[r.ID] = "pending"
			}
		} else {
			// DOMAIN rule (B184+B185) — propagate status from
			// the autoupdater's resolved subnets. approved
			// iff at least one resolved CIDR is in
			// headscale for the rule's ExitNode. B185
			// adds the cdn:*:<domain> alias lookup so
			// Cloudflare/Fastly/Google/Akamai-routed
			// domains don't stay "pending" when their
			// CDN ranges are already in headscale.
			resolved := LookupResolvedForDomain(resolvedByDomain,
				int64(r.UserID), int64(r.DeviceID), r.ExitNodeID, r.TargetValue)
			if len(resolved) == 0 {
				statusByRuleID[r.ID] = "pending"
			} else {
				approved, aok := approvedByExitNode[r.ExitNodeID]
				if !aok {
					statusByRuleID[r.ID] = "pending"
				} else {
					approvedViaDomain := false
					for cid := range resolved {
						if approved[cid] {
							approvedViaDomain = true
							break
						}
					}
					if approvedViaDomain {
						statusByRuleID[r.ID] = "approved"
					} else {
						statusByRuleID[r.ID] = "pending"
					}
				}
			}
		}
	}
	// User-level preferred exit-node (fallback when no per-device
	// pref is set). Used by the "Use device's preferred exit-node"
	// button in the form.
	//
	// B343: which of THIS user's devices have enabled rules but no exit-node
	// preference (their rules are inert until the relay is assigned). A read
	// failure degrades to "nothing marked" rather than a broken page — the page is
	// a view, and the reconciler's journal remains the authority on WHY.
	noExitNodeDevices, inertErr := s.DevicesWithoutExitNodePrefForService(c.UserID)
	if inertErr != nil {
		noExitNodeDevices = map[string]int{}
	}
	// B347 (2026-10-04): the SAME state, but page-independent. The badges above
	// live inside the per-device rule GROUPS, and the groups come from the
	// current page of rule rows — measured live, page 1 of 5 held only
	// skyworker's rows, so «cyborg (1/500)» in the picker had no marker anywhere
	// and adding a rule for it changed nothing the operator could see. This read
	// covers the WHOLE rule set in one query, so the strip below is the answer
	// that does not move when the operator pages. A read failure degrades to an
	// empty strip (the groups and the B343 banner still render) rather than
	// breaking the page.
	deviceStatus, statusErr := s.DeviceStatusRowsForService(c.UserID)
	if statusErr != nil {
		deviceStatus = []DeviceStatusRow{}
	}
	// B347: the post-save flash must name the relay the operator just got
	// («правило добавлено — устройство выходит через emilia»), because the
	// duplicate notice they used to get answered a different question. The relay
	// comes from the SAME DeviceInfo the group badge uses, resolved from the
	// saved form's device, so the flash and the badge cannot disagree.
	assignedRelay := ""
	if r.URL.Query().Get("applied") != "" {
		wantID := r.URL.Query().Get("form_device_id")
		for _, di := range deviceInfos {
			if wantID != "" && di.ID == wantID {
				assignedRelay = di.PreferredExitNode
				break
			}
		}
	}
	s.Backend.RenderWithLayout(w, r, "exit_rules.html", c, map[string]any{
		"Page":              "exit-rules",
		"Title":             "Exit Rules",
		"Rules":             rules,
		"Devices":           devices,
		"DeviceInfos":       deviceInfos,
		"DeviceRoutes":      deviceRoutes,
		"ExitNodes":         exitServers,
		"DeviceNames":       deviceNames,
		"Grouped":           grouped,
		"GroupedByHostname": groupedByHostname,
		// 2026-09-07: B237.22 / TD-11 — UI-only CDN-grouped
		// view. Same shape as GroupedByHostname but each
		// (host, exitNode) value is []CDNDisplayItem
		// instead of []db.DeviceRule. The template iterates
		// this and renders CDN groups as <details>
		// headers + the per-CIDR rows underneath. Storage
		// is unchanged; only the view layer is affected.
		"GroupedByHostnameCDN": groupedByHostnameCDN,
		// B276.2 (2026-09-21): the dedicated ALL-DEVICES section.
		// Rules with all_devices=true are de-duplicated by natural
		// key here so the section renders one logical rule with a
		// "применено к N устройств(ам)" badge instead of N rows
		// duplicated across the per-host view.
		"AllDevicesByExitNodeCDN": allDevicesByExitNodeCDN,
		// v1.5.43: pagination. The template uses these to render
		// "Showing X–Y of Z" + the prev/next controls. Rules
		// above is the page slice only — TotalRules below is
		// the unpaged count (kept for the system-load badge).
		"RulePage":  rulePage,
		"PageRules": len(rules),
		// 2026-08-25 (B182): per-rule headscale-state status
		// for the three-state ✅/⏳/⚠️ badge. See the
		// for-loop above for the four possible values.
		"StatusByRuleID":  statusByRuleID,
		"TotalRules":      totalRules,
		"MaxTotalRules":   maxTotal,
		"LoadPct":         loadPct,
		"UserFacingCount": userFacingCount,
		"MaxPerUser":      maxPerUser,
		"MaxPerDevice":    maxPerDeviceLimit,
		// 2026-08-06: preferred-exit-node cross-check. The
		// template renders a warning banner when MismatchCount > 0
		// and offers UserPreferred as a "use this" button.
		"MismatchCount": mismatchCount,
		"UserPreferred": userPreferredHost,
		"FormValues": map[string]string{
			"device_id":    formDeviceID,
			"exit_node":    formExitNode,
			"target_type":  formTargetType,
			"target_value": formTargetValue,
			"action":       formAction,
		},
		"duplicate":     duplicate,
		"warn":          r.URL.Query().Get("warn"),
		"err":           errMsg,
		"target":        target,
		"existing_id":   existingIDInt,
		"blocking_ip":   blockingIP,
		"parent_domain": parentDomain,
		"partial":       partial,
		"HasRoutes":     anyRoutes,
		// B328: the result of the «распространить на все мои устройства» action.
		// spread="" (absent) renders nothing; "ok" reports how many devices and rows
		// it produced; "marked" means the intent is recorded but the immediate fan-out
		// failed and the maintenance pass will retry; "already" means the rule was
		// already an all-devices rule. A silent success was the trap here: the action
		// can legitimately create zero rows (the other devices already had the rule),
		// so "nothing happened" must be distinguishable from "nothing needed doing".
		"spread":         r.URL.Query().Get("spread"),
		"spread_devices": spreadDevices,
		"spread_created": spreadCreated,
		"spread_err":     r.URL.Query().Get("spread_err"),
		// B343 (2026-10-02, operator report): «правило уже есть, а доступа нет».
		// A device with ENABLED rules and NO exit-node preference has inert rules —
		// nothing pins it to a relay, so the client never selects one — and until
		// now the page said nothing about it while the ACL looked correct (the
		// per-CIDR pins come from prefix_owner since B275). The badge and the banner
		// are what make the state visible; the reconciler derives the relay on its
		// own tick (B341.1), and when it cannot, the journal carries the reason.
		"no_exit_node_devices": noExitNodeDevices,
		"no_exit_node_list":    inertDeviceLabels(noExitNodeDevices),
		// B347 (2026-10-04): every device with enabled rules + the relay it is
		// pinned to, computed from the whole rule set so the answer is not
		// paginated away, plus the relay named in the post-save flash.
		"device_status": deviceStatus,
		"AssignedRelay": assignedRelay,
		// B348 (2026-10-04): the device drill-down and the complete inventory.
		// DeviceFilter is the hostname whose rules THIS page shows ("" = the
		// paginated list of everything), DeviceRuleCount is that device's rule
		// count for the banner, and DeviceIndex is every device with enabled
		// rules + its count, so nothing is invisible behind a page window.
		"DeviceFilter":    deviceFilter,
		"DeviceRuleCount": len(rules),
		"DeviceIndex":     deviceIndex,
	})
}
