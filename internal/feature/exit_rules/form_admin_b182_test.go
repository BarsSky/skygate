// 2026-08-25 (B182): regression tests for ApprovedInHeadscale.
//
// Live bug (operator report, 2026-08-25):
//   "правила что решил поставить себе пользователь michail
//    они не применились на exit node но в skygate в exit
//    rules помечены как принятые и текущая проверка
//    показывает конфликт"
//
// Root cause: B178's "Applicable" check is purely logical —
// "rule.ExitNode matches the device's preferred exit-node" —
// it does NOT verify the rule's actual target CIDR is in
// headscale ApprovedRoutes for that exit-node. So a rule
// could show ✅ "accepted" in /admin/exit-rules and
// /my/exit-rules while the rule's CIDR was never pushed
// to headscale (or headscale's auto_approve silently
// rejected it).
//
// B182 fix: add ApprovedInHeadscale bool to AdminRule. The
// annotator looks up the rule's target_value in
// approvedByExitNode[rule.ExitNode] which is built from
// headscale's ListAllNodes.ApprovedRoutes. The template
// renders three states:
//   ✅ approved      — Applicable AND ApprovedInHeadscale
//   ⏳ pending       — Applicable but ApprovedInHeadscale=false
//   ⚠️ wrong-node   — Applicable=false (the existing dead-rule case)

package exit_rules

import "testing"

// TestAnnotateRulesWithPrefs_ApprovedInHeadscale_SimpleMatch —
// the headline case: a subnet rule on emilia, emilia has
// that subnet in headscale's ApprovedRoutes → both
// Applicable=true AND ApprovedInHeadscale=true. This is the
// "fully working" state.
func TestAnnotateRulesWithPrefs_ApprovedInHeadscale_SimpleMatch(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
			TargetType: "subnet", TargetValue: "104.16.0.0/12",
			ExitNode: "emilia"},
	}
	prefFn := func(uid int64, hn string) string { return "emilia" } // basic prefers emilia
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"104.16.0.0/12": true, "0.0.0.0/0": true, "::/0": true},
	}
	_ = annotateRulesWithPrefs(rr, prefFn, approvedByExitNode, nil)
	r := rr[0]
	if !r.Applicable {
		t.Errorf("basic/emilia: Applicable=false, want true (emilia is the preferred exit-node)")
	}
	if !r.ApprovedInHeadscale {
		t.Errorf("basic/emilia/subnet-104.16.0.0/12: ApprovedInHeadscale=false, want true (headscale has 104.16.0.0/12 in emilia's ApprovedRoutes)")
	}
}

// TestAnnotateRulesWithPrefs_ApprovedInHeadscale_Pending —
// the regression case the operator reported: the rule's
// ExitNode matches the device's preferred, but the rule's
// target_value is NOT in headscale ApprovedRoutes. B182
// distinguishes this from "fully working" — the rule
// shows ⏳ pending, not ✅ approved.
func TestAnnotateRulesWithPrefs_ApprovedInHeadscale_Pending(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
			TargetType: "subnet", TargetValue: "104.16.0.0/12",
			ExitNode: "emilia"},
	}
	prefFn := func(uid int64, hn string) string { return "emilia" }
	// emilia's ApprovedRoutes has 0.0.0.0/0, ::/0, and OTHER
	// CIDRs but NOT 104.16.0.0/12. Rule is applicable
	// (exit_node matches preferred) but not approved in
	// headscale.
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"0.0.0.0/0": true, "::/0": true, "8.8.8.0/24": true},
	}
	_ = annotateRulesWithPrefs(rr, prefFn, approvedByExitNode, nil)
	r := rr[0]
	if !r.Applicable {
		t.Errorf("Applicable should be true (emilia is the preferred exit-node)")
	}
	if r.ApprovedInHeadscale {
		t.Errorf("ApprovedInHeadscale=true, want false — emilia does NOT have 104.16.0.0/12 in its ApprovedRoutes (this is the live bug)")
	}
}

// TestAnnotateRulesWithPrefs_ApprovedInHeadscale_WrongExitNode —
// the existing dead-rule case: rule.ExitNode differs from
// the device's preferred. B178 set Applicable=false. B182
// adds the headscale-state check on top: ApprovedInHeadscale
// is computed against the RULE'S exit-node (not the device's
// preferred exit-node), so a rule that points at karolina
// with karolina's approved CIDR would have
// ApprovedInHeadscale=true even though Applicable=false
// (the rule is dead for THIS device but the CIDR IS
// configured in headscale for karolina — useful diagnostic
// info for the operator).
//
// Template rendering priority: Applicable=false takes
// precedence over ApprovedInHeadscale — the rule shows the
// ⚠️ red "wrong exit-node" badge regardless of the
// headscale-state check.
func TestAnnotateRulesWithPrefs_ApprovedInHeadscale_WrongExitNode(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
			TargetType: "subnet", TargetValue: "104.16.0.0/12",
			ExitNode: "karolina"}, // karolina, but basic prefers emilia
	}
	prefFn := func(uid int64, hn string) string { return "emilia" }
	// Both emilia AND karolina have 104.16.0.0/12 in their
	// ApprovedRoutes — the rule is dead for basic (wrong
	// exit-node for this device) but the CIDR IS in
	// karolina's headscale state. The headscale-state check
	// is against the rule's exit-node, not the device's
	// preferred.
	approvedByExitNode := map[string]map[string]bool{
		"emilia":   {"104.16.0.0/12": true},
		"karolina": {"104.16.0.0/12": true},
	}
	_ = annotateRulesWithPrefs(rr, prefFn, approvedByExitNode, nil)
	r := rr[0]
	if r.Applicable {
		t.Errorf("Applicable=true, want false (rule.ExitNode=karolina, basic prefers emilia)")
	}
	// ApprovedInHeadscale is INDEPENDENT of Applicable —
	// it's a headscale-state check on the rule's ExitNode.
	// Here the CIDR IS in karolina's headscale state, so
	// the field is true (this is the "useful diagnostic"
	// case the operator can see at a glance — the CIDR
	// is approved, just for the wrong exit-node).
	if !r.ApprovedInHeadscale {
		t.Errorf("ApprovedInHeadscale=false, want true (104.16.0.0/12 IS in karolina's headscale ApprovedRoutes — this is the diagnostic case)")
	}
}

// TestAnnotateRulesWithPrefs_ApprovedInHeadscale_DomainRule —
// domain rules are NOT checked against headscale directly
// (the autoupdater resolves them to subnets first). The
// rule's ApprovedInHeadscale stays false until the
// resolved subnet rows go through the same check.
func TestAnnotateRulesWithPrefs_ApprovedInHeadscale_DomainRule(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
			TargetType: "domain", TargetValue: "discord.com",
			ExitNode: "emilia"},
	}
	prefFn := func(uid int64, hn string) string { return "emilia" }
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"104.16.0.0/12": true}, // some other CIDR
	}
	_ = annotateRulesWithPrefs(rr, prefFn, approvedByExitNode, nil)
	r := rr[0]
	if !r.Applicable {
		t.Errorf("Applicable=false, want true (emilia is the preferred exit-node)")
	}
	if r.ApprovedInHeadscale {
		t.Errorf("ApprovedInHeadscale=true for domain rule, want false (domains are not directly approved in headscale — autoupdater must resolve to subnets first)")
	}
}

// TestAnnotateRulesWithPrefs_ApprovedInHeadscale_UnknownExitNode —
// a rule points at an exit-node hostname headscale has no
// record of (e.g. the rule was added manually for a node
// that was later removed). ApprovedInHeadscale=false.
func TestAnnotateRulesWithPrefs_ApprovedInHeadscale_UnknownExitNode(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 6, DeviceID: 29, DeviceName: "basic",
			TargetType: "subnet", TargetValue: "104.16.0.0/12",
			ExitNode: "deleted-exit-node"},
	}
	prefFn := func(uid int64, hn string) string { return "emilia" }
	// emilia is in headscale but deleted-exit-node is not
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"104.16.0.0/12": true},
	}
	_ = annotateRulesWithPrefs(rr, prefFn, approvedByExitNode, nil)
	r := rr[0]
	if r.Applicable {
		t.Errorf("Applicable=true, want false (rule.ExitNode=deleted-exit-node, basic prefers emilia)")
	}
	if r.ApprovedInHeadscale {
		t.Errorf("ApprovedInHeadscale=true, want false (deleted-exit-node is not in headscale's node list)")
	}
}

// TestAnnotateRulesWithPrefs_ApprovedInHeadscale_EmptyApprovedMap —
// when headscale returns no nodes (or s.HS is unavailable),
// the approvedByExitNode map is empty / nil. Every rule
// gets ApprovedInHeadscale=false. This is the "headscale
// unreachable" fallback — the operator should re-check.
func TestAnnotateRulesWithPrefs_ApprovedInHeadscale_EmptyApprovedMap(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 1, DeviceID: 9, DeviceName: "skyworker",
			TargetType: "subnet", TargetValue: "104.16.0.0/12",
			ExitNode: "emilia"},
	}
	prefFn := func(uid int64, hn string) string { return "emilia" }
	// nil — simulates headscale being unreachable
	_ = annotateRulesWithPrefs(rr, prefFn, nil, nil)
	r := rr[0]
	if r.ApprovedInHeadscale {
		t.Errorf("ApprovedInHeadscale=true with nil approvedByExitNode, want false (defensive: headscale not reachable)")
	}
	// Applicable should still be true (that's a separate check
	// against the device's preferred exit-node, not headscale)
	if !r.Applicable {
		t.Errorf("Applicable=false with nil approvedByExitNode, want true (Applicable is a separate check)")
	}
}

// TestRuleApprovedInHeadscale_IPRule — direct unit test of
// the helper function. IP rules (target_type='ip') also
// check directly.
func TestRuleApprovedInHeadscale_IPRule(t *testing.T) {
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"8.8.8.8": true},
	}
	if !ruleApprovedInHeadscale(
		AdminRule{ExitNode: "emilia", TargetType: "ip", TargetValue: "8.8.8.8"},
		approvedByExitNode, nil,
	) {
		t.Error("8.8.8.8 should be approved in emilia's headscale state")
	}
	if ruleApprovedInHeadscale(
		AdminRule{ExitNode: "emilia", TargetType: "ip", TargetValue: "1.1.1.1"},
		approvedByExitNode, nil,
	) {
		t.Error("1.1.1.1 should NOT be approved in emilia's headscale state")
	}
}

// TestRuleApprovedInHeadscale_EmptyFields — defensive: nil
// fields return false (rule can't possibly be approved).
func TestRuleApprovedInHeadscale_EmptyFields(t *testing.T) {
	approvedByExitNode := map[string]map[string]bool{
		"emilia": {"104.16.0.0/12": true},
	}
	// empty ExitNode
	if ruleApprovedInHeadscale(
		AdminRule{TargetType: "subnet", TargetValue: "104.16.0.0/12"},
		approvedByExitNode, nil,
	) {
		t.Error("empty ExitNode should return false")
	}
	// empty TargetValue
	if ruleApprovedInHeadscale(
		AdminRule{ExitNode: "emilia", TargetType: "subnet"},
		approvedByExitNode, nil,
	) {
		t.Error("empty TargetValue should return false")
	}
	// empty TargetType
	if ruleApprovedInHeadscale(
		AdminRule{ExitNode: "emilia", TargetValue: "104.16.0.0/12"},
		approvedByExitNode, nil,
	) {
		t.Error("empty TargetType should return false (only subnet/ip are checked)")
	}
}
