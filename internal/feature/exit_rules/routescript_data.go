// Package exit_rules — routescript_data.go owns the DB and
// headscale lookups for the per-OS route-setup script
// generator. The pure per-OS body builders live in
// routescript_linux_body.go and routescript_windows_body.go;
// the public orchestrator lives in routescript.go.
//
// refactor-v0.30 Phase B step 4c (2026-07-29): moved from
// internal/handlers/exit_rules_routescript_data.go.
//
// B276.2 (2026-09-21): the data layer used to return a flat
// []routeEntry + a single exitNodeIP (the FIRST exit node from
// ListExitNodes()). That was honest but wrong once a user
// could have rules targeting different relays (B275): the
// generated script routed EVERY rule through the same relay
// regardless of what the rule said. The new shape is
// []ScriptRouteGroup — one bundle per exit node — and rules
// whose exit_node is empty (engine-auto) are folded into the
// user's preferred exit node, falling back to the first
// healthy one if no preference is set.
package exit_rules

import (
	"fmt"
	"sort"
	"strings"

	"skygate/internal/db"
)

// routeEntry is one (target_type, target_value) row pulled from
// device_rules, restricted to ip/subnet targets (DNS domains and
// telegram entries are filtered out — the script only deals with
// static IP routes).
//
// B277.4 (2026-09-21): the old struct had a `deviceIP` field that
// was never read by any body builder. The field was loaded from
// DB but not used anywhere in the rendered script — it was
// purely a "for diagnostics" comment that never materialised.
// Removed so the SELECT only pulls the columns the body actually
// needs (target_type + target_value + exit_node_id), saving a
// small amount of DB I/O on every /my/exit-rules?script= click.
type routeEntry struct {
	targetType string // "ip" or "subnet"
	targetVal  string // e.g. "8.8.8.8" or "10.0.0.0/24"
}

// ScriptRouteGroup is one (exit-node, routes) bundle that the
// generated client-side script installs. Per-exit-node because
// the new rule model (B275) lets a user have rules targeting
// different relays — the script must route each rule's traffic
// through ITS declared relay, not through a single global one.
//
// ExitNode is the empty string for engine-auto rules (B275.1):
// the engine has not yet assigned a relay, so we route them
// through the user's preferred exit node (fallback: first
// healthy). The script can't express "engine picks later" on a
// Linux/Windows host without tailscale up --exit-node, and
// every real-world deploy is pinned to one exit node anyway,
// so a deterministic fallback is what the user expects.
type ScriptRouteGroup struct {
	// ExitNode is the user-facing relay name ("karolina",
	// "emilia", or "" for the auto group).
	ExitNode string

	// ExitNodeIP is the resolved Tailscale IP. Empty string
	// means "no IP was found" (headscale unreachable or the
	// relay was untagged) — the body builder emits a
	// placeholder so the operator can edit by hand.
	ExitNodeIP string

	// Source describes how the group was formed — "preferred"
	// (user-level pref), "first_healthy" (fallback when no
	// preference), "explicit" (rule's exit_node matched an
	// actual relay). The body builder prints this as a comment
	// in the generated script.
	Source string

	// Routes are the IP/subnet rules for this group, in their
	// original device_rules order.
	Routes []routeEntry
}

// loadRoutesForScriptGroups returns per-exit-node route bundles
// for the user, sorted with the preferred group first. If
// deviceID > 0, the rules are filtered to that single device —
// but all_devices=true rules still apply (B276.1 fan-out) so
// the operator's "for all my devices" intent is honoured on
// every device the script runs on.
func (s *Service) loadRoutesForScriptGroups(userID int, deviceID int) ([]ScriptRouteGroup, error) {
	query := "SELECT target_type, target_value, COALESCE(exit_node_id,'') FROM device_rules WHERE enabled = 1 AND user_id = " + db.PlaceholdersList(1)
	args := []any{userID}
	if deviceID > 0 {
		query += " AND (device_id = " + db.PlaceholdersList(1) + " OR all_devices = 1)"
		args = append(args, deviceID)
	}
	query += " ORDER BY id"

	rows, err := s.dbc().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query device_rules: %w", err)
	}
	defer rows.Close()

	type rowKey struct {
		tt, tv, exitNode string
	}
	buckets := map[rowKey][]routeEntry{}
	for rows.Next() {
		var tt, tv, exitNode string
		if err := rows.Scan(&tt, &tv, &exitNode); err != nil {
			continue
		}
		if tt != "ip" && tt != "subnet" {
			continue
		}
		k := rowKey{tt, tv, exitNode}
		buckets[k] = append(buckets[k], routeEntry{
			targetType: tt,
			targetVal:  tv,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device_rules: %w", err)
	}
	if len(buckets) == 0 {
		return nil, nil
	}

	// Resolve every exit-node hostname mentioned in the rules
	// (plus the user's preferred + the first healthy fallback)
	// to its Tailscale IP, in one headscale round-trip.
	ipByHost, firstHealthy, err := s.loadExitNodeIPMap()
	if err != nil {
		return nil, fmt.Errorf("load exit-node IPs: %w", err)
	}

	// User-level preferred exit-node (DB key, possibly empty).
	userPreferred, _ := db.GetUserExitNodePref(s.dbc(), int64(userID))
	userPreferredHost := TagToHostname(userPreferred.ExitNodeTag)

	// Fold buckets into ScriptRouteGroups. Rules with empty
	// exit_node land in the preferred group (or first_healthy if
	// no preference is set).
	type groupKey struct {
		exitNode string
		source   string
	}
	groups := map[groupKey]*ScriptRouteGroup{}
	groupOrder := []groupKey{}
	resolveGroup := func(exitNode string) groupKey {
		if exitNode != "" {
			return groupKey{exitNode: exitNode, source: "explicit"}
		}
		host := userPreferredHost
		src := "preferred"
		if host == "" {
			host = firstHealthy
			src = "first_healthy"
		}
		return groupKey{exitNode: host, source: src}
	}
	for k, routes := range buckets {
		gk := resolveGroup(k.exitNode)
		g := groups[gk]
		if g == nil {
			g = &ScriptRouteGroup{
				ExitNode:   gk.exitNode,
				ExitNodeIP: ipByHost[gk.exitNode],
				Source:     gk.source,
			}
			groups[gk] = g
			groupOrder = append(groupOrder, gk)
		}
		g.Routes = append(g.Routes, routes...)
	}

	// Sort: preferred first, then alphabetical by exit node.
	sort.SliceStable(groupOrder, func(i, j int) bool {
		gi, gj := groups[groupOrder[i]], groups[groupOrder[j]]
		if gi.Source == "preferred" && gj.Source != "preferred" {
			return true
		}
		if gj.Source == "preferred" && gi.Source != "preferred" {
			return false
		}
		return gi.ExitNode < gj.ExitNode
	})

	out := make([]ScriptRouteGroup, 0, len(groupOrder))
	for _, gk := range groupOrder {
		g := groups[gk]
		// Within a group, sort routes by target for deterministic output.
		sort.SliceStable(g.Routes, func(i, j int) bool {
			if g.Routes[i].targetVal != g.Routes[j].targetVal {
				return g.Routes[i].targetVal < g.Routes[j].targetVal
			}
			return g.Routes[i].targetType < g.Routes[j].targetType
		})
		out = append(out, *g)
	}
	return out, nil
}

// loadExitNodeIPMap returns hostname -> first Tailscale IP for
// every node headscale reports as an exit node, plus the
// hostname of the FIRST healthy one (for the auto fallback).
// On headscale error, the map is empty and firstHealthy is "".
func (s *Service) loadExitNodeIPMap() (map[string]string, string, error) {
	nodes, err := s.HS.ListExitNodes()
	if err != nil {
		return map[string]string{}, "", err
	}
	out := make(map[string]string, len(nodes))
	var firstHealthy string
	for _, n := range nodes {
		if !n.IsExitNode {
			continue
		}
		if len(n.IPAddresses) == 0 {
			continue
		}
		host := strings.TrimSpace(n.Hostname)
		if host == "" {
			continue
		}
		out[host] = n.IPAddresses[0]
		if firstHealthy == "" {
			firstHealthy = host
		}
	}
	return out, firstHealthy, nil
}
