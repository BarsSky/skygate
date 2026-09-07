// Package exit_rules — cdn_group_test.go (B237.22).
//
// Pure unit tests for the CDN-grouping helpers in cdn_group.go.
// No DB, no Service, no mocks — the helpers are pure
// functions over the RuleRow projection. Test cases are
// chosen to pin the contract the /admin/exit-rules +
// /admin/exit-nodes pages rely on:
//
//   - CDN markers ("cdn:cloudflare:foo.com") get grouped
//   - Manual rules (parent_domain="") stay ungrouped
//   - A mix of grouped + ungrouped in the same input
//     produces both outputs cleanly
//   - The order is stable (group by Source, rules by
//     target_value)
//   - Edge cases: empty input, single group, multiple
//     groups, malformed markers
//
// The actual template rendering + DB schema are tested
// separately in cdn_group_template_test.go (or the existing
// form_my_test.go) — this file is just the data layer.

package exit_rules

import (
	"testing"
)

func TestIsCDNGroupMarker(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"discordapp.com", false},
		{"cdn:cloudflare:discordapp.com", true},
		{"cdn:fastly:github.com", true},
		{"cdn:google:youtube.com", true},
		{"cdn:akamai:agent.example.io", true},
		{"cdn:", false},              // missing both name + domain
		{"cdn:cloudflare", false},   // missing domain
		{"CDN:cloudflare:foo.com", false}, // case-sensitive on "cdn:" prefix
		{"cdn:CloudFlare:foo.com", true},  // CDN name case is NOT validated
		                                   // — the prefix check is the only
		                                   // structural rule. "CloudFlare" is
		                                   // treated as a valid CDN slot
		                                   // (the autoupdate uses
		                                   // cdnParentMarkerGuess("foo.com")
		                                   // with "cdn:%:foo.com" to match
		                                   // any CDN name).
		{"cdn:unknown:foo.com", true},     // "unknown" is a valid marker
		                                   // (just not a known CDN); the
		                                   // grouping helper doesn't care
		                                   // about the CDN name, only
		                                   // about the marker format.
	}
	for _, c := range cases {
		if got := IsCDNGroupMarker(c.in); got != c.want {
			t.Errorf("IsCDNGroupMarker(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseCDNGroupMarker(t *testing.T) {
	cases := []struct {
		in       string
		wantCDN  string
		wantSrc  string
		wantOK   bool
	}{
		{"cdn:cloudflare:discordapp.com", "cloudflare", "discordapp.com", true},
		{"cdn:fastly:github.com", "fastly", "github.com", true},
		{"cdn:google:youtube.com", "google", "youtube.com", true},
		{"cdn:akamai:agent.example.io", "akamai", "agent.example.io", true},
		// Domain with multiple colons (defensive — autoupdate
		// doesn't produce these but we want the parser robust).
		{"cdn:cloudflare:foo:bar.com", "cloudflare", "foo:bar.com", true},
		// Invalid markers
		{"", "", "", false},
		{"discordapp.com", "", "", false},
		{"cdn:", "", "", false},
		{"cdn:cloudflare", "", "", false},
		{"CDN:cloudflare:foo.com", "", "", false}, // case-sensitive
	}
	for _, c := range cases {
		gotCDN, gotSrc, gotOK := ParseCDNGroupMarker(c.in)
		if gotCDN != c.wantCDN || gotSrc != c.wantSrc || gotOK != c.wantOK {
			t.Errorf("ParseCDNGroupMarker(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.in, gotCDN, gotSrc, gotOK, c.wantCDN, c.wantSrc, c.wantOK)
		}
	}
}

func TestGroupRulesByCDN_Empty(t *testing.T) {
	// Empty input: empty outputs (no groups, no ungrouped).
	groups, ungrouped := GroupRulesByCDN(nil)
	if len(groups) != 0 {
		t.Errorf("empty input: got %d groups, want 0", len(groups))
	}
	if len(ungrouped) != 0 {
		t.Errorf("empty input: got %d ungrouped, want 0", len(ungrouped))
	}
}

func TestGroupRulesByCDN_AllUngrouped(t *testing.T) {
	// All rules are manual (parent_domain="") → 0 groups,
	// all rules returned as ungrouped in original order.
	rules := []RuleRow{
		{ID: 1, TargetValue: "1.2.3.4/32", ParentDomain: ""},
		{ID: 2, TargetValue: "5.6.7.8/32", ParentDomain: ""},
		{ID: 3, TargetValue: "9.10.11.12/24", ParentDomain: ""},
	}
	groups, ungrouped := GroupRulesByCDN(rules)
	if len(groups) != 0 {
		t.Errorf("all-ungrouped: got %d groups, want 0", len(groups))
	}
	if len(ungrouped) != 3 {
		t.Fatalf("all-ungrouped: got %d ungrouped, want 3", len(ungrouped))
	}
	// Order preserved
	for i, r := range ungrouped {
		if r.ID != int64(i+1) {
			t.Errorf("all-ungrouped: ungrouped[%d].ID = %d, want %d", i, r.ID, i+1)
		}
	}
}

func TestGroupRulesByCDN_SingleGroup(t *testing.T) {
	// 15 Cloudflare CIDRs for one domain → 1 group with 15 rules.
	rules := makeCloudflareRules("discordapp.com", 1, 15)
	groups, ungrouped := GroupRulesByCDN(rules)
	if len(groups) != 1 {
		t.Fatalf("single-group: got %d groups, want 1", len(groups))
	}
	if len(ungrouped) != 0 {
		t.Errorf("single-group: got %d ungrouped, want 0", len(ungrouped))
	}
	g := groups[0]
	if g.Source != "discordapp.com" {
		t.Errorf("single-group: Source = %q, want discordapp.com", g.Source)
	}
	if g.CDN != "cloudflare" {
		t.Errorf("single-group: CDN = %q, want cloudflare", g.CDN)
	}
	if g.ParentDomain != "cdn:cloudflare:discordapp.com" {
		t.Errorf("single-group: ParentDomain = %q, want cdn:cloudflare:discordapp.com", g.ParentDomain)
	}
	if g.Count != 15 {
		t.Errorf("single-group: Count = %d, want 15", g.Count)
	}
	if len(g.Rules) != 15 {
		t.Errorf("single-group: len(Rules) = %d, want 15", len(g.Rules))
	}
	// Rules are sorted by TargetValue (lexicographic)
	for i := 1; i < len(g.Rules); i++ {
		if g.Rules[i-1].TargetValue > g.Rules[i].TargetValue {
			t.Errorf("single-group: rules not sorted at index %d: %q > %q",
				i, g.Rules[i-1].TargetValue, g.Rules[i].TargetValue)
		}
	}
}

func TestGroupRulesByCDN_MultipleGroups(t *testing.T) {
	// Multiple groups, sorted alphabetically by Source.
	rules := append(
		makeCloudflareRules("youtube.com", 1, 10),
		makeCloudflareRules("agent.example.io", 20, 5)...,
	)
	groups, ungrouped := GroupRulesByCDN(rules)
	if len(groups) != 2 {
		t.Fatalf("multiple-groups: got %d groups, want 2", len(groups))
	}
	if len(ungrouped) != 0 {
		t.Errorf("multiple-groups: got %d ungrouped, want 0", len(ungrouped))
	}
	// Sorted by Source: "agent.example.io" < "youtube.com"
	if groups[0].Source != "agent.example.io" {
		t.Errorf("multiple-groups: groups[0].Source = %q, want agent.example.io (sorted first)",
			groups[0].Source)
	}
	if groups[1].Source != "youtube.com" {
		t.Errorf("multiple-groups: groups[1].Source = %q, want youtube.com (sorted second)",
			groups[1].Source)
	}
	if groups[0].CDN != "cloudflare" {
		// makeCloudflareRules always creates cloudflare markers,
		// so BOTH groups have CDN="cloudflare" here. The sort
		// differentiates them by Source (agent.example.io vs
		// youtube.com), not by CDN.
		t.Errorf("multiple-groups: groups[0].CDN = %q, want cloudflare (all rules use cloudflare markers)", groups[0].CDN)
	}
	if groups[1].CDN != "cloudflare" {
		t.Errorf("multiple-groups: groups[1].CDN = %q, want cloudflare (all rules use cloudflare markers)", groups[1].CDN)
	}
	if groups[0].Count != 5 {
		t.Errorf("multiple-groups: groups[0].Count = %d, want 5", groups[0].Count)
	}
	if groups[1].Count != 10 {
		t.Errorf("multiple-groups: groups[1].Count = %d, want 10", groups[1].Count)
	}
}

func TestGroupRulesByCDN_Mixed(t *testing.T) {
	// The realistic case: a (user, device, exit_node) tuple
	// has BOTH a CDN-tracked domain (15 rows under 1 marker)
	// AND a few manual /32 rules (parent_domain="").
	rules := append(
		makeCloudflareRules("discordapp.com", 1, 15),
		[]RuleRow{
			{ID: 100, TargetValue: "192.0.2.1/32", ParentDomain: ""},
			{ID: 101, TargetValue: "198.51.100.0/24", ParentDomain: ""},
		}...,
	)
	groups, ungrouped := GroupRulesByCDN(rules)
	if len(groups) != 1 {
		t.Errorf("mixed: got %d groups, want 1", len(groups))
	}
	if len(ungrouped) != 2 {
		t.Errorf("mixed: got %d ungrouped, want 2", len(ungrouped))
	}
	// Ungrouped order preserved (the 2 manual rules appear
	// after the 15 CDN rules in the input → they appear
	// at the end of ungrouped in the same order)
	if ungrouped[0].ID != 100 {
		t.Errorf("mixed: ungrouped[0].ID = %d, want 100", ungrouped[0].ID)
	}
	if ungrouped[1].ID != 101 {
		t.Errorf("mixed: ungrouped[1].ID = %d, want 101", ungrouped[1].ID)
	}
}

func TestGroupRulesByCDN_MultipleCDNs(t *testing.T) {
	// Two CDN groups + manual rules. The "multiple CDNs"
	// case is rare (a single domain is typically on one
	// CDN) but the helper must not crash if it happens
	// — e.g. if the autoupdate switched a domain from
	// Cloudflare to Akamai mid-stream.
	rules := []RuleRow{
		{ID: 1, TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:foo.com"},
		{ID: 2, TargetValue: "104.24.0.0/14", ParentDomain: "cdn:cloudflare:foo.com"},
		{ID: 3, TargetValue: "104.16.0.0/12", ParentDomain: "cdn:akamai:foo.com"},
		{ID: 4, TargetValue: "192.0.2.1/32", ParentDomain: ""},
	}
	groups, ungrouped := GroupRulesByCDN(rules)
	// Two groups, even though Source is the same (foo.com).
	// The marker is the key, not the Source. The operator
	// would see "foo.com [cloudflare] (2 ranges) + foo.com
	// [akamai] (1 range)" — slightly weird but
	// informative.
	if len(groups) != 2 {
		t.Fatalf("multiple-CDNs: got %d groups, want 2", len(groups))
	}
	if groups[0].CDN != "akamai" {
		t.Errorf("multiple-CDNs: groups[0].CDN = %q, want akamai (sorted first alphabetically)", groups[0].CDN)
	}
	if groups[1].CDN != "cloudflare" {
		t.Errorf("multiple-CDNs: groups[1].CDN = %q, want cloudflare (sorted second)", groups[1].CDN)
	}
	if len(ungrouped) != 1 {
		t.Errorf("multiple-CDNs: got %d ungrouped, want 1", len(ungrouped))
	}
}

// TestGroupRulesByCDN_OrderIndependence is a property-style
// test: the output is the same regardless of the input
// order. This pins the B237.22 contract that "the
// grouping is pure (no DB-dependent order)" — a refactor
// that adds accidental order-dependence would fail this
// test.
func TestGroupRulesByCDN_OrderIndependence(t *testing.T) {
	original := append(
		makeCloudflareRules("a.com", 1, 3),
		makeCloudflareRules("b.com", 10, 4)...,
	)
	// Reverse the input
	reversed := make([]RuleRow, len(original))
	for i, r := range original {
		reversed[len(original)-1-i] = r
	}

	g1, u1 := GroupRulesByCDN(original)
	g2, u2 := GroupRulesByCDN(reversed)

	if len(g1) != len(g2) {
		t.Fatalf("order-independence: group count differs (%d vs %d)", len(g1), len(g2))
	}
	for i := range g1 {
		if g1[i].Source != g2[i].Source {
			t.Errorf("order-independence: group[%d].Source = %q (orig) vs %q (rev)",
				i, g1[i].Source, g2[i].Source)
		}
		if g1[i].Count != g2[i].Count {
			t.Errorf("order-independence: group[%d].Count = %d (orig) vs %d (rev)",
				i, g1[i].Count, g2[i].Count)
		}
	}
	if len(u1) != len(u2) {
		t.Errorf("order-independence: ungrouped count differs (%d vs %d)", len(u1), len(u2))
	}
}

// makeCloudflareRules is a test helper that builds a
// realistic Cloudflare marker + 15 CIDRs (the live
// published set as of 2026-08).
func makeCloudflareRules(domain string, startID, count int) []RuleRow {
	cidrs := []string{
		"2.16.0.0/13",
		"23.192.0.0/11",
		"23.32.0.0/11",
		"103.21.244.0/22",
		"103.22.200.0/22",
		"103.31.4.0/22",
		"104.16.0.0/12",
		"104.24.0.0/14",
		"108.162.192.0/18",
		"131.0.72.0/22",
		"141.101.64.0/18",
		"162.158.0.0/15",
		"172.64.0.0/13",
		"173.245.48.0/20",
		"188.114.96.0/20",
		"190.93.240.0/20",
		"197.234.240.0/22",
		"198.41.128.0/17",
	}
	if count > len(cidrs) {
		count = len(cidrs)
	}
	marker := "cdn:cloudflare:" + domain
	rows := make([]RuleRow, count)
	for i := 0; i < count; i++ {
		rows[i] = RuleRow{
			ID:           int64(startID + i),
			TargetValue:  cidrs[i],
			ParentDomain: marker,
		}
	}
	return rows
}
