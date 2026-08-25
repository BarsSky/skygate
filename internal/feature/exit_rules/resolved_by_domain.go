// Package exit_rules — resolved_by_domain.go owns the helper
// that backs B184's "DOMAIN rule status propagates from its
// resolved subnets" check.
//
// 2026-08-25 (B184): pre-B184, a DOMAIN rule (target_type =
// "domain", target_value = "discord.com") always rendered as
// ⏳ pending in /my/exit-rules + /admin/exit-rules, even when
// the autoupdater had already resolved the domain to one or
// more subnets and headscale had approved those subnets. The
// user-facing symptom: 8 of 10 YouTube subnets showed ✅
// "accepted" but the parent "youtube.com" row showed ⏳ — the
// two states disagreed even though the subnets were
// literally the resolved-from-this-domain rows.
//
// B184 fix: a DOMAIN rule is ✅ approved iff AT LEAST ONE
// device_rule row with `parent_domain = THIS_DOMAIN` and the
// same (user_id, device_id, exit_node_id) and
// target_type IN ('subnet', 'ip') has its target_value in
// headscale ApprovedRoutes for the rule's ExitNode. Otherwise
// the rule stays in the same state as before (⏳ if no
// resolution yet, ⏳ if resolution exists but headscale hasn't
// approved, ⚠️ if the rule's exit-node differs from the
// device's preferred).
//
// LoadResolvedByDomain reads all (subnet, ip) rules with a
// non-empty parent_domain and groups them by
// "userID:deviceID:exitNode:parent_domain" → set(target_value).
// The admin + my handlers pass this map to
// ruleApprovedInHeadscale, which uses the
// approvedByExitNode[rule.ExitNode] set to decide whether ANY
// of the resolved CIDRs is in headscale state.
//
// The volume is small: 213 device_rules total, of which ~30
// have a non-empty parent_domain (the autoupdater-derived
// subnets). One SQL query covers all (user, device, exit)
// tuples — no per-rule query.
package exit_rules

import (
	"database/sql"
	"fmt"
)

// qSelectResolvedByDomain selects every (subnet, ip) rule
// with a non-empty parent_domain. The caller groups by
// (user_id, device_id, exit_node_id, parent_domain) to build
// the lookup map. parent_domain is COALESCEd because some
// rules have NULL in that column (manual /32 entries without
// a domain origin).
//
// 2026-08-25 (B184): the B183 5-col UNIQUE INDEX
// (device_rules_natural_key_uniq on user_id, device_id,
// exit_node_id, target_type, target_value) prevents duplicate
// resolved rows, so this query returns at most one row per
// (tuple, target_value).
const qSelectResolvedByDomain = `
SELECT user_id, device_id, exit_node_id, COALESCE(parent_domain, ''), target_value
  FROM device_rules
 WHERE target_type IN ('subnet', 'ip')
   AND COALESCE(parent_domain, '') <> ''
`

// LoadResolvedByDomain returns a nested map:
//
//	key   = "userID:deviceID:exitNode:parent_domain"
//	value = set(target_value) of resolved subnets / IPs
//
// grouped by the natural (user, device, exit, parent_domain)
// tuple. Empty map (NOT nil) when no resolved rows exist.
//
// The key format MUST match the key built by
// resolvedKeyForRule (see below) — they share the same
// separator + field order. If you change one, change the
// other.
//
// 2026-08-25 (B184): backs the DOMAIN-status-propagation
// check in ruleApprovedInHeadscale.
func LoadResolvedByDomain(d *sql.DB) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	rows, err := d.Query(qSelectResolvedByDomain)
	if err != nil {
		return out, fmt.Errorf("LoadResolvedByDomain query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var uid, did int64
		var exitNode, parentDomain, targetValue string
		if err := rows.Scan(&uid, &did, &exitNode, &parentDomain, &targetValue); err != nil {
			return out, fmt.Errorf("LoadResolvedByDomain scan: %w", err)
		}
		k := ResolvedKeyForTuple(uid, did, exitNode, parentDomain)
		if out[k] == nil {
			out[k] = map[string]bool{}
		}
		out[k][targetValue] = true
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("LoadResolvedByDomain rows: %w", err)
	}
	return out, nil
}

// ResolvedKeyForTuple builds the lookup key used by both
// LoadResolvedByDomain (the producer) and
// ruleApprovedInHeadscale (the consumer). Centralised here so
// the two sides can never drift apart.
//
// Format: "userID:deviceID:exitNode:parent_domain" — colon
// separators because the colon is not a legal character in
// any of the four fields (Tailscale IPs use dots, headscale
// hostnames use alphanumerics + dashes, target_value CIDRs
// use dots, parent_domain is a domain name). userID and
// deviceID are decimal integers.
//
// 2026-08-25 (B184): kept as a tiny pure helper so the test
// suite can pin the exact key format with a unit test
// (TestResolvedKeyForTuple_Stable).
func ResolvedKeyForTuple(userID, deviceID int64, exitNode, parentDomain string) string {
	return fmt.Sprintf("%d:%d:%s:%s", userID, deviceID, exitNode, parentDomain)
}
