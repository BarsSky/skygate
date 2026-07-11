package handlers

// exit_rules_form_my.go — user-facing form handlers for /my/exit-rules.
// - GetMyExitRules       (GET  /my/exit-rules, also handles ?script= download)
// - PostMyExitRule      (POST /my/exit-rules, add a single rule with DNS resolve)
// - PostDeleteExitRule  (POST /my/exit-rules/delete, single or multi-delete with cascade)
//
// These handlers share DB / GenerateACL / saveACLSnapshot / insertRuleUnique
// which remain in exit_rules.go. Per-handler types (DeviceInfo, DeviceUsage,
// ruleInfo) and closures (countUserFacing) are defined inline where used.

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)



func (a *App) GetMyExitRules(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
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
		script, err := a.GenerateRouteSetupScript(int(c.UserID), devID, os, restore)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// Build filename with device name if specified
		fname := "skygate-routes"
		if restore {
			fname = "skygate-routes-restore"
		}
		if devID > 0 {
			if nodes, _ := a.HS.ListAllNodes(); nodes != nil {
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

	rules, _ := a.getDeviceRules(int(c.UserID))

	var devices []map[string]any
	if nodes, e := a.HS.ListAllNodes(); e == nil {
		userNodes := map[int]bool{}
		if !c.IsAdmin {
			if rows, qe := a.DB.Query("SELECT node_id FROM node_owner_map WHERE username=?", c.Username); qe == nil {
				for rows.Next() {
					var nid int
					if rows.Scan(&nid) == nil {
						userNodes[nid] = true
					}
				}
				rows.Close()
			}
		}
		for _, n := range nodes {
			if !c.IsAdmin {
				nid, _ := strconv.Atoi(n.ID)
				if !userNodes[nid] {
					continue
				}
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
	if nodes, err := a.HS.ListExitNodes(); err == nil {
		for _, n := range nodes {
			exitServers = append(exitServers, map[string]any{"hostname": n.Hostname})
		}
	}
	if exitServers == nil {
		exitServers = []map[string]any{}
	}

	// Build per-device route info — match by hostname (resolved from IP)
	deviceRoutes := map[string][]DeviceRule{}  // hostname -> rules
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
		ID            string
		Hostname      string
		RuleCount     int
		UserFacing    int // 2026-07-09: user-facing count (excludes /32 from autoupdater)
		HasRoutes     bool
		MaxForDevice  int // 2026-07-09: per-device limit (MaxRulesPerDevice)
	}
	var deviceInfos []DeviceInfo
	maxPerDeviceLimit := 0
	if a.Cfg != nil {
		maxPerDeviceLimit = a.Cfg.MaxRulesPerDevice
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
		// Count user-facing rules for THIS device (excludes autoupdater /32).
		did, _ := strconv.Atoi(info.ID)
		if did > 0 {
			a.DB.QueryRow(
				"SELECT COUNT(*) FROM device_rules WHERE user_id=? AND device_id=? AND enabled=1 AND (target_type!='subnet' OR COALESCE(parent_domain,'')='')",
				c.UserID, did).Scan(&info.UserFacing)
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
	grouped := map[int]map[string][]DeviceRule{}
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
			grouped[r.DeviceID] = map[string][]DeviceRule{}
		}
		grouped[r.DeviceID][r.ExitNodeID] = append(grouped[r.DeviceID][r.ExitNodeID], r)
	}

	// 2026-07-09: GroupedByHostname collapses rules from the SAME logical
	// device that were accidentally recorded under multiple headscale node
	// ids. node IDs are monotonically increasing and never re-used: when a
	// node gets re-provisioned (eg tagged, re-keyed, brand-new host) the
	// replacement arrives under a new id, but pre-existing rules still
	// carry the OLD id. The hierarchical view used to render those as two
	// identical sections ("skyworker" twice). GroupedByHostname reroutes
	// the template over (hostname -> exitNode -> []rules), so device_id=1
	// and device_id=9 (both skyworker) collapse into one section.
	groupedByHostname := map[string]map[string][]DeviceRule{}
	for _, r := range rules {
		hn := deviceNames[r.DeviceID]
		if groupedByHostname[hn] == nil {
			groupedByHostname[hn] = map[string][]DeviceRule{}
		}
		groupedByHostname[hn][r.ExitNodeID] = append(groupedByHostname[hn][r.ExitNodeID], r)
	}

	// Total rules count (all enabled)
	totalRules := 0
	if a.Cfg != nil && a.Cfg.MaxTotalRules > 0 {
		a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE enabled=1").Scan(&totalRules)
	}
	loadPct := 0
	maxPerDeviceMax := 0
	if a.Cfg != nil {
		maxPerDeviceMax = a.Cfg.MaxTotalRules
		if a.Cfg.MaxTotalRules > 0 {
			loadPct = totalRules * 100 / a.Cfg.MaxTotalRules
		}
	}
	_ = loadPct // used by /admin/exit-rules/nodes; not used here but compiler may complain

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
		a.DB.QueryRow(
			"SELECT COUNT(*) FROM device_rules WHERE user_id=? AND enabled=1 AND (target_type!='subnet' OR COALESCE(parent_domain,'')='')",
			c.UserID).Scan(&userFacingCount)
	}
	maxPerUser := a.getMaxRulesForUser(c.Username)

	// 2026-07-09: per-device breakdown — shows count per device_id so the
	// UI can label each device with its own quota.
	type DeviceUsage struct {
		DeviceID int
		Count    int
	}
	var deviceUsageList []DeviceUsage
	rowsUsage, qerr := a.DB.Query(
		"SELECT device_id, COUNT(*) FROM device_rules WHERE user_id=? AND enabled=1 AND (target_type!='subnet' OR COALESCE(parent_domain,'')='') GROUP BY device_id",
		c.UserID)
	if qerr == nil {
		for rowsUsage.Next() {
			var du DeviceUsage
			if rowsUsage.Scan(&du.DeviceID, &du.Count) == nil {
				deviceUsageList = append(deviceUsageList, du)
			}
		}
		rowsUsage.Close()
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

a.renderWithLayout(w, r, "exit_rules.html", c, map[string]any{
		"Page":             "exit-rules",
		"Title":            "Exit Rules",
		"Rules":            rules,
		"Devices":          devices,
		"DeviceInfos":      deviceInfos,
		"DeviceRoutes":     deviceRoutes,
		"ExitNodes":        exitServers,
		"DeviceNames":      deviceNames,
		"Grouped":          grouped,
		"GroupedByHostname": groupedByHostname,
		"TotalRules":       totalRules,
		"MaxTotalRules":    maxPerDeviceMax,
		"LoadPct":          loadPct,
		"UserFacingCount":  userFacingCount,
		"MaxPerUser":       maxPerUser,
		"MaxPerDevice":     maxPerDeviceLimit,
				"FormValues": map[string]string{
			"device_id":    formDeviceID,
			"exit_node":    formExitNode,
			"target_type":  formTargetType,
			"target_value": formTargetValue,
			"action":       formAction,
		},
		"duplicate": duplicate,
		"warn":  r.URL.Query().Get("warn"),
		"existing":  existing,
		"partial":   partial,

"HasRoutes":   anyRoutes,
	})
}

func (a *App) PostMyExitRule(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
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
		http.Error(w, "missing fields", 400)
		return
	}

	// 2026-07-09: issue — лимиты per-user / per-device / total теперь считают
	// только "user-facing" правила (target_type != 'subnet' ИЛИ
	// parent_domain == '').  /32 правила, созданные autoupdater'ом для
	// резолва домена, считаются СЛУЖЕБНЫМИ и не должны блокировать
	// добавление новых доменов.  В противном случае у пользователя
	// 9 доменов и 243 правила → лимит 200 забит и невозможно ничего
	// добавить.  IP / subnet правила, введённые вручную (без parent_domain),
	// по-прежнему считаются.
	countUserFacing := func(userID int64, deviceID int, total bool) int {
		q := "SELECT COUNT(*) FROM device_rules WHERE enabled=1 AND (target_type != 'subnet' OR COALESCE(parent_domain,'') = '')"
		args := []any{}
		if userID > 0 { q += " AND user_id=?"; args = append(args, userID) }
		if deviceID > 0 { q += " AND device_id=?"; args = append(args, deviceID) }
		var n int
		_ = a.DB.QueryRow(q, args...).Scan(&n)
		return n
	}
	// 2026-07-07: issue #12 — limit check
	// 2026-07-09: считаем только "user-facing" правила (см. выше).
	maxPerUser := a.getMaxRulesForUser(c.Username)
	if maxPerUser > 0 {
		userRuleCount := countUserFacing(c.UserID, 0, false)
		if userRuleCount >= maxPerUser {
			http.Error(w, fmt.Sprintf("user limit exceeded: %d/%d rules for user %s (auto-resolved /32 IP rules не учитываются)", userRuleCount, maxPerUser, c.Username), 403)
			return
		}
	}
	maxPerDevice := a.Cfg.MaxRulesPerDevice
	if maxPerDevice > 0 {
		deviceRuleCount := countUserFacing(0, devID, false)
		if deviceRuleCount >= maxPerDevice {
			http.Error(w, fmt.Sprintf("device limit exceeded: %d/%d user-facing rules on this device (auto-resolved /32 IP rules не учитываются)", deviceRuleCount, maxPerDevice), 403)
			return
		}
	}
	maxTotal := a.Cfg.MaxTotalRules
	if maxTotal > 0 {
		totalCount := countUserFacing(0, 0, true)
		if totalCount >= maxTotal {
			http.Error(w, fmt.Sprintf("system limit exceeded: %d/%d user-facing rules", totalCount, maxTotal), 403)
			return
		}
	}

	// Validate device via node_owner_map, fallback headscale API
	var count int
	a.DB.QueryRow("SELECT COUNT(*) FROM node_owner_map WHERE node_id = ? AND username = ?", devID, c.Username).Scan(&count)
	// Resolve device Tailscale IP
	var deviceIP string
	if nodes, err := a.HS.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID == strconv.Itoa(devID) {
				count = 1
				if len(n.IPAddresses) > 0 {
					deviceIP = n.IPAddresses[0]
				}
				break
			}
		}
	}
	if count == 0 {
		http.Error(w, "invalid device", 403)
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
				if strings.Contains(a, ":") { continue }
				if seen[a] { continue }
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
		var existingDomainID int
		_ = a.DB.QueryRow(
			"SELECT id FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='domain' AND target_value=? LIMIT 1",
			c.UserID, devID, exitNode, targetValue).Scan(&existingDomainID)
		if existingDomainID == 0 {
			_, _ = a.DB.Exec(
				"INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, 'domain', ?, ?, ?, ?)",
				c.UserID, devID, exitNode, targetValue, action, deviceIP, targetValue)
		}
	}

	dupCount := 0
	dupIDs := []int{}
	insertedCount := 0
	for _, ip := range ipsToInsert {
		ok, existingID := a.insertRuleUnique(c.UserID, devID, exitNode, typeToInsert, ip, action, deviceIP)
		if !ok {
			http.Error(w, "db error", 500)
			return
		}
		if existingID > 0 {
			var existingParent string
			_ = a.DB.QueryRow("SELECT COALESCE(parent_domain,'') FROM device_rules WHERE id=?", existingID).Scan(&existingParent)
			if existingParent == "" || existingParent == targetValue {
				// Ручной IP/subnet (без parent_domain) или уже наш parent_domain → дубликат
				dupCount++
				dupIDs = append(dupIDs, existingID)
			} else {
				// Shared IP: уже есть /32 с другим parent_domain (другой домен
				// резолвится в тот же IP).  Не создаём дубль — autoupdater
				// всё равно не удалит этот IP (см. DomainAutoUpdater), потому
				// что для другого домена этот IP ещё нужен.
				dupCount++
				dupIDs = append(dupIDs, existingID)
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
	if dnsWarning != "" { warnParam = "&warn=" + url.QueryEscape(dnsWarning) }
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
	acl, err := a.GenerateACL()
	if err == nil {
		ver := a.saveACLSnapshot(acl, c.Username)
		if err := a.HS.SetPolicy(acl); err == nil {
			a.DB.Exec("UPDATE acl_snapshots SET applied_success=1 WHERE version=?", ver)
			a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'apply', ?)", ver,
				fmt.Sprintf("user %s added rule %s (type=%s) for %s->%s", c.Username, targetType, typeToInsert, targetValue, exitNode))
			// 2026-07-06: issue #2 — sync advertised routes на exit-nodes.
			// SetPolicy() обновляет ACL в Headscale, но advertised-routes
			// (через которые фактически идёт трафик клиентов) не обновлялись.
			if sync := a.SyncAdvertisedRoutes(); sync != nil {
				for node, status := range sync {
					a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'sync', ?)", ver,
						fmt.Sprintf("sync %s: %s", node, status))
				}
			}
		} else {
			a.DB.Exec("UPDATE acl_snapshots SET applied_success=0, error_msg=? WHERE version=?", err.Error(), ver)
			a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'apply_fail', ?)", ver,
				fmt.Sprintf("user %s: %v", c.Username, err))
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?applied=1&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s%s",
		url.QueryEscape(strconv.Itoa(devID)),
		url.QueryEscape(exitNode),
		url.QueryEscape(typeToInsert),
		url.QueryEscape(targetValue),
		url.QueryEscape(action), warnParam), http.StatusFound)
}

func (a *App) PostDeleteExitRule(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", 401)
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
		http.Error(w, "missing id(s)", 400)
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
	for _, s := range rawIDs {
		id, _ := strconv.Atoi(s)
		if id == 0 { continue }
		var targetType, parentDomain string
		_ = a.DB.QueryRow("SELECT target_type, COALESCE(parent_domain,'') FROM device_rules WHERE id=? AND user_id=?", id, c.UserID).Scan(&targetType, &parentDomain)
		infos = append(infos, ruleInfo{id: id, targetType: targetType, parentDomain: parentDomain})
	}

	// Удаление: для каждого правила удаляем его + если это домен — все /32
	// с тем же parent_domain.  Идемпотентно.
	for _, info := range infos {
		if info.targetType == "domain" && info.parentDomain != "" {
			res, _ := a.DB.Exec(
				"DELETE FROM device_rules WHERE user_id=? AND (id=? OR (target_type='subnet' AND parent_domain=?))",
				c.UserID, info.id, info.parentDomain)
			if n, err := res.RowsAffected(); err == nil {
				totalCascade += int(n) - 1
			}
		} else {
			a.DB.Exec("DELETE FROM device_rules WHERE id=? AND user_id=?", info.id, c.UserID)
		}
	}

	if acl, err := a.GenerateACL(); err == nil {
		ver := a.saveACLSnapshot(acl, c.Username)
		if err := a.HS.SetPolicy(acl); err == nil {
			a.DB.Exec("UPDATE acl_snapshots SET applied_success=1 WHERE version=?", ver)
			detail := fmt.Sprintf("user %s deleted %d rule(s)", c.Username, len(infos))
			if totalCascade > 0 {
				detail += fmt.Sprintf(" (cascade: %d /32)", totalCascade)
			}
			a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'delete', ?)", ver, detail)
			// 2026-07-06: re-sync advertised routes after delete
			if sync := a.SyncAdvertisedRoutes(); sync != nil {
				for node, status := range sync {
					a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'sync', ?)", ver,
						fmt.Sprintf("sync %s: %s", node, status))
				}
			}
		} else {
			a.DB.Exec("UPDATE acl_snapshots SET applied_success=0, error_msg=? WHERE version=?", err.Error(), ver)
			a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'delete_fail', ?)", ver, fmt.Sprintf("user %s: %v", c.Username, err))
		}
	}
	http.Redirect(w, r, "/my/exit-rules?deleted=1", http.StatusFound)
}
