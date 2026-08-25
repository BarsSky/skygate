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
	"strings"
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
//
// 2026-08-25 (B185): the autoupdater stores resolved
// subnets under TWO different parent_domain formats:
//   - "<domain>"           — when the autoupdater resolves
//     the domain directly via net.LookupHost (e.g. "t.me"
//     → 149.154.167.99/32, parent_domain="t.me")
//   - "cdn:<provider>:<domain>" — when the CDN-detector
//     identifies the site as Cloudflare/Fastly/Google/Akamai
//     and uses the published IP ranges for that CDN
//     (e.g. discord.gg → cdn:cloudflare:discord.gg, then
//     INSERTs the 15 published Cloudflare CIDRs).
// The B184 DOMAIN-rule status propagation must look up
// BOTH formats — otherwise a Cloudflare-routed domain
// shows ⏳ pending even when its 15 published CDN ranges
// are sitting in headscale ApprovedRoutes. The helper
// `LookupResolvedForDomain` does the merged lookup
// (key + cdn:*:key) in one call.
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

// LookupResolvedForDomain (B185) returns the merged set of
// resolved CIDRs for a DOMAIN rule — both the direct
// parent_domain match AND the cdn:*:<domain> alias. The
// autoupdater stores resolved subnets under either format
// depending on whether the resolution came from
// net.LookupHost (parent_domain = "<domain>") or from the
// CDN-detector (parent_domain = "cdn:<provider>:<domain>").
// Without the alias lookup, Cloudflare/Fastly/Google/Akamai-
// routed domains would always show ⏳ pending in the B184
// three-state badge even when their CDN ranges are sitting
// in headscale ApprovedRoutes.
//
// Returns nil when neither key has a match. The caller
// (`ruleApprovedInHeadscale` + form_my statusByRuleID) treats
// nil and an empty set identically ("no resolution yet").
func LookupResolvedForDomain(resolvedByDomain map[string]map[string]bool, userID, deviceID int64, exitNode, domain string) map[string]bool {
	merged := map[string]bool{}
	// (a) direct match — autoupdater's net.LookupHost path
	// stores parent_domain = "<domain>".
	direct := resolvedByDomain[ResolvedKeyForTuple(userID, deviceID, exitNode, domain)]
	for cid, ok := range direct {
		merged[cid] = ok
	}
	// (b) cdn alias — the CDN-detector stores
	// parent_domain = "cdn:<provider>:<domain>". We don't
	// know which provider the autoupdater picked (it picks
	// by ASN match in cdn.go), so we look up by suffix.
	// The map size is small (a few hundred entries across
	// the whole admin view) so an O(n) suffix scan is
	// fine; the alternative is a second SQL query with
	// `parent_domain LIKE 'cdn:%:discord.gg'`, but that
	// would re-introduce the autoupdater's exact LIKE
	// pattern which is what B175 already does.
	prefix := ResolvedKeyForTuple(userID, deviceID, exitNode, "cdn:")
	suffix := ":" + domain
	for k, v := range resolvedByDomain {
		if len(k) < len(prefix)+len(suffix) {
			continue
		}
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !strings.HasSuffix(k, suffix) {
			continue
		}
		for cid, ok := range v {
			merged[cid] = ok
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}
