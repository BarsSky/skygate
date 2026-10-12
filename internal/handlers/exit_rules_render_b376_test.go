package handlers

// exit_rules_render_b376_test.go — B376 (2026-10-11) — the two NEW render paths.
//
// The source contracts in scripts/check_b376_exit_rules_batch_and_target_view.sh
// prove that the view model and the batch form EXIST; they cannot prove the page
// renders. This file executes the real templates with a synthetic context, which is
// what catches the class of defect a grep is blind to: a field the template reads
// but the handler no longer passes (that is exactly how /my/exit-rules once crashed
// with "can't evaluate field AllDevices in type exit_rules.RuleRow"), a template
// function that is not registered, and a branch that renders nothing at all.
//
// Scope: the by-target table (with its device tags and its expander), the batch form
// on BOTH surfaces, the view switch + collapse control, and the admin device picker
// scoped to the selected user.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// loadExitRulesBody parses one exit-rules body with the same minimal funcmap the
// other render tests use (mirrors templates.go, so an unregistered helper fails
// here instead of on the page).
func loadExitRulesBody(t *testing.T, file string) *template.Template {
	t.Helper()
	data, err := templatesFS.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":        func(key string) string { return key },
		"tf":       func(key string, args ...any) string { return key },
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeJSON": func(s string) template.JS {
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
		"usageLevel": func(used, max int) string { return "ok" },
		"divceil":    func(a, b int) int { return (a + b - 1) / b },
	}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return tpl
}

// myExitRulesBase is the minimum context the /my/exit-rules body dereferences
// unconditionally. A missing key here fails the render, which is the point.
func myExitRulesBase() map[string]any {
	return map[string]any{
		"Page":                    "exit-rules",
		"Rules":                   []exit_rules.RuleRow{},
		"DeviceInfos":             []any{},
		"DeviceRoutes":            map[string]any{},
		"ExitNodes":               []any{},
		"DeviceNames":             map[int]string{},
		"Grouped":                 map[int]any{},
		"GroupedByHostname":       map[string]any{},
		"GroupedByHostnameCDN":    map[string]any{},
		"AllDevicesByExitNodeCDN": map[string]any{},
		"RulePage":                struct{ Page, PageSize, Total, TotalGroups int }{Page: 1, PageSize: 50, Total: 0},
		"PageRules":               0,
		"StatusByRuleID":          map[int64]string{},
		"TotalRules":              0,
		"MaxTotalRules":           0,
		"LoadPct":                 0,
		"UserFacingCount":         0,
		"MaxPerUser":              500,
		"MaxPerDevice":            500,
		"MismatchCount":           0,
		"UserPreferred":           "",
		"FormValues":              map[string]string{"device_id": "", "exit_node": "", "target_type": "ip", "target_value": "", "action": "accept"},
		"duplicate":               "",
		"warn":                    "",
		"err":                     "",
		"target":                  "",
		"existing_id":             0,
		"blocking_ip":             "",
		"parent_domain":           "",
		"partial":                 "",
		"HasRoutes":               false,
		"spread":                  "",
		"spread_devices":          0,
		"spread_created":          0,
		"spread_err":              "",
		"no_exit_node_devices":    map[string]bool{},
		"no_exit_node_list":       []string{},
		"device_status":           []any{},
		"AssignedRelay":           "",
		"DeviceFilter":            "",
		"DeviceRuleCount":         0,
		"DeviceIndex":             []any{},
		"ViewMode":                "target",
		"TargetRows":              []exit_rules.TargetRow{},
		"TargetDeviceCount":       0,
		"bulk":                    false,
		"bulk_added":              "",
		"bulk_dup":                "",
		"bulk_skip":               "",
		"bulk_devices":            "",
		"bulk_limit":              false,
		"bulk_err":                "",
	}
}

// TestExitRulesMyRendersByTargetView_B376 is the operator's actual complaint: the
// page must show ONE row per destination with the devices as tags, and the CDN rule
// must be named after the domain the operator typed rather than after its ranges.
func TestExitRulesMyRendersByTargetView_B376(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")

	data := myExitRulesBase()
	data["Rules"] = []exit_rules.RuleRow{
		{ID: 1, DeviceID: 9, ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
		{ID: 2, DeviceID: 29, ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
	}
	data["StatusByRuleID"] = map[int64]string{1: "approved", 2: "pending"}
	data["TargetRows"] = []exit_rules.TargetRow{
		{
			Key: "cdn|cdn:cloudflare:discordapp.com|accept", TargetType: "domain",
			TargetValue: "discordapp.com", Source: "discordapp.com", CDN: "cloudflare",
			IsCDNGroup: true, Action: "accept", Count: 2,
			Rules: []exit_rules.RuleRow{
				{ID: 1, DeviceID: 9, ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
				{ID: 2, DeviceID: 29, ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept"},
			},
			Devices: []exit_rules.TargetDeviceTag{
				{DeviceID: 29, DeviceName: "cyborg", ExitNode: "emilia", RuleCount: 1},
				{DeviceID: 9, DeviceName: "skyworker", ExitNode: "emilia", RuleCount: 1, AllDevices: true},
			},
		},
	}
	data["DeviceInfos"] = []any{
		map[string]any{"ID": "9", "Hostname": "skyworker", "RuleCount": 1, "UserFacing": 1, "HasRoutes": true, "MaxForDevice": 500, "PreferredExitNode": "emilia"},
		map[string]any{"ID": "29", "Hostname": "cyborg", "RuleCount": 1, "UserFacing": 1, "HasRoutes": true, "MaxForDevice": 500, "PreferredExitNode": "emilia"},
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules (target view): %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		`id="rules-view"`,               // the list body the collapse controls drive
		"exit_rules.target_col_devices", // the new column
		"discordapp.com",                // the domain the operator typed, not the CIDR
		"104.16.0.0/12",                 // ...and the ranges are still there, in the expander
		"skyworker",                     // device tags on the row
		"cyborg",
		"emilia",                        // the relay each device egresses through
		"exit_rules.target_cidr_count",  // "N ranges"
		"skyToggleAllDetails",           // collapse / expand all
		`id="view-target-link"`,         // the view switch
		`id="view-device-link"`,         //
		`action="/my/exit-rules/bulk"`,  // the batch form
		`enctype="multipart/form-data"`, // ...with its file input
		`name="bulk_values"`,            //
		`name="bulk_file"`,              //
		`name="bulk_devices"`,           // several devices at once
		`value="all"`,                   // «все мои устройства»
		`value="auto"`,                  // «определять по значению»
		`exit_rules.bulk_form_title`,    // every string goes through i18n
		`exit_rules.target_show_rules`,  //
		`exit_rules.approved_in_headscale_title_short`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("target view: rendered page is missing %q", want)
		}
	}

	// The device TREE must not render in the target view: if both rendered, the
	// operator would scroll past the same rules twice and the "one row per
	// destination" promise would be false.
	if strings.Contains(got, `exit_rules.all_devices_section_title`) {
		t.Error("target view rendered the device tree's all-devices section too — the view switch is not exclusive")
	}
}

// TestExitRulesMyRendersDeviceView_B376: the tree stays one click away and keeps its
// own collapse control, so nothing was removed by the refactor.
func TestExitRulesMyRendersDeviceView_B376(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")

	data := myExitRulesBase()
	data["ViewMode"] = "device"
	data["Rules"] = []exit_rules.RuleRow{{ID: 1, DeviceID: 9, ExitNode: "emilia", TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"}}
	data["TargetRows"] = []exit_rules.TargetRow{{TargetValue: "TARGET-ROW-MUST-NOT-RENDER", TargetType: "subnet", Action: "accept"}}
	data["GroupedByHostname"] = map[string]any{"skyworker": map[string]any{"emilia": []exit_rules.RuleRow{{ID: 1}}}}
	data["GroupedByHostnameCDN"] = map[string]any{"skyworker": map[string]any{"emilia": exit_rules.CDNDisplayView{TotalCount: 1, Items: []exit_rules.CDNDisplayItem{{
		IsCDNGroup: false, Count: 1,
		Rules: []exit_rules.RuleRow{{ID: 1, TargetType: "subnet", TargetValue: "10.0.0.0/8", Action: "accept"}},
	}}}}}
	data["DeviceInfos"] = []any{
		map[string]any{"ID": "9", "Hostname": "skyworker", "RuleCount": 1, "UserFacing": 1, "HasRoutes": true, "MaxForDevice": 500, "PreferredExitNode": "emilia"},
	}
	data["StatusByRuleID"] = map[int64]string{1: "approved"}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules (device view): %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "skyworker") {
		t.Error("device view: the host section did not render")
	}
	if strings.Contains(got, "TARGET-ROW-MUST-NOT-RENDER") {
		t.Error("device view rendered the by-target table too — the view switch is not exclusive")
	}
	if !strings.Contains(got, `action="/my/exit-rules/bulk"`) {
		t.Error("device view: the batch form disappeared with the view switch")
	}
}

// TestExitRulesAdminRendersBatchForm_B376: the admin page gets the same batch form,
// with the device list scoped to the selected user (a device checkbox that belongs
// to another user must not be submittable).
func TestExitRulesAdminRendersBatchForm_B376(t *testing.T) {
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
		"form_user_id": "6", "form_device_id": "", "form_exit_node": "",
		"form_target_type": "", "form_target_value": "", "form_action": "",
		"all_devices":      false,
		"limit_per_device": 500, "limit_per_user": 500, "limit_total": 500,
		"limit_per_device_source": "default", "limit_per_user_source": "default", "limit_total_source": "default",
		"limit_per_device_override": "", "limit_total_override": "",
		"limits_saved": false, "limits_err": "",
		"BulkUsers": []exit_rules.BulkUserOption{
			{ID: 6, Username: "skyadmin", Devices: []exit_rules.BulkDeviceOption{{ID: 9, Name: "skyworker"}, {ID: 29, Name: "cyborg"}}},
			{ID: 7, Username: "michail", Devices: []exit_rules.BulkDeviceOption{{ID: 55, Name: "basic"}}},
		},
		"bulk": false, "bulk_added": "", "bulk_dup": "", "bulk_skip": "",
		"bulk_devices": "", "bulk_limit": false, "bulk_err": "",
		"ViewMode": "device", "TargetRowsAdmin": []exit_rules.TargetRowAdmin{}, "TargetDevicesAdmin": 0,
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules", data); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		`action="/admin/exit-rules/bulk"`,
		`id="bulk-user-select"`,
		`name="bulk_user_id"`,
		`data-user="6"`,               // skyadmin's devices
		`data-user="7"`,               // michail's devices
		`> skyworker`,                 // the device checkbox labels
		`> basic`,                     //
		`skyBulkUserChanged`,          // the JS that hides the other user's boxes
		`skyToggleAllDetails`,         // collapse / expand all
		`id="admin-rules-view"`,       //
		`exit_rules_admin.bulk_title`, // i18n, not a hardcoded string
		`name="bulk_values"`,          //
		`name="bulk_file"`,            // the JSON/CSV import
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admin page: rendered page is missing %q", want)
		}
	}
	// The preselect follows the redirect's form_user_id, so a refused batch does
	// not silently retarget another user.
	if !strings.Contains(got, `value="6" selected`) {
		t.Error("admin page: the bulk user select did not preselect form_user_id=6")
	}
}

// TestExitRulesAdminRendersByTargetView_B376: the cross-user by-target view. The
// point of the page is "who else routes through this destination", so two users'
// devices must land in ONE row and each tag must name its owner.
func TestExitRulesAdminRendersByTargetView_B376(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")

	data := map[string]any{
		"Page": "exit-rules", "Title": "Exit Rules",
		"Rules": []any{}, "Logs": []any{}, "Snapshots": []any{},
		"GroupedByUser": map[string]any{},
		"TotalRules":    2, "MaxTotalRules": 0, "LoadPct": 0, "ShownRules": 2,
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
		"ViewMode": "target", "TargetDevicesAdmin": 2,
		"TargetRowsAdmin": []exit_rules.TargetRowAdmin{{
			Key: "cdn|cdn:cloudflare:discordapp.com|accept", TargetType: "domain",
			TargetValue: "discordapp.com", Source: "discordapp.com", CDN: "cloudflare",
			IsCDNGroup: true, Action: "accept", Count: 2,
			Rules: []exit_rules.AdminRule{
				{ID: 1, UserID: 6, UserName: "skyadmin", DeviceID: 9, DeviceName: "skyworker", ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept", Applicable: true, ApprovedInHeadscale: true, CreatedAt: "2026-10-11"},
				{ID: 2, UserID: 7, UserName: "michail", DeviceID: 55, DeviceName: "basic", ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "cdn:cloudflare:discordapp.com", Action: "accept", Applicable: false, PreferredHost: "karolina", CreatedAt: "2026-10-11"},
			},
			Devices: []exit_rules.TargetDeviceTag{
				{DeviceID: 55, DeviceName: "michail/basic", ExitNode: "emilia", RuleCount: 1},
				{DeviceID: 9, DeviceName: "skyadmin/skyworker", ExitNode: "emilia", RuleCount: 1},
			},
		}},
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules", data); err != nil {
		t.Fatalf("render body-admin-exit_rules (target view): %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		"discordapp.com",                      // the domain, not the CIDR
		"michail/basic",                       // the OWNER is part of the tag
		"skyadmin/skyworker",                  //
		"104.16.0.0/12",                       // inner rows in the expander
		`id="admin-view-target-link"`,         // the view switch
		"exit_rules_admin.target_delete_hint", // deletion is pointed at the tree
		"exit_rules.target_cidr_count",        //
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admin target view: rendered page is missing %q", want)
		}
	}
	// The tree must not render at the same time: two lists of the same rules is
	// exactly what the operator called inconvenient.
	if strings.Contains(got, "exit_rules_admin.device_index_title") {
		t.Error("admin target view rendered the device inventory/tree too — the switch is not exclusive")
	}
}
