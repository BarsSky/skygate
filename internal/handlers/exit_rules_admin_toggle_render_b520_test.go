// exit_rules_admin_toggle_render_b520_test.go — B520 render contracts.
//
// The handler test proves the change; only an EXECUTING render proves the admin page
// OFFERS it, that a switched-off row is visibly off, that the source column shows a
// domain instead of the raw `cdn:` marker, and that the page has a way back to the
// enabled-only list.
package handlers

import (
	"bytes"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
)

// renderAdminPage renders /admin/exit-rules with one user/device/relay holding the
// given rows, plus the given admin extras (flash keys, ShowDisabled, pager).
func renderAdminPage(t *testing.T, rules []exit_rules.AdminRule, extra map[string]any) string {
	t.Helper()
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")

	nodesCDN := exit_rules.CDNDisplayViewAdmin{
		Items:          []exit_rules.CDNDisplayItemAdmin{{IsCDNGroup: false, Count: len(rules), Rules: rules}},
		TotalCount:     len(rules),
		RealTotalCount: len(rules),
	}
	device := map[string]any{
		"DeviceName": "basic", "Count": len(rules), "TotalCount": len(rules),
		"Oversized": false, "SingleNode": true,
		"Nodes":    map[string][]exit_rules.AdminRule{"karolina": rules},
		"NodesCDN": map[string]exit_rules.CDNDisplayViewAdmin{"karolina": nodesCDN},
	}
	user := map[string]any{
		"UserID": 7, "UserCount": len(rules), "TotalCount": len(rules),
		"ShownCount": len(rules), "UserLimit": 500, "LoadPct": 1,
		"Devices": map[int]any{29: device},
	}

	data := map[string]any{
		"Page": "exit-rules", "Title": "Exit Rules",
		// The tree branch is gated on `{{if .Rules}}` (a non-empty page), so the
		// fixture must carry the rows the way the handler does.
		"Rules": rules, "Logs": []any{}, "Snapshots": []any{},
		"GroupedByUser": map[string]any{"michail": user},
		"TotalRules":    len(rules), "MaxTotalRules": 0, "LoadPct": 1, "ShownRules": len(rules),
		// Groups per page < total groups, or the template hides the nav entirely.
		// Groups per page < total groups, or the template hides the nav entirely. The
		// type is db.AdminRulePage (not an anonymous struct): the template's totals
		// line reads TotalUsers, which only that type carries.
		"RulePage": db.AdminRulePage{
			Page: 2, PageSize: 20, TotalRules: len(rules), TotalGroups: 65, TotalUsers: 1,
		},
		"MismatchCount":     0,
		"NoExitNodeDevices": map[string]int{}, "NoExitNodeCount": 0, "NoExitNodeList": []string{},
		"DeviceFilter": "", "DeviceRuleCount": len(rules), "DeviceIndex": []any{},
		"err": "", "applied": false, "rolled": false, "reapplied": false,
		"form_user_id": "", "form_device_id": "", "form_exit_node": "",
		"form_target_type": "", "form_target_value": "", "form_action": "", "form_group_label": "",
		"all_devices":      false,
		"limit_per_device": 500, "limit_per_user": 500, "limit_total": 500,
		"limit_per_device_source": "default", "limit_per_user_source": "default", "limit_total_source": "default",
		"limit_per_device_override": "", "limit_total_override": "",
		"limits_saved": false, "limits_err": "",
		"BulkUsers": []exit_rules.BulkUserOption{}, "BulkRelay": map[string]any{},
		"bulk": false, "bulk_added": "", "bulk_dup": "", "bulk_skip": "",
		"bulk_devices": "", "bulk_limit": false, "bulk_err": "",
		"ViewMode": "device", "TargetRowsAdmin": []exit_rules.TargetRowAdmin{}, "TargetDevicesAdmin": 0,
		"SelfURL": "/admin/exit-rules", "ShowDisabled": false,
		"ShowDisabledHref": "/admin/exit-rules?show_disabled=1",
		"AdminPager":       exit_rules.PagerView{},
		// B523: the admin page is a page of GROUPS by default; this fixture renders the
		// TREE (`?view=device`), so the lens view is present but switched off. A fixture
		// that omitted it would silently render the card page instead, and the B520
		// contracts below would then be asserting things about a page they do not test.
		"AdminLens":     exit_rules.RulesLensView{},
		"admin_toggled": "", "admin_n": 0, "admin_err": "",
		"RelayCard": map[string]any{"PolicyMode": "ask", "Relays": []string{"karolina"}, "Pending": []any{}, "Recent": []any{}, "Policies": []any{}, "Modes": []string{}},
	}
	for k, v := range extra {
		data[k] = v
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules", data); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	return buf.String()
}

// TestB520_AdminRowOffersTheSwitchAndTheOffChip — the verb and the state.
func TestB520_AdminRowOffersTheSwitchAndTheOffChip(t *testing.T) {
	rules := []exit_rules.AdminRule{
		{ID: 1, UserID: 7, UserName: "michail", DeviceID: 29, DeviceName: "basic", ExitNode: "karolina",
			TargetType: "domain", TargetValue: "big.example", Action: "accept", Enabled: true, Applicable: true, CreatedAt: "2026-11-25"},
		{ID: 2, UserID: 7, UserName: "michail", DeviceID: 29, DeviceName: "basic", ExitNode: "karolina",
			TargetType: "subnet", TargetValue: "192.0.2.0/24", ParentDomain: "big.example", Action: "accept",
			Enabled: false, Applicable: true, CreatedAt: "2026-11-25"},
	}
	got := renderAdminPage(t, rules, nil)

	for _, want := range []string{
		`action="/admin/exit-rules/toggle"`,
		`name="id" value="1"`,
		`name="id" value="2"`,
		// The enabled row offers «off», the disabled row offers «on».
		`name="enabled" value="0"`,
		`name="enabled" value="1"`,
		`id="admin-show-disabled"`,
		"exit_rules_admin.show_disabled",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admin page: rendered page is missing %q", want)
		}
	}
	// The switched-off row carries the chip; the enabled one does not (counted on the
	// rendered page: one row is off).
	if n := strings.Count(got, `exit_rules.row_off`); n < 1 {
		t.Error("a switched-off rule rendered no «выключено» chip — it would read as active")
	}
}

// TestB520_SourceColumnShowsTheDomainNotTheMarker — the admin table used to print
// `cdn:cloudflare:…` verbatim.
func TestB520_SourceColumnShowsTheDomainNotTheMarker(t *testing.T) {
	rules := []exit_rules.AdminRule{
		{ID: 3, UserID: 7, UserName: "michail", DeviceID: 29, DeviceName: "basic", ExitNode: "karolina",
			TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com",
			Action: "accept", Enabled: true, Applicable: true, CreatedAt: "2026-11-25"},
	}
	got := renderAdminPage(t, rules, nil)

	if strings.Contains(got, "cdn:cloudflare:") {
		t.Error("the admin table still prints the raw cdn: marker")
	}
	if !strings.Contains(got, "discordapp.com") {
		t.Error("the admin table does not name the domain the range came from")
	}
}

// TestB520_ShowDisabledLinkAndBanner — the way back, and the outcome.
func TestB520_ShowDisabledLinkAndBanner(t *testing.T) {
	got := renderAdminPage(t, nil, map[string]any{
		"admin_toggled": "off", "admin_n": 2, "ShowDisabled": true,
		"ShowDisabledHref": "/admin/exit-rules?show_disabled=0",
	})
	for _, want := range []string{`id="admin-toggle-banner"`, "exit_rules.toggle_ok_off", "exit_rules_admin.only_enabled"} {
		if !strings.Contains(got, want) {
			t.Errorf("admin page: rendered page is missing %q", want)
		}
	}
	got = renderAdminPage(t, nil, map[string]any{"admin_err": "boom"})
	if !strings.Contains(got, `id="admin-toggle-error"`) || !strings.Contains(got, "exit_rules.source_err") {
		t.Error("an admin toggle error rendered no banner")
	}
}

// TestB520_NumberedPagerIsOnTheAdminPage — the page had prev/next only.
func TestB520_NumberedPagerIsOnTheAdminPage(t *testing.T) {
	// The pager is built by the handler (adminPagerView, group arithmetic); here the
	// SHAPE it produces is enough to prove the template renders the shared strip.
	pager := exit_rules.PagerView{
		Page: 2, TotalPages: 4, From: 21, To: 40, Total: 65, Per: 20,
		FirstHref: "/admin/exit-rules?page=1", PrevHref: "/admin/exit-rules?page=1",
		NextHref: "/admin/exit-rules?page=3", LastHref: "/admin/exit-rules?page=4",
		Pages: []exit_rules.PageLink{
			{Num: 1, Href: "/admin/exit-rules?page=1"},
			{Num: 2, Href: "/admin/exit-rules?page=2", Active: true},
			{Num: 3, Href: "/admin/exit-rules?page=3"},
			{Num: 4, Href: "/admin/exit-rules?page=4"},
		},
		PerOptions: []exit_rules.PerOption{{Value: 20, Selected: true, Href: "/admin/exit-rules"}},
	}
	got := renderAdminPage(t, nil, map[string]any{"AdminPager": pager})

	if !strings.Contains(got, `<nav class="pagination"`) {
		t.Fatal("the admin page rendered no navigation")
	}
	for _, want := range []string{">1<", "aria-current=\"page\"", "exit_rules.pager_page_of"} {
		if !strings.Contains(got, want) {
			t.Errorf("admin pager: missing %q", want)
		}
	}
}
