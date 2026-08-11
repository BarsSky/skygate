// 2026-08-06: regression tests for preferred_check.go.
//
// The pure helpers (IsRuleApplicable, tagToHostname) don't
// need a DB — they're string math. The DB-bound ones
// (PreferredExitNodeForRule, RulesByDeviceHostname) are thin
// wrappers over db.GetDeviceExitNodePref / GetUserExitNodePref
// which already have their own tests in internal/db/.
//
// The pure tests cover the bug we hit live: the operator's
// rules pointed at karolina but every device was pinned to
// emilia via device_exit_node_prefs — so the "is this rule
// applicable?" check would have flagged the mismatch and
// saved a 30-minute debug session.

package exit_rules

import "testing"

// TestIsRuleApplicable_NoPreference — when there's NO preferred
// exit-node, Tailscale picks by metrics. The rule MAY apply.
// All cases should return true.
func TestIsRuleApplicable_NoPreference(t *testing.T) {
	cases := []struct {
		rule, preferred string
	}{
		{"karolina", ""},
		{"emilia", ""},
		{"anything", ""},
	}
	for _, c := range cases {
		if !IsRuleApplicable(c.rule, c.preferred) {
			t.Errorf("IsRuleApplicable(%q, %q) = false, want true (no preferred means 'depends on metrics')",
				c.rule, c.preferred)
		}
	}
}

// TestIsRuleApplicable_ExactMatch — preferred = rule's exit_node
// → applicable.
func TestIsRuleApplicable_ExactMatch(t *testing.T) {
	if !IsRuleApplicable("karolina", "karolina") {
		t.Error("IsRuleApplicable(karolina, karolina) = false, want true")
	}
}

// TestIsRuleApplicable_Mismatch — preferred != rule's exit_node
// → NOT applicable. This is the "dead rule" case.
func TestIsRuleApplicable_Mismatch(t *testing.T) {
	if IsRuleApplicable("karolina", "emilia") {
		t.Error("IsRuleApplicable(karolina, emilia) = true, want false (dead rule)")
	}
}

// TestIsRuleApplicable_WhitespaceHandling — defensive: spaces
// around either side shouldn't change the answer.
func TestIsRuleApplicable_WhitespaceHandling(t *testing.T) {
	if !IsRuleApplicable("  karolina  ", "  karolina  ") {
		t.Error("whitespace should not flip the match")
	}
	if IsRuleApplicable("  karolina  ", "  emilia  ") {
		t.Error("whitespace should not flip the mismatch")
	}
}

// TestIsRuleApplicable_RuleEmpty — empty rule exit_node means
// the rule has no exit-node (shouldn't render in the template,
// but defensive: it shouldn't match a non-empty preferred).
func TestIsRuleApplicable_RuleEmpty(t *testing.T) {
	if IsRuleApplicable("", "emilia") {
		t.Error("empty rule exit_node should not be 'applicable' when preferred is set")
	}
	if !IsRuleApplicable("", "") {
		t.Error("empty + empty → no preferred, should be applicable")
	}
}

// TestTagToHostname_StandardForms — the "tag:exit-X" → "X"
// conversion. The DB stores tags as "tag:exit-emilia" but
// device_rules.exit_node_id stores bare hostnames ("emilia"),
// so the comparison needs this helper.
func TestTagToHostname_StandardForms(t *testing.T) {
	cases := map[string]string{
		"tag:exit-emilia":    "emilia",
		"tag:exit-karolina":  "karolina",
		"tag:exit-sharlotta": "sharlotta",
		"tag:public":         "public", // non-exit-node tag — defensive
		"emilia":             "emilia", // already bare — no-op
		"":                   "",       // empty — no-op
		"  tag:exit-emilia ": "emilia", // whitespace trim
	}
	for in, want := range cases {
		got := TagToHostname(in)
		if got != want {
			t.Errorf("TagToHostname(%q) = %q, want %q", in, got, want)
		}
	}
}
