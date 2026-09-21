// approved_routes_index.go — extracted helper for the
// hostname → approved-routes map used by both /my/exit-rules and
// /admin/exit-rules.
//
// B-pending-write (v1.5.42, 2026-09-21): the pre-fix inline loops
// in form_my.go and form_admin.go indexed the map by `n.GivenName`
// first with a `Hostname` fallback, which silently broke the
// look-up whenever headscale returned `GivenName != Hostname`
// (the typical case for nodes with a MagicDNS suffix —
// GivenName="node.tail-scale.ts.net", Hostname="node"). The
// rule's exit_node_id is ALWAYS `n.Hostname` (set at rule-creation
// time when the operator picks from the /my/exit-rules dropdown —
// the dropdown shows `n.Hostname`), so the look-up miss left every
// rule in `pending` even after SyncAdvertisedRoutes had
// successfully pushed the routes to headscale. Fix: index the
// SAME set under BOTH `GivenName` AND `Hostname` so a rule written
// against either key resolves correctly.
//
// The shared map (not two copies) is the second invariant — the
// aliases point at the same Set, so a future mutation through one
// alias is reflected in the other (matters when the next caller
// does an "approve /32 on click" sort of thing).

package exit_rules

import "skygate/internal/headscale"

// indexNodesApprovedRoutes returns the hostname → approved-routes
// map used by the rule-status loop. Index under BOTH GivenName and
// Hostname (when either is non-empty) so a rule's look-up by either
// key resolves correctly.
//
// The shared Set is intentional: alias entries point at the same
// map so future mutations through one alias propagate to the other.
// This is also why the map values are NOT defensively copied —
// the inner Set is short-lived (consumed within one page render)
// and the cost of two copies at scale (1500 rules × N nodes) is
// non-trivial.
func indexNodesApprovedRoutes(nodes []headscale.NodeView) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, n := range nodes {
		if len(n.ApprovedRoutes) == 0 {
			continue
		}
		set := map[string]bool{}
		for _, r := range n.ApprovedRoutes {
			set[r] = true
		}
		for _, host := range []string{n.GivenName, n.Hostname} {
			if host == "" {
				continue
			}
			out[host] = set
		}
	}
	return out
}
