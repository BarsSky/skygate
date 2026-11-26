// exit_rules_admin_lens_b523_test.go — B523 render contracts.
//
// The model test proves the grouping; only an EXECUTING render proves the admin page
// OFFERS it — the selector with the owner pivot, the group list, the numbered pager
// above AND below, the owner column, and the fact that the old tree is not rendered
// at the same time (two lists of the same rules is the «разнобой» the operator
// reported on the user page).
package handlers

import (
	"net/url"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// b523AdminRules is the two-owner fixture the render tests render.
func b523AdminRules() []exit_rules.AdminRule {
	return []exit_rules.AdminRule{
		{ID: 1, UserID: 7, UserName: "michail", DeviceID: 29, DeviceName: "basic", ExitNode: "karolina",
			TargetType: "domain", TargetValue: "rutracker.org", GroupLabel: "Игры", Action: "accept",
			Enabled: true, Applicable: true, CreatedAt: "2026-11-25"},
		{ID: 2, UserID: 7, UserName: "michail", DeviceID: 29, DeviceName: "basic", ExitNode: "karolina",
			TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "rutracker.org",
			Action: "accept", Enabled: false, Applicable: true, CreatedAt: "2026-11-25"},
		{ID: 3, UserID: 9, UserName: "anna", DeviceID: 31, DeviceName: "cyborg", ExitNode: "",
			TargetType: "ip", TargetValue: "198.51.100.7", Action: "accept", Enabled: true,
			Applicable: true, CreatedAt: "2026-11-25"},
	}
}

// renderAdminLensPage renders the admin body with the CARD page switched on.
func renderAdminLensPage(t *testing.T, q url.Values, groupKey string) string {
	t.Helper()
	rules := b523AdminRules()
	lens := exit_rules.BuildRulesLensViewFor("/admin/exit-rules", q,
		adminRowProjection(rules), q.Get("lens"), groupKey, exit_rules.AdminRuleLensOrder())
	lens.Mode = true
	// The DATABASE pager the handler passes (B358: the window is a set of whole
	// (user, device) groups — 65 in the fixture's RulePage, so page 2 of 4). The card
	// list pages by THIS arithmetic, not by the lens groups of the window.
	dbPager := exit_rules.PagerView{
		Page: 2, TotalPages: 4, From: 21, To: 40, Total: 65, Per: 20,
		Pages: []exit_rules.PageLink{
			{Num: 1, Href: "/admin/exit-rules?page=1"},
			{Num: 2, Href: "/admin/exit-rules?page=2", Active: true},
		},
	}
	return renderAdminPage(t, rules, map[string]any{"AdminLens": lens, "AdminPager": dbPager})
}

// adminRowProjection mirrors form_admin.go's projection for the fixture. The
// production projection is covered by the package test; here it only has to carry
// the fields the template reads (id, owner, device, source, state).
func adminRowProjection(rules []exit_rules.AdminRule) []exit_rules.RuleRow {
	out := make([]exit_rules.RuleRow, 0, len(rules))
	for _, a := range rules {
		out = append(out, exit_rules.RuleRow{
			ID: int64(a.ID), UserID: int64(a.UserID), UserName: a.UserName,
			DeviceID: a.DeviceID, DeviceName: a.DeviceName, ExitNode: a.ExitNode,
			TargetType: a.TargetType, TargetValue: a.TargetValue, Action: a.Action,
			ParentDomain: a.ParentDomain, GroupLabel: a.GroupLabel,
			Enabled: a.Enabled,
		})
	}
	return out
}

// TestB523_AdminRendersTheGroupList — the card page: selector, summary, list, pager
// above and below, and NO tree (the two must never render together).
func TestB523_AdminRendersTheGroupList(t *testing.T) {
	q := url.Values{"lens": []string{"source"}}
	got := renderAdminLensPage(t, q, "")

	for _, want := range []string{
		`id="admin-rules-groups"`,       // the card page
		`id="admin-rules-lens-bar"`,     // the grouping selector
		"exit_rules.lens_user",          // the owner pivot IS offered here…
		"exit_rules.lens_source",        // …next to the shared lenses
		"exit_rules_admin.col_user",     // the owner column
		`id="admin-rules-pager-top"`,    // numbers above…
		`id="admin-rules-pager-bottom"`, // …and below
		"rutracker.org",                 // the domain card
		`class="admin-rule-group-row"`,  // one row per group
		"exit_rules.kind_domain",        // its kind chip
		"exit_rules_admin.col_switch",   // the on/off column
		// B358 arithmetic, not the lens window's: the window is a set of whole
		// (user, device) groups, so the page numbers must count the DATABASE.
		"exit_rules_admin.pagination_page_of_groups",
		"exit_rules_admin.pagination_total_groups",
		"/admin/exit-rules?page=2", // the DB pager's link is rendered
		// …and the counters SAY they are the window's.
		"exit_rules_admin.lens_page_summary",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admin card page is missing %q", want)
		}
	}
	for _, unwanted := range []string{
		// The lens-window pager text would quote the window as if it were the table.
		"exit_rules.pager_group_range",
		// The user page's summary wording, which has no «на этой странице» scope.
		"exit_rules.lens_summary<",
	} {
		if strings.Contains(got, unwanted) {
			t.Errorf("admin card page still renders %q — that quotes the window as a total", unwanted)
		}
	}
	if strings.Contains(got, `id="admin-all-rules-fold"`) {
		t.Error("the card page ALSO rendered the tree — two lists of the same rules is the «разнобой» the rework removed")
	}
}

// TestB523_AdminGroupPageRendersTheRows — one group open: its facts, its whole card
// (users included) and the per-row switch.
func TestB523_AdminGroupPageRendersTheRows(t *testing.T) {
	q := url.Values{"lens": []string{"source"}}
	got := renderAdminLensPage(t, q, "dom:rutracker.org")

	for _, want := range []string{
		`id="admin-rules-back"`,   // «все группы»
		`id="admin-group-header"`, // the card's facts
		`id="admin-group-rows"`,   // its rows
		`id="admin-rule-1"`,       // the domain row
		`id="admin-rule-2"`,       // its resolved subnet
		`action="/admin/exit-rules/toggle"`,
		`name="enabled" value="1"`, // the off row offers «on»
		`name="enabled" value="0"`, // the on row offers «off»
		"michail",                  // the owner is visible on every row
		`id="admin-rules-pager-top"`,
		`id="admin-rules-pager-bottom"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("admin group page is missing %q", want)
		}
	}
	if strings.Contains(got, `id="admin-all-rules-fold"`) {
		t.Error("the group page rendered the tree as well")
	}
}

// TestB523_AdminUserLensRendersOwnerCards — the pivot itself.
func TestB523_AdminUserLensRendersOwnerCards(t *testing.T) {
	q := url.Values{"lens": []string{"user"}}
	got := renderAdminLensPage(t, q, "")

	if !strings.Contains(got, `id="admin-group-usr:michail"`) || !strings.Contains(got, `id="admin-group-usr:anna"`) {
		t.Errorf("owner lens did not render one card per owner")
	}
	if !strings.Contains(got, "exit_rules.kind_user") {
		t.Errorf("the owner card's kind chip is missing")
	}
}

// TestB523_LegacyTreeStillRenders — `?view=` keeps the old page, with the same
// flash card (the switch outcome must not go silent on either page).
func TestB523_LegacyTreeStillRenders(t *testing.T) {
	got := renderAdminPage(t, b523AdminRules(), map[string]any{
		"admin_toggled": "off", "admin_n": 2,
		"AdminLens": exit_rules.RulesLensView{}, // Mode=false: the tree
	})
	for _, want := range []string{
		`id="admin-rules-flash"`, // the outcome is rendered outside both layouts
		`id="admin-toggle-banner"`,
		`id="admin-all-rules-fold"`, // the tree
		`id="admin-view-controls"`,  // and its own selector
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the ?view= page is missing %q", want)
		}
	}
	if strings.Contains(got, `id="admin-rules-groups"`) {
		t.Error("the ?view= page ALSO rendered the card page")
	}
}
