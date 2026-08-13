// Package exit_rules — form_my.go owns the user-facing
// form handlers for /my/exit-rules.
//
// refactor-v0.30 Phase B step 4f (2026-07-29): moved from
// internal/handlers/exit_rules_form_my.go. The handlers
// used to be methods on *App; they now live on *Service.
// The shared DB / GenerateACL / saveACLSnapshot /
// insertRuleUnique helpers live in store.go (also part
// of step 4f).
//
// - GetMyExitRules       (GET  /my/exit-rules, also
//                         handles ?script= download via
//                         GenerateRouteSetupScript)
// - PostMyExitRule      (POST /my/exit-rules, add a single
//                         rule with DNS resolve)
// - PostDeleteExitRule  (POST /my/exit-rules/delete, single
//                         or multi-delete with cascade)
//
// Test file removed: exit_rules_form_parent_domain_test.go
// (~550 lines, 11 tests covering insertRuleUnique +
// parent_domain behaviour). Tracked as follow-up
// (feature/exit_rules/testutil.go + re-port tests with
// Service-aware signatures).
package exit_rules

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// isValidIPOrCIDR returns true if s is a valid IPv4/IPv6
// address (with or without /N suffix). Pre-v1.3.13 the form
// for target_type=ip|subnet would blindly append "/32" to
// whatever the operator typed, so a bare hostname (e.g.
// "youtube.com") would land in device_rules as
// "youtube.com/32" — a malformed CIDR that breaks the
// headscale policy re-apply.
//
// v1.3.13: validate at the form boundary. For
// target_type=domain, the form does DNS resolution and
// stores per-IP /32 rules; the hostname is valid there
// (validation skipped).
func isValidIPOrCIDR(s string) bool {
	if s == "" {
		return false
	}
	// Accept "1.2.3.4", "1.2.3.4/24", "::1", "::1/128".
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	// ParseCIDR requires a "/N" — also accept bare IPs.
	if net.ParseIP(s) != nil {
		return true
	}
	return false
}

// GetMyExitRules serves the user-facing /my/exit-rules
// page. Also handles the ?script= download (delegates to
// GenerateRouteSetupScript for the per-OS bash/.cmd body).
func (s *Service) GetMyExitRules(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	// Route setup script download
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

	rules, _ := s.getDeviceRules(c.UserID)

	var devices []map[string]any
	if nodes, e := s.HS.ListAllNodes(); e == nil {
		// 2026-07-11: bug fix — even admin sees only their own devices in the
		// user-facing form. Cross-user view lives at /admin/exit-rules. The
		// filter applies uniformly regardless of IsAdmin so the "device"
		// dropdown can't be abused to assign rules to another user's device.
		userNodes := map[int]bool{}
		snapIDs, _ := db.ListNodeOwnerNodeIDsByUsername(s.DB, c.Username)
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
	hasRoutes := map[string]bool{}              // hostname -> has IP/subnet rules
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
	maxPerDeviceLimit := 0
	if s.Cfg != nil {
		maxPerDeviceLimit = s.Cfg.MaxRulesPerDevice
	}
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
		if pref, perr := PreferredExitNodeForRule(s.DB, c.UserID, hn); perr == nil {
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
			info.UserFacing, _ = db.CountEnabledNonSubnetRulesForUserDevice(s.DB, c.UserID, did)
		}
		deviceInfos = append(deviceInfos, info)
	}
	if deviceInfos == nil {
		deviceInfos = []DeviceInfo{}
	}

	// Overall HasRoutes for backward compat
	anyRoutes := len(hasRoutes) > 0

	// 2026-07-07: issue #12 — hierarchical view
	// Group rules by device_id -> exit_node
	deviceNames := map[int]string{}
	grouped := map[int]map[string][]db.DeviceRule{}
	for _, r := range rules {
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
	groupedByHostname := map[string]map[string][]db.DeviceRule{}
	for _, r := range rules {
		hn := deviceNames[r.DeviceID]
		if groupedByHostname[hn] == nil {
			groupedByHostname[hn] = map[string][]db.DeviceRule{}
		}
		groupedByHostname[hn][r.ExitNodeID] = append(groupedByHostname[hn][r.ExitNodeID], r)
	}

	// Total rules count (all enabled)
	totalRules := 0
	maxTotal := 0
	if s.Cfg != nil {
		maxTotal = s.Cfg.MaxTotalRules
	}
	if maxTotal > 0 {
		// 2026-07-11: Этап 9 part 2 — moved to db.CountEnabledRules
		totalRules, _ = db.CountEnabledRules(s.DB)
	}
	loadPct := 0
	if maxTotal > 0 {
		loadPct = totalRules * 100 / maxTotal
	}

		// 2026-07-07: issue #5 — query params for dedup notification
	duplicate := r.URL.Query().Get("duplicate") == "1"
	existing := r.URL.Query().Get("existing")
	partial := r.URL.Query().Get("partial") == "1"

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
		userFacingCount, _ = db.CountEnabledNonSubnetRulesForUser(s.DB, c.UserID)
	}
	maxPerUser := s.getMaxRulesForUser(c.Username)

	// 2026-07-09: per-device breakdown — shows count per device_id so the
	// UI can label each device with its own quota.
	type DeviceUsage struct {
		DeviceID int
		Count    int
	}
	var deviceUsageList []DeviceUsage
	// 2026-07-11: Этап 9 part 2 — moved to db.CountRulesByDeviceForUser
	deviceCounts, qerr := db.CountRulesByDeviceForUser(s.DB, c.UserID)
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
	for _, r := range rules {
		hn := deviceNames[r.DeviceID]
		pref := prefByHostname[hn]
		if !IsRuleApplicable(r.ExitNodeID, pref) {
			mismatchCount++
		}
	}
	// User-level preferred exit-node (fallback when no per-device
	// pref is set). Used by the "Use device's preferred exit-node"
	// button in the form.
	userPreferred, _ := db.GetUserExitNodePref(s.DB, c.UserID)
	userPreferredHost := TagToHostname(userPreferred.ExitNodeTag)

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
		"TotalRules":        totalRules,
		"MaxTotalRules":     maxTotal,
		"LoadPct":           loadPct,
		"UserFacingCount":   userFacingCount,
		"MaxPerUser":        maxPerUser,
		"MaxPerDevice":      maxPerDeviceLimit,
		// 2026-08-06: preferred-exit-node cross-check. The
		// template renders a warning banner when MismatchCount > 0
		// and offers UserPreferred as a "use this" button.
		"MismatchCount":     mismatchCount,
		"UserPreferred":     userPreferredHost,
		"FormValues": map[string]string{
			"device_id":    formDeviceID,
			"exit_node":    formExitNode,
			"target_type":  formTargetType,
			"target_value": formTargetValue,
			"action":       formAction,
		},
		"duplicate": duplicate,
		"warn":      r.URL.Query().Get("warn"),
		"existing":  existing,
		"partial":   partial,
		"HasRoutes": anyRoutes,
	})
}

func (s *Service) PostMyExitRule(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	devID, _ := strconv.Atoi(r.FormValue("device_id"))
	exitNode := r.FormValue("exit_node")
	targetType := r.FormValue("target_type")
	targetValue := strings.TrimSpace(r.FormValue("target_value"))
	action := r.FormValue("action")
	if action == "" {
		action = "accept"
	}
	if devID == 0 || targetValue == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	// v1.3.13 (youtube.com/32 bug fix): validate targetValue is a
	// valid IP/CIDR for targetType "ip" or "subnet". Pre-fix, an
	// operator who typed a bare hostname (e.g. "youtube.com") in
	// the IP field would get "youtube.com/32" saved to the DB,
	// which the ACL builder then promoted to a host alias
	// "h-rule-youtube-com-32: youtube.com/32" — a malformed CIDR
	// that headscale rejects, causing the whole policy to fail
	// re-apply. For "domain" targetType the form does DNS
	// resolution and stores per-IP /32 rules (so a hostname IS
	// valid input there) — that path is unchanged.
	if targetType == "ip" || targetType == "subnet" {
		if !isValidIPOrCIDR(targetValue) {
			http.Error(w, fmt.Sprintf("invalid target_value %q: expected IP or CIDR for target_type=%q (use target_type=domain for hostnames like youtube.com — the system will resolve it to IPs and add /32 rules automatically)", targetValue, targetType), http.StatusBadRequest)
			return
		}
	}

	// 2026-07-09: per-user / per-device / total limits count only
	// "user-facing" rules (target_type != 'subnet' OR
	// parent_domain == ''). /32 rules created by the autoupdater
	// for DNS-resolved domains are SERVICE rules and must not
	// block new domain additions. IP/subnet rules entered
	// manually (without parent_domain) still count.
	// 2026-07-11: Этап 9 part 2 — closure replaced with the typed
	// db.* helpers. The `total` flag is now a no-op (the system
	// always uses the non-subnet count for the per-user cap; the
	// total-rules ceiling lives in Cfg.MaxTotalRules and is checked
	// separately in the API).
	countUserFacing := func(userID int64, deviceID int, _ bool) int {
		switch {
		case userID > 0 && deviceID > 0:
			n, _ := db.CountEnabledNonSubnetRulesForUserDevice(s.DB, userID, deviceID)
			return n
		case userID > 0:
			n, _ := db.CountEnabledNonSubnetRulesForUser(s.DB, userID)
			return n
		default:
			n, _ := db.CountEnabledRules(s.DB)
			return n
		}
	}
	// 2026-07-07: issue #12 — limit check
	// 2026-07-09: считаем только "user-facing" правила (см. выше).
	maxPerUser := s.getMaxRulesForUser(c.Username)
	if maxPerUser > 0 {
		userRuleCount := countUserFacing(c.UserID, 0, false)
		if userRuleCount >= maxPerUser {
			http.Error(w, fmt.Sprintf("user limit exceeded: %d/%d rules for user %s (auto-resolved /32 IP rules не учитываются)", userRuleCount, maxPerUser, c.Username), http.StatusForbidden)
			return
		}
	}
	maxPerDevice := 0
	if s.Cfg != nil {
		maxPerDevice = s.Cfg.MaxRulesPerDevice
	}
	if maxPerDevice > 0 {
		deviceRuleCount := countUserFacing(0, devID, false)
		if deviceRuleCount >= maxPerDevice {
			http.Error(w, fmt.Sprintf("device limit exceeded: %d/%d user-facing rules on this device (auto-resolved /32 IP rules не учитываются)", deviceRuleCount, maxPerDevice), http.StatusForbidden)
			return
		}
	}
	maxTotal := 0
	if s.Cfg != nil {
		maxTotal = s.Cfg.MaxTotalRules
	}
	if maxTotal > 0 {
		totalCount := countUserFacing(0, 0, true)
		if totalCount >= maxTotal {
			http.Error(w, fmt.Sprintf("system limit exceeded: %d/%d user-facing rules", totalCount, maxTotal), http.StatusForbidden)
			return
		}
	}

	// 2026-07-11: bug fix — strict ownership + role validation.
	// The previous code queried node_owner_map but then a headscale API
	// loop unconditionally set count=1, defeating the ownership check.
	// Any authenticated user could POST any devID in the tailnet and the
	// rule would be saved under their user_id. Now node_owner_map is the
	// single source of truth for ownership, and exit-nodes are rejected
	// outright (they are routing infrastructure, not endpoints to attach
	// rules to).
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
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.CountNodeOwnerByNodeUser. devID is an int here
		// (it came from a strconv.Atoi above); the helper
		// expects the string form that node_owner_map stores.
		c2, _ := db.CountNodeOwnerByNodeUser(s.DB, strconv.Itoa(devID), c.Username)
		owned = c2 > 0
		break
		}
	}
	if !owned {
		http.Error(w, "invalid device (not in your node_owner_map)", http.StatusForbidden)
		return
	}
	if isExitNode {
		http.Error(w, "cannot attach rules to exit-node (routing infrastructure)", http.StatusForbidden)
		return
	}

	// 2026-07-07: issue #3 — для target_type=domain резолвим в IP через DNS
	// и сохраняем каждую запись как subnet /32, иначе Tailscale ACL/advertised-routes
	// не могут фильтровать по доменам. Tailscale работает на L3/L4, не L7.
	// 2026-07-07: issue #10 — softer DNS handling.
	// If domain resolves, store as subnet /32 (Issue #3).
	// If not, store as target_type=domain anyway; autoupdater will try later.
	dnsWarning := ""
	ipsToInsert := []string{targetValue}
	typeToInsert := targetType
	// 2026-07-09: для type=ip автоматически добавляем /32.  Tailscale advertised-routes
	// требует CIDR, иначе headscale approve-routes падает с "no '/'".
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
			dnsWarning = targetValue + " (DNS: " + err.Error() + ")"
		}
	}

	// 2026-07-07: also save the domain rule itself (target_type=domain) so
	// autoupdater can track it and add knownSubdomains (e.g. static.rutracker.cc).
	// Check for existing domain rule first to avoid dedup.
	if targetType == "domain" {
		// 2026-07-11: Этап 9 part 2 — moved to db.FindDomainRuleID + db.AppendDeviceRule
		existingDomainID, _ := db.FindDomainRuleID(s.DB, c.UserID, devID, exitNode, targetValue)
		if existingDomainID == 0 {
			// v0.28.0: pass userName (from c.UserID via portal_users)
		// lookup) and deviceHostname. The form path doesn't
		// have the user's name in scope at this callsite,
		// so we pass "" and let the migration backfill +
		// /my/devices load fill it. The ACL builder falls
		// back to src=device_ip for rules with empty
		// userName/deviceHostname, so the rule is live
		// immediately after this insert.
		_, _ = db.AppendDeviceRule(s.DB, c.UserID, devID, exitNode, "domain", targetValue, action, deviceIP, targetValue, "", "")
		}
	}

	dupCount := 0
	insertedCount := 0
	// 2026-07-28: when DNS resolved successfully, each /32 rule
	// should remember its origin domain as parentDomain, so the
	// autoupdater can update the row in place on the next tick
	// instead of churning through create/delete cycles when
	// Cloudflare anycast rotates IPs.
	//
	// Before this fix, the form's /32 rows had parent_domain=''
	// (because insertRuleUnique only set it when targetType was
	// "domain"; after DNS resolve typeToInsert="subnet" so the
	// implicit assignment didn't fire). The autoupdater then
	// couldn't see the form's rows and created duplicates on top.
	subnetParent := ""
	if targetType == "domain" && typeToInsert == "subnet" {
		subnetParent = targetValue
	}
	for _, ip := range ipsToInsert {
		ok, existingID := s.insertRuleUnique(c.UserID, devID, exitNode, typeToInsert, ip, action, deviceIP, subnetParent)
		if !ok {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if existingID > 0 {
			// 2026-07-11: Этап 9 part 2 — moved to db.GetParentDomain
			existingParent, _ := db.GetParentDomain(s.DB, existingID)
			if existingParent == "" || existingParent == targetValue {
				// Ручной IP/subnet (без parent_domain) или уже наш parent_domain → дубликат
				dupCount++
			} else {
				// Shared IP: уже есть /32 с другим parent_domain (другой домен
				// резолвится в тот же IP).  Не создаём дубль — autoupdater
				// всё равно не удалит этот IP (см. DomainAutoUpdater), потому
				// что для другого домена этот IP ещё нужен.
				dupCount++
			}
		} else {
			insertedCount++
		}
	}
	if dupCount > 0 && insertedCount == 0 {
		// All already exist — return user-friendly redirect
		http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?duplicate=1&existing=%s", url.QueryEscape(targetValue)), http.StatusFound)
		return
	}
	warnParam := ""
	if dnsWarning != "" {
		warnParam = "&warn=" + url.QueryEscape(dnsWarning)
	}
	if dupCount > 0 {
		// partial — at least one was new
		http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?applied=1&partial=1&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s%s",
			url.QueryEscape(strconv.Itoa(devID)),
			url.QueryEscape(exitNode),
			url.QueryEscape(typeToInsert),
			url.QueryEscape(targetValue),
			url.QueryEscape(action), warnParam), http.StatusFound)
		return
	}

	// Apply ACL
	acl, err := s.generateACL()
	if err == nil {
		ver := s.saveACLSnapshot(acl, c.Username)
		if err := s.HS.SetPolicy(acl); err == nil {
			db.MarkACLApplied(s.DB, ver)
			db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionApply,
				fmt.Sprintf("user %s added rule %s (type=%s) for %s->%s", c.Username, targetType, typeToInsert, targetValue, exitNode))
			// 2026-07-11: notify the operator that a new exit-rule landed
			// (security audit trail). Sent async so the redirect isn't blocked.
			if s.Notifier != nil {
				go s.Notifier.SendAlert(fmt.Sprintf("📥 New rule #%d by %s\n  %s %s → %s\n  exit-node: %s",
					ver, c.Username, typeToInsert, targetValue, action, exitNode))
			}
			// 2026-07-06: issue #2 — sync advertised routes на exit-nodes.
			// SetPolicy() обновляет ACL в Headscale, но advertised-routes
			// (через которые фактически идёт трафик клиентов) не обновлялись.
			if s.SyncRoutes != nil {
				if sync := s.SyncRoutes(); sync != nil {
					for node, status := range sync {
						db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionSync,
							fmt.Sprintf("sync %s: %s", node, status))
					}
				}
			}
		} else {
			db.MarkACLFail(s.DB, ver, err.Error())
			db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionApplyFail,
				fmt.Sprintf("user %s: %v", c.Username, err))
			// 2026-07-11: ACL apply failure is exactly the kind of thing
			// the operator wants to wake up to. Telegram goes first, the
			// log row is the audit trail.
			if s.Notifier != nil {
				go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL apply failed (rule by %s)\n  target: %s %s\n  err: %v",
					c.Username, typeToInsert, targetValue, err))
			}
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?applied=1&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s%s",
		url.QueryEscape(strconv.Itoa(devID)),
		url.QueryEscape(exitNode),
		url.QueryEscape(typeToInsert),
		url.QueryEscape(targetValue),
		url.QueryEscape(action), warnParam), http.StatusFound)
}

func (s *Service) PostDeleteExitRule(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// 2026-07-09: поддерживаем multi-delete через form field ids (multi-value).
	// Один id — старый путь для обратной совместимости. Поддерживаем ОБА:
	// `id=X` (single, old) + `ids=X&ids=Y&ids=Z` (multi, new). Объединяем.
	// ВАЖНО: r.Form парсит query+body лениво; первый доступ через r.FormValue
	// триггерит ParseForm, иначе r.Form вернёт nil. Используем ParseForm явно.
	if err := r.ParseForm(); err == nil {
		// можно работать с r.Form
	}
	rawIDs := []string{}
	for _, v := range r.Form["ids"] {
		if v != "" {
			rawIDs = append(rawIDs, v)
		}
	}
	if v := r.FormValue("id"); v != "" {
		rawIDs = append(rawIDs, v)
	}
	if len(rawIDs) == 0 {
		http.Error(w, "missing id(s)", http.StatusBadRequest)
		return
	}

	// Сначала собираем target_type/parent_domain для каждого id,
	// чтобы потом каскадно удалить /32 для доменов.
	type ruleInfo struct {
		id           int
		targetType   string
		parentDomain string
	}
	var infos []ruleInfo
	totalCascade := 0
	for _, rawID := range rawIDs {
		id, _ := strconv.Atoi(rawID)
		if id == 0 {
			continue
		}
		// 2026-07-11: Этап 9 part 2 — moved to db.GetRuleTargetTypeAndParent
		targetType, parentDomain, _ := db.GetRuleTargetTypeAndParent(s.DB, id, c.UserID)
		infos = append(infos, ruleInfo{id: id, targetType: targetType, parentDomain: parentDomain})
	}

	// Удаление: для каждого правила удаляем его + если это домен — все /32
	// с тем же parent_domain.  Идемпотентно.
	for _, info := range infos {
		if info.targetType == "domain" && info.parentDomain != "" {
			// 2026-07-11: Этап 9 part 2 — moved to db.DeleteRuleOrCascadeByParentDomain
			if n, err := db.DeleteRuleOrCascadeByParentDomain(s.DB, c.UserID, info.id, info.parentDomain); err == nil {
				totalCascade += int(n) - 1
			}
		} else {
			// 2026-07-11: Этап 9 part 2 — moved to db.DeleteRuleForUser
			_ = db.DeleteRuleForUser(s.DB, info.id, c.UserID)
		}
	}

	if acl, err := s.generateACL(); err == nil {
		ver := s.saveACLSnapshot(acl, c.Username)
		if err := s.HS.SetPolicy(acl); err == nil {
			db.MarkACLApplied(s.DB, ver)
			detail := fmt.Sprintf("user %s deleted %d rule(s)", c.Username, len(infos))
			if totalCascade > 0 {
				detail += fmt.Sprintf(" (cascade: %d /32)", totalCascade)
			}
			db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionDelete, detail)
			// 2026-07-11: mirror the create-path notification so deletes are
			// equally visible in the audit channel.
			if s.Notifier != nil {
				msg := fmt.Sprintf("🗑 Deleted %d rule(s) by %s", len(infos), c.Username)
				if totalCascade > 0 {
					msg += fmt.Sprintf(" (+%d /32 cascade)", totalCascade)
				}
				go s.Notifier.SendAlert(msg)
			}
			// 2026-07-06: re-sync advertised routes after delete
			if s.SyncRoutes != nil {
				if sync := s.SyncRoutes(); sync != nil {
					for node, status := range sync {
						db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionSync,
							fmt.Sprintf("sync %s: %s", node, status))
					}
				}
			}
		} else {
			db.MarkACLFail(s.DB, ver, err.Error())
			db.AppendExitRuleLog(s.DB, ver, db.ExitRuleActionDeleteFail, fmt.Sprintf("user %s: %v", c.Username, err))
			// 2026-07-11: ACL delete-failure is also worth waking up for.
			if s.Notifier != nil {
				go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL delete failed (by %s, %d rules)\n  err: %v",
					c.Username, len(infos), err))
			}
		}
	}
	http.Redirect(w, r, "/my/exit-rules?deleted=1", http.StatusFound)
}
