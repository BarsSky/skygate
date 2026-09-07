// Package exit_rules — cdn_group_admin.go (B237.22 / Approach G).
//
// Admin-side counterpart of cdn_group.go. Where
// cdn_group.go operates on the slim RuleRow projection
// (used by the /my/exit-rules page), cdn_group_admin.go
// operates on the richer AdminRule struct (used by
// /admin/exit-rules + /admin/exit-nodes).
//
// Like cdn_group.go, this file is UI-only grouping
// (Approach G). No migration, no schema change, no
// autoupdate change. The storage model is unchanged:
// each rule is a separate row in device_rules; the
// grouping is purely for the view layer.
//
// Why a separate type:
//
//   - AdminRule carries annotation fields (Applicable,
//     ApprovedInHeadscale, PreferredHost) that RuleRow
//     doesn't. These are filled in by
//     annotateRulesWithPrefs in form_admin.go (B178 +
//     B182 + B184 contracts) and the admin template
//     reads them on every per-rule row. Stripping them
//     out for the grouping would lose information.
//   - The /my/exit-rules template doesn't read those
//     annotation fields, so RuleRow is fine there.
//   - Two thin wrappers that share the same sort/group
//     logic is cleaner than a generic helper (Go generics
//     would work, but the per-call boilerplate of a
//     generic helper is bigger than 2x 30-line functions
//     for this use case).
//
// The grouping contract is identical to cdn_group.go:
//
//   - Group key = full parent_domain marker
//     ("cdn:cloudflare:discordapp.com"). Two markers
//     with the same Source but different CDNs are two
//     different groups.
//   - Sort: by Source (parent domain, primary) + CDN
//     (secondary tiebreaker when Sources are equal).
//   - Per-group rules sorted by TargetValue (lexicographic)
//     so the operator's "show ranges" expander is stable.
//   - Ungrouped rules (parent_domain="" or non-marker)
//     returned in original order.
//
// UI rendering: see admin/exit_rules.html and
// admin/exit_nodes.html. Each CDN group becomes a
// collapsible <details> header; the per-CIDR AdminRule
// rows are inside (each row keeps its own remove
// button + audit log entry — same as pre-B237.22).
package exit_rules

import "sort"

// CDNGroupAdmin is the admin-side counterpart of CDNGroup
// (cdn_group.go). Same fields + a Rules slice of AdminRule
// instead of RuleRow.
type CDNGroupAdmin struct {
	Source       string
	CDN          string
	ParentDomain string
	Count        int
	Rules        []AdminRule
}

// CDNDisplayItemAdmin is the admin-side counterpart of
// CDNDisplayItem (cdn_group.go). Same IsCDNGroup flag +
// Source / CDN / Count fields + a Rules slice of AdminRule
// instead of RuleRow.
//
// Why a separate type (instead of reusing CDNDisplayItem):
// AdminRule carries B178/B182/B184 annotation fields
// (Applicable, ApprovedInHeadscale, PreferredHost) that
// RuleRow doesn't. The admin template reads those on
// every per-rule row, so the grouping must preserve them
// verbatim.
type CDNDisplayItemAdmin struct {
	IsCDNGroup bool
	Source     string
	CDN        string
	Count      int
	Rules      []AdminRule
}

// CDNDisplayViewAdmin is the admin-side counterpart of
// CDNDisplayView. Same TotalCount contract (sum of per-CIDR
// row counts). The /admin/exit-rules template reads
// .TotalCount directly to render the per-(user, device,
// exitNode) "N rules" badge.
type CDNDisplayViewAdmin struct {
	Items      []CDNDisplayItemAdmin
	TotalCount int
}

// GroupAdminRulesByCDN groups AdminRule rows by their
// parent_domain marker. Returns the CDN groups (sorted
// by Source + CDN) + the ungrouped rules (in original
// order). Same semantics as GroupRulesByCDN; see the
// comment on that function for the full contract.
func GroupAdminRulesByCDN(rules []AdminRule) (groups []CDNGroupAdmin, ungrouped []AdminRule) {
	// Pass 1: build the group index. Key is the marker
	// (parent_domain), value is the index into the
	// `groups` slice.
	groupIndex := map[string]int{} // marker -> index
	for _, r := range rules {
		if !IsCDNGroupMarker(r.ParentDomain) {
			continue
		}
		if _, exists := groupIndex[r.ParentDomain]; !exists {
			cdn, source, _ := ParseCDNGroupMarker(r.ParentDomain)
			groups = append(groups, CDNGroupAdmin{
				Source:       source,
				CDN:          cdn,
				ParentDomain: r.ParentDomain,
			})
			groupIndex[r.ParentDomain] = len(groups) - 1
		}
		// Rules are appended in pass 2 (so count is set
		// correctly when the group is built).
	}

	// Pass 2: collect the rules.
	for _, r := range rules {
		if IsCDNGroupMarker(r.ParentDomain) {
			idx := groupIndex[r.ParentDomain]
			groups[idx].Rules = append(groups[idx].Rules, r)
		} else {
			ungrouped = append(ungrouped, r)
		}
	}

	// Per-group sort by TargetValue + count.
	for i := range groups {
		sort.Slice(groups[i].Rules, func(a, b int) bool {
			return groups[i].Rules[a].TargetValue < groups[i].Rules[b].TargetValue
		})
		groups[i].Count = len(groups[i].Rules)
	}

	// Group sort by Source + CDN (secondary tiebreaker).
	// See cdn_group.go for the rationale on the CDN
	// tiebreaker (it matters when the autoupdate switched
	// a domain from cloudflare to akamai mid-stream).
	sort.Slice(groups, func(a, b int) bool {
		if groups[a].Source != groups[b].Source {
			return groups[a].Source < groups[b].Source
		}
		return groups[a].CDN < groups[b].CDN
	})

	return groups, ungrouped
}
