// Package exit_rules — prefix_owner.go owns the B274 answer to
// "which exit node may advertise this prefix?".
//
// WHY THIS EXISTS (live incident, 2026-09-20, host SKYWORKER):
//
// `StaggeredSync` built each relay's `--advertise-routes` list from
// THAT relay's own rules, so two exit nodes ended up advertising the
// SAME prefixes whenever two devices (of different users) pointed
// their rules at different relays. Live case: `basic` (michail) pinned
// 28 Cloudflare/Google ranges to `emilia`, `skyworker` (skyadmin)
// pinned the same 28 to `karolina` — because both devices carried
// rules for Cloudflare-fronted domains (discord.* / rutracker.org on
// one, auth.docker.io / registry.npmjs.org on the other) and the CDN
// resolver expands a domain to the CDN's published range set.
//
// headscale assigns ONE primary (`node.SubnetRoutes()` returns only
// IsPrimary routes, and the netmap is built from that), so a prefix
// advertised by two relays can be served by only one of them — and
// WHICH one is not stable: it changed between two `nodes list` dumps on
// the same day (`emilia` served the contested ranges first, `karolina`
// later, after a staggeredSync pass had rewritten both relays' route
// sets). A client whose ACL pin named the relay that was not primary at
// that moment lost those destinations ("проблемы с некоторыми сайтами")
// while every device WITHOUT a per-CIDR pin kept working, because an
// unpinned `autogroup:internet` grant follows whatever primary exists.
//
// B274 makes ownership explicit and stable:
//
//   - every prefix has exactly ONE advertising relay per pass;
//   - the owner is the relay that the most enabled rules name for that
//     prefix (that is the device whose traffic depends on it most),
//     with the hostname as a deterministic tie-break so the choice
//     cannot oscillate between passes;
//   - a prefix a relay does not own is DROPPED from its
//     `--advertise-routes` list, so the primary cannot flap;
//   - the losers are reported (log + audit) instead of silently
//     half-working.
//
// The device's own rule list is never rewritten: `device_rules` keeps
// `exit_node_id` exactly as the operator set it, and the per-device
// grant keeps carrying `via=[that exit node]`. What B274 fixes is that
// the relay serving the prefix is now the same relay the pin names.
package exit_rules

import "sort"

// PrefixClaim is one "exit node X has a rule for prefix P" statement.
// Node is the exit_node_id (the relay hostname, as stored in
// device_rules / exit_servers). The caller feeds it one entry per
// enabled ip/subnet rule; duplicates (the same node claiming the same
// prefix several times, e.g. five Cloudflare CIDRs with different
// parent_domain values) are expected and are exactly the weight that
// decides ownership.
type PrefixClaim struct {
	Node   string
	Prefix string
}

// PrefixOwnership returns prefix → owning relay hostname.
//
// Deterministic by construction:
//
//  1. more claims wins (the device(s) that depend on the prefix most);
//  2. equal claim counts → the lexicographically smaller hostname wins.
//
// An empty Node or Prefix is ignored (a rule with no exit node cannot
// advertise anything).
func PrefixOwnership(claims []PrefixClaim) map[string]string {
	counts := make(map[string]map[string]int, len(claims))
	for _, c := range claims {
		if c.Node == "" || c.Prefix == "" {
			continue
		}
		byNode, ok := counts[c.Prefix]
		if !ok {
			byNode = map[string]int{}
			counts[c.Prefix] = byNode
		}
		byNode[c.Node]++
	}
	out := make(map[string]string, len(counts))
	for prefix, byNode := range counts {
		nodes := make([]string, 0, len(byNode))
		for n := range byNode {
			nodes = append(nodes, n)
		}
		sort.Slice(nodes, func(i, j int) bool {
			if byNode[nodes[i]] != byNode[nodes[j]] {
				return byNode[nodes[i]] > byNode[nodes[j]]
			}
			return nodes[i] < nodes[j]
		})
		out[prefix] = nodes[0]
	}
	return out
}

// PrefixLoses returns the (node → dropped prefixes) map for the relays
// that are NOT the owner of a prefix they claim. Used for the operator
// report: a non-empty result means at least one device's rule is pinned
// to a relay that will not serve its prefix, and the operator has to
// decide (re-point that rule, or hand the prefix to that relay).
func PrefixLosers(claims []PrefixClaim) map[string][]string {
	owners := PrefixOwnership(claims)
	out := map[string][]string{}
	for _, c := range claims {
		if c.Node == "" || c.Prefix == "" {
			continue
		}
		if owners[c.Prefix] == c.Node {
			continue
		}
		out[c.Node] = append(out[c.Node], c.Prefix)
	}
	for node := range out {
		sort.Strings(out[node])
	}
	return out
}

// OwnedPrefixes filters one relay's candidate route list down to the
// prefixes that relay owns. `0.0.0.0/0` / `::/0` (the exit-node bases)
// are always kept: every relay is an exit node and must advertise the
// default route.
func OwnedPrefixes(node string, candidates []string, owners map[string]string) []string {
	out := make([]string, 0, len(candidates))
	for _, p := range candidates {
		if p == "0.0.0.0/0" || p == "::/0" {
			out = append(out, p)
			continue
		}
		if owners[p] == node {
			out = append(out, p)
		}
	}
	return out
}

// OwnedPrefixesForRelay returns every prefix the assignment table gives
// to `node`, sorted. Unlike OwnedPrefixes it needs no candidate list:
// the table IS the list.
//
// B279 (v1.5.46): the sync loop used to iterate only the relays named
// by `device_rules.exit_node_id`, while the assignment table (B275) is
// what decides who actually serves a prefix. When the table moved a
// prefix to a relay that no rule names — which is exactly what it does
// when the rule's relay is unhealthy, or after an operator pin — that
// relay was never even visited, so nobody advertised the prefix. Live on
// `aro`: `prefix-ownership(staggeredSync): node drops 19 claimed
// prefix(es) owned by another relay`, with every row of `prefix_owner`
// pointing at `exit-node-vps` and no rule naming it.
func OwnedPrefixesForRelay(node string, owners map[string]string) []string {
	out := make([]string, 0, len(owners))
	for prefix, owner := range owners {
		if prefix == "" || owner != node {
			continue
		}
		out = append(out, prefix)
	}
	sort.Strings(out)
	return out
}

// CollapseDuplicateDerivedRules (B274) deletes rows that repeat the
// same natural key — (user_id, device_id, exit_node_id, target_type,
// target_value) — keeping one. The redundant rows are what the CDN
// expansion creates: a rule for a Cloudflare-fronted domain expands to
// the CDN's published range set, so N domains of the same CDN produce N
// rows per CIDR, differing only in `parent_domain` (which B183
// deliberately keeps OUT of the natural key, so the schema allows
// them).
//
// Winner: a `cdn:`-prefixed parent first (the most informative — it
// names the CDN, which is also B183's stated preference), then the
// lowest id.
//
// ROW_NUMBER() exists in both supported backends (PostgreSQL and SQLite
// 3.25+), so this stays one dialect-neutral statement. Returns the
// number of removed rows.
func (s *Service) CollapseDuplicateDerivedRules() (int64, error) {
	res, err := s.dbc().Exec(`
		DELETE FROM device_rules WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY user_id, device_id, exit_node_id, target_type, target_value
					ORDER BY CASE WHEN COALESCE(parent_domain,'') LIKE 'cdn:%' THEN 0 ELSE 1 END, id
				) AS rn
				FROM device_rules
			) ranked WHERE ranked.rn > 1
		)`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}
