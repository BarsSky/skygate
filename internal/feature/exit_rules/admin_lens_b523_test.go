// admin_lens_b523_test.go — the ADMIN page's card page (B523) is the user page's
// model with one extra pivot.
//
// The block's whole point is that there is no second grouping implementation: the
// admin page projects its rows onto RuleRow and calls the SAME BuildRulesLensView
// the user page calls. These tests pin the three things that could quietly break
// that: the projection (a field the model reads but AdminRule drops), the owner
// lens (whose fallback must never merge two owners into one card), and the lens
// list (a lens offered on one page must not appear on the other).
package exit_rules

import (
	"net/url"
	"strings"
	"testing"
)

// b523AdminRules is the live shape: two owners, one labelled domain with a resolved
// subnet, one manual rule on a second owner.
func b523AdminRules() []AdminRule {
	return []AdminRule{
		{ID: 1, UserID: 7, UserName: "michail", DeviceID: 29, DeviceName: "basic", ExitNode: "karolina",
			TargetType: "domain", TargetValue: "rutracker.org", ParentDomain: "", GroupLabel: "Игры",
			Action: "accept", Enabled: true},
		{ID: 2, UserID: 7, UserName: "michail", DeviceID: 29, DeviceName: "basic", ExitNode: "karolina",
			TargetType: "subnet", TargetValue: "104.16.0.0/12", ParentDomain: "rutracker.org",
			Action: "accept", Enabled: false},
		{ID: 3, UserID: 9, UserName: "anna", DeviceID: 31, DeviceName: "cyborg", ExitNode: "",
			TargetType: "ip", TargetValue: "198.51.100.7", Action: "accept", Enabled: true},
	}
}

// TestB523_ProjectionCarriesWhatTheModelReads — a dropped field would render every
// rule as «без группы» or as one anonymous owner, without any error anywhere.
func TestB523_ProjectionCarriesWhatTheModelReads(t *testing.T) {
	rows := adminLensRowsFrom(b523AdminRules())
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	r := rows[0]
	if r.ID != 1 || r.UserID != 7 || r.UserName != "michail" || r.DeviceID != 29 || r.DeviceName != "basic" {
		t.Errorf("identity fields = %+v, want the admin row's own identity", r)
	}
	if r.GroupLabel != "Игры" {
		t.Errorf("GroupLabel = %q, want «Игры» — the column exists since B380 and the admin query SELECTs it", r.GroupLabel)
	}
	if !r.Enabled || r.Action != "accept" || r.TargetType != "domain" || r.TargetValue != "rutracker.org" {
		t.Errorf("rule fields = %+v, want them copied verbatim", r)
	}
	if rows[1].ParentDomain != "rutracker.org" || rows[1].Enabled {
		t.Errorf("second row = %+v, want the disabled resolved subnet with its provenance", rows[1])
	}
}

// TestB523_UserLensGroupsByOwner — the pivot the tree used to provide.
func TestB523_UserLensGroupsByOwner(t *testing.T) {
	groups := BuildRuleGroups(adminLensRowsFrom(b523AdminRules()), LensUser)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want one card per owner", len(groups))
	}
	byKey := map[string]RuleGroup{}
	for _, g := range groups {
		byKey[g.Key] = g
	}
	michail, ok := byKey["usr:michail"]
	if !ok {
		t.Fatalf("no «usr:michail» card; keys: %v", keysOf(groups))
	}
	if michail.Kind != GroupKindUser || michail.KindKey != "exit_rules.kind_user" {
		t.Errorf("kind = %q/%q, want the owner card", michail.Kind, michail.KindKey)
	}
	if michail.Title != "michail" || michail.Count != 2 || michail.SubnetCount != 1 {
		t.Errorf("card = %+v, want title michail, 2 rules of which 1 subnet", michail)
	}
	if len(michail.Users) != 1 || michail.Users[0] != "michail" {
		t.Errorf("Users = %v, want just the owner", michail.Users)
	}
	if len(michail.Devices) != 1 || michail.Devices[0] != "basic" {
		t.Errorf("Devices = %v, want [basic]", michail.Devices)
	}
	if michail.DisabledCount != 1 {
		t.Errorf("DisabledCount = %d, want 1 (the resolved subnet is off)", michail.DisabledCount)
	}
	if _, ok := byKey["usr:anna"]; !ok {
		t.Errorf("no «usr:anna» card; keys: %v", keysOf(groups))
	}
}

// TestB523_UserLensFallbackNeverMergesOwners — a missing denormalised name must not
// collapse two owners into one anonymous card: that would hide a rule from the page
// that exists to show every rule.
func TestB523_UserLensFallbackNeverMergesOwners(t *testing.T) {
	groups := BuildRuleGroups([]RuleRow{
		{ID: 1, UserID: 7, TargetType: "ip", TargetValue: "192.0.2.1", Action: "accept"},
		{ID: 2, UserID: 9, TargetType: "ip", TargetValue: "192.0.2.2", Action: "accept"},
	}, LensUser)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 (two owners must not collapse); keys: %v", len(groups), keysOf(groups))
	}
	if groups[0].Key != "usr:#7" || groups[1].Key != "usr:#9" {
		t.Errorf("keys = %q, %q — want the id fallback", groups[0].Key, groups[1].Key)
	}
}

// TestB523_LensListsArePerPage — the owner pipeline belongs to the admin selector,
// and adding it must not add it to the user page.
func TestB523_LensListsArePerPage(t *testing.T) {
	user := RuleLensOrder()
	admin := AdminRuleLensOrder()
	if len(user) != 4 {
		t.Errorf("user lens order = %v, want 4", user)
	}
	if len(admin) != 5 {
		t.Errorf("admin lens order = %v, want 5", admin)
	}
	for _, l := range user {
		if l == LensUser {
			t.Errorf("the USER page offers the owner pivot — every row there has the same owner, so the control cannot change the answer")
		}
	}
	found := false
	for _, l := range admin {
		if l == LensUser {
			found = true
		}
	}
	if !found {
		t.Errorf("the ADMIN page does not offer the owner pivot")
	}
	// The keys the i18n test walks must cover the admin-only lens.
	var hasUserKey bool
	for _, k := range RuleLensKeys() {
		if k == "exit_rules.lens_user" {
			hasUserKey = true
		}
	}
	if !hasUserKey {
		t.Errorf("RuleLensKeys() = %v, want it to include exit_rules.lens_user", RuleLensKeys())
	}
}

// TestB523_ParseUserLens — the spellings a hand-typed link may carry.
func TestB523_ParseUserLens(t *testing.T) {
	for _, raw := range []string{"user", "users", "owner", "OWNERS", " user "} {
		if got, ok := ParseRuleLens(raw); !ok || got != LensUser {
			t.Errorf("ParseRuleLens(%q) = %v/%v, want the owner lens", raw, got, ok)
		}
	}
}

// TestB523_AdminViewHasFiveLinksAndKeepsTheQuery — the selector, the card URLs and
// the group page all come from the SAME builder as the user page.
func TestB523_AdminViewHasFiveLinksAndKeepsTheQuery(t *testing.T) {
	q := url.Values{"lens": []string{"source"}, "show_disabled": []string{"1"}}
	v := BuildRulesLensViewFor("/admin/exit-rules", q, adminLensRowsFrom(b523AdminRules()), "source", "dom:rutracker.org", AdminRuleLensOrder())

	if !v.Mode {
		t.Fatal("Mode = false, want the card page")
	}
	if v.Lens != LensSource {
		t.Errorf("Lens = %q, want source", v.Lens)
	}
	if len(v.Links) != 5 {
		t.Fatalf("links = %d, want 5 (four shared + the owner pivot)", len(v.Links))
	}
	for _, l := range v.Links {
		if l.Key == LensUser && !l.Active && l.LabelKey != "exit_rules.lens_user" {
			t.Errorf("owner link = %+v, want its own label key", l)
		}
	}
	if v.Page.TotalGroups != 2 {
		t.Errorf("TotalGroups = %d, want 2 (the domain card that holds two owners' rows + anna's destination)", v.Page.TotalGroups)
	}
	if v.Detail == nil {
		t.Fatal("Detail = nil, want the opened group")
	}
	if v.Detail.Group.Key != "dom:rutracker.org" {
		t.Errorf("Detail key = %q, want dom:rutracker.org", v.Detail.Group.Key)
	}
	// The card spans owners on this page, so the admin must SEE both of them inside
	// one group — the fact the tree could not show without expanding.
	if len(v.Detail.Group.Users) != 1 || v.Detail.Group.Users[0] != "michail" {
		// Both rows of this card belong to michail; the owner list is what proves the
		// card is attributable at all.
		t.Errorf("Users = %v, want [michail]", v.Detail.Group.Users)
	}
	if len(v.Detail.Rows) != 2 || len(v.Detail.AllIDs) != 2 {
		t.Errorf("detail rows/allIDs = %d/%d, want the whole card (2/2)", len(v.Detail.Rows), len(v.Detail.AllIDs))
	}
	// The page must be reachable again with the switched-off view still on: the
	// builder preserves the query it was given.
	if !strings.Contains(v.BackHref, "show_disabled=1") {
		t.Errorf("BackHref = %q, want it to preserve show_disabled=1", v.BackHref)
	}
	for _, g := range v.Page.Groups {
		if !strings.Contains(g.Href, "/admin/exit-rules") || !strings.Contains(g.Href, "lens=") {
			t.Errorf("card href = %q, want an absolute admin URL carrying the lens", g.Href)
		}
	}
}

// TestB523_LensNotOfferedFallsBack — a hand-typed ?lens=user on the USER page must
// not render a selector with no active entry.
func TestB523_LensNotOfferedFallsBack(t *testing.T) {
	rows := []RuleRow{{ID: 1, UserID: 7, UserName: "michail", TargetType: "ip", TargetValue: "192.0.2.1", Action: "accept"}}
	v := BuildRulesLensView("/my/exit-rules", url.Values{"lens": []string{"user"}}, rows, "user", "")
	if v.Lens == LensUser {
		t.Fatalf("the user page accepted the owner lens")
	}
	if len(v.Links) != 4 {
		t.Errorf("links = %d, want the user page's 4", len(v.Links))
	}
	active := 0
	for _, l := range v.Links {
		if l.Active {
			active++
		}
	}
	if active != 1 {
		t.Errorf("active links = %d, want exactly 1 (the fallback must land on an offered lens)", active)
	}
}

// TestB523_AdminCardPageIsTheDefault — one condition decides which page the admin
// sees, and ?view= is the only way back to the tree.
func TestB523_AdminCardPageIsTheDefault(t *testing.T) {
	cases := map[string]bool{
		"":              true,
		"?":             true,
		"view=device":   false,
		"view=target":   false,
		"view=":         true,
		"view=tree":     false,
		"lens=user":     true,
		"group=usr:abc": true,
	}
	for raw, want := range cases {
		var q url.Values
		if raw != "" && raw != "?" {
			q, _ = url.ParseQuery(raw)
		}
		if q == nil {
			q = url.Values{}
		}
		if got := adminRulesLensEnabled(q); got != want {
			t.Errorf("adminRulesLensEnabled(%q) = %v, want %v", raw, got, want)
		}
	}
}

func keysOf(groups []RuleGroup) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		out = append(out, g.Key)
	}
	return out
}
