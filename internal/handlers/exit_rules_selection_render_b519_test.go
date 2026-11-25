// exit_rules_selection_render_b519_test.go — B519 render contracts.
//
// The handler test proves the action; only an executing render proves the
// multi-select bar is on the page the operator sees, that its four verbs reach the
// right endpoints (three through `op`, delete through `formaction`), and that the
// checkboxes are bound to the bar's form — a checkbox nobody collects is a control
// that silently does nothing.
package handlers

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// TestB519_SelectionBarCarriesEveryVerb — one form, four submit paths.
func TestB519_SelectionBarCarriesEveryVerb(t *testing.T) {
	got := renderMyCard(t, b518CardRows(3), "source", "dom:big.example")

	for _, want := range []string{
		`id="rules-selection"`,
		`action="/my/exit-rules/selection"`,
		`id="rules-selection-bar"`,
		`name="op" value="group"`,
		`name="op" value="disable"`,
		`name="op" value="enable"`,
		// Delete posts to the endpoint that OWNS the cascade.
		`formaction="/my/exit-rules/delete"`,
		`exit_rules.confirm_multi_delete`,
		`exit_rules.selection_off`,
		`exit_rules.selection_on`,
		// The counter and the select-all box.
		`id="rules-select-all"`,
		`id="rules-sel-count"`,
		`skyRulesSelCount`,
		`skyRulesSelAll`,
		// …and the field the filing verb needs.
		`name="group_to"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("selection bar: rendered page is missing %q", want)
		}
	}
}

// TestB519_EveryRowHasABoundCheckbox — a checkbox must be collected by the bar.
func TestB519_EveryRowHasABoundCheckbox(t *testing.T) {
	got := renderMyCard(t, b518CardRows(2), "source", "dom:big.example")

	// One checkbox per rendered row (1 domain + 2 subnets), each bound to the
	// selection form. The binding is matched together with its aria-label: the
	// inline script also contains the bare `form="rules-selection"` selector text,
	// and counting that string alone would count the script, not the inputs.
	if n := strings.Count(got, `class="rule-check" name="ids"`); n != 3 {
		t.Errorf("rendered %d bound checkboxes, want 3", n)
	}
	if n := strings.Count(got, `form="rules-selection" aria-label=`); n != 3 {
		t.Errorf("%d inputs are bound to the selection form, want 3 — an unbound checkbox is never submitted", n)
	}
	// The header gained the checkbox column, so the table's column count still lines up.
	if !strings.Contains(got, `<th class="col-check"></th>`) {
		t.Error("the row table has no checkbox header column")
	}
}

// TestB519_SelectionBannerRenders — the outcome is on the page, from the URL.
func TestB519_SelectionBannerRenders(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	data := myExitRulesBase()
	data["RulesLens"] = exit_rules.BuildRulesLensView(
		"/my/exit-rules", url.Values{"lens": []string{"source"}}, b518CardRows(2), "source", "dom:big.example")
	data["GroupLabelNames"] = []string{}
	data["GroupLabelMaxLen"] = exit_rules.GroupLabelMaxRunes
	data["SelfURL"] = "/my/exit-rules?lens=source"
	data["StatusByRuleID"] = map[int64]string{}
	data["ServedByRuleID"] = map[string]string{}
	data["selection_op"] = "off"
	data["selection_n"] = 2

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `id="selection-banner"`) {
		t.Error("a selection outcome rendered no banner")
	}
	if !strings.Contains(out, "exit_rules.selection_ok_off") {
		t.Error("the banner does not use the «switched off» wording")
	}

	data["selection_op"] = "on"
	buf.Reset()
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules (on): %v", err)
	}
	if !strings.Contains(buf.String(), "exit_rules.selection_ok_on") {
		t.Error("the «switched on» outcome does not use its own wording")
	}
}
