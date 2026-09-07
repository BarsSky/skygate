// Package exit_rules — cdn_group_admin_test.go (B237.22).
//
// Unit tests for the admin-side grouping helper
// (GroupAdminRulesByCDN). Mirrors cdn_group_test.go but
// operates on AdminRule rows (the richer struct used by
// /admin/exit-rules + /admin/exit-nodes).
//
// The contracts pinned here:
//
//   - CDN markers (parent_domain = "cdn:<name>:<domain>")
//     get grouped, with the AdminRule rows inside each group.
//   - Non-CDN rules (parent_domain="" or non-marker) stay
//     ungrouped in original order.
//   - Group key = full marker (not just Source), so two
//     markers with the same Source but different CDNs are
//     two different groups.
//   - Sort: by Source (parent domain, primary) + CDN
//     (secondary tiebreaker for stable order when Sources
//     are equal).
//   - The AdminRule rows are returned with all their
//     annotation fields intact (Applicable,
//     ApprovedInHeadscale, PreferredHost, etc.) — the
//     grouping does NOT drop the B178/B182/B184 contract
//     fields the admin template needs.
package exit_rules

import (
	"testing"
)

func TestGroupAdminRulesByCDN_Empty(t *testing.T) {
	groups, ungrouped := GroupAdminRulesByCDN(nil)
	if len(groups) != 0 {
		t.Errorf("empty: got %d groups, want 0", len(groups))
	}
	if len(ungrouped) != 0 {
		t.Errorf("empty: got %d ungrouped, want 0", len(ungrouped))
	}
}

func TestGroupAdminRulesByCDN_AllUngrouped(t *testing.T) {
	// 3 manual rules → 0 groups, 3 ungrouped in original order.
	rr := []AdminRule{
		{ID: 1, TargetValue: "1.2.3.4/32", ParentDomain: ""},
		{ID: 2, TargetValue: "5.6.7.8/32", ParentDomain: ""},
		{ID: 3, TargetValue: "9.10.11.12/24", ParentDomain: ""},
	}
	groups, ungrouped := GroupAdminRulesByCDN(rr)
	if len(groups) != 0 {
		t.Errorf("all-ungrouped: got %d groups, want 0", len(groups))
	}
	if len(ungrouped) != 3 {
		t.Fatalf("all-ungrouped: got %d ungrouped, want 3", len(ungrouped))
	}
	for i, r := range ungrouped {
		if r.ID != i+1 {
			t.Errorf("all-ungrouped: ungrouped[%d].ID = %d, want %d", i, r.ID, i+1)
		}
	}
}

func TestGroupAdminRulesByCDN_SingleGroup(t *testing.T) {
	// 5 Cloudflare CIDRs for one domain. The annotation
	// fields (Applicable, ApprovedInHeadscale, PreferredHost)
	// must be preserved on each AdminRule.
	rr := []AdminRule{
		{ID: 1, TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Applicable: true, ApprovedInHeadscale: true, PreferredHost: "emilia"},
		{ID: 2, TargetValue: "104.24.0.0/14", ParentDomain: "cdn:cloudflare:discordapp.com", Applicable: true, ApprovedInHeadscale: true, PreferredHost: "emilia"},
		{ID: 3, TargetValue: "172.64.0.0/13", ParentDomain: "cdn:cloudflare:discordapp.com", Applicable: true, ApprovedInHeadscale: false, PreferredHost: "emilia"},
		{ID: 4, TargetValue: "162.158.0.0/15", ParentDomain: "cdn:cloudflare:discordapp.com", Applicable: true, ApprovedInHeadscale: true, PreferredHost: "emilia"},
		{ID: 5, TargetValue: "173.245.48.0/20", ParentDomain: "cdn:cloudflare:discordapp.com", Applicable: false, ApprovedInHeadscale: false, PreferredHost: "karolina"},
	}
	groups, ungrouped := GroupAdminRulesByCDN(rr)
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
	if g.Count != 5 {
		t.Errorf("single-group: Count = %d, want 5", g.Count)
	}
	// Annotation fields preserved on each row.
	if g.Rules[0].Applicable != true || g.Rules[0].ApprovedInHeadscale != true || g.Rules[0].PreferredHost != "emilia" {
		t.Errorf("single-group: Rules[0] annotation lost: %+v", g.Rules[0])
	}
	if g.Rules[4].Applicable != false || g.Rules[4].PreferredHost != "karolina" {
		t.Errorf("single-group: Rules[4] annotation lost: %+v", g.Rules[4])
	}
	// Rules sorted by TargetValue (lexicographic).
	for i := 1; i < len(g.Rules); i++ {
		if g.Rules[i-1].TargetValue > g.Rules[i].TargetValue {
			t.Errorf("single-group: rules not sorted at index %d: %q > %q",
				i, g.Rules[i-1].TargetValue, g.Rules[i].TargetValue)
		}
	}
}

func TestGroupAdminRulesByCDN_MultipleGroups(t *testing.T) {
	// 3 groups: cloudflare+discordapp (5), cloudflare+youtube
	// (3), akamai+agent.example.io (2). Plus 1 manual rule.
	rr := []AdminRule{
		{ID: 1, TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com"},
		{ID: 2, TargetValue: "104.24.0.0/14", ParentDomain: "cdn:cloudflare:discordapp.com"},
		{ID: 3, TargetValue: "172.64.0.0/13", ParentDomain: "cdn:cloudflare:discordapp.com"},
		{ID: 4, TargetValue: "162.158.0.0/15", ParentDomain: "cdn:cloudflare:discordapp.com"},
		{ID: 5, TargetValue: "173.245.48.0/20", ParentDomain: "cdn:cloudflare:discordapp.com"},
		{ID: 6, TargetValue: "192.0.2.0/24", ParentDomain: "cdn:cloudflare:youtube.com"},
		{ID: 7, TargetValue: "198.51.100.0/24", ParentDomain: "cdn:cloudflare:youtube.com"},
		{ID: 8, TargetValue: "203.0.113.0/24", ParentDomain: "cdn:cloudflare:youtube.com"},
		{ID: 9, TargetValue: "23.32.0.0/11", ParentDomain: "cdn:akamai:agent.example.io"},
		{ID: 10, TargetValue: "23.192.0.0/11", ParentDomain: "cdn:akamai:agent.example.io"},
		{ID: 11, TargetValue: "10.0.0.0/8", ParentDomain: ""},
	}
	groups, ungrouped := GroupAdminRulesByCDN(rr)
	if len(groups) != 3 {
		t.Fatalf("multiple-groups: got %d groups, want 3", len(groups))
	}
	if len(ungrouped) != 1 {
		t.Errorf("multiple-groups: got %d ungrouped, want 1", len(ungrouped))
	}
	// Sorted by Source: agent.example.io < discordapp.com <
	// youtube.com.
	wantSources := []string{"agent.example.io", "discordapp.com", "youtube.com"}
	wantCDNs := []string{"akamai", "cloudflare", "cloudflare"}
	wantCounts := []int{2, 5, 3}
	for i, g := range groups {
		if g.Source != wantSources[i] {
			t.Errorf("multiple-groups: groups[%d].Source = %q, want %q", i, g.Source, wantSources[i])
		}
		if g.CDN != wantCDNs[i] {
			t.Errorf("multiple-groups: groups[%d].CDN = %q, want %q", i, g.CDN, wantCDNs[i])
		}
		if g.Count != wantCounts[i] {
			t.Errorf("multiple-groups: groups[%d].Count = %d, want %d", i, g.Count, wantCounts[i])
		}
	}
	if ungrouped[0].ID != 11 {
		t.Errorf("multiple-groups: ungrouped[0].ID = %d, want 11", ungrouped[0].ID)
	}
}

func TestGroupAdminRulesByCDN_MultipleCDNs_SameSource(t *testing.T) {
	// Two markers with the same Source but different CDNs
	// (e.g. the autoupdate switched a domain from
	// cloudflare to akamai mid-stream — the domain still
	// has rows under the OLD cloudflare marker, plus new
	// rows under the NEW akamai marker). Two groups, not
	// one. Sort tiebreaker: akamai < cloudflare.
	rr := []AdminRule{
		{ID: 1, TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:foo.com"},
		{ID: 2, TargetValue: "104.24.0.0/14", ParentDomain: "cdn:cloudflare:foo.com"},
		{ID: 3, TargetValue: "23.32.0.0/11", ParentDomain: "cdn:akamai:foo.com"},
	}
	groups, ungrouped := GroupAdminRulesByCDN(rr)
	if len(groups) != 2 {
		t.Fatalf("multiple-CDNs: got %d groups, want 2", len(groups))
	}
	if len(ungrouped) != 0 {
		t.Errorf("multiple-CDNs: got %d ungrouped, want 0", len(ungrouped))
	}
	// akamai first (CDN tiebreaker), then cloudflare.
	if groups[0].CDN != "akamai" {
		t.Errorf("multiple-CDNs: groups[0].CDN = %q, want akamai (tiebreaker)", groups[0].CDN)
	}
	if groups[1].CDN != "cloudflare" {
		t.Errorf("multiple-CDNs: groups[1].CDN = %q, want cloudflare (tiebreaker)", groups[1].CDN)
	}
	// Same Source on both.
	if groups[0].Source != "foo.com" || groups[1].Source != "foo.com" {
		t.Errorf("multiple-CDNs: Sources not both foo.com: %q / %q",
			groups[0].Source, groups[1].Source)
	}
}

func TestGroupAdminRulesByCDN_PreservesAnnotations(t *testing.T) {
	// Regression: the admin grouping must NOT strip the
	// B178/B182/B184 annotation fields. The admin template
	// reads .Applicable, .ApprovedInHeadscale, .PreferredHost
	// on every row, and the per-rule state badge depends on
	// all three.
	rr := []AdminRule{
		{ID: 1, TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:foo.com",
			Applicable: true, ApprovedInHeadscale: true, PreferredHost: "emilia",
			DeviceName: "workstation-1", UserName: "michail", ExitNode: "emilia", CreatedAt: "2026-08-25"},
	}
	groups, _ := GroupAdminRulesByCDN(rr)
	if len(groups) != 1 {
		t.Fatalf("preserves-annotations: got %d groups, want 1", len(groups))
	}
	got := groups[0].Rules[0]
	if got.Applicable != true {
		t.Errorf("Applicable field lost: %+v", got)
	}
	if got.ApprovedInHeadscale != true {
		t.Errorf("ApprovedInHeadscale field lost: %+v", got)
	}
	if got.PreferredHost != "emilia" {
		t.Errorf("PreferredHost field lost: %+v", got)
	}
	if got.DeviceName != "workstation-1" {
		t.Errorf("DeviceName field lost: %+v", got)
	}
	if got.UserName != "michail" {
		t.Errorf("UserName field lost: %+v", got)
	}
	if got.ExitNode != "emilia" {
		t.Errorf("ExitNode field lost: %+v", got)
	}
	if got.CreatedAt != "2026-08-25" {
		t.Errorf("CreatedAt field lost: %+v", got)
	}
}
