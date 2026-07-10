package handlers

import (
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)



type DeviceRule struct {
	ID           int
	UserID       int
	DeviceID     int
	DeviceName   string
	ExitNodeID   string
	TargetType   string
	TargetValue  string
	Action       string
	DeviceIP     string
	Enabled      bool
	ParentDomain string
}



// 2026-07-07: issue #5 — dedup protection.
// Returns:
//   (true, existingID) — rule already existed; do not re-insert.
//   (true, 0)          — new rule inserted successfully.
//   (false, 0)         — DB error.
func (a *App) insertRuleUnique(userID int64, deviceID int, exitNode, targetType, targetValue, action, deviceIP string) (bool, int) {
	var existingID int
	err := a.DB.QueryRow(
		"SELECT id FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type=? AND target_value=? LIMIT 1",
		userID, deviceID, exitNode, targetType, targetValue).Scan(&existingID)
	if err == nil {
		return true, existingID
	}
	// not found → insert. Set parent_domain = target_value for domain rules so
	// autoupdater can track them and UI can show "auto" badge.
	parentDomain := ""
	if targetType == "domain" {
		parentDomain = targetValue
	}
	res, err := a.DB.Exec(
		"INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		userID, deviceID, exitNode, targetType, targetValue, action, deviceIP, parentDomain)
	if err != nil {
		return false, 0
	}
	newID, _ := res.LastInsertId()
	return true, int(newID)
}

func scanRules(rows *sql.Rows) ([]DeviceRule, error) {
	var rr []DeviceRule
	for rows.Next() {
		var r DeviceRule
		var en int
		var pd string
		if err := rows.Scan(&r.ID, &r.UserID, &r.DeviceID, &r.ExitNodeID, &r.TargetType, &r.TargetValue, &r.Action, &r.DeviceIP, &en, &pd); err != nil {
			return nil, err
		}
		r.Enabled = en == 1
		r.ParentDomain = pd
		rr = append(rr, r)
	}
	return rr, rows.Err()
}

func (a *App) getDeviceRules(userID int) ([]DeviceRule, error) {
	rows, err := a.DB.Query("SELECT d.id, d.user_id, d.device_id, d.exit_node_id, d.target_type, d.target_value, COALESCE(d.action,'accept') as action, COALESCE(d.device_ip,'') as device_ip, d.enabled, COALESCE(d.parent_domain,'') as parent_domain FROM device_rules d WHERE d.user_id = ? ORDER BY d.id", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rr, err := scanRules(rows)
	if err != nil {
		return nil, err
	}
	// Resolve device hostnames from headscale API — match by Tailscale IP
	if nodes, e := a.HS.ListAllNodes(); e == nil {
		for i := range rr {
			if rr[i].DeviceIP == "" {
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
		}
	}
	return rr, nil
}

func (a *App) getUserDevices(userID int) ([]map[string]any, error) {
	rows, err := a.DB.Query("SELECT id, hostname, last_seen FROM devices WHERE user_id = ? ORDER BY hostname", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dd []map[string]any
	for rows.Next() {
		var id int
		var hn string
		var ls sql.NullInt64
		if err := rows.Scan(&id, &hn, &ls); err != nil {
			return nil, err
		}
		m := map[string]any{"id": id, "hostname": hn}
		if ls.Valid {
			m["last_seen"] = time.Unix(ls.Int64, 0).Format("2006-01-02 15:04")
		}
		dd = append(dd, m)
	}
	if len(dd) == 0 {
		if nodes, err := a.HS.NodeList(); err == nil {
			for _, n := range nodes {
				dd = append(dd, map[string]any{"id": n["id"], "hostname": n["hostname"], "is_hs": true})
			}
		}
	}
	return dd, rows.Err()
}

// GenerateACL builds valid headscale 0.29 HuJSON.
// ACL controls ACCESS (not routing). Exit-node selection is client-side.
// When exit rules exist, per-device rules are added for audit/restriction,
// but routing is controlled via the route setup script (see GenerateRouteSetupScript).
func (a *App) GenerateACL() (string, error) {
	rows, err := a.DB.Query("SELECT target_type, target_value, action, COALESCE(device_ip,'') as device_ip FROM device_rules WHERE enabled = 1")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	type ruleEntry struct {
		deviceIP string
		target   string
		action   string
	}
	var entries []ruleEntry
	for rows.Next() {
		var tt, tv, action, dip string
		if err := rows.Scan(&tt, &tv, &action, &dip); err != nil {
			return "", err
		}
		if tt == "subnet" || tt == "ip" {
			entries = append(entries, ruleEntry{deviceIP: dip, target: tv, action: action})
		}
	}

	var sb strings.Builder
	sb.WriteString("{\n  \"acls\": [\n")
	// Always allow all tailnet + internet traffic (ACL doesn't control exit-node routing).
	// Per-device rules below are informational/restrictive — they don't affect routing.
	sb.WriteString("    { \"action\": \"accept\", \"src\": [\"*\"], \"dst\": [\"*:*\"] }")
	for _, e := range entries {
		src := "\"*\""
		if e.deviceIP != "" {
			src = fmt.Sprintf("\"%s\"", e.deviceIP)
		}
		sb.WriteString(",\n    { \"action\": \"" + e.action + "\", \"src\": [" + src + "], \"dst\": [\"" + e.target + ":*\"] }")
	}
	sb.WriteString("\n  ],\n")
	sb.WriteString("  \"tagOwners\": {\n")
	sb.WriteString("    \"tag:public\": [\"skyadmin@tsnet.skynas.ru\"],\n")
	sb.WriteString("    \"tag:exit-node\": [\"skyadmin@tsnet.skynas.ru\"],\n")
	sb.WriteString("    \"tag:client\": [\"skyadmin@tsnet.skynas.ru\"],\n")
	sb.WriteString("    \"tag:private\": [\"skyadmin@tsnet.skynas.ru\"]\n")
	sb.WriteString("  },\n")
	sb.WriteString("  \"groups\": {\n")
	sb.WriteString("    \"group:skyadmin\": [\"skyadmin@tsnet.skynas.ru\"]\n")
	sb.WriteString("  },\n")
	sb.WriteString("  \"ssh\": [\n")
	sb.WriteString("    {\n")
	sb.WriteString("      \"action\": \"accept\",\n")
	sb.WriteString("      \"src\": [\"tag:private\", \"skyadmin@tsnet.skynas.ru\"],\n")
	sb.WriteString("      \"dst\": [\"tag:exit-node\"],\n")
	sb.WriteString("      \"users\": [\"root\"]\n")
	sb.WriteString("    }\n")
	sb.WriteString("  ],\n")
	sb.WriteString("}")
	return sb.String(), nil
}

func (a *App) saveACLSnapshot(config, username string) int {
	var maxVer int
	a.DB.QueryRow("SELECT COALESCE(MAX(version),0) FROM acl_snapshots").Scan(&maxVer)
	ver := maxVer + 1
	a.DB.Exec("INSERT INTO acl_snapshots (version, config, created_by, applied_success) VALUES (?, ?, ?, 1)", ver, config, username)
	return ver
}

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

a.renderWithLayout(w, "exit_rules.html", c, map[string]any{
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

	a.renderWithLayout(w, "admin/exit_rules.html", c, map[string]any{
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

func (a *App) PostAdminRollbackACL(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	verStr := r.FormValue("version")
	ver, _ := strconv.Atoi(verStr)
	if ver == 0 {
		http.Error(w, "invalid version", 400)
		return
	}
	var config string
	if err := a.DB.QueryRow("SELECT config FROM acl_snapshots WHERE version = ?", ver).Scan(&config); err != nil {
		http.Error(w, "version not found", 404)
		return
	}
	if err := a.HS.SetPolicy(config); err != nil {
		a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'rollback_fail', ?)", ver, err.Error())
		http.Error(w, err.Error(), 500)
		return
	}
	a.saveACLSnapshot(config, c.Username)
	a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'rollback', ?)", ver, fmt.Sprintf("rolled back by %s", c.Username))
	http.Redirect(w, r, "/admin/exit-rules?rolled=1", http.StatusFound)
}

// --- JSON API for AI assistant integration ---

// apiRule is the JSON structure for rule creation/listing.
type apiRule struct {
	ID          int    `json:"id,omitempty"`
	DeviceID    int    `json:"device_id"`
	DeviceName  string `json:"device_name,omitempty"`
	ExitNode    string `json:"exit_node"`
	TargetType  string `json:"target_type"`  // "ip", "subnet", "domain"
	TargetValue string `json:"target_value"`
	Action      string `json:"action"`        // "accept" or "deny"
	DeviceIP    string `json:"device_ip,omitempty"`
}

// REST API handlers moved to exit_rules_api.go.
// (GetExitRulesAPI, PostExitRulesAPI, GetExitRulesAPIHelp)
// GetAdminNodesLoad renders the admin node load dashboard.
// GET /admin/exit-rules/nodes
func (a *App) GetAdminNodesLoad(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	// Collect per-exit-node metrics
	type NodeLoad struct {
		Name           string
		ApprovedRoutes int
		AvailableRoutes int
		RuleCount      int
		LastSync       string
		LoadPct        int
	}
	var nodes []NodeLoad
	maxPerNode := a.Cfg.MaxRulesPerDevice * 5 // heuristic: total rules / 5 nodes
	if maxPerNode == 0 { maxPerNode = 1000 }
	// Get distinct exit_nodes from device_rules
	rows, _ := a.DB.Query("SELECT DISTINCT exit_node_id FROM device_rules WHERE enabled=1 AND exit_node_id != ''")
	exitNodeSet := map[string]bool{}
	if rows != nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil { exitNodeSet[n] = true }
		}
		rows.Close()
	}
	// Also add known exit_servers
	serverRows, _ := a.DB.Query("SELECT name FROM exit_servers WHERE enabled=1")
	if serverRows != nil {
		for serverRows.Next() {
			var n string
			if serverRows.Scan(&n) == nil { exitNodeSet[n] = true }
		}
		serverRows.Close()
	}
	for name := range exitNodeSet {
		nl := NodeLoad{Name: name}
		a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE enabled=1 AND exit_node_id=?", name).Scan(&nl.RuleCount)
		// Get from headscale
		// Find node by hostname
		if allNodes, err := a.HS.ListAllNodes(); err == nil {
			for _, n := range allNodes {
				if strings.EqualFold(n.Hostname, name) || strings.EqualFold(n.GivenName, name) {
					nl.AvailableRoutes = len(n.AvailableRoutes)
					// ApprovedRoutes not in NodeView — show 0 or call separate API
					nl.ApprovedRoutes = nl.AvailableRoutes // approximation
					break
				}
			}
		}
		nl.LoadPct = nl.RuleCount * 100 / maxPerNode
		// Last sync: find most recent log
		var lastSync time.Time
		a.DB.QueryRow("SELECT COALESCE(MAX(created_at), '1970-01-01') FROM exit_rule_logs WHERE action='sync' AND detail LIKE ?", "%"+name+"%").Scan(&lastSync)
		if !lastSync.IsZero() && lastSync.Year() > 2000 {
			nl.LastSync = lastSync.Format("2006-01-02 15:04:05")
		} else {
			nl.LastSync = "никогда"
		}
		nodes = append(nodes, nl)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].LoadPct > nodes[j].LoadPct })
	totalRules := 0
	for _, n := range nodes { totalRules += n.RuleCount }
	loadPct := 0
	if a.Cfg != nil && a.Cfg.MaxTotalRules > 0 {
		loadPct = totalRules * 100 / a.Cfg.MaxTotalRules
	}
	a.renderWithLayout(w, "admin/exit_rules_nodes.html", c, map[string]any{
		"Page":         "exit-rules-nodes",
		"Title":        "Node Load",
		"Nodes":        nodes,
		"TotalRules":   totalRules,
		"MaxTotalRules": a.Cfg.MaxTotalRules,
		"LoadPct":      loadPct,
	})
}

