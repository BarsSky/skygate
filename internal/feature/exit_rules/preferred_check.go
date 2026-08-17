// Package exit_rules — preferred_check.go owns the cross-check
// between device_rules and the device/user preferred exit-node
// prefs.
//
// Why this exists:
//
// A device_rule in `device_rules` (target=rutracker.org, exit_node=karolina)
// only takes effect if the Tailscale client on that device actually
// uses karolina as its exit-node. That decision is made by
// `device_exit_node_prefs` (per-device, overrides everything) or
// `user_exit_node_prefs` (per-user fallback). If a rule points at
// karolina but the device's preferred exit-node is emilia, the rule
// is silently ignored by Tailscale — the operator sees a "dead rule"
// in /my/exit-rules.
//
// The three helpers in this file let the handlers + system_tests
// surface that mismatch:
//
//   - PreferredExitNodeForRule resolves "what's the device/user pref
//     for THIS rule?" (per-device > per-user > "")
//   - RulesByDeviceHostname indexes all enabled rules by hostname
//     (so the admin/devices page can render a per-device warning
//     count without N queries)
//   - IsRuleApplicable compares the rule's exit_node_id against the
//     preferred tag's hostname (extracted from "tag:exit-X" → "X")
//
// 2026-08-06: introduced after the v0.33.1.16 debug session where
// the operator's Cloudflare CIDR rules for rutracker.org were
// pointed at karolina but every device was pinned to emilia via
// device_exit_node_prefs — so the rules never took effect.
//
// All three helpers are pure data — no HTTP, no App, no
// headscale. Tested in preferred_check_test.go.

package exit_rules

import (
	"database/sql"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// PreferredExitNodeForRule returns the effective preferred exit-node
// hostname for a given (user, device) pair. Per-device pref wins;
// per-user pref is the fallback. Returns "" when neither is set
// (no preferred — the client picks based on Tailscale metrics).
//
// The returned value is the EXIT-NODE HOSTNAME, NOT the tag.
// "tag:exit-emilia" → "emilia". The "tag:" prefix is stripped
// because device_rules.exit_node_id stores the bare hostname
// ("emilia"), not the tag — so a direct string compare is what
// the template/handler wants.
func PreferredExitNodeForRule(s *sql.DB, userID int64, deviceHostname string) (string, error) {
	if s == nil {
		return "", nil
	}
	hostname := strings.ToLower(strings.TrimSpace(deviceHostname))
	if hostname == "" {
		return "", nil
	}
	// 1) per-device pref (the strongest signal — overrides user)
	pref, err := db.GetDeviceExitNodePref(s, userID, hostname)
	if err == nil && pref.ExitNodeTag != "" {
		return TagToHostname(pref.ExitNodeTag), nil
	}
	// 2) per-user pref (fallback)
	if userID > 0 {
		upref, uerr := db.GetUserExitNodePref(s, userID)
		if uerr == nil && upref.ExitNodeTag != "" {
			return TagToHostname(upref.ExitNodeTag), nil
		}
	}
	// 3) no preference
	return "", nil
}

// RulesByDeviceHostname returns a map: (userID:hostname) → list of
// rule exit_node_ids that point at SOME exit-node for that device.
// Used by /admin/devices to compute "how many rules for this device
// are dead because they reference a non-preferred exit-node?"
//
// The returned map only includes (user, hostname) pairs that have
// ≥1 enabled rule. Pairs with no rules are omitted — empty map
// entries would force the template to do an extra check.
func RulesByDeviceHostname(s *sql.DB) (map[string][]string, error) {
	if s == nil {
		return map[string][]string{}, nil
	}
	rows, err := s.Query(`
		SELECT user_id, COALESCE(device_hostname, ''), exit_node_id
		  FROM device_rules
		 WHERE enabled = 1
		   AND exit_node_id != ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var uid int64
		var hn, exitNode string
		if err := rows.Scan(&uid, &hn, &exitNode); err != nil {
			continue
		}
		hn = strings.ToLower(strings.TrimSpace(hn))
		if hn == "" || exitNode == "" {
			continue
		}
		key := strconv.FormatInt(uid, 10) + ":" + hn
		out[key] = append(out[key], exitNode)
	}
	return out, rows.Err()
}

// IsRuleApplicable reports whether a rule whose exit_node_id is
// `ruleExitNode` would take effect on a device whose preferred
// exit-node hostname is `preferredHost`. Returns true when either:
//   - there's no preferred host (Tailscale picks by metrics — any
//     rule MAY take effect)
//   - the rule's exit_node matches the preferred host
//
// Returns false ONLY when there IS a preferred host AND the rule's
// exit_node differs. That's the "dead rule" case the templates warn
// about.
func IsRuleApplicable(ruleExitNode, preferredHost string) bool {
	ruleExitNode = strings.TrimSpace(ruleExitNode)
	preferredHost = strings.TrimSpace(preferredHost)
	if preferredHost == "" {
		return true
	}
	return ruleExitNode == preferredHost
}

// TagToHostname strips the "tag:dev-infra-" / "tag:exit-" /
// "tag:" prefix from a headscale tag and returns the bare hostname.
//
// 4 formats supported (must mirror the LOCAL `tagToHost`
// closure in internal/feature/admin/system_tests.go that
// was fixed in v1.3.18.1 for the post-B111 tag format):
//
//   "tag:dev-infra-emilia"    → "emilia"      (B111+ format)
//   "tag:dev-infra-karolina"  → "karolina"    (B111+ format)
//   "tag:exit-emilia"         → "emilia"      (legacy pre-B93 format)
//   "tag:exit-karolina"       → "karolina"    (legacy pre-B93 format)
//   "emilia"                  → "emilia"      (no-op for already-bare)
//   "tag:public"              → "public"      (non-exit-node tag, defensive)
//
// The DB stores tags as "tag:dev-infra-emilia" but
// device_rules.exit_node_id stores bare hostnames ("emilia"),
// so the comparison needs this helper. Pre-v1.3.19.1
// `TagToHostname` only handled "tag:exit-X" and "tag:X" forms
// — the v1.3.18.1 fix updated the LOCAL `tagToHost` in
// system_tests.go but missed this exported function. As a
// result, every rule whose exit_node was "karolina" was
// flagged as a "preferred_mismatch" against the device's
// preferred "tag:dev-infra-karolina" (which the buggy helper
// returned as "dev-infra-karolina" — the wrong hostname).
// This is the v1.3.19.1 follow-up.
func TagToHostname(tag string) string {
	t := strings.TrimSpace(tag)
	switch {
	case strings.HasPrefix(t, "tag:dev-infra-"):
		return strings.TrimPrefix(t, "tag:dev-infra-")
	case strings.HasPrefix(t, "tag:exit-"):
		return strings.TrimPrefix(t, "tag:exit-")
	case strings.HasPrefix(t, "tag:"):
		return strings.TrimPrefix(t, "tag:")
	default:
		return t
	}
}
