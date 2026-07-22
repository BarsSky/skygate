package handlers

// exit_rules_routescript_data.go — DB / headscale lookups for the
// route-setup script generator.
//
// Split from exit_rules_routescript.go so the orchestrator (which
// switches on OS and dispatches to the per-OS builders) stays slim
// and free of I/O noise.

import "fmt"

// routeEntry is one (target_type, target_value, device_ip) row pulled
// from device_rules, restricted to ip/subnet targets (DNS domains and
// telegram entries are filtered out — the script only deals with
// static IP routes).
type routeEntry struct {
	targetType string // "ip" or "subnet"
	targetVal  string // e.g. "8.8.8.8" or "10.0.0.0/24"
	deviceIP   string // optional originating device, for diagnostics
}

// loadRoutesForScript returns enabled IP/subnet rules for userID,
// optionally filtered to a single device. Returns the empty slice if
// the user has no such rules (the orchestrator turns that into a
// friendly "no rules configured" comment in the generated script).
func (a *App) loadRoutesForScript(userID int, deviceID int) ([]routeEntry, error) {
	query := "SELECT target_type, target_value, COALESCE(device_ip,'') FROM device_rules WHERE enabled = 1 AND user_id = $1"
	args := []any{userID}
	if deviceID > 0 {
		query += " AND device_id = ?"
		args = append(args, deviceID)
	}
	query += " ORDER BY id"

	rows, err := a.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query device_rules: %w", err)
	}
	defer rows.Close()

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
	return routes, nil
}

// resolveExitNodeIPForScript picks the Tailscale IP of the first
// exit node reachable via headscale. Returns a placeholder
// ("EXIT_NODE_TAILSCALE_IP") when headscale is unreachable so the
// generated script can still be downloaded and edited by hand.
func (a *App) resolveExitNodeIPForScript() string {
	if nodes, err := a.HS.ListExitNodes(); err == nil {
		for _, n := range nodes {
			if len(n.IPAddresses) > 0 {
				return n.IPAddresses[0]
			}
		}
	}
	return "EXIT_NODE_TAILSCALE_IP"
}
