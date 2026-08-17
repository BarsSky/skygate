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

// TestTagToHostname_PostB111_DevInfraFormat — the v1.3.19.1
// follow-up. Pre-fix, `TagToHostname` only stripped "tag:exit-X"
// (the pre-B93 format). After B111 (v1.3.11), the operator's
// prefs use the "tag:dev-infra-X" format — and the buggy
// helper returned the WHOLE "dev-infra-X" string instead of
// just "X". This caused every "exit_node=karolina" rule to
// be flagged as a "preferred_mismatch" against a pref of
// "tag:dev-infra-karolina" (which was extracted to
// "dev-infra-karolina" — the wrong hostname). Fix: handle
// the dev-infra- prefix BEFORE the legacy tag:exit- prefix.
func TestTagToHostname_PostB111_DevInfraFormat(t *testing.T) {
	cases := map[string]string{
		// B111+ format (the format currently used in production)
		"tag:dev-infra-emilia":         "emilia",
		"tag:dev-infra-karolina":       "karolina",
		"tag:dev-infra-sharlotta":      "sharlotta",
		"tag:dev-infra-skygate-host-1": "skygate-host-1",
		// Whitespace + post-fix
		"  tag:dev-infra-emilia  ":     "emilia",
	}
	for in, want := range cases {
		got := TagToHostname(in)
		if got != want {
			t.Errorf("TagToHostname(%q) = %q, want %q (v1.3.19.1 follow-up)", in, got, want)
		}
	}
}

// TestTagToHostname_PrefixOrder — regression test for the
// v1.3.19.1 fix. Pre-fix, the switch statement used
// `strings.TrimPrefix(rest, "exit-")` on the rest AFTER
// stripping "tag:" — so "tag:dev-infra-emilia" was reduced
// to "dev-infra-emilia" (the whole rest, not just "emilia").
// The fix uses case-based ordering: "tag:dev-infra-" first,
// then "tag:exit-", then "tag:". This test ensures the
// prefix order is correct for the edge case "tag:dev-infra-X"
// which DOES contain "tag:" but NOT "tag:exit-".
func TestTagToHostname_PrefixOrder(t *testing.T) {
	// The "tag:dev-infra-" prefix MUST be checked before "tag:"
	// or "tag:exit-". A naive `TrimPrefix(t, "tag:")` then
	// `TrimPrefix(rest, "exit-")` would leave "dev-infra-emilia"
	// unchanged (the bug).
	if got := TagToHostname("tag:dev-infra-emilia"); got != "emilia" {
		t.Errorf("pre-fix bug: TagToHostname(\"tag:dev-infra-emilia\") = %q, want \"emilia\"", got)
	}
	// Same for the "tag:exit-" legacy format.
	if got := TagToHostname("tag:exit-emilia"); got != "emilia" {
		t.Errorf("legacy: TagToHostname(\"tag:exit-emilia\") = %q, want \"emilia\"", got)
	}
	// Bare hostname — no-op.
	if got := TagToHostname("emilia"); got != "emilia" {
		t.Errorf("bare: TagToHostname(\"emilia\") = %q, want \"emilia\"", got)
	}
	// Empty — no-op.
	if got := TagToHostname(""); got != "" {
		t.Errorf("empty: TagToHostname(\"\") = %q, want \"\"", got)
	}
}

// TestIsRuleApplicable_PostB111_DevInfraPref — the v1.3.19.1
// integration test. The rule exit_node is a BARE hostname
// ("karolina"). The device's preferred is stored in the DB
// as "tag:dev-infra-karolina" (the post-B111 format) and
// passed through TagToHostname. Pre-fix, the helper returned
// "dev-infra-karolina" so the comparison failed. Post-fix,
// the helper returns "karolina" so the comparison succeeds
// → IsRuleApplicable = true (the rule IS applicable).
func TestIsRuleApplicable_PostB111_DevInfraPref(t *testing.T) {
	// The HANDLER does: pref = TagToHostname(dbPref.ExitNodeTag)
	// So we simulate that here.
	dbPref := "tag:dev-infra-karolina"
	pref := TagToHostname(dbPref) // what form_my.go gets back
	if !IsRuleApplicable("karolina", pref) {
		t.Errorf("pre-fix bug: IsRuleApplicable(\"karolina\", TagToHostname(%q)=%q) = false, want true (v1.3.19.1 follow-up)", dbPref, pref)
	}
	// And the inverse: rule on emilia, device prefers karolina.
	dbPref2 := "tag:dev-infra-karolina"
	pref2 := TagToHostname(dbPref2)
	if IsRuleApplicable("emilia", pref2) {
		t.Errorf("true mismatch: IsRuleApplicable(\"emilia\", TagToHostname(%q)=%q) = true, want false (genuine mismatch)", dbPref2, pref2)
	}
}
