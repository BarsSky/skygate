package handlers

// exit_rules_routescript_windows_body.go — generates the Windows .cmd
// script for split-tunnel exit-node routing.
//
// Pure function: takes the resolved route list + exit node IP, emits
// the .cmd body. No I/O, no headscale, no DB. Kept separate from
// the orchestrator (exit_rules_routescript.go) and the Linux/macOS
// builder (exit_rules_routescript_linux_body.go) so each OS body is
// readable on its own.
//
// Filename avoids the `_windows.go` GOOS build tag — the Go code
// that builds a Windows .cmd is identical on any host platform
// (the script is just a string), so we want this file compiled on
// Linux/macOS too. Same for the linux_body file.

import (
	"fmt"
	"net"
	"strings"
)

// buildWindowsRouteScript returns the .cmd body that sets up
// (or rolls back, when restore=true) the per-IP /32 + per-subnet
// routes pointing at the exit node on Windows.
//
// It also detects and saves the original default gateway via three
// fallback methods (netsh → route print → wmic) so the script can
// restore the LAN gateway if Tailscale has overwritten it.
func buildWindowsRouteScript(routes []routeEntry, exitNodeIP string, restore bool) string {
	var sb strings.Builder

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
		writeWindowsRestoreScript(&sb, routes, exitNodeIP)
	} else {
		writeWindowsSetupScript(&sb, routes, exitNodeIP)
	}

	return sb.String()
}

// writeWindowsRestoreScript removes the per-IP /32 + subnet routes
// added by setup, then re-adds 0.0.0.0/0 via the exit node.
func writeWindowsRestoreScript(sb *strings.Builder, routes []routeEntry, exitNodeIP string) {
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
}

// writeWindowsSetupScript removes the Tailscale default route,
// restores the original LAN gateway, then adds per-IP /32 +
// per-subnet routes via the exit node.
func writeWindowsSetupScript(sb *strings.Builder, routes []routeEntry, exitNodeIP string) {
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
