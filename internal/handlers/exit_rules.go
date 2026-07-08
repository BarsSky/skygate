package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"net/url"
	"io"
	"regexp"
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
	_, err = a.DB.Exec(
		"INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		userID, deviceID, exitNode, targetType, targetValue, action, deviceIP, parentDomain)
	if err != nil {
		return false, 0
	}
	return true, 0
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

// GenerateRouteSetupScript creates a shell script that sets up static routes
// so that ONLY the specified IPs/subnets go through the exit node via Tailscale.
// If restore is true, generates a rollback script that removes specific routes
// and re-adds the default route through the exit node.
// If deviceID > 0, filters rules for that specific device only.
func (a *App) GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error) {
	query := "SELECT target_type, target_value, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND user_id = ?"
	args := []any{userID}
	if deviceID > 0 {
		query += " AND device_id = ?"
		args = append(args, deviceID)
	}
	query += " ORDER BY id"
	rows, err := a.DB.Query(query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	type routeEntry struct {
		targetType string
		targetVal  string
		deviceIP   string
	}
	var routes []routeEntry
	for rows.Next() {
		var tt, tv, dip string
		if err := rows.Scan(&tt, &tv, &dip); err != nil {
			continue
		}
		if tt == "ip" || tt == "subnet" {
			routes = append(routes, routeEntry{targetType: tt, targetVal: tv, deviceIP: dip})
		}
	}

	if len(routes) == 0 {
		return "# No IP/subnet exit rules configured.\n# Add rules first at /my/exit-rules\n", nil
	}

	// Resolve exit node Tailscale IP
	exitNodeIP := ""
	if nodes, err := a.HS.ListExitNodes(); err == nil {
		for _, n := range nodes {
			if len(n.IPAddresses) > 0 {
				exitNodeIP = n.IPAddresses[0]
				break
			}
		}
	}
	if exitNodeIP == "" {
		exitNodeIP = "EXIT_NODE_TAILSCALE_IP"
	}

	var sb strings.Builder

	switch os {
	case "windows":
		// Auto-elevation — request admin via UAC if not already admin
		sb.WriteString("@echo off\n")
		sb.WriteString("setlocal enabledelayedexpansion\n")
		sb.WriteString("net session >nul 2>&1\n")
		sb.WriteString("if %errorlevel% neq 0 (\n")
		sb.WriteString("    echo Requesting administrator privileges...\n")
		sb.WriteString("    powershell -Command \"Start-Process '%~f0' -Verb RunAs\"\n")
		sb.WriteString("    exit /b\n")
		sb.WriteString(")\n\n")

		if restore {
			sb.WriteString("echo === Skygate Restore — return all traffic through Tailscale exit node ===\n\n")
			// Check if any split-tunnel routes exist
			sb.WriteString("echo Checking for applied split-tunnel routes...\n")
			sb.WriteString("route print -4 | findstr \"" + exitNodeIP + "\" >nul 2>&1\n")
			sb.WriteString("if %errorlevel% neq 0 (\n")
			sb.WriteString("    echo.\n")
			sb.WriteString("    echo WARNING: No split-tunnel routes detected for " + exitNodeIP + ".\n")
			sb.WriteString("    echo The exit node default route may already be active.\n")
			sb.WriteString("    echo If you just ran tailscale up --exit-node, no restore is needed.\n")
			sb.WriteString("    echo.\n")
			sb.WriteString("    route print -4\n")
			sb.WriteString("    exit /b 0\n")
			sb.WriteString(")\n\n")
			// Remove specific routes
			sb.WriteString("echo Removing specific routes...\n")
			for _, r := range routes {
				target := r.targetVal
				if r.targetType == "ip" && !strings.Contains(target, "/") {
					target += "/32"
				}
				_, ipNet, err := net.ParseCIDR(target)
				if err != nil {
					continue
				}
				mask := fmt.Sprintf("%d.%d.%d.%d", ipNet.Mask[0], ipNet.Mask[1], ipNet.Mask[2], ipNet.Mask[3])
				sb.WriteString(fmt.Sprintf("route delete %s mask %s %s 2>nul\n", ipNet.IP.String(), mask, exitNodeIP))
			}
			// Re-add default route through exit node
			sb.WriteString("\necho Restoring default route via Tailscale exit node...\n")
			sb.WriteString("route add 0.0.0.0 mask 0.0.0.0 " + exitNodeIP + " 2>nul\n")
			sb.WriteString("\necho Done. All traffic now goes through Tailscale exit node.\n")
			sb.WriteString("route print -4\n")
		} else {
			sb.WriteString("echo === Skygate Exit Route Setup (Windows) ===\n")
			sb.WriteString("echo First, make sure Tailscale is connected:\n")
			sb.WriteString("echo   tailscale up --exit-node=EXIT_NODE_NAME\n\n")

			// Save original gateway via wmic (locale-independent)
			sb.WriteString("echo Detecting original default gateway...\n")
			sb.WriteString("set ORIG_GW=\n")
			sb.WriteString("rem Method 1: netsh (works on all Windows versions, locale-independent)\n")
			sb.WriteString("for /f \"tokens=6\" %%a in ('netsh interface ipv4 show route 2^>nul ^| findstr /R \"0\\.0\\.0\\.0/0\"') do (\n")
			sb.WriteString("    set GW=%%a\n")
			sb.WriteString("    echo !GW! | findstr \"100\\.\" >nul\n")
			sb.WriteString("    if errorlevel 1 (\n")
			sb.WriteString("        if \"!ORIG_GW!\"==\"\" set ORIG_GW=!GW!\n")
			sb.WriteString("    )\n")
			sb.WriteString(")\n")
			sb.WriteString("rem Method 2: route print fallback\n")
			sb.WriteString("if \"!ORIG_GW!\"==\"\" (\n")
			sb.WriteString("    for /f \"tokens=3\" %%a in ('route print -4 2^>nul ^| findstr /R /C:\"0.0.0.0[\t ]*0.0.0.0\" ^| findstr /V \"100\\.\"') do (\n")
			sb.WriteString("        if \"!ORIG_GW!\"==\"\" set ORIG_GW=%%a\n")
			sb.WriteString("    )\n")
			sb.WriteString(")\n")
			sb.WriteString("rem Method 3: wmic (legacy, may be absent on Win11 24H2+)\n")
			sb.WriteString("if \"!ORIG_GW!\"==\"\" (\n")
			sb.WriteString("    for /f \"tokens=2 delims==\" %%a in ('wmic nicconfig where IPEnabled=True get DefaultIPGateway /value 2^>nul ^| find \"{\"') do (\n")
			sb.WriteString("        set GW=%%a\n")
			sb.WriteString("        set GW=!GW:\"=!\n")
			sb.WriteString("        set GW=!GW:}=!\n")
			sb.WriteString("        set GW=!GW:{=!\n")
			sb.WriteString("        set GW=!GW: =!\n")
			sb.WriteString("        if not \"!GW!\"==\"\" if \"!ORIG_GW!\"==\"\" set ORIG_GW=!GW!\n")
			sb.WriteString("    )\n")
			sb.WriteString(")\n")
			sb.WriteString("if not \"!ORIG_GW!\"==\"\" (\n")
			sb.WriteString("    echo   Gateway: !ORIG_GW!\n")
			sb.WriteString(") else (\n")
			sb.WriteString("    echo   ERROR: Could not detect default gateway!\n")
			sb.WriteString("    echo   Run: ipconfig ^| findstr \"Default Gateway\"\n")
			sb.WriteString("    echo   Then: route add 0.0.0.0 mask 0.0.0.0 YOUR_GATEWAY_IP\n")
			sb.WriteString("    pause\n")
			sb.WriteString("    exit /b 1\n")
			sb.WriteString(")\n")
			sb.WriteString("\n")


		// Compute local subnet from gateway for Tailscale route cleanup
		sb.WriteString("rem Compute local subnet from gateway for cleanup\n")
		sb.WriteString("for /f \"tokens=1,2,3 delims=.\" %%a in (\"!ORIG_GW!\") do set LOCAL_SUBNET=%%a.%%b.%%c.0\n")
		sb.WriteString("\n")
			sb.WriteString("echo Removing all default routes...\n")
			sb.WriteString("route delete 0.0.0.0 mask 0.0.0.0 2>nul\n")

		// Remove Tailscale-added local subnet route (blocks LAN gateway access)
		sb.WriteString("rem Remove Tailscale-added local subnet route\n")
		sb.WriteString("if defined LOCAL_SUBNET (\n")
		sb.WriteString("    route delete !LOCAL_SUBNET! mask 255.255.255.0 100.100.100.100 2>nul\n")
		sb.WriteString("    route delete !LOCAL_SUBNET! mask 255.255.255.0 100.64.0.2 2>nul\n")
		sb.WriteString("    if !errorlevel! equ 0 (echo   Cleaned Tailscale local subnet route) else (echo   No local subnet route to clean)\n")
		sb.WriteString(")\n")
		sb.WriteString("\n")
			sb.WriteString("if not \"!ORIG_GW!\"==\"\" (\n")
			sb.WriteString("    route add 0.0.0.0 mask 0.0.0.0 !ORIG_GW! 2>nul\n")
			sb.WriteString("    echo   Original default route restored via !ORIG_GW!\n")
			sb.WriteString(")\n")

			// Add DNS route for MagicDNS
			sb.WriteString("echo Adding DNS route (100.100.100.100)...\n")
			sb.WriteString("route add 100.100.100.100 mask 255.255.255.255 " + exitNodeIP + " 2>nul\n")
			sb.WriteString("if %errorlevel% equ 0 (echo   Done.) else (echo   Already exists.)\n")
			sb.WriteString("\n")

			sb.WriteString("echo Adding specific routes via Tailscale...\n")
			for _, r := range routes {
				target := r.targetVal
				if r.targetType == "ip" && !strings.Contains(target, "/") {
					target += "/32"
				}
				// Windows route add requires "mask <netmask>" syntax, not CIDR
				_, ipNet, err := net.ParseCIDR(target)
				if err != nil {
					continue
				}
				mask := fmt.Sprintf("%d.%d.%d.%d", ipNet.Mask[0], ipNet.Mask[1], ipNet.Mask[2], ipNet.Mask[3])
				sb.WriteString(fmt.Sprintf("route add %s mask %s %s 2>nul\n", ipNet.IP.String(), mask, exitNodeIP))
			}
			// Verify default route restored
			sb.WriteString("\necho Checking default route...\n")
			sb.WriteString("if \"!ORIG_GW!\"==\"\" (\n")
			sb.WriteString("    echo   WARNING: No default gateway saved. Run: route add 0.0.0.0 mask 0.0.0.0 YOUR_ROUTER_IP\n")
			sb.WriteString(") else (\n")
			sb.WriteString("    echo   Default route: 0.0.0.0 -^> !ORIG_GW!\n")
			sb.WriteString(")\n")
			sb.WriteString("\necho Done. Only these destinations go through Tailscale.\n")
			sb.WriteString("route print -4\n")
		}

	default: // linux / mac
		sb.WriteString("#!/bin/bash\n")
		if restore {
			sb.WriteString("# === Skygate Restore — return all traffic through Tailscale exit node ===\n")
		} else {
			sb.WriteString("# === Skygate Exit Route Setup (Linux/macOS) ===\n")
		}
		sb.WriteString("# Run as root: sudo bash this_script.sh\n")
		sb.WriteString("set -e\n\n")

		// Find the tailscale interface
		sb.WriteString("TS_IFACE=$(ip -o link show | grep -o 'tailscale[0-9]*' | head -1)\n")
		sb.WriteString("if [ -z \"$TS_IFACE\" ]; then\n")
		sb.WriteString("    echo \"ERROR: tailscale interface not found. Run: tailscale up --exit-node=EXIT_NODE_NAME\"\n")
		sb.WriteString("    exit 1\n")
		sb.WriteString("fi\n\n")

		if restore {
			// Check if any split-tunnel routes exist
			sb.WriteString("# Check if split-tunnel routes are applied\n")
			sb.WriteString("if ! ip route show | grep -q \"$TS_IFACE.*" + exitNodeIP + "\"; then\n")
			sb.WriteString("    echo \"WARNING: No split-tunnel routes detected for " + exitNodeIP + ".\"\n")
			sb.WriteString("    echo \"The exit node default route may already be active.\"\n")
			sb.WriteString("    echo \"If you just ran tailscale up --exit-node, no restore is needed.\"\n")
			sb.WriteString("    ip route show | grep \"$TS_IFACE\" || true\n")
			sb.WriteString("    exit 0\n")
			sb.WriteString("fi\n\n")
			// Remove specific routes
			sb.WriteString("echo \"Removing specific routes from $TS_IFACE...\"\n")
			for _, r := range routes {
				target := r.targetVal
				if r.targetType == "ip" && !strings.Contains(target, "/") {
					target += "/32"
				}
				sb.WriteString(fmt.Sprintf("ip route del %s via %s dev \"$TS_IFACE\" 2>/dev/null || echo \"  (route for %s not found)\"\n", target, exitNodeIP, target))
			}
			// Re-add default route
			sb.WriteString("\necho \"Restoring default route via $TS_IFACE...\"\n")
			sb.WriteString("ip route add default dev \"$TS_IFACE\" 2>/dev/null || echo \"  (default route already exists)\"\n")
			sb.WriteString("\necho \"=== Current routes via Tailscale ===\"\n")
			sb.WriteString("ip route show | grep \"$TS_IFACE\"\n")
			sb.WriteString("echo \"\"\n")
			sb.WriteString("echo \"Done. All traffic now goes through Tailscale exit node.\"\n")
		} else {
			// Save original default route for fallback
			sb.WriteString("# Save original default route (non-Tailscale) for fallback\n")
			sb.WriteString("ORIG_DEFAULT=$(ip route show default | grep -v \"$TS_IFACE\" | head -1)\n")
			sb.WriteString("if [ -n \"$ORIG_DEFAULT\" ]; then\n")
			sb.WriteString("    echo \"Original default route: $ORIG_DEFAULT\"\n")
			sb.WriteString("else\n")
			sb.WriteString("    echo \"WARNING: No non-Tailscale default route found.\"\n")
			sb.WriteString("fi\n\n")

			// Remove default route via tailscale (added by --exit-node)
			sb.WriteString("# Remove default route via Tailscale (added by --exit-node)\n")
			sb.WriteString("DEFAULT_VIA_TS=$(ip route show default | grep \"$TS_IFACE\" | head -1)\n")
			sb.WriteString("if [ -n \"$DEFAULT_VIA_TS\" ]; then\n")
			sb.WriteString("    echo \"Removing default route via $TS_IFACE...\"\n")
			sb.WriteString("    ip route del default dev \"$TS_IFACE\" 2>/dev/null || true\n")
			sb.WriteString("fi\n\n")

			// Add DNS route for MagicDNS
			sb.WriteString("# Add DNS route for MagicDNS (100.100.100.100)\n")
			sb.WriteString("echo \"Adding DNS route for MagicDNS...\"\n")
			sb.WriteString("ip route add 100.100.100.100/32 via " + exitNodeIP + " dev \"$TS_IFACE\" 2>/dev/null || echo \"  (already exists)\"")
			sb.WriteString("\n")

			sb.WriteString("# Add specific routes via Tailscale (exit node: " + exitNodeIP + ")\n")
			sb.WriteString("echo \"Adding routes via $TS_IFACE...\"\n")
			for _, r := range routes {
				target := r.targetVal
				if r.targetType == "ip" && !strings.Contains(target, "/") {
					target += "/32"
				}
				sb.WriteString(fmt.Sprintf("ip route add %s via %s dev \"$TS_IFACE\" 2>/dev/null || echo \"  (route for %s already exists)\"\n", target, exitNodeIP, target))
			}
			sb.WriteString("\necho \"=== Current routes via Tailscale ===\"\n")
			sb.WriteString("ip route show | grep \"$TS_IFACE\"\n")
			sb.WriteString("echo \"\"\n")
			// Verify default route still exists
			sb.WriteString("echo \"\"\n")
			sb.WriteString("echo \"Checking fallback default route...\"\n")
			sb.WriteString("if [ -z \"$(ip route show default | grep -v \"$TS_IFACE\")\" ]; then\n")
			sb.WriteString("    if [ -n \"$ORIG_DEFAULT\" ]; then\n")
			sb.WriteString("        GW=$(echo \"$ORIG_DEFAULT\" | awk '{print $3}')\n")
			sb.WriteString("        DEV=$(echo \"$ORIG_DEFAULT\" | awk '{print $5}')\n")
			sb.WriteString("        echo \"  NO DEFAULT ROUTE! Restoring original...\"\n")
			sb.WriteString("        if [ -n \"$GW\" ] && [ -n \"$DEV\" ]; then\n")
			sb.WriteString("            ip route add default via \"$GW\" dev \"$DEV\"\n")
			sb.WriteString("        elif [ -n \"$GW\" ]; then\n")
			sb.WriteString("            ip route add default via \"$GW\"\n")
			sb.WriteString("        fi\n")
			sb.WriteString("    else\n")
			sb.WriteString("        echo \"  WARNING: No default route. Run: ip route add default via YOUR_ROUTER_IP\"\n")
			sb.WriteString("    fi\n")
			sb.WriteString("else\n")
			sb.WriteString("    echo \"  Default route exists - OK\"\n")
			sb.WriteString("fi\n")
			sb.WriteString("echo \"\"\n")

			sb.WriteString("echo \"Done. Only these destinations go through Tailscale exit node.\"\n")
			sb.WriteString("echo \"All other internet traffic uses your normal connection.\"\n")
		}
	}

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
		ID        string
		Hostname  string
		RuleCount int
		HasRoutes bool
	}
	var deviceInfos []DeviceInfo
	for _, d := range devices {
		hn := fmt.Sprint(d["hostname"])
		info := DeviceInfo{
			ID:        fmt.Sprint(d["id"]),
			Hostname:  hn,
			RuleCount: len(deviceRoutes[hn]),
			HasRoutes: hasRoutes[hn],
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

a.renderWithLayout(w, "exit_rules.html", c, map[string]any{
		"Page":          "exit-rules",
		"Title":         "Exit Rules",
		"Rules":         rules,
		"Devices":       devices,
		"DeviceInfos":   deviceInfos,
		"DeviceRoutes":  deviceRoutes,
		"ExitNodes":     exitServers,
		"DeviceNames":   deviceNames,
		"Grouped":       grouped,
		"TotalRules":    totalRules,
		"MaxTotalRules": maxPerDeviceMax,
		"LoadPct":       loadPct,
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

	// 2026-07-07: issue #12 — limit check
	// Check per-device and total limits before insert.
	// 2026-07-07: per-user limit takes precedence over per-device
	maxPerUser := a.getMaxRulesForUser(c.Username)
	if maxPerUser > 0 {
		var userRuleCount int
		a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE user_id=? AND enabled=1", c.UserID).Scan(&userRuleCount)
		if userRuleCount >= maxPerUser {
			http.Error(w, fmt.Sprintf("user limit exceeded: %d/%d rules for user %s", userRuleCount, maxPerUser, c.Username), 403)
			return
		}
	}
	maxPerDevice := a.Cfg.MaxRulesPerDevice
	if maxPerDevice > 0 {
		var deviceRuleCount int
		a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE device_id=? AND enabled=1", devID).Scan(&deviceRuleCount)
		if deviceRuleCount >= maxPerDevice {
			http.Error(w, fmt.Sprintf("device limit exceeded: %d/%d rules on this device", deviceRuleCount, maxPerDevice), 403)
			return
		}
	}
	maxTotal := a.Cfg.MaxTotalRules
	if maxTotal > 0 {
		var totalCount int
		a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE enabled=1").Scan(&totalCount)
		if totalCount >= maxTotal {
			http.Error(w, fmt.Sprintf("system limit exceeded: %d/%d total rules", totalCount, maxTotal), 403)
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
			// 2026-07-07: only count as dup if same parent_domain (or no parent_domain)
			// If existing /32 has DIFFERENT parent_domain, it's a shared IP — insert new one
			// with this domain's parent_domain (allowed for autoupdater to track).
			var existingParent string
			_ = a.DB.QueryRow("SELECT COALESCE(parent_domain,'') FROM device_rules WHERE id=?", existingID).Scan(&existingParent)
			if existingParent == "" || existingParent == targetValue {
				dupCount++
				dupIDs = append(dupIDs, existingID)
			} else {
				// Shared IP with different parent — create new with our parent_domain
				_, _ = a.DB.Exec(
					"INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, 'subnet', ?, ?, ?, ?)",
					c.UserID, devID, exitNode, ip, action, deviceIP, targetValue)
				insertedCount++
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
	id, _ := strconv.Atoi(r.FormValue("id"))
	if id == 0 {
		http.Error(w, "missing id", 400)
		return
	}
	a.DB.Exec("DELETE FROM device_rules WHERE id = ? AND user_id = ?", id, c.UserID)
	if acl, err := a.GenerateACL(); err == nil {
		ver := a.saveACLSnapshot(acl, c.Username)
		if err := a.HS.SetPolicy(acl); err == nil {
			a.DB.Exec("UPDATE acl_snapshots SET applied_success=1 WHERE version=?", ver)
			a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'delete', ?)", ver, fmt.Sprintf("user %s deleted rule #%d", c.Username, id))
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

// GetExitRulesAPI returns all rules for the current user as JSON.
// GET /my/exit-rules/api
func (a *App) GetExitRulesAPI(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Error(w, `{"error":"unauthorized"}`, 401)
		return
	}
	rules, err := a.getDeviceRules(int(c.UserID))
	if err != nil {
		http.Error(w, `{"error":"db error"}`, 500)
		return
	}
	var out []apiRule
	for _, rl := range rules {
		out = append(out, apiRule{
			ID:          rl.ID,
			DeviceID:    rl.DeviceID,
			DeviceName:  rl.DeviceName,
			ExitNode:    rl.ExitNodeID,
			TargetType:  rl.TargetType,
			TargetValue: rl.TargetValue,
			Action:      rl.Action,
			DeviceIP:    rl.DeviceIP,
		})
	}
	if out == nil {
		out = []apiRule{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"rules": out})
}

// PostExitRulesAPI creates one or more rules from JSON body.
// POST /my/exit-rules/api
// Body: {"rules": [{"device_id":2,"exit_node":"karolina","target_type":"ip","target_value":"8.8.8.8","action":"accept"}, ...]}
func (a *App) PostExitRulesAPI(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Error(w, `{"error":"unauthorized"}`, 401)
		return
	}
	var req struct {
		Rules []apiRule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json: `+err.Error()+`"}`, 400)
		return
	}
	if len(req.Rules) == 0 {
		http.Error(w, `{"error":"empty rules array"}`, 400)
		return
	}

	// Resolve device IPs from headscale
	nodes, _ := a.HS.ListAllNodes()
	nodeIPs := map[int]string{}
	if nodes != nil {
		for _, n := range nodes {
			nid, _ := strconv.Atoi(n.ID)
			if len(n.IPAddresses) > 0 {
				nodeIPs[nid] = n.IPAddresses[0]
			}
		}
	}

	added := 0
	dupCount := 0
	errors := []string{}
	// 2026-07-07: issue #12 — pre-check total limit before processing
	maxTotal := a.Cfg.MaxTotalRules
	if maxTotal > 0 {
		var currentTotal int
		a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE enabled=1").Scan(&currentTotal)
		if currentTotal+len(req.Rules) > maxTotal {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]any{
				"error":     fmt.Sprintf("system limit exceeded: %d/%d", currentTotal, maxTotal),
				"current":   currentTotal,
				"max":       maxTotal,
				"requested": len(req.Rules),
			})
			return
		}
	}
	for i, rl := range req.Rules {
		// 2026-07-07: per-device limit
		maxPerDevice := a.Cfg.MaxRulesPerDevice
		if maxPerDevice > 0 {
			var deviceRuleCount int
			a.DB.QueryRow("SELECT COUNT(*) FROM device_rules WHERE device_id=? AND enabled=1", rl.DeviceID).Scan(&deviceRuleCount)
			if deviceRuleCount >= maxPerDevice {
				errors = append(errors, fmt.Sprintf("rule[%d]: device limit exceeded (%d/%d)", i, deviceRuleCount, maxPerDevice))
				continue
			}
		}
		if rl.DeviceID == 0 || rl.TargetValue == "" {
			errors = append(errors, fmt.Sprintf("rule[%d]: missing device_id or target_value", i))
			continue
		}
		if rl.Action == "" {
			rl.Action = "accept"
		}
		deviceIP := nodeIPs[rl.DeviceID]
		ok, existingID := a.insertRuleUnique(c.UserID, rl.DeviceID, rl.ExitNode, rl.TargetType, rl.TargetValue, rl.Action, deviceIP)
		if !ok {
			errors = append(errors, fmt.Sprintf("rule[%d]: db error", i))
			continue
		}
		if existingID > 0 {
			errors = append(errors, fmt.Sprintf("rule[%d]: duplicate of #%d", i, existingID))
			dupCount++
			continue
		}
		added++
	}

	// Apply ACL if anything was added
	if added > 0 {
		if acl, err := a.GenerateACL(); err == nil {
			ver := a.saveACLSnapshot(acl, c.Username)
			if err := a.HS.SetPolicy(acl); err == nil {
				a.DB.Exec("UPDATE acl_snapshots SET applied_success=1 WHERE version=?", ver)
				a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (?, 'api_bulk', ?)", ver,
					fmt.Sprintf("user %s added %d rules via API", c.Username, added))
				_ = a.SyncAdvertisedRoutes()
			}
		}
	}

	resp := map[string]any{"added": added,
		"duplicates": dupCount, "errors": errors}
	if errors == nil {
		resp["errors"] = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetExitRulesAPIHelp renders the API documentation page.
// GET /my/exit-rules/help
func (a *App) GetExitRulesAPIHelp(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	a.renderWithLayout(w, "exit_rules_help.html", c, map[string]any{
		"Page":  "exit-rules",
		"Title": "Exit Rules API Help",
	})
}

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

// SyncAdvertisedRoutes collects all enabled IP/subnet rules and pushes to exit nodes.
func (a *App) SyncAdvertisedRoutes() map[string]string {
	result := map[string]string{}
	rows, err := a.DB.Query("SELECT DISTINCT exit_node_id, target_value FROM device_rules WHERE enabled = 1 AND (target_type = 'ip' OR target_type = 'subnet') ORDER BY exit_node_id")
	if err != nil {
		result["error"] = err.Error()
		return result
	}
	defer rows.Close()
	exitRoutes := map[string][]string{}
	for rows.Next() {
		var node, target string
		if err := rows.Scan(&node, &target); err != nil {
			continue
		}
		exitRoutes[node] = append(exitRoutes[node], target)
	}
	for node, routes := range exitRoutes {
		// 2026-07-08: prepend base exit-node routes (0.0.0.0/0, ::/0) so the
		// node stays an exit node after sync. SetAdvertisedRoutes already
		// adds these on the SSH side, but the headscale CLI approve-routes
		// call below only knows about the routes we pass explicitly.
		approveRoutes := []string{"0.0.0.0/0", "::/0"}
		seen := map[string]bool{"0.0.0.0/0": true, "::/0": true}
		for _, r := range routes {
			if !seen[r] {
				seen[r] = true
				approveRoutes = append(approveRoutes, r)
			}
		}
		msg, err := a.HS.SetAdvertisedRoutes(node, approveRoutes)
		if err != nil {
			result[node] = "ssh: " + err.Error()
		} else {
			result[node] = "ok"
			_ = msg
		}
		// Approve all routes (including base 0.0.0.0/0, ::/0) for this exit
		// node via headscale CLI (docker exec).
		// 2026-07-08: pass full list (base + per-rule) so the node keeps
		// its exit-node capability (default route advertised AND approved).
		if approved, approveErr := a.HS.ApproveAllRoutesWithList(node, approveRoutes); approveErr != nil {
			result[node+"_approve_err"] = approveErr.Error()
			result[node] = "ssh:ok approve:err=" + approveErr.Error()
		} else if approved > 0 {
			result[node] = fmt.Sprintf("ok approved=%d", approved)
		}
	}
	if len(exitRoutes) == 0 {
		result["info"] = "no IP/subnet rules configured"
	}
	return result
}

// 2026-07-07: issue #12 — staggered sync.
// If SKYGATE_STAGGER_SYNC=true and total rules > batchSize, run sync in goroutine
// that splits work by exit-node and applies batches with delay between them.
// This prevents headscale from being overwhelmed by large approve-routes calls.
func (a *App) staggeredSync() {
	if a.Cfg == nil || !a.Cfg.StaggerSync {
		a.SyncAdvertisedRoutes()
		return
	}
	batchSize := a.Cfg.StaggerBatchSize
	if batchSize <= 0 { batchSize = 20 }
	interval := a.Cfg.StaggerInterval
	if interval <= 0 { interval = 30 * time.Second }
	maxPerNode := batchSize
	// Collect exit_nodes with their rule counts
	rows, _ := a.DB.Query("SELECT exit_node_id, COUNT(*) FROM device_rules WHERE enabled=1 AND exit_node_id != '' GROUP BY exit_node_id")
	if rows == nil {
		a.SyncAdvertisedRoutes()
		return
	}
	defer rows.Close()
	type nodeRules struct { name string; count int }
	var nodes []nodeRules
	totalRules := 0
	for rows.Next() {
		var n string; var c int
		if rows.Scan(&n, &c) == nil {
			nodes = append(nodes, nodeRules{n, c})
			totalRules += c
		}
	}
	// If small enough, sync immediately
	if totalRules <= maxPerNode {
		a.SyncAdvertisedRoutes()
		return
	}
	log.Printf("staggeredSync: %d rules across %d nodes, batch=%d interval=%s",
		totalRules, len(nodes), maxPerNode, interval)
	go func() {
		for _, n := range nodes {
			// Sync this node alone (smaller batch)
			rules, _ := a.DB.Query("SELECT target_value FROM device_rules WHERE enabled=1 AND exit_node_id=? AND target_type IN ('subnet', 'ip')", n.name)
			if rules == nil { continue }
			defer rules.Close()
			var routeList []string
			for rules.Next() {
				var v string
				if rules.Scan(&v) == nil { routeList = append(routeList, v) }
			}
			// 2026-07-08: always include base exit-node routes in every batch so
			// the node never loses its exit-node capability mid-sync.
			withBase := func(batch []string) []string {
				out := []string{"0.0.0.0/0", "::/0"}
				seen := map[string]bool{"0.0.0.0/0": true, "::/0": true}
				for _, r := range batch {
					if !seen[r] { seen[r] = true; out = append(out, r) }
				}
				return out
			}
			if len(routeList) > maxPerNode {
				// Split this node into batches
				for i := 0; i < len(routeList); i += maxPerNode {
					end := i + maxPerNode
					if end > len(routeList) { end = len(routeList) }
					batch := withBase(routeList[i:end])
					log.Printf("staggeredSync: %s batch %d-%d/%d", n.name, i, end, len(routeList))
					msg, _ := a.HS.SetAdvertisedRoutes(n.name, batch)
					log.Printf("staggeredSync: %s advertised: %s", n.name, msg)
					if _, err := a.HS.ApproveAllRoutesWithList(n.name, batch); err != nil {
						log.Printf("staggeredSync: %s approve err: %v", n.name, err)
					}
					time.Sleep(interval)
				}
			} else {
				batch := withBase(routeList)
				msg, _ := a.HS.SetAdvertisedRoutes(n.name, batch)
				log.Printf("staggeredSync: %s advertised: %s", n.name, msg)
				if _, err := a.HS.ApproveAllRoutesWithList(n.name, batch); err != nil {
					log.Printf("staggeredSync: %s approve err: %v", n.name, err)
				}
			}
			time.Sleep(interval / 2)
		}
		log.Printf("staggeredSync: done")
	}()
}

// SyncAdvertisedRoutesHandler triggers route sync (admin only).
func (a *App) SyncAdvertisedRoutesHandler(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, `{"error":"forbidden"}`, 403)
		return
	}
	result := a.SyncAdvertisedRoutes()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// knownSubdomains maps a main domain to its known subdomain hosts for static assets.
// 2026-07-07: issue #9 — Cloudflare-routed sites have static on different subdomains.
var knownSubdomains = map[string][]string{
	"rutracker.org": {"static.rutracker.cc"},
	"rutracker.cc":  {"static.rutracker.cc"},
}

// 2026-07-07: issue #6 — DomainAutoUpdater
// Background job: resolves all domain rules every interval, reconciles with /32 IP rules.
// Returns count of changes (added + removed) and writes log entries.
func (a *App) DomainAutoUpdater() (added, removed int, err error) {
	rows, qerr := a.DB.Query("SELECT id, user_id, device_id, exit_node_id, target_value, action, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND target_type = 'domain'")
	if qerr != nil {
		return 0, 0, qerr
	}
	defer rows.Close()
	type domainRule struct {
		id       int
		userID   int64
		deviceID int
		exitNode string
		domain   string
		action   string
		deviceIP string
	}
	var domains []domainRule
	for rows.Next() {
		var r domainRule
		var uid int64
		if err := rows.Scan(&r.id, &uid, &r.deviceID, &r.exitNode, &r.domain, &r.action, &r.deviceIP); err == nil {
			r.userID = uid
			domains = append(domains, r)
		}
	}

	for _, d := range domains {
		addrs, lerr := net.LookupHost(d.domain)
		if lerr != nil {
			a.logAutoUpdate(d.id, d.domain, 0, 0, "lookup failed: "+lerr.Error())
			continue
		}
		currentIPs := map[string]bool{}
		for _, a := range addrs {
			if strings.Contains(a, ":") { continue } // skip IPv6
			currentIPs[a] = true
		}
		if extraIPs := a.resolveDomainSubdomains(d.domain); extraIPs != nil {
			for ip := range extraIPs { currentIPs[ip] = true }
		}

		// Get existing /32 rules for this domain
		existing := map[string]int{} // IP -> rule id
		rows2, eerr := a.DB.Query("SELECT id, target_value FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND target_value LIKE '%/32'",
			d.userID, d.deviceID, d.exitNode)
		if eerr != nil {
			continue
		}
		// Filter: only IPs that are NOT explicitly in currentIPs (could be from other rules)
		// Strategy: for each IP in currentIPs that's not in DB → INSERT
		//           for each /32 IP in DB that resolves to a removed domain IP → DELETE
		// We track: for THIS domain, which /32 IPs correspond?
		// Simplification: we know d.domain is the source, so any /32 that matches
		// the pattern and exists in oldIPs but not in currentIPs is from this domain.
		_ = existing
		rows2.Close()

		// Find all /32 rules for (user, device, exit_node) that LOOK like auto-resolved from this domain
		// We track them via a side table OR a heuristic: for this domain, list all /32 rules where
		// the same domain's last resolved IPs included them.
		// Pragmatic approach: maintain a comment-style hint in another table? Or use a marker.
		// Simpler: for this domain, list ALL /32 rules and diff against currentIPs.
		// User-added /32 rules (manual) get deleted if we don't track — TOO DANGEROUS.
		// Better: introduce column `parent_domain` (NULL = manual).
		all32 := map[string]int{}
		rows3, _ := a.DB.Query("SELECT id, target_value FROM device_rules WHERE user_id=? AND device_id=? AND exit_node_id=? AND target_type='subnet' AND target_value LIKE '%/32' AND COALESCE(parent_domain,'')=?",
			d.userID, d.deviceID, d.exitNode, d.domain)
		if rows3 != nil {
			for rows3.Next() {
				var rid int
				var val string
				if rows3.Scan(&rid, &val) == nil {
					// strip /32
					ip := strings.TrimSuffix(val, "/32")
					all32[ip] = rid
				}
			}
			rows3.Close()
		}

		// Add new IPs
		for ip := range currentIPs {
			if _, exists := all32[ip]; exists { continue }
			if _, ierr := a.DB.Exec(
				"INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain) VALUES (?, ?, ?, 'subnet', ?, ?, ?, ?)",
				d.userID, d.deviceID, d.exitNode, ip+"/32", d.action, d.deviceIP, d.domain); ierr == nil {
				added++
			}
		}
		// Remove old IPs
		for ip, rid := range all32 {
			if currentIPs[ip] { continue }
			if _, derr := a.DB.Exec("DELETE FROM device_rules WHERE id=?", rid); derr == nil {
				removed++
			}
		}

		if len(currentIPs) > 0 || len(all32) > 0 {
			a.logAutoUpdate(d.id, d.domain, added, removed, "")
		}
	}

	return added, removed, nil
}


// resolveDomainSubdomains resolves known subdomains and (optionally) fetches
// the main page to discover subdomains from href/src attributes. Returns a set
// of IPv4 addresses to add to the rule list.
func (a *App) resolveDomainSubdomains(domain string) map[string]bool {
	httpClient := &http.Client{Timeout: 8 * time.Second}
	var body []byte

	// Check known subdomains first (fast path)
	ips := map[string]bool{}
	for _, sd := range knownSubdomains[domain] {
		if addrs, err := net.LookupHost(sd); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") { ips[ip] = true }
			}
		}
	}
	if len(ips) > 0 {
		a.logAutoUpdate(0, domain, len(ips), 0, "known subdomains resolved: "+strconv.Itoa(len(knownSubdomains[domain])))
		return ips
	}

	for _, scheme := range []string{"https", "http"} {
		resp, err := httpClient.Get(scheme + "://" + domain + "/")
		if err != nil { continue }
		b, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
		resp.Body.Close()
		if err == nil {
			body = b
			break
		}
	}
	if len(body) == 0 { return nil }

	subdomains := map[string]bool{}
	hostRe := regexp.MustCompile(`(?:href|src)=["\']https?://([^/\s"\']+)`)
	for _, m := range hostRe.FindAllStringSubmatch(string(body), -1) {
		host := m[1]
		// Skip self and subdomains of self
		if host == domain || strings.HasSuffix(host, "."+domain) { continue }
		subdomains[host] = true
	}
	for host := range subdomains {
		if addrs, err := net.LookupHost(host); err == nil {
			for _, ip := range addrs {
				if !strings.Contains(ip, ":") { ips[ip] = true }
			}
		}
	}
	if len(ips) > 0 {
		a.logAutoUpdate(0, domain, len(ips), 0, "subdomains resolved: "+strconv.Itoa(len(subdomains)))
	}
	return ips
}

func (a *App) logAutoUpdate(ruleID int, domain string, added, removed int, errMsg string) {
	detail := fmt.Sprintf("domain=%s added=%d removed=%d", domain, added, removed)
	if errMsg != "" {
		detail += " err=" + errMsg
	}
	_, _ = a.DB.Exec("INSERT INTO exit_rule_logs (version, action, detail) VALUES (0, 'autoupdate', ?)", detail)
}

func (a *App) RunDomainAutoUpdater(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		log.Printf("autoupdater: disabled (interval=0)")
		return
	}
	log.Printf("autoupdater: starting (interval=%s)", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	// Run once immediately, then on tick
	added, removed, err := a.DomainAutoUpdater()
	if err != nil {
		log.Printf("autoupdater: initial: %v", err)
	} else if added > 0 || removed > 0 {
		log.Printf("autoupdater: initial: added=%d removed=%d", added, removed)
		a.staggeredSync() // 2026-07-07: issue #12 — staggered
	}
	for {
		select {
		case <-ctx.Done():
			log.Printf("autoupdater: stopping")
			return
		case <-t.C:
			added, removed, err := a.DomainAutoUpdater()
			if err != nil {
				log.Printf("autoupdater: %v", err)
				continue
			}
			if added > 0 || removed > 0 {
				log.Printf("autoupdater: added=%d removed=%d, syncing exit-nodes", added, removed)
				a.staggeredSync() // 2026-07-07: issue #12
			}
		}
	}
}

