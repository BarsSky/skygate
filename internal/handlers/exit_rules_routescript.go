package handlers

import (
	"fmt"
	"net"
	"strings"
)

// GenerateRouteSetupScript — extracted from exit_rules.go (was lines 214-510).
// Generates a per-OS shell script that sets up Tailscale static routes on
// the user's device so that ONLY the specified IPs/subnets go through the
// exit node.  If restore=true, generates a rollback script that removes the
// specific routes and re-adds the default route through the exit node.
//
// This is a pure function (no shared state, no *a-dependent helpers besides
// a.DB and a.HS which are already on App). Kept in its own file because
// the body is ~300 lines of inline shell scripts (case 'windows': / linux),
// which would otherwise dominate exit_rules.go.


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
