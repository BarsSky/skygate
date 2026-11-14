// relay_bulk_b471_render_test.go — B471 (2026-11-14) — the two new controls RENDER.
//
// The grep contract proves the form, the route and the i18n pairs exist; it cannot prove
// that the templates read the data the handlers pass. This file executes the real templates
// against synthetic data, which is the failure class a grep is blind to: a renamed field
// renders nothing, a missing key throws, a branch hides the control.
package handlers

import (
	"bytes"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// b471AdminData is the minimum the admin body dereferences, plus the two cards this block
// touches. The harness stubs `t`/`tf` with "return the key", so assertions are made on KEYS
// and on raw interpolated values, never on translated text.
func b471AdminData(bulk exit_rules.BulkRelayCard, rc exit_rules.RelayCardData) map[string]any {
	return map[string]any{
		"Page": "exit-rules", "Title": "Exit Rules",
		"Rules": []any{}, "Logs": []any{}, "Snapshots": []any{},
		"GroupedByUser": map[string]any{},
		"TotalRules":    0, "MaxTotalRules": 0, "LoadPct": 0, "ShownRules": 0,
		"RulePage":          struct{ Page, PageSize, Total, TotalGroups int }{},
		"MismatchCount":     0,
		"NoExitNodeDevices": map[string]int{}, "NoExitNodeCount": 0, "NoExitNodeList": []string{},
		"DeviceFilter": "", "DeviceRuleCount": 0, "DeviceIndex": []any{},
		"err": "", "applied": false, "rolled": false, "reapplied": false,
		"form_user_id": "", "form_device_id": "", "form_exit_node": "",
		"form_target_type": "", "form_target_value": "", "form_action": "",
		"all_devices":      false,
		"limit_per_device": 500, "limit_per_user": 500, "limit_total": 500,
		"limit_per_device_source": "default", "limit_per_user_source": "default", "limit_total_source": "default",
		"limit_per_device_override": "", "limit_total_override": "",
		"limits_saved": false, "limits_err": "",
		"BulkUsers": []exit_rules.BulkUserOption{},
		"bulk":      false, "bulk_added": "", "bulk_dup": "", "bulk_skip": "",
		"bulk_devices": "", "bulk_limit": false, "bulk_err": "",
		"ViewMode": "device", "TargetRowsAdmin": []exit_rules.TargetRowAdmin{}, "TargetDevicesAdmin": 0,
		"RelayCard": rc,
		"BulkRelay": bulk,
	}
}

// TestB471_AdminPageRendersTheBulkRelayForm — the group move exists as a form, its result
// travels in the flash, and the rule tree's checkboxes point at THAT form by id (so one form
// can cover rows the template renders in three different groups, without JavaScript).
func TestB471_AdminPageRendersTheBulkRelayForm(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")
	data := b471AdminData(
		exit_rules.BulkRelayCard{
			Relays:  []string{"emilia", "karolina"},
			FlashOK: "Перенесено правил: 12 → karolina.",
		},
		exit_rules.RelayCardData{
			PolicyMode: exit_rules.RelayPolicyRequireApproval,
			GlobalMode: exit_rules.RelayPolicyRequireApproval,
			Relays:     []string{"karolina", "emilia"},
		},
	)
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules", data); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		`id="admin-bulk-relay-card"`,
		`action="/admin/exit-rules/bulk-relay"`,
		`id="bulk-relay-form"`,
		`name="to_relay"`,
		`name="move_pins"`,
		`<option value="karolina">karolina</option>`,
		`exit_rules_admin.bulk_relay_submit`,
		`exit_rules_admin.bulk_relay_hint`,
		"Перенесено правил: 12 → karolina.", // the flash is rendered, not swallowed
	} {
		if !strings.Contains(got, want) {
			t.Errorf("bulk-relay card: rendered page is missing %q", want)
		}
	}
}

// TestB471_AdminDecisionCardShowsManualPins — an approval OVERWRITES a hand-made pin, so the
// pending row must show it. The assertion is on the BRANCH: the badge key appears only when
// the row carries a manual-pin label (a badge rendered unconditionally would shout at every
// request).
func TestB471_AdminDecisionCardShowsManualPins(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")
	render := func(manualLabel string) string {
		rc := exit_rules.RelayCardData{
			PolicyMode: exit_rules.RelayPolicyRequireApproval,
			GlobalMode: exit_rules.RelayPolicyRequireApproval,
			Relays:     []string{"karolina", "emilia"},
			Pending: []exit_rules.RelayRequestRow{{
				RelayRequest: exit_rules.RelayRequest{
					ID: 9, UserID: 6, Username: "skyadmin", DeviceHostname: "skyworker",
					FromRelay: "emilia", ToRelay: "karolina",
					Prefixes: []string{"104.16.0.0/12", "162.158.0.0/15"},
					Status:   exit_rules.RelayRequestPending,
				},
				AffectedLabel: "michail",
				StatusLabel:   "ожидает решения",
				ManualLabel:   manualLabel,
			}},
		}
		data := b471AdminData(exit_rules.BulkRelayCard{Relays: []string{"karolina"}}, rc)
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules", data); err != nil {
			t.Fatalf("render body-admin-exit_rules: %v", err)
		}
		return buf.String()
	}

	withPin := render("104.16.0.0/12 → emilia")
	if !strings.Contains(withPin, `exit_rules_admin.relay_request_manual_pins`) {
		t.Error("a pending row with a hand-made pin does not show it: the operator cannot see that the approval overwrites it")
	}
	if !strings.Contains(withPin, `exit_rules_admin.relay_request_manual_pins_title`) {
		t.Error("the badge carries no explanation of what an approval does to the pin")
	}
	without := render("")
	if strings.Contains(without, `exit_rules_admin.relay_request_manual_pins`) {
		t.Error("the manual-pin badge renders for a request that touches no manual pin")
	}
}

// TestB471_MyPageRendersTheGroupSelect — the user picks SEVERAL rules and they travel as one
// ask; the select must therefore be `multiple`, and the hint must say so.
func TestB471_MyPageRendersTheGroupSelect(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	data := myExitRulesWithRelayCard(exit_rules.RelayCardData{
		PolicyMode: exit_rules.RelayPolicyRequireApproval,
		Relays:     []string{"karolina"},
		Rules: []exit_rules.RelayRuleChoice{
			{ID: 41, Label: "skyworker → discordapp.com"},
			{ID: 42, Label: "skyworker → discord.gg"},
		},
	})
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `name="rule_id" multiple`) {
		t.Error("the rule picker is single-select: a group ask cannot be formed from the panel")
	}
	if !strings.Contains(got, `exit_rules.relay_request_rule_multi`) {
		t.Error("the multi-select hint did not render, so Ctrl-click is undiscoverable")
	}
	if strings.Count(got, `<option value="4`) != 2 {
		t.Error("both of the user's rules must be offered in the picker")
	}
}
