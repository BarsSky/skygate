package handlers

// relay_request_b440_render_test.go — B440 (2026-10-29) — the request channel RENDERS.
//
// The grep contract in scripts/check_b440_relay_requests.sh proves the three handlers,
// the routes, the policy precedence and the i18n pairs exist; it cannot prove the two
// new cards render. This file executes the real templates against a synthetic card,
// which is the class of defect a grep is blind to: a template that reads a field the
// handler no longer passes, an unregistered helper, a branch that renders nothing.
//
// The three states the USER card must distinguish are the whole point of the policy:
//   - require_approval: the form renders and says the request will be reviewed,
//   - auto_approve:    the form renders and says the move happens at once,
//   - deny:            the form does NOT render and the reason is named.

import (
	"bytes"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// myExitRulesWithRelayCard is the B376 base plus the B440 card.
func myExitRulesWithRelayCard(card exit_rules.RelayCardData) map[string]any {
	data := myExitRulesBase()
	data["RelayCard"] = card
	return data
}

func TestB440_MyPageRendersTheRequestForm(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	card := exit_rules.RelayCardData{
		PolicyMode: exit_rules.RelayPolicyRequireApproval,
		Relays:     []string{"karolina", "emilia"},
		Rules:      []exit_rules.RelayRuleChoice{{ID: 41, Device: "skyworker", Target: "discordapp.com", Label: "skyworker → discordapp.com"}},
		Mine: []exit_rules.RelayRequestRow{{
			RelayRequest: exit_rules.RelayRequest{
				ID: 7, UserID: 6, Username: "skyadmin", DeviceHostname: "skyworker",
				FromRelay: "emilia", ToRelay: "karolina", Prefixes: []string{"104.16.0.0/12", "104.17.0.0/12"},
				Status: exit_rules.RelayRequestPending, Note: "latency",
			},
			AffectedUsers: []string{"michail"}, AffectedLabel: "michail",
			StatusLabel: "ожидает решения",
		}},
	}
	data := myExitRulesWithRelayCard(card)

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		`action="/my/exit-rules/relay-request"`, // the form posts to the registered route
		`name="rule_id"`,
		`name="to_relay"`,
		`name="note"`,
		`<option value="41">skyworker → discordapp.com</option>`, // the rule is named, not numbered
		`<option value="karolina">karolina</option>`,             // both relays offered
		`exit_rules.relay_request_policy_ask`,                    // the mode is stated
		`exit_rules.relay_request_mine_title`,                    // the history section
		`exit_rules.relay_request_shared`,                        // the cross-user fact on the user's own row
		`exit_rules.relay_request_status_pending`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("user card: rendered page is missing %q", want)
		}
	}
	// The ask-the-admin mode must NOT claim the move is applied.
	if strings.Contains(got, `exit_rules.relay_request_policy_auto`) {
		t.Error("user card: require_approval rendered the auto-apply sentence")
	}
}

func TestB440_MyPageDenyHidesTheForm(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	card := exit_rules.RelayCardData{
		PolicyMode: exit_rules.RelayPolicyDeny,
		Relays:     []string{"karolina"},
		Rules:      []exit_rules.RelayRuleChoice{{ID: 41, Label: "skyworker → discordapp.com"}},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", myExitRulesWithRelayCard(card)); err != nil {
		t.Fatalf("render body-exit_rules (deny): %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `exit_rules.relay_request_policy_deny`) {
		t.Error("deny: the reason is not named")
	}
	if strings.Contains(got, `action="/my/exit-rules/relay-request"`) {
		t.Error("deny: the form rendered even though the policy forbids requests")
	}
}

func TestB440_MyPageAutoApproveSaysSo(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	card := exit_rules.RelayCardData{
		PolicyMode: exit_rules.RelayPolicyAutoApprove,
		Relays:     []string{"karolina"},
		Rules:      []exit_rules.RelayRuleChoice{{ID: 41, Label: "skyworker → discordapp.com"}},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", myExitRulesWithRelayCard(card)); err != nil {
		t.Fatalf("render body-exit_rules (auto): %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `exit_rules.relay_request_policy_auto`) {
		t.Error("auto_approve: the card does not say the move happens immediately")
	}
	if !strings.Contains(got, `action="/my/exit-rules/relay-request"`) {
		t.Error("auto_approve: the form did not render")
	}
}

func TestB440_AdminPageRendersTheQueueAndThePolicy(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")
	data := map[string]any{
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
		"RelayCard": exit_rules.RelayCardData{
			PolicyMode: exit_rules.RelayPolicyRequireApproval,
			GlobalMode: exit_rules.RelayPolicyRequireApproval,
			Relays:     []string{"karolina", "emilia"},
			Users:      []string{"michail", "skyadmin"},
			Pending: []exit_rules.RelayRequestRow{{
				RelayRequest: exit_rules.RelayRequest{
					ID: 7, UserID: 6, Username: "skyadmin", DeviceHostname: "skyworker",
					FromRelay: "emilia", ToRelay: "karolina", Prefixes: []string{"104.16.0.0/12"},
					Status: exit_rules.RelayRequestPending, Note: "latency",
				},
				AffectedUsers: []string{"michail"}, AffectedLabel: "michail",
				StatusLabel: "ожидает решения",
			}},
			Policies: []exit_rules.RelayPolicyRow{{Scope: "user:skyadmin", Mode: exit_rules.RelayPolicyAutoApprove}},
			Recent: []exit_rules.RelayRequestRow{{
				RelayRequest: exit_rules.RelayRequest{
					ID: 6, UserID: 7, Username: "michail", FromRelay: "karolina", ToRelay: "emilia",
					Prefixes: []string{"8.8.8.0/24"}, Status: exit_rules.RelayRequestRejected, Note: "kept on karolina",
				},
				StatusLabel: "отклонён",
			}},
		},
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules", data); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		`action="/admin/exit-rules/relay-request"`,
		`name="id" value="7"`,
		`name="decision" value="approve"`,
		`name="decision" value="reject"`,
		`action="/admin/exit-rules/relay-policy"`,
		`name="username"`,
		`name="mode"`,
		`<option value="skyadmin">skyadmin</option>`,
		`<option value="auto_approve">`,
		`<option value="deny">`,
		`user:skyadmin`, // the stored per-user row is listed, not hidden
		`michail`,       // the cross-user blast radius
		"отклонён",      // the decided tail renders the TRANSLATED status, not the raw token
		`exit_rules_admin.relay_policy_effective`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admin card: rendered page is missing %q", want)
		}
	}
	// A raw status token in the queue table would be an English word in a Russian page.
	if strings.Contains(got, ">pending<") {
		t.Error("admin card: the raw status token leaked into the page instead of the translated label")
	}
}
