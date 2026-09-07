// Package exit_rules — cdn_group.go (B237.22 / Approach G).
//
// UI-only grouping of device_rules by their CDN parent_domain
// marker. Does NOT change the storage model: each rule
// remains a separate row in device_rules. Does NOT change
// the autoupdate, SyncAdvertisedRoutes, or audit log paths.
// The grouping is purely for display so the operator sees
// "1 grouped rule" instead of "15 raw subnet rows" when a
// domain is on a known CDN (Cloudflare, Fastly, Google,
// Akamai).
//
// Why this exists:
//
// The current CDN detection in cdn.go (sync.go:473) replaces
// per-IP /32 rules with the CDN's published CIDR list. For
// Cloudflare, that's 15 disjoint CIDRs per (user, device,
// exit_node, domain) tuple. The /admin/exit-rules page
// shows all 15 rows as a flat list, which is correct but
// visually noisy (the operator's live data: 46/151 = 30%
// of rows are CDN-derivable from 5 distinct parent_domains).
//
// This file groups the 15 rows under 1 collapsible header
// for display purposes. The per-CIDR rows STILL EXIST in
// the DB and are still individually editable (each row
// keeps its own remove button + audit log entry). The
// grouping is purely a view-layer concern.
//
// Critical non-features (deliberately NOT in this file):
//
//   - No "remove all 15" button (the operator must
//     intentionally remove each CIDR they don't want —
//     no bulk delete that would erase exceptions).
//   - No "edit the grouped rule" form (no such concept
//     exists; the rows are independent).
//   - No "show me only groups / hide groups" filter (the
//     data is always shown; the expander is just visual).
//   - No migration (the storage model is unchanged).
//   - No changes to the autoupdate or SyncAdvertisedRoutes
//     (the headscale ACL gets the same 15 CIDRs as before;
//     grouping is UI-only).

package exit_rules

import (
	"sort"
	"strings"
)

// IsCDNGroupMarker reports whether a parent_domain value
// is a CDN grouping marker (e.g. "cdn:cloudflare:discordapp.com").
//
// Wraps isCDNMarker (the existing private helper in cdn.go)
// to expose the check to the new code paths. Same format
// (B252 contracts pin "cdn:<name>:<domain>" as the canonical
// CDN marker format).
func IsCDNGroupMarker(parentDomain string) bool {
	return isCDNMarker(parentDomain)
}

// ParseCDNGroupMarker splits a CDN marker into its
// (cdn_name, domain) parts. Returns ok=false if the input
// is not a valid marker (e.g. empty, "cdn:" without the
// rest, or "cdn:cloudflare" without a domain).
//
// The marker format is "cdn:<name>:<domain>" (B252). The
// "<name>" slot is always the CDN name (cloudflare, fastly,
// google, akamai). The "<domain>" slot is the original
// domain the operator added (e.g. discordapp.com).
//
// Domain may itself contain colons in theory (e.g. IPv6
// literal hosts, though we don't have any in practice).
// To be safe, we split on the FIRST colon after "cdn:"
// (not the last), so "cdn:cloudflare:foo:bar.com" parses
// as cdn="cloudflare", domain="foo:bar.com". The autoupdate
// never produces such markers, but the parser is
// defensive.
func ParseCDNGroupMarker(parentDomain string) (cdn, domain string, ok bool) {
	if !IsCDNGroupMarker(parentDomain) {
		return "", "", false
	}
	rest := strings.TrimPrefix(parentDomain, "cdn:")
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// CDNDisplayItem is one row in the per-(host, exitNode) rules
// list passed to the /my/exit-rules + /admin/exit-rules
// templates. Each item is either a CDN group (with a header)
// or the ungrouped-rules tail (no header).
//
// Why a struct with a flag instead of two separate slices:
// the template iterates ONE list per (host, exitNode) tuple
// and switches on `.IsCDNGroup` to decide whether to render
// a <details> header. Two slices would require a more
// complex template (with a sentinel row) to interleave.
//
// Same shape for /my and /admin (only the Rules element
// type differs — RuleRow vs AdminRule). The form_my /
// form_admin handlers do the conversion before calling
// the helper.
type CDNDisplayItem struct {
	// IsCDNGroup=true means this is a CDN-grouped bundle
	// of N per-CIDR rules (Source + CDN are populated,
	// Rules has Count items). IsCDNGroup=false means
	// this is the ungrouped-rules tail (Source + CDN +
	// Count are empty/zero, Rules is a flat slice of
	// non-CDN rules).
	IsCDNGroup bool

	// Source is the parent domain the operator added
	// (e.g. "discordapp.com"). Empty when IsCDNGroup=false.
	Source string

	// CDN is the CDN name extracted from the marker
	// ("cloudflare" / "fastly" / "google" / "akamai").
	// Empty when IsCDNGroup=false.
	CDN string

	// Count is len(Rules) for CDN-grouped items (the
	// "CDN: discordapp.com — 15 ranges" badge). 0 for
	// ungrouped items.
	Count int

	// Rules is the slice of per-CIDR rows. For
	// IsCDNGroup=true this is the per-group rows
	// (sorted by TargetValue). For IsCDNGroup=false
	// this is the flat list of manual / non-CDN rules
	// in their original order.
	Rules []RuleRow
}

// CDNDisplayView wraps the CDNDisplayItem slice with a
// pre-computed total rule count. The /my/exit-rules +
// /admin/exit-rules templates read .TotalCount directly
// to render the per-(host, exitNode) "N rules" badge.
//
// The total is the SUM of:
//   - .Count for each CDN group (the per-CIDR rule count
//     under the group header), PLUS
//   - len(.Rules) for the ungrouped-rules tail
//
// This way the badge shows the same number the operator
// would have seen pre-B237.22 (a flat list of N rows per
// (host, exitNode) tuple), even though the actual rows
// are now visually grouped under collapsible headers.
type CDNDisplayView struct {
	Items      []CDNDisplayItem
	TotalCount int
}

// CDNGroup is a set of device_rules that share the same
// parent_domain marker. The "Source" field is the parent
// domain the operator added (e.g. discordapp.com). The
// "CDN" field is the CDN name (cloudflare / fastly /
// google / akamai). The "Rules" slice contains the
// individual subnet rows the autoupdate inserted.
//
// Sorting: groups are sorted by Source (the parent domain)
// alphabetically so the operator's /admin/exit-rules page
// has a stable order.
type CDNGroup struct {
	// Source is the parent domain the operator added
	// (e.g. "discordapp.com"). This is the "what" the
	// operator sees in the UI.
	Source string

	// CDN is the CDN name extracted from the marker
	// (cloudflare / fastly / google / akamai). This is
	// shown as a small badge next to the Source.
	CDN string

	// ParentDomain is the full marker string
	// ("cdn:cloudflare:discordapp.com"). Stored so the
	// template doesn't have to re-parse the marker for
	// each rule.
	ParentDomain string

	// Count is the number of rules in the group
	// (len(Rules)). Stored as a separate field so the
	// template doesn't have to call len() on every render.
	Count int

	// Rules are the per-CIDR subnet rows. Sorted by
	// target_value (lexicographic on the CIDR string) so
	// the operator's expanded view has a stable order.
	Rules []RuleRow
}

// RuleRow is the projection of db.DeviceRule the template
// needs. The same shape as the current per-row template
// fields, so the existing per-rule rendering block can be
// reused verbatim (no need to migrate the template's
// rule-row markup to the new structure).
//
// Defined as a named type (not an alias to db.DeviceRule)
// to:
//
//   - Keep the cdn_group.go file independent of the db
//     package's import cycle (the existing form_my.go
//     already imports db.DeviceRule; this avoids pulling
//     in the full DB shape).
//   - Let the unit tests in cdn_group_test.go work with
//     a minimal test fixture (just the fields the helper
//     actually reads + the fields the template needs).
type RuleRow struct {
	ID           int64
	UserID       int64
	DeviceID     int
	ExitNode     string
	TargetType   string
	TargetValue  string
	ParentDomain string
	Action       string
}

// GroupRulesByCDN takes a slice of RuleRow and returns:
//
//   1. The CDN groups (rules whose parent_domain is a
//      "cdn:<name>:<domain>" marker), grouped by the
//      marker. Each group is sorted by Source.
//   2. The ungrouped rules (parent_domain is empty, or not
//      a CDN marker). These are returned in their original
//      order.
//
// The function is pure (no DB access, no side effects).
// Callers are responsible for fetching the rules from
// db.GetDeviceRulesForUser (or similar) and converting
// them to the RuleRow projection.
//
// Why return a struct with two slices (not a single mixed
// slice): the template renders grouped + ungrouped rules
// differently:
//
//   - Grouped: one `<details>` header with the Source + CDN
//     badge + count, followed by N rule rows that are
//     indented under the header.
//   - Ungrouped: flat list of rule rows (no header).
//
// A mixed slice would require the template to switch on a
// type tag in every iteration. A two-slice struct is
// simpler to template.
//
// Why pure function (not a method on *Service):
//
//   - The grouping logic is testable in isolation
//     (no DB, no Service, no mocks).
//   - The helper is shared between GetMyExitRules and
//     GetAdminExitRules (both pages group the same way).
//   - The unit test file is small + fast.
func GroupRulesByCDN(rules []RuleRow) (groups []CDNGroup, ungrouped []RuleRow) {
	// Two-pass: first pass finds the groups, second pass
	// fills them. We can't do this in one pass because
	// we need to know the group's full Source + count
	// before we can emit any rule under it.

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
			groups = append(groups, CDNGroup{
				Source:       source,
				CDN:          cdn,
				ParentDomain: r.ParentDomain,
			})
			groupIndex[r.ParentDomain] = len(groups) - 1
		}
		// We don't append the rule here — pass 2 does
		// that, with the count already known.
	}

	// Pass 2: collect the rules. We iterate in two
	// separate ranges so the function returns "all
	// groups first (sorted by Source), then all
	// ungrouped rules in original order".
	for _, r := range rules {
		if IsCDNGroupMarker(r.ParentDomain) {
			idx := groupIndex[r.ParentDomain]
			groups[idx].Rules = append(groups[idx].Rules, r)
		} else {
			ungrouped = append(ungrouped, r)
		}
	}

	// Sort each group's rules by target_value
	// (lexicographic). Stable order so the operator's
	// "show ranges" expander is the same on every render.
	for i := range groups {
		sort.Slice(groups[i].Rules, func(a, b int) bool {
			return groups[i].Rules[a].TargetValue < groups[i].Rules[b].TargetValue
		})
		groups[i].Count = len(groups[i].Rules)
	}

	// Sort the groups themselves by Source (the parent
	// domain, primary) + CDN (secondary tiebreaker). Stable
	// so the order is the same on every render.
	//
	// The CDN tiebreaker matters when two markers share the
	// same Source (e.g. the autoupdate switched a domain
	// from cloudflare to akamai mid-stream — the domain
	// still has rows under the old marker, plus new rows
	// under the new one). Without the tiebreaker, the order
	// would be non-deterministic (map iteration in pass 1).
	// With the tiebreaker, the operator sees a stable
	// "akamai" then "cloudflare" order (alphabetical on CDN
	// when Sources are equal).
	sort.Slice(groups, func(a, b int) bool {
		if groups[a].Source != groups[b].Source {
			return groups[a].Source < groups[b].Source
		}
		return groups[a].CDN < groups[b].CDN
	})

	return groups, ungrouped
}
