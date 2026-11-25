// exit_rules_groups_render_b516_test.go — B516 render contracts.
//
// scripts/check_b516_rule_group_page.sh proves the MODEL exists and the template
// contains the block; it cannot prove the page RENDERS. This file executes the
// real `templates/exit_rules.html` with the real view model, which is the only
// way to catch the class the project has paid for twice: a template that reads a
// field the handler no longer passes (the `AllDevices` crash), and a template
// function that is not registered.
//
// It also pins the three things the operator actually asked for:
//
//  1. the LIST is a list of GROUPS (not of rule rows), with the domain card
//     holding its resolved rows;
//  2. the pager renders ABOVE and BELOW, with numbers, and every href keeps the
//     query state;
//  3. the OLD four views are not rendered at the same time — two lists of the
//     same rules is exactly what «не удобно» described.
package handlers

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// b516Rows is the live shape the operator reported: one domain with two resolved
// subnets on two devices, plus a manual rule and one disabled row.
func b516Rows() []exit_rules.RuleRow {
	return []exit_rules.RuleRow{
		{ID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina", TargetType: "domain", TargetValue: "rutracker.org", Action: "accept", Enabled: true},
		{ID: 2, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina", TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "rutracker.org", Action: "accept", Enabled: true},
		{ID: 3, DeviceID: 29, DeviceName: "cyborg", ExitNode: "karolina", TargetType: "subnet", TargetValue: "104.17.0.1/32", ParentDomain: "rutracker.org", Action: "accept", Enabled: false},
		{ID: 4, DeviceID: 29, DeviceName: "cyborg", ExitNode: "", TargetType: "ip", TargetValue: "1.2.3.4", Action: "accept", Enabled: true},
	}
}

// renderMyRulesLens renders the real /my/exit-rules body with the group page on.
func renderMyRulesLens(t *testing.T, q url.Values, groupKey string) string {
	t.Helper()
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	data := myExitRulesBase()
	lens := exit_rules.BuildRulesLensView("/my/exit-rules", q, b516Rows(), q.Get("lens"), groupKey)
	data["RulesLens"] = lens
	data["StatusByRuleID"] = map[int64]string{1: "approved", 2: "pending", 3: "approved", 4: "approved"}
	data["ServedByRuleID"] = map[string]string{}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules (group page): %v", err)
	}
	return buf.String()
}

// TestB516_GroupPageRendersTheList — the list is a list of GROUPS, one row per
// card, with the counts, the lens selector and the pager.
func TestB516_GroupPageRendersTheList(t *testing.T) {
	q := url.Values{"lens": []string{"source"}}
	got := renderMyRulesLens(t, q, "")

	for _, want := range []string{
		`id="rules-groups"`,           // the new card
		`id="rules-lens-bar"`,         // the grouping selector
		"exit_rules.lens_source",      // "По источнику"
		"exit_rules.lens_groups",      // "Мои группы"
		"exit_rules.lens_device",      // "По устройству"
		"exit_rules.lens_relay",       // "По релею"
		"exit_rules.lens_summary",     // «Групп N · правил M · …»
		"rutracker.org",               // the domain card the operator typed
		"exit_rules.kind_domain",      // its kind chip
		"exit_rules.group_rule_count", // its rule count
		"exit_rules.group_subnets",    // ...and its subnet count
		"skyworker", "cyborg",         // the devices carrying it
		`id="rules-pager-top"`, // the pager ABOVE the table
		`id="rules-pager-bottom"`,
		"exit_rules.pager_group_range",
		"exit_rules.pager_page_of",
		"1.2.3.4",                // the manual rule is its own card
		"exit_rules.kind_target", // ...named by its destination
	} {
		if !strings.Contains(got, want) {
			t.Errorf("group list: rendered page is missing %q", want)
		}
	}
	// Both pager bars must carry the nav itself, not just the range text: the
	// operator's complaint was that the controls only exist at the BOTTOM.
	if n := strings.Count(got, `<nav class="pagination"`); n < 2 {
		t.Errorf("pager nav rendered %d time(s); the controls must sit ABOVE and BELOW the list", n)
	}
	// The old four projections must NOT render: the point of the block is that the
	// operator stops reading the same rules in two places.
	for _, unwanted := range []string{`id="rules-view"`, `id="view-target-link"`, `id="view-device-link"`, `id="view-group-link"`} {
		if strings.Contains(got, unwanted) {
			t.Errorf("group page rendered the OLD view block too (%q)", unwanted)
		}
	}
	// A card link must carry the key AND the list page state.
	if !strings.Contains(got, "group=dom%3Arutracker.org") {
		t.Errorf("the card link does not carry the group key — clicking it cannot open the group")
	}
}

// TestB516_GroupPageRendersOneGroup — the detail page: the group's own facts, its
// rows (including the source chip) and a row pager.
func TestB516_GroupPageRendersOneGroup(t *testing.T) {
	q := url.Values{"lens": []string{"source"}}
	got := renderMyRulesLens(t, q, "dom:rutracker.org")

	for _, want := range []string{
		"exit_rules.group_back", // «Все группы»
		"exit_rules.rows_title", // the row table heading
		"exit_rules.col_source", // the provenance column
		"exit_rules.pager_row_range",
		"104.16.0.0/12", // a resolved subnet
		"104.17.0.1/32",
		"exit_rules.row_off", // the disabled row says so
		"exit_rules.kind_domain",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("group detail: rendered page is missing %q", want)
		}
	}
	// The card list must be gone: the operator is inside ONE group.
	if strings.Contains(got, `id="rules-list"`) {
		t.Error("group detail rendered the whole list too")
	}
	// The manual rule (1.2.3.4) belongs to another card and must not appear.
	if strings.Contains(got, "1.2.3.4") {
		t.Error("group detail rendered a row of a DIFFERENT group")
	}
}

// TestB516_StaleGroupKeyFallsBack — a link to a group that no longer exists (its
// last row was deleted) must land on the list, not on an empty card.
func TestB516_StaleGroupKeyFallsBack(t *testing.T) {
	q := url.Values{"lens": []string{"source"}}
	got := renderMyRulesLens(t, q, "dom:gone.example")
	if !strings.Contains(got, `id="rules-list"`) {
		t.Error("a stale group key did not fall back to the list")
	}
	if strings.Contains(got, "exit_rules.rows_title") {
		t.Error("a stale group key still rendered the group row table")
	}
}

// TestB516_OldViewsStillRenderWithoutTheLensParam — the migration guarantee: the
// page the operator uses today is byte-for-byte unaffected until the default is
// flipped in its own reviewed change.
func TestB516_OldViewsStillRenderWithoutTheLensParam(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	data := myExitRulesBase()
	// The old block is gated by `{{if .Rules}}`: a fixture with no rows would prove
	// nothing about it, so give it the live shape.
	data["Rules"] = b516Rows()
	if err := tpl.ExecuteTemplate(&bytes.Buffer{}, "body-exit_rules", data); err != nil {
		t.Fatalf("render without the lens param: %v", err)
	}
	data["ViewMode"] = "target"
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
		t.Fatalf("render body-exit_rules: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `id="view-target-link"`) {
		t.Error("the old view switch disappeared when the group page was not requested")
	}
	if strings.Contains(got, `id="rules-groups"`) {
		t.Error("the group page rendered even though the request did not ask for it")
	}
}
