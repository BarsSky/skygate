// exit_rules_source_actions_render_b518_test.go — B518 render contracts.
//
// The model test proves the action decides the right rows; only an EXECUTING render
// proves the card OFFERS the verb, addresses the WHOLE card (not the visible page)
// and hides the verbs that do not apply to this kind of card.
package handlers

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// renderMyCard renders /my/exit-rules with a fixture and one card open.
func renderMyCard(t *testing.T, rows []exit_rules.RuleRow, lens, groupKey string) string {
	t.Helper()
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	data := myExitRulesBase()
	q := url.Values{"lens": []string{lens}}
	data["RulesLens"] = exit_rules.BuildRulesLensView("/my/exit-rules", q, rows, lens, groupKey)
	data["SelfURL"] = "/my/exit-rules?lens=" + lens + "&group=" + groupKey
	data["toggled"] = ""
	data["toggle_n"] = 0
	data["toggle_err"] = ""
	data["group_assigned"] = 0
	data["assigned_to"] = ""
	data["source_resolved"] = ""
	data["source_added"] = 0
	data["source_removed"] = 0
	data["source_err"] = ""
	data["group_label_names"] = []string{}
	data["GroupLabelNames"] = []string{"Игры"}
	data["GroupLabelMaxLen"] = exit_rules.GroupLabelMaxRunes
	data["StatusByRuleID"] = map[int64]string{}
	data["ServedByRuleID"] = map[string]string{}
	// The relay-request channel: allowed, with one relay, so the card's ask renders.
	data["RelayCard"] = map[string]any{"PolicyMode": "ask", "Relays": []string{"karolina"}}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules: %v", err)
	}
	return buf.String()
}

// b518CardRows is one domain with N subnets on one device.
func b518CardRows(n int) []exit_rules.RuleRow {
	rows := []exit_rules.RuleRow{
		{ID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina", TargetType: "domain", TargetValue: "big.example", Action: "accept", Enabled: true},
	}
	for i := 0; i < n; i++ {
		rows = append(rows, exit_rules.RuleRow{
			ID: int64(2 + i), DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina",
			TargetType: "subnet", TargetValue: "192.0.2." + itoaB518(i) + "/32",
			ParentDomain: "big.example", Action: "accept", Enabled: true,
		})
	}
	return rows
}

// TestB518_SourceCardOffersItsVerbs — the four controls of a source card.
func TestB518_SourceCardOffersItsVerbs(t *testing.T) {
	got := renderMyCard(t, b518CardRows(3), "source", "dom:big.example")

	for _, want := range []string{
		`id="group-actions"`,
		`action="/my/exit-rules/source-resolve"`,
		`id="source-resolve-btn"`,
		`action="/my/exit-rules/assign-group"`,
		`id="group-assign-btn"`,
		`action="/my/exit-rules/relay-request"`,
		`action="/my/exit-rules/delete"`,
		`id="source-delete-btn"`,
		"exit_rules.source_resolve_confirm",
		"exit_rules.source_delete_confirm",
		"exit_rules.group_assign_hint",
		// The label suggestions the control needs — rendered ONCE, outside both views.
		`id="group-label-names"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("source card: rendered page is missing %q", want)
		}
	}
	if n := strings.Count(got, `id="group-label-names"`); n != 1 {
		t.Errorf("the group-name datalist rendered %d time(s), want exactly 1 (a duplicate id breaks the link)", n)
	}
}

// TestB518_CardActionsCarryEveryRowNotOnePage — the 200-subnet card.
func TestB518_CardActionsCarryEveryRowNotOnePage(t *testing.T) {
	// 120 subnets: the card's own table is paged (50 per page), the ACTIONS are not.
	rows := b518CardRows(120)
	got := renderMyCard(t, rows, "source", "dom:big.example")

	if n := strings.Count(got, `type="hidden" name="ids" value="`); n != 121 {
		t.Errorf("the delete form carries %d ids, want 121 (the whole card: 1 domain + 120 subnets); the HIDDEN inputs are the card action, the checkbox set is the B519 selection", n)
	}
	if n := strings.Count(got, `name="rule_id" value="`); n != 121 {
		t.Errorf("the relay ask carries %d ids, want 121", n)
	}
	// …and the table below still renders ONE page of rows.
	if n := strings.Count(got, `class="rule-row"`); n > 60 {
		t.Errorf("the row table rendered %d rows — the page window is 50 and the actions must not change that", n)
	}
}

// TestB518_LabelCardHidesTheResolveVerb — a card without a domain must not offer
// «переразрешить»: it would answer another question.
func TestB518_LabelCardHidesTheResolveVerb(t *testing.T) {
	rows := b518CardRows(2)
	for i := range rows {
		rows[i].GroupLabel = "Игры"
	}
	got := renderMyCard(t, rows, "groups", "label:игры")

	if strings.Contains(got, `id="source-resolve-btn"`) {
		t.Error("a label card offered the domain re-resolve verb")
	}
	// The other three remain: filing, asking for a relay and deleting are actions on
	// ROWS, not on a domain.
	for _, want := range []string{`id="group-assign-btn"`, `action="/my/exit-rules/relay-request"`, `id="source-delete-btn"`} {
		if !strings.Contains(got, want) {
			t.Errorf("label card: missing %q", want)
		}
	}
}

// TestB518_RelayAskHiddenWhenThePolicyForbidsIt — the ask is a privilege, and the
// card must not render a control that will be refused.
func TestB518_RelayAskHiddenWhenThePolicyForbidsIt(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	data := myExitRulesBase()
	q := url.Values{"lens": []string{"source"}}
	data["RulesLens"] = exit_rules.BuildRulesLensView("/my/exit-rules", q, b518CardRows(2), "source", "dom:big.example")
	data["GroupLabelNames"] = []string{}
	data["GroupLabelMaxLen"] = exit_rules.GroupLabelMaxRunes
	data["SelfURL"] = "/my/exit-rules?lens=source"
	data["StatusByRuleID"] = map[int64]string{}
	data["ServedByRuleID"] = map[string]string{}
	data["RelayCard"] = map[string]any{"PolicyMode": "deny", "Relays": []string{"karolina"}}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules: %v", err)
	}
	if strings.Contains(buf.String(), `action="/my/exit-rules/relay-request"`) {
		t.Error("the card offered a relay ask while the policy denies it")
	}
}

// itoaB518 is the local fixture printer (no strconv in a test that reads better
// without it).
func itoaB518(n int) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string(rune('0'+n%10)) + out
		n /= 10
	}
	return out
}
