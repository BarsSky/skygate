// Package exit_rules — routescript_linux_body.go generates
// the bash script for split-tunnel exit-node routing on
// Linux and macOS.
//
// refactor-v0.30 Phase B step 4c (2026-07-29): moved from
// internal/handlers/exit_rules_routescript_linux_body.go.
//
// B276.2 (2026-09-21): the script is now per-exit-node. The
// generator hands the body builder a []ScriptRouteGroup, one
// bundle per relay (the rules' exit_node field names the
// relay; empty exit_node lands in the user's preferred
// group). The body emits one `=== exit-node: NAME ===`
// block per group with the matching `ip route add` commands
// and the resolved Tailscale IP — so a user with rules on
// karolina AND emilia ends up with routes through BOTH
// relays, not just whichever one headscale returned first.
package exit_rules

import (
	"fmt"
	"strings"
)

// buildLinuxRouteScript returns the .sh body that sets up
// (or rolls back, when restore=true) the per-IP /32 +
// per-subnet routes pointing at the exit node on Linux and
// macOS. Pure function. Per-exit-node (B276.2).
func buildLinuxRouteScript(groups []ScriptRouteGroup, restore bool) string {
	var sb strings.Builder

	sb.WriteString("#!/bin/bash\n")
	if restore {
		sb.WriteString("# === Skygate Restore — return all traffic through Tailscale exit node ===\n")
	} else {
		sb.WriteString("# === Skygate Exit Route Setup (Linux/macOS) ===\n")
	}
	sb.WriteString("# Per-exit-node routing (B276.2): each block below routes its targets\n")
	sb.WriteString("# through ITS declared exit node. Empty exit_node rules are folded into\n")
	sb.WriteString("# the user's preferred exit node (fallback: first healthy).\n")
	sb.WriteString("# Run as root: sudo bash this_script.sh\n")
	sb.WriteString("set -e\n\n")

	// Find the tailscale interface
	sb.WriteString("TS_IFACE=$(ip -o link show | grep -o 'tailscale[0-9]*' | head -1)\n")
	sb.WriteString("if [ -z \"$TS_IFACE\" ]; then\n")
	sb.WriteString("    echo \"ERROR: tailscale interface not found. Run: tailscale up --exit-node=EXIT_NODE_NAME\"\n")
	sb.WriteString("    exit 1\n")
	sb.WriteString("fi\n\n")

	if restore {
		writeLinuxRestoreScript(&sb, groups)
	} else {
		writeLinuxSetupScript(&sb, groups)
	}

	return sb.String()
}

// preferredGroup returns the ScriptRouteGroup whose Source is
// "preferred", or the first group if none is preferred. The
// restore path uses it for the default-route re-add.
func preferredGroup(groups []ScriptRouteGroup) *ScriptRouteGroup {
	for i := range groups {
		if groups[i].Source == "preferred" {
			return &groups[i]
		}
	}
	if len(groups) > 0 {
		return &groups[0]
	}
	return nil
}

// writeLinuxRestoreScript removes the per-IP /32 + subnet routes
// added by setup (across every exit-node group), then re-adds
// the default route via the preferred exit node.
func writeLinuxRestoreScript(sb *strings.Builder, groups []ScriptRouteGroup) {
	pref := preferredGroup(groups)
	defaultIP := ""
	if pref != nil {
		defaultIP = pref.ExitNodeIP
	}
	if defaultIP == "" {
		defaultIP = "EXIT_NODE_TAILSCALE_IP"
	}

	sb.WriteString("# Check if split-tunnel routes are applied\n")
	sb.WriteString("if ! ip route show | grep -q \"$TS_IFACE.*" + defaultIP + "\"; then\n")
	sb.WriteString("    echo \"WARNING: No split-tunnel routes detected for " + defaultIP + "\".\n")
	sb.WriteString("    echo \"The exit node default route may already be active.\"\n")
	sb.WriteString("    echo \"If you just ran tailscale up --exit-node, no restore is needed.\"\n")
	sb.WriteString("    ip route show | grep \"$TS_IFACE\" || true\n")
	sb.WriteString("    exit 0\n")
	sb.WriteString("fi\n\n")
	for _, g := range groups {
		if g.ExitNodeIP == "" {
			continue
		}
		sb.WriteString(fmt.Sprintf("echo \"Removing routes from %s (%s)...\"\n", g.ExitNode, g.Source))
		for _, r := range g.Routes {
			target := r.targetVal
			if r.targetType == "ip" && !strings.Contains(target, "/") {
				target += "/32"
			}
			sb.WriteString(fmt.Sprintf("ip route del %s via %s dev \"$TS_IFACE\" 2>/dev/null || echo \"  (route for %s not found)\"\n", target, g.ExitNodeIP, target))
		}
	}
	sb.WriteString("\necho \"Restoring default route via $TS_IFACE (" + defaultIP + ")...\"\n")
	sb.WriteString("ip route add default dev \"$TS_IFACE\" 2>/dev/null || echo \"  (default route already exists)\"\n")
	sb.WriteString("\necho \"=== Current routes via Tailscale ===\"\n")
	sb.WriteString("ip route show | grep \"$TS_IFACE\"\n")
	sb.WriteString("echo \"\"\n")
	sb.WriteString("echo \"Done. All traffic now goes through the preferred Tailscale exit node.\"\n")
}

// writeLinuxSetupScript removes the Tailscale default route
// (added by `--exit-node`), then adds per-IP /32 + per-subnet
// routes via the matching exit node — one block per group. The
// DNS route (MagicDNS 100.100.100.100) goes via the preferred
// group so MagicDNS queries resolve through the operator's
// chosen exit.
func writeLinuxSetupScript(sb *strings.Builder, groups []ScriptRouteGroup) {
	// Save original default route for fallback.
	sb.WriteString("# Save original default route (non-Tailscale) for fallback\n")
	sb.WriteString("ORIG_DEFAULT=$(ip route show default | grep -v \"$TS_IFACE\" | head -1)\n")
	sb.WriteString("if [ -n \"$ORIG_DEFAULT\" ]; then\n")
	sb.WriteString("    echo \"Original default route: $ORIG_DEFAULT\"\n")
	sb.WriteString("else\n")
	sb.WriteString("    echo \"WARNING: No non-Tailscale default route found.\"\n")
	sb.WriteString("fi\n\n")

	sb.WriteString("# Remove default route via Tailscale (added by --exit-node)\n")
	sb.WriteString("DEFAULT_VIA_TS=$(ip route show default | grep \"$TS_IFACE\" | head -1)\n")
	sb.WriteString("if [ -n \"$DEFAULT_VIA_TS\" ]; then\n")
	sb.WriteString("    echo \"Removing default route via $TS_IFACE...\"\n")
	sb.WriteString("    ip route del default dev \"$TS_IFACE\" 2>/dev/null || true\n")
	sb.WriteString("fi\n\n")

	// DNS route via the preferred group.
	pref := preferredGroup(groups)
	if pref != nil && pref.ExitNodeIP != "" {
		sb.WriteString("# Add DNS route for MagicDNS (100.100.100.100) via the preferred exit node\n")
		sb.WriteString("echo \"Adding DNS route for MagicDNS via " + pref.ExitNode + "...\"\n")
		sb.WriteString(fmt.Sprintf("ip route add 100.100.100.100/32 via %s dev \"$TS_IFACE\" 2>/dev/null || echo \"  (already exists)\"\n", pref.ExitNodeIP))
		sb.WriteString("\n")
	}

	// One block per group.
	for _, g := range groups {
		if g.ExitNodeIP == "" {
			sb.WriteString(fmt.Sprintf("# === exit-node: %s (NO TAILSCALE IP — set manually) ===\n", g.ExitNode))
			continue
		}
		sb.WriteString(fmt.Sprintf("# === exit-node: %s (%s) ===\n", g.ExitNode, g.Source))
		sb.WriteString(fmt.Sprintf("echo \"Adding %d route(s) via $TS_IFACE → %s (%s)...\"\n", len(g.Routes), g.ExitNodeIP, g.Source))
		for _, r := range g.Routes {
			target := r.targetVal
			if r.targetType == "ip" && !strings.Contains(target, "/") {
				target += "/32"
			}
			sb.WriteString(fmt.Sprintf("ip route add %s via %s dev \"$TS_IFACE\" 2>/dev/null || echo \"  (route for %s already exists)\"\n", target, g.ExitNodeIP, target))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("echo \"=== Current routes via Tailscale ===\"\n")
	sb.WriteString("ip route show | grep \"$TS_IFACE\"\n")
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
	sb.WriteString("echo \"Done. Each rule's traffic goes through its declared exit node.\"\n")
	sb.WriteString("echo \"All other internet traffic uses your normal connection.\"\n")
}
