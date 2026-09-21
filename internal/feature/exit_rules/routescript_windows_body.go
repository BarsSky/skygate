// Package exit_rules — routescript_windows_body.go generates
// the Windows .cmd script for split-tunnel exit-node routing.
//
// refactor-v0.30 Phase B step 4c (2026-07-29): moved from
// internal/handlers/exit_rules_routescript_windows_body.go.
//
// B276.2 (2026-09-21): per-exit-node blocks. The body builder
// emits one `=== exit-node: NAME ===` section per group with
// the matching `route add` commands — so a user with rules on
// karolina AND emilia ends up with routes through BOTH
// relays, not just the first one headscale returned.
package exit_rules

import (
	"fmt"
	"net"
	"strings"
)

// buildWindowsRouteScript returns the .cmd body that sets up
// (or rolls back, when restore=true) the per-IP /32 + subnet
// routes pointing at the exit node on Windows. Per-exit-node
// (B276.2).
func buildWindowsRouteScript(groups []ScriptRouteGroup, restore bool) string {
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
		writeWindowsRestoreScript(&sb, groups)
	} else {
		writeWindowsSetupScript(&sb, groups)
	}

	return sb.String()
}

// writeWindowsRestoreScript removes the per-IP /32 + subnet
// routes added by setup (across every exit-node group), then
// re-adds 0.0.0.0/0 via the preferred exit node.
func writeWindowsRestoreScript(sb *strings.Builder, groups []ScriptRouteGroup) {
	pref := preferredGroup(groups)
	defaultIP := ""
	if pref != nil {
		defaultIP = pref.ExitNodeIP
	}
	if defaultIP == "" {
		defaultIP = "EXIT_NODE_TAILSCALE_IP"
	}

	sb.WriteString("echo === Skygate Restore — return all traffic through Tailscale exit node ===\n\n")
	sb.WriteString("echo Checking for applied split-tunnel routes...\n")
	sb.WriteString("route print -4 | findstr \"" + defaultIP + "\" >nul 2>&1\n")
	sb.WriteString("if %errorlevel% neq 0 (\n")
	sb.WriteString("    echo.\n")
	sb.WriteString("    echo WARNING: No split-tunnel routes detected for " + defaultIP + ".\n")
	sb.WriteString("    echo The exit node default route may already be active.\n")
	sb.WriteString("    echo If you just ran tailscale up --exit-node, no restore is needed.\n")
	sb.WriteString("    echo.\n")
	sb.WriteString("    route print -4\n")
	sb.WriteString("    exit /b 0\n")
	sb.WriteString(")\n\n")

	for _, g := range groups {
		if g.ExitNodeIP == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("echo Removing routes from %s (%s)...\n", g.ExitNode, g.Source))
		for _, r := range g.Routes {
			target := r.targetVal
			if r.targetType == "ip" && !strings.Contains(target, "/") {
				target += "/32"
			}
			_, ipNet, err := net.ParseCIDR(target)
			if err != nil {
				continue
			}
			mask := fmt.Sprintf("%d.%d.%d.%d", ipNet.Mask[0], ipNet.Mask[1], ipNet.Mask[2], ipNet.Mask[3])
			sb.WriteString(fmt.Sprintf("route delete %s mask %s %s 2>nul\n", ipNet.IP.String(), mask, g.ExitNodeIP))
		}
	}
	sb.WriteString("\necho Restoring default route via Tailscale exit node (" + defaultIP + ")...\n")
	sb.WriteString("route add 0.0.0.0 mask 0.0.0.0 " + defaultIP + " 2>nul\n")
	sb.WriteString("\necho Done. All traffic now goes through the preferred Tailscale exit node.\n")
	sb.WriteString("route print -4\n")
}

// writeWindowsSetupScript removes the Tailscale default route,
// restores the original LAN gateway, then adds per-IP /32 +
// per-subnet routes via the matching exit node — one block
// per group. The DNS route (MagicDNS 100.100.100.100) goes
// via the preferred group.
func writeWindowsSetupScript(sb *strings.Builder, groups []ScriptRouteGroup) {
	sb.WriteString("echo === Skygate Exit Route Setup (Windows) ===\n")
	sb.WriteString("echo Per-exit-node routing (B276.2): each block below installs routes\n")
	sb.WriteString("echo through ITS declared exit node. Make sure Tailscale is up first:\n")
	sb.WriteString("echo   tailscale up\n\n")

	// Save original gateway via three fallback methods.
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

	// Compute local subnet from gateway for Tailscale route cleanup.
	sb.WriteString("rem Compute local subnet from gateway for cleanup\n")
	sb.WriteString("for /f \"tokens=1,2,3 delims=.\" %%a in (\"!ORIG_GW!\") do set LOCAL_SUBNET=%%a.%%b.%%c.0\n")
	sb.WriteString("\n")
	sb.WriteString("echo Removing all default routes...\n")
	sb.WriteString("route delete 0.0.0.0 mask 0.0.0.0 2>nul\n")

	sb.WriteString("rem Remove Tailscale-added local subnet route\n")
	sb.WriteString("if defined LOCAL_SUBNET (\n")
	sb.WriteString("    route delete !LOCAL_SUBNET! mask 255.255.255.0 100.100.100.100 2>nul\n")
	sb.WriteString("    route delete !LOCAL_SUBNET! mask 255.255.255.0 100.64.100.2 2>nul\n")
	sb.WriteString("    if !errorlevel! equ 0 (echo   Cleaned Tailscale local subnet route) else (echo   No local subnet route to clean)\n")
	sb.WriteString(")\n")
	sb.WriteString("\n")
	sb.WriteString("if not \"!ORIG_GW!\"==\"\" (\n")
	sb.WriteString("    route add 0.0.0.0 mask 0.0.0.0 !ORIG_GW! 2>nul\n")
	sb.WriteString("    echo   Original default route restored via !ORIG_GW!\n")
	sb.WriteString(")\n")

	// DNS route via preferred group.
	pref := preferredGroup(groups)
	if pref != nil && pref.ExitNodeIP != "" {
		sb.WriteString("\necho Adding DNS route (100.100.100.100) via " + pref.ExitNode + "...\n")
		sb.WriteString(fmt.Sprintf("route add 100.100.100.100 mask 255.255.255.255 %s 2>nul\n", pref.ExitNodeIP))
		sb.WriteString("if %errorlevel! equ 0 (echo   Done.) else (echo   Already exists.)\n")
	}
	sb.WriteString("\n")

	// One block per group.
	for _, g := range groups {
		if g.ExitNodeIP == "" {
			sb.WriteString(fmt.Sprintf("rem === exit-node: %s (NO TAILSCALE IP — set manually) ===\n", g.ExitNode))
			continue
		}
		sb.WriteString(fmt.Sprintf("rem === exit-node: %s (%s) ===\n", g.ExitNode, g.Source))
		sb.WriteString(fmt.Sprintf("echo Adding %d route(s) via Tailscale -> %s (%s)...\n", len(g.Routes), g.ExitNodeIP, g.Source))
		for _, r := range g.Routes {
			target := r.targetVal
			if r.targetType == "ip" && !strings.Contains(target, "/") {
				target += "/32"
			}
			_, ipNet, err := net.ParseCIDR(target)
			if err != nil {
				continue
			}
			mask := fmt.Sprintf("%d.%d.%d.%d", ipNet.Mask[0], ipNet.Mask[1], ipNet.Mask[2], ipNet.Mask[3])
			sb.WriteString(fmt.Sprintf("route add %s mask %s %s 2>nul\n", ipNet.IP.String(), mask, g.ExitNodeIP))
		}
	}
	sb.WriteString("\n")
	sb.WriteString("echo Checking default route...\n")
	sb.WriteString("if \"!ORIG_GW!\"==\"\" (\n")
	sb.WriteString("    echo   WARNING: No default gateway saved. Run: route add 0.0.0.0 mask 0.0.0.0 YOUR_ROUTER_IP\n")
	sb.WriteString(") else (\n")
	sb.WriteString("    echo   Default route: 0.0.0.0 -^> !ORIG_GW!\n")
	sb.WriteString(")\n")
	sb.WriteString("\necho Done. Each rule's traffic goes through its declared exit node.\n")
	sb.WriteString("route print -4\n")
}
