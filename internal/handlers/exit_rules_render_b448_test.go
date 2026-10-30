// exit_rules_render_b448_test.go — B448 (2026-10-30).
//
// «проблема с админской таблицей правил группировка получилась по одной строчке и нельзя
// свернуть всю таблицу чтобы дойти до самого низа».
//
// The tree is user → device → exit node → rules, and the exit-node level was ALWAYS closed,
// so a device with one relay hid its only row behind a header reading «1». The collapse
// controls existed since B376 but sat at the TOP of a page that, fully expanded, is
// thousands of pixels tall.
//
// These are RENDER assertions on the real template: a grep can see the class name, only
// execution proves which <details> come out open and that the second control bar is inside
// the page at all.
package handlers

import (
	"bytes"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// b448DevGroup / b448UserGroup mirror the (function-local, anonymous) view-model types the
// handler builds in form_admin.go. Go templates resolve fields by NAME, so a structurally
// identical type renders the very same markup — and this file then owes an update if the
// template starts reading a field the handler does not fill.
type b448DevGroup struct {
	DeviceName string
	Count      int
	TotalCount int
	Oversized  bool
	SingleNode bool
	Nodes      map[string][]exit_rules.AdminRule
	NodesCDN   map[string]exit_rules.CDNDisplayViewAdmin
}

type b448UserGroup struct {
	UserID     int64
	UserCount  int
	TotalCount int
	ShownCount int
	UserLimit  int
	LoadPct    int
	Devices    map[int]b448DevGroup
}

// b448AdminData is the minimum context the tree branch dereferences. NOTE: Rules must be
// non-empty — the template renders the whole tree only `{{if .Rules}}` (the flat slice is the
// "are there any rules at all" gate; the grouped map is the tree), so an empty slice silently
// takes the "no rules" branch and proves nothing about the nesting.
func b448AdminData(grouped map[string]b448UserGroup) map[string]any {
	return map[string]any{
		"Page": "exit-rules", "Title": "Exit Rules",
		"Rules": []exit_rules.RuleRow{{}}, "Logs": []any{}, "Snapshots": []any{},
		"GroupedByUser": grouped,
		"TotalRules":    1, "MaxTotalRules": 0, "LoadPct": 0, "ShownRules": 1,
		"RulePage":          struct{ Page, PageSize, Total, TotalGroups int }{},
		"MismatchCount":     0,
		"NoExitNodeDevices": map[string]int{}, "NoExitNodeCount": 0, "NoExitNodeList": []string{},
		"DeviceFilter": "", "DeviceRuleCount": 0, "DeviceIndex": []any{},
		"err": "", "applied": false, "rolled": false, "reapplied": false,
		"form_user_id": "6", "form_device_id": "", "form_exit_node": "",
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
	}
}

func b448Rule(value string) exit_rules.AdminRule {
	return exit_rules.AdminRule{
		ID:          7,
		Action:      "accept",
		TargetType:  "domain",
		TargetValue: value,
		ExitNode:    "emilia",
		CreatedAt:   "2026-10-30 09:00",
	}
}

// TestAdminTreeSingleGroupRendersOpen_B448 is the operator's first half: ONE relay group per
// device must render its rows immediately, not behind a click that reveals one line.
func TestAdminTreeSingleGroupRendersOpen_B448(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")

	rule := b448Rule("rutracker.org")
	dev := b448DevGroup{
		DeviceName: "skyworker", Count: 1, TotalCount: 1,
		SingleNode: true, // the handler sets this when the device has exactly one relay group
		Nodes:      map[string][]exit_rules.AdminRule{"emilia": {rule}},
		NodesCDN: map[string]exit_rules.CDNDisplayViewAdmin{
			"emilia": {Items: []exit_rules.CDNDisplayItemAdmin{{Rules: []exit_rules.AdminRule{rule}, Count: 1}}},
		},
	}
	user := b448UserGroup{UserID: 6, UserCount: 1, TotalCount: 1, ShownCount: 1, Devices: map[int]b448DevGroup{9: dev}}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules",
		b448AdminData(map[string]b448UserGroup{"skyadmin": user})); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	got := buf.String()

	// The rule row itself must be in the page (it is the thing being hidden), and the
	// exit-node group carrying it must be OPEN.
	if !strings.Contains(got, "rutracker.org") {
		t.Fatal("the single rule is not rendered at all")
	}
	if !strings.Contains(got, `<details open style="margin:4px 0 4px 24px">`) {
		t.Error("the device's ONLY relay group did not render open — the row is still one click away")
	}
	// And the delete control of that row must be there (the tree is where rule deletion lives).
	if !strings.Contains(got, `action="/admin/exit-rules/delete"`) {
		t.Error("the row's delete form is missing from the rendered tree")
	}
}

// TestAdminTreeMultiGroupStaysClosed_B448 is the other side: with two or more relay groups the
// header IS the choice, so it must stay closed (collapsing everything must still mean
// something).
func TestAdminTreeMultiGroupStaysClosed_B448(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")

	a, b := b448Rule("rutracker.org"), b448Rule("discord.com")
	b.ExitNode = "karolina"
	dev := b448DevGroup{
		DeviceName: "skyworker", Count: 2, TotalCount: 2, SingleNode: false,
		Nodes: map[string][]exit_rules.AdminRule{"emilia": {a}, "karolina": {b}},
		NodesCDN: map[string]exit_rules.CDNDisplayViewAdmin{
			"emilia":   {Items: []exit_rules.CDNDisplayItemAdmin{{Rules: []exit_rules.AdminRule{a}, Count: 1}}},
			"karolina": {Items: []exit_rules.CDNDisplayItemAdmin{{Rules: []exit_rules.AdminRule{b}, Count: 1}}},
		},
	}
	user := b448UserGroup{UserID: 6, UserCount: 2, TotalCount: 2, ShownCount: 2, Devices: map[int]b448DevGroup{9: dev}}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules",
		b448AdminData(map[string]b448UserGroup{"skyadmin": user})); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	got := buf.String()

	if strings.Contains(got, `<details open style="margin:4px 0 4px 24px">`) {
		t.Error("a multi-group device rendered its relay groups open — the header is the choice")
	}
	if !strings.Contains(got, `<details style="margin:4px 0 4px 24px">`) {
		t.Error("the collapsed relay-group markup disappeared")
	}
}

// TestAdminTreeCollapseControlsReachable_B448 is the operator's second half: the controls must
// be usable from where the operator actually is (the BOTTOM of a long list) and must not forget
// the choice on the next page.
func TestAdminTreeCollapseControlsReachable_B448(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules",
		b448AdminData(map[string]b448UserGroup{})); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	got := buf.String()

	for _, want := range []struct{ needle, why string }{
		{`position:sticky`, "the control bar is not sticky — it is unreachable at the bottom of the tree"},
		{`id="admin-view-controls-bottom"`, "there is no second bar at the BOTTOM of the tree"},
		{`skyToggleAllDetails(false)`, "no collapse-all control is rendered"},
		{`skygate.admin.exit_rules.tree_open`, "the choice is not remembered, so every page load re-expands the tree"},
		{`localStorage`, "no persistence at all"},
	} {
		if !strings.Contains(got, want.needle) {
			t.Errorf("admin page: %s (missing %q)", want.why, want.needle)
		}
	}
	// The bottom bar must come AFTER the tree container closes, otherwise it is just another
	// copy at the top: find the last occurrence of each and compare.
	treeAt := strings.LastIndex(got, `id="admin-rules-view"`)
	bottomAt := strings.LastIndex(got, `id="admin-view-controls-bottom"`)
	if treeAt < 0 || bottomAt < 0 {
		t.Fatalf("tree container or bottom bar missing (tree=%d bottom=%d)", treeAt, bottomAt)
	}
	if bottomAt < treeAt {
		t.Error("the second control bar is rendered BEFORE the tree — it must be after it")
	}
}
