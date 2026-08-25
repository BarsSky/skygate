// Package exit_rules — form_admin_b184_test.go pins the
// B184 DOMAIN-status-propagation behaviour. Live-verified
// 2026-08-25: YouTube subnets (8.8.8.0/24, 142.250.0.0/15,
// etc.) showed ✅ in /admin/exit-rules while the parent
// "youtube.com" row showed ⏳ — the two states disagreed
// even though the subnets were literally the
// resolved-from-this-domain rows. B184 closes the gap:
//
//   DOMAIN rule status = "approved" iff AT LEAST ONE of its
//   resolved subnets (device_rules rows with parent_domain
//   = this rule's target_value, same (user, device, exit)
//   triple) is in headscale ApprovedRoutes for the rule's
//   ExitNode.
//
// The tests cover the 6 critical paths (resolved+approved,
// resolved+none-in-headscale, no-resolved, cross-tuple
// isolation, empty-parent-domain, unknown-domain).
package exit_rules

import "testing"

// TestRuleApprovedInHeadscale_B184_DomainResolvedApproved —
// the headline B184 case: a DOMAIN rule whose autoupdater-
// derived subnets are ALL in headscale ApprovedRoutes must
// render as ✅ approved (not ⏳ pending like pre-B184).
func TestRuleApprovedInHeadscale_B184_DomainResolvedApproved(t *testing.T) {
	rule := AdminRule{
		ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
		TargetType: "domain", TargetValue: "t.me", ExitNode: "emilia",
	}
	// 1.1.1.1/32 was resolved by autoupdater (parent_domain=t.me) and IS in headscale.
	resolved := map[string]map[string]bool{
		"6:29:emilia:t.me": {"1.1.1.1/32": true},
	}
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"1.1.1.1/32": true, "0.0.0.0/0": true, "::/0": true},
	}
	if !ruleApprovedInHeadscale(rule, approvedByExitNode, resolved) {
		t.Error("t.me should be approved — its resolved 1.1.1.1/32 is in headscale for emilia")
	}
}

// TestRuleApprovedInHeadscale_B184_DomainResolvedNoneInHeadscale —
// DOMAIN rule whose autoupdater-derived subnets exist in DB
// but NONE of them are in headscale ApprovedRoutes yet. The
// autoupdater ran, but headscale hasn't approved the new
// routes. Must stay ⏳ pending.
func TestRuleApprovedInHeadscale_B184_DomainResolvedNoneInHeadscale(t *testing.T) {
	rule := AdminRule{
		ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
		TargetType: "domain", TargetValue: "discord.com", ExitNode: "emilia",
	}
	// 9.9.9.9/32 was resolved by autoupdater but headscale doesn't have it yet.
	resolved := map[string]map[string]bool{
		"6:29:emilia:discord.com": {"9.9.9.9/32": true},
	}
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"1.1.1.1/32": true}, // different IP — discord's IP is NOT in headscale
	}
	if ruleApprovedInHeadscale(rule, approvedByExitNode, resolved) {
		t.Error("discord.com should be pending — its only resolved IP is not in headscale yet")
	}
}

// TestRuleApprovedInHeadscale_B184_DomainNoResolved — DOMAIN
// rule with NO resolved subnets in the database at all. The
// autoupdater hasn't run for this domain yet (or DNS
// resolution returned 0 results). Must stay ⏳ pending, same
// as pre-B184 behaviour.
func TestRuleApprovedInHeadscale_B184_DomainNoResolved(t *testing.T) {
	rule := AdminRule{
		ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
		TargetType: "domain", TargetValue: "rutracker.org", ExitNode: "emilia",
	}
	// resolvedByDomain has NO entry for rutracker.org.
	resolved := map[string]map[string]bool{
		"6:29:emilia:other-domain.com": {"1.1.1.1/32": true},
	}
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"0.0.0.0/0": true, "::/0": true},
	}
	if ruleApprovedInHeadscale(rule, approvedByExitNode, resolved) {
		t.Error("rutracker.org should be pending — no resolved subnets in DB")
	}
}

// TestRuleApprovedInHeadscale_B184_CrossTupleIsolation —
// resolved subnets scoped to (user, device, exit) triple A
// must NOT propagate to a DOMAIN rule with the same
// target_value but in triple B. The (user, device, exit)
// triple is part of the lookup key — same domain, different
// device = different resolved set.
func TestRuleApprovedInHeadscale_B184_CrossTupleIsolation(t *testing.T) {
	// basic (user 6, device 29) has the resolved subnets
	rule := AdminRule{
		ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
		TargetType: "domain", TargetValue: "youtube.com", ExitNode: "emilia",
	}
	// skyworker (user 1, device 9) has the resolved subnets — DIFFERENT triple
	resolved := map[string]map[string]bool{
		"1:9:karolina:youtube.com": {"8.8.8.0/24": true}, // skyworker's karolina
	}
	approvedByExitNode := map[string]map[string]bool{
		"karolina": {"8.8.8.0/24": true}, // karolina has 8.8.8.0/24
		"emilia":   {"0.0.0.0/0": true, "::/0": true}, // emilia has no 8.8.8.0/24
	}
	if ruleApprovedInHeadscale(rule, approvedByExitNode, resolved) {
		t.Error("basic/youtube.com should be pending — the resolved 8.8.8.0/24 belongs to skyworker, not basic")
	}
}

// TestRuleApprovedInHeadscale_B184_EmptyParentDomain —
// resolvedByDomain is a non-nil empty map. All DOMAIN rules
// must return false (no data to propagate from).
func TestRuleApprovedInHeadscale_B184_EmptyParentDomain(t *testing.T) {
	rule := AdminRule{
		ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
		TargetType: "domain", TargetValue: "any.com", ExitNode: "emilia",
	}
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"1.1.1.1/32": true},
	}
	if ruleApprovedInHeadscale(rule, approvedByExitNode, map[string]map[string]bool{}) {
		t.Error("empty resolvedByDomain should propagate nothing — DOMAIN rule must stay pending")
	}
}

// TestRuleApprovedInHeadscale_B184_UnknownExitNode —
// DOMAIN rule pointing at an exit-node that headscale has no
// record of. Even if the resolved subnets exist, headscale
// hasn't approved them for an unknown exit. Must return
// false.
func TestRuleApprovedInHeadscale_B184_UnknownExitNode(t *testing.T) {
	rule := AdminRule{
		ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
		TargetType: "domain", TargetValue: "t.me", ExitNode: "deleted-exit-node",
	}
	resolved := map[string]map[string]bool{
		"6:29:deleted-exit-node:t.me": {"1.1.1.1/32": true},
	}
	// approvedByExitNode does NOT contain "deleted-exit-node".
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"1.1.1.1/32": true},
	}
	if ruleApprovedInHeadscale(rule, approvedByExitNode, resolved) {
		t.Error("DOMAIN rule with unknown exit-node should be pending, not approved")
	}
}

// TestResolvedKeyForTuple_Stable — pins the exact key format
// used by both the producer (LoadResolvedByDomain) and the
// consumer (ruleApprovedInHeadscale DOMAIN branch). If the
// format changes, the cross-side lookup silently breaks and
// every DOMAIN rule regresses to ⏳ pending.
func TestResolvedKeyForTuple_Stable(t *testing.T) {
	got := ResolvedKeyForTuple(6, 29, "emilia", "discord.com")
	want := "6:29:emilia:discord.com"
	if got != want {
		t.Errorf("key format drift: got %q, want %q", got, want)
	}
	// Edge cases
	if k := ResolvedKeyForTuple(0, 0, "", ""); k != "0:0::" {
		t.Errorf("zero-value key format drift: got %q", k)
	}
	if k := ResolvedKeyForTuple(1, 2, "karolina", "youtube.com"); k != "1:2:karolina:youtube.com" {
		t.Errorf("basic key format drift: got %q", k)
	}
}
