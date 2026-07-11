package handlers

// exit_rules_routescript_linux_body.go — generates the bash script for
// split-tunnel exit-node routing on Linux and macOS.
//
// Pure function: takes the resolved route list + exit node IP, emits
// the .sh body. No I/O, no headscale, no DB. Kept separate from the
// orchestrator (exit_rules_routescript.go) and the Windows builder
// (exit_rules_routescript_windows_body.go) so each OS body is readable
// on its own.
//
// macOS uses the same script body (with `route` swapped in would
// be a future improvement — for now users on macOS edit the
// generated .sh to use `route add` instead of `ip route add`).
//
// Filename avoids the `_linux.go` GOOS build tag — the Go code that
// builds a Linux bash script is identical on any host platform
// (the script is just a string), so we want this file compiled on
// Windows/macOS too. Same for the windows_body file.

import (
	"fmt"
	"strings"
)

func buildLinuxRouteScript(routes []routeEntry, exitNodeIP string, restore bool) string {
	var sb strings.Builder

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
		writeLinuxRestoreScript(&sb, routes, exitNodeIP)
	} else {
		writeLinuxSetupScript(&sb, routes, exitNodeIP)
	}

	return sb.String()
}

// writeLinuxRestoreScript removes the per-IP /32 + subnet routes
// added by setup, then re-adds the default route via tailscale.
func writeLinuxRestoreScript(sb *strings.Builder, routes []routeEntry, exitNodeIP string) {
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
}

// writeLinuxSetupScript removes the Tailscale default route (added
// by `--exit-node`), then adds per-IP /32 + per-subnet routes via
// the exit node. Falls back to restoring the original non-Tailscale
// default route at the end if no default exists.
func writeLinuxSetupScript(sb *strings.Builder, routes []routeEntry, exitNodeIP string) {
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
