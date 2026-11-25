// rules_view_b516_test.go — the contracts of the group model (B516).
//
// These tests are the reason the rework can be trusted with a PAGE whose only
// visible symptom was «правила отображаются в разнобой»: the properties below
// are exactly what the old row-window could not provide.
//
//  1. A DOMAIN AND EVERY ROW RESOLVED FROM IT ARE ONE GROUP — the /32s, the CDN
//     range set and the domain row the operator typed itself. This is the answer
//     to «не понятно к какому домену относилась та или иная подсеть».
//  2. A PAGE IS A PAGE OF GROUPS, so a group is never torn across two pages and a
//     group's count is the database's count, not the page's.
//  3. The ORDER is deterministic for a given row set, so acting on a card does
//     not move every other card.
//  4. DISABLED ROWS are counted and shown, not silently dropped (B517 makes them
//     operable; the model must already be truthful about them).
package exit_rules

import (
	"reflect"
	"testing"
)

// b516Rule builds a row with the fields the grouping reads. Everything else is
// irrelevant to the model, so the fixtures stay readable.
func b516Rule(id int64, device string, devID int, exit, ttype, tvalue string) RuleRow {
	return RuleRow{
		ID:          id,
		DeviceID:    devID,
		DeviceName:  device,
		ExitNode:    exit,
		TargetType:  ttype,
		TargetValue: tvalue,
		Action:      "accept",
		Enabled:     true,
	}
}

// TestB516_DomainAndItsResolvedRowsAreOneGroup is THE regression of the block:
// the operator added rutracker.org and saw the domain row and twelve plausible-
// looking subnets as unrelated destinations.
func TestB516_DomainAndItsResolvedRowsAreOneGroup(t *testing.T) {
	rules := []RuleRow{
		b516Rule(1, "skyworker", 9, "karolina", "domain", "rutracker.org"),
		b516Rule(2, "skyworker", 9, "karolina", "subnet", "104.16.0.0/12"),
		b516Rule(3, "skyworker", 9, "karolina", "subnet", "104.17.0.1/32"),
	}
	rules[1].ParentDomain = "rutracker.org"
	rules[2].ParentDomain = "rutracker.org"

	groups := BuildRuleGroups(rules, LensSource)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (the domain and its resolvable rows are ONE card): %+v", len(groups), groups)
	}
	g := groups[0]
	if g.Title != "rutracker.org" {
		t.Errorf("title = %q, want the domain the operator typed", g.Title)
	}
	if g.Kind != GroupKindDomain {
		t.Errorf("kind = %q, want %q", g.Kind, GroupKindDomain)
	}
	if g.Count != 3 || g.SubnetCount != 2 {
		t.Errorf("count=%d subnets=%d, want 3 rows / 2 subnets (the number the card shows next to the domain)", g.Count, g.SubnetCount)
	}
	// The domain row renders FIRST: it is the parent the others came from.
	if g.Rules[0].TargetType != "domain" {
		t.Errorf("first row = %q, want the domain row first", g.Rules[0].TargetType)
	}
}

// TestB516_CDNRangesShareTheDomainCard — a CDN marker and a plain domain row
// both answer «this is what I added for discordapp.com», so they must not render
// as two cards with the same title.
func TestB516_CDNRangesShareTheDomainCard(t *testing.T) {
	rules := []RuleRow{
		b516Rule(1, "basic", 29, "karolina", "domain", "discordapp.com"),
		b516Rule(2, "basic", 29, "karolina", "subnet", "104.16.0.0/12"),
		b516Rule(3, "basic", 29, "karolina", "subnet", "104.17.0.1/32"),
	}
	rules[1].ParentDomain = "cdn:cloudflare:discordapp.com"
	rules[2].ParentDomain = "cdn:cloudflare:discordapp.com"

	groups := BuildRuleGroups(rules, LensSource)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1: %+v", len(groups), groups)
	}
	if groups[0].Kind != GroupKindCDN || groups[0].Subtitle != "cloudflare" {
		t.Errorf("kind=%q subtitle=%q, want the CDN provenance promoted onto the card", groups[0].Kind, groups[0].Subtitle)
	}
	if groups[0].Count != 3 {
		t.Errorf("count = %d, want 3", groups[0].Count)
	}
}

// TestB516_LabelsComeFirstAndRowlessGroupsWouldBePointless — the operator's own
// names are the first cards, and rows they did not label keep a readable card
// instead of landing in one giant "ungrouped" pile.
func TestB516_LabelsComeFirstAndRowlessGroupsWouldBePointless(t *testing.T) {
	rules := []RuleRow{
		b516Rule(1, "cyborg", 56, "emilia", "subnet", "10.0.0.0/8"),
		b516Rule(2, "cyborg", 56, "emilia", "domain", "youtube.com"),
		b516Rule(3, "cyborg", 56, "emilia", "ip", "1.2.3.4"),
	}
	rules[0].GroupLabel = "Работа"
	rules[1].ParentDomain = "" // the typed domain row itself
	rules[2].GroupLabel = "Работа"

	groups := BuildRuleGroups(rules, LensGroups)
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 (one label card + one source card): %+v", len(groups), groups)
	}
	if groups[0].Kind != GroupKindLabel || groups[0].Title != "Работа" {
		t.Errorf("first card = %q/%q, want the operator's label first", groups[0].Kind, groups[0].Title)
	}
	if groups[0].Count != 2 {
		t.Errorf("label card count = %d, want 2", groups[0].Count)
	}
	if groups[1].Kind != GroupKindDomain || groups[1].Title != "youtube.com" {
		t.Errorf("second card = %q/%q, want the unlabelled domain as its own card", groups[1].Kind, groups[1].Title)
	}
}

// TestB516_LabelFoldingIsTheB380One — «Игры» and «игры» are one card, because the
// key folds and the displayed spelling is the first one stored.
func TestB516_LabelFoldingIsTheB380One(t *testing.T) {
	rules := []RuleRow{
		b516Rule(1, "a", 1, "", "ip", "1.1.1.1"),
		b516Rule(2, "a", 1, "", "ip", "2.2.2.2"),
	}
	rules[0].GroupLabel = "Игры"
	rules[1].GroupLabel = " игры "

	groups := BuildRuleGroups(rules, LensGroups)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1 (case/space are not a second group): %+v", len(groups), groups)
	}
	if groups[0].Title != "Игры" {
		t.Errorf("title = %q, want the first stored spelling", groups[0].Title)
	}
}

// TestB516_DeviceAndRelayLenses — the other two questions the page must answer,
// with the auto-relay rows in their own named card (Title "" + Auto true).
func TestB516_DeviceAndRelayLenses(t *testing.T) {
	rules := []RuleRow{
		b516Rule(1, "skyworker", 9, "karolina", "subnet", "10.1.0.0/16"),
		b516Rule(2, "cyborg", 56, "", "subnet", "10.2.0.0/16"),
		b516Rule(3, "cyborg", 56, "", "subnet", "10.3.0.0/16"),
	}

	byDevice := BuildRuleGroups(rules, LensDevice)
	if len(byDevice) != 2 {
		t.Fatalf("device lens groups = %d, want 2: %+v", len(byDevice), byDevice)
	}
	if byDevice[0].Title != "cyborg" || byDevice[1].Title != "skyworker" {
		t.Errorf("device order = %q,%q; want alphabetical", byDevice[0].Title, byDevice[1].Title)
	}

	byRelay := BuildRuleGroups(rules, LensRelay)
	if len(byRelay) != 2 {
		t.Fatalf("relay lens groups = %d, want 2: %+v", len(byRelay), byRelay)
	}
	// The auto card is the one the engine decides: it must be NAMED by the
	// template (Title is empty on purpose — the wording is i18n's job).
	var auto, named *RuleGroup
	for i := range byRelay {
		if byRelay[i].Auto {
			auto = &byRelay[i]
		} else {
			named = &byRelay[i]
		}
	}
	if auto == nil || auto.Count != 2 {
		t.Fatalf("auto relay card = %+v, want 2 rows", auto)
	}
	if named == nil || named.Title != "karolina" {
		t.Fatalf("named relay card = %+v, want karolina", named)
	}
}

// TestB516_DisabledRowsAreCountedNotDropped — a row that is off still EXISTS, and
// the card has to say so; silently hiding it is how «выключил и оно исчезло»
// becomes indistinguishable from «удалил».
func TestB516_DisabledRowsAreCountedNotDropped(t *testing.T) {
	rules := []RuleRow{
		b516Rule(1, "a", 1, "", "domain", "example.com"),
		b516Rule(2, "a", 1, "", "subnet", "1.0.0.0/8"),
		b516Rule(3, "a", 1, "", "subnet", "2.0.0.0/8"),
	}
	rules[1].ParentDomain = "example.com"
	rules[2].ParentDomain = "example.com"
	rules[2].Enabled = false

	groups := BuildRuleGroups(rules, LensSource)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.EnabledCount != 2 || g.DisabledCount != 1 {
		t.Errorf("enabled=%d disabled=%d, want 2/1", g.EnabledCount, g.DisabledCount)
	}
	if g.AllDevices {
		t.Error("AllDevices must be false unless EVERY row carries the marker")
	}
}

// TestB516_GroupCountIsTheDatabaseCount — the arithmetic the old page got wrong:
// a card's count is its own, and the page summary counts every group.
func TestB516_GroupCountIsTheDatabaseCount(t *testing.T) {
	rules := []RuleRow{}
	id := int64(0)
	for i := 0; i < 45; i++ {
		id++
		r := b516Rule(id, "a", 1, "", "ip", "10.0."+string(rune('0'+i%10))+".1")
		r.ParentDomain = "dom" + itoaB516(i%7) + ".example"
		rules = append(rules, r)
	}
	groups := BuildRuleGroups(rules, LensSource)
	if len(groups) != 7 {
		t.Fatalf("groups = %d, want 7 (one card per source)", len(groups))
	}

	page := WindowRuleGroups(LensSource, groups, 1, 10)
	if page.TotalGroups != 7 || page.TotalPages != 1 {
		t.Fatalf("total groups=%d pages=%d, want 7/1", page.TotalGroups, page.TotalPages)
	}
	if page.TotalRules != 45 {
		t.Errorf("summary rules = %d, want 45 (the whole set, not this page)", page.TotalRules)
	}
	if page.From != 1 || page.To != 7 {
		t.Errorf("from/to = %d/%d, want 1/7", page.From, page.To)
	}
}

// TestB516_PageIsAPageOfGroups — three groups of two rows each, one group per
// page: no page may ever hold half a group.
func TestB516_PageIsAPageOfGroups(t *testing.T) {
	rules := []RuleRow{}
	id := int64(0)
	for i := 0; i < 3; i++ {
		for j := 0; j < 2; j++ {
			id++
			r := b516Rule(id, "a", 1, "", "subnet", "10."+itoaB516(i)+"."+itoaB516(j)+".0/24")
			r.ParentDomain = "dom" + itoaB516(i) + ".example"
			rules = append(rules, r)
		}
	}
	groups := BuildRuleGroups(rules, LensSource)
	for _, per := range []int{10, 1} {
		total := 0
		seen := map[string]int{}
		for p := 1; p <= WindowRuleGroups(LensSource, groups, 1, per).TotalPages; p++ {
			page := WindowRuleGroups(LensSource, groups, p, per)
			for _, g := range page.Groups {
				seen[g.Key]++
				if g.Count != 2 {
					t.Errorf("per=%d page=%d group=%q count=%d, want the full 2 rows (a group is never split)", per, p, g.Key, g.Count)
				}
				total += g.Count
			}
		}
		if total != 6 {
			t.Errorf("per=%d: rows across all pages = %d, want 6", per, total)
		}
		for k, n := range seen {
			if n != 1 {
				t.Errorf("group %q rendered %d times, want exactly once", k, n)
			}
		}
	}
}

// TestB516_DeterministicOrder — the same rows in a different input order must
// produce the same cards in the same order, or acting on one card moves the rest.
func TestB516_DeterministicOrder(t *testing.T) {
	base := []RuleRow{
		b516Rule(1, "a", 1, "", "domain", "zeta.example"),
		b516Rule(2, "a", 1, "", "domain", "alpha.example"),
		b516Rule(3, "b", 2, "", "ip", "9.9.9.9"),
	}
	base[1].GroupLabel = "Работа"

	fwd := BuildRuleGroups(base, LensGroups)
	rev := []RuleRow{base[2], base[1], base[0]}
	back := BuildRuleGroups(rev, LensGroups)

	keys := func(gs []RuleGroup) []string {
		out := make([]string, len(gs))
		for i, g := range gs {
			out[i] = g.Key
		}
		return out
	}
	if !reflect.DeepEqual(keys(fwd), keys(back)) {
		t.Fatalf("order depends on input order: %v vs %v", keys(fwd), keys(back))
	}
}

// TestB516_PageNumbers — the strip the pager renders: first, last, a window
// around the current page, and a gap marker.
func TestB516_PageNumbers(t *testing.T) {
	got := LensPageNumbers(5, 12, 1)
	want := []int{1, -1, 4, 5, 6, -1, 12}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("page numbers = %v, want %v", got, want)
	}
	if got := LensPageNumbers(1, 1, 2); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("single page = %v, want [1]", got)
	}
	if got := LensPageNumbers(3, 3, 2); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Errorf("last page = %v, want 1 2 3 (no orphan ellipsis)", got)
	}
}

// TestB516_ClampAndParse — a hand-typed URL must be clamped, never rejected.
func TestB516_ClampAndParse(t *testing.T) {
	if got := ClampLensPageSize(0); got != 20 {
		t.Errorf("default per = %d, want 20", got)
	}
	if got := ClampLensPageSize(7); got != 10 {
		t.Errorf("per 7 = %d, want 10 (clamped up into the offered sizes)", got)
	}
	if got := ClampLensPageSize(1000); got != 50 {
		t.Errorf("per 1000 = %d, want 50", got)
	}
	if got := ClampGroupRowPageSize(0); got != 50 {
		t.Errorf("default row per = %d, want 50", got)
	}
	// The absent parameter is NOT a lens: the handler falls back to
	// RuleLensDefault, which needs the rows. Reporting not-ok keeps that decision
	// in one place instead of hardcoding a default here.
	if got, ok := ParseRuleLens(""); ok {
		t.Errorf("ParseRuleLens(\"\") = %q, want not-ok (the default needs the rows)", got)
	}
	for in, want := range map[string]RuleLens{
		" groups ": LensGroups, "label": LensGroups,
		"source": LensSource, "domain": LensSource, "target": LensSource,
		"devices": LensDevice, "exit_node": LensRelay, "nonsense": "",
	} {
		got, ok := ParseRuleLens(in)
		if want == "" {
			if ok {
				t.Errorf("ParseRuleLens(%q) = %q, want not-ok", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("ParseRuleLens(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

// TestB516_DefaultLens — a fresh install has no labels and must not open on a
// lens whose only card is the synthetic bucket.
func TestB516_DefaultLens(t *testing.T) {
	if got := RuleLensDefault(nil); got != LensSource {
		t.Errorf("empty = %q, want source", got)
	}
	plain := []RuleRow{b516Rule(1, "a", 1, "", "ip", "1.1.1.1")}
	if got := RuleLensDefault(plain); got != LensSource {
		t.Errorf("no labels = %q, want source", got)
	}
	labelled := []RuleRow{b516Rule(1, "a", 1, "", "ip", "1.1.1.1")}
	labelled[0].GroupLabel = "Игры"
	if got := RuleLensDefault(labelled); got != LensGroups {
		t.Errorf("labels present = %q, want groups", got)
	}
}

// TestB516_FindGroupAndRowWindow — the detail page resolves the key it was
// handed, and a stale key falls back instead of rendering an empty card.
func TestB516_FindGroupAndRowWindow(t *testing.T) {
	rules := []RuleRow{}
	id := int64(0)
	for i := 0; i < 60; i++ {
		id++
		r := b516Rule(id, "a", 1, "", "subnet", "10.0.0."+itoaB516(i))
		r.ParentDomain = "big.example"
		rules = append(rules, r)
	}
	groups := BuildRuleGroups(rules, LensSource)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g, ok := FindRuleGroup(groups, "dom:big.example")
	if !ok {
		t.Fatalf("FindRuleGroup(dom:big.example) not found; keys=%v", groups[0].Key)
	}
	if _, ok := FindRuleGroup(groups, "dom:nope.example"); ok {
		t.Error("a stale key must NOT resolve")
	}
	if _, ok := FindRuleGroup(groups, ""); ok {
		t.Error("an empty key must NOT resolve (it would open a blank card)")
	}

	p1 := WindowRuleGroupRows(LensSource, g, 1, 25)
	if p1.TotalRows != 60 || p1.TotalPages != 3 || len(p1.Group.Rules) != 25 {
		t.Fatalf("page 1 = %d rows in %d pages, first page has %d rows; want 60/3/25", p1.TotalRows, p1.TotalPages, len(p1.Group.Rules))
	}
	if p1.Group.Count != 60 {
		t.Errorf("card count = %d, want the FULL 60 even though the page renders 25", p1.Group.Count)
	}
	p3 := WindowRuleGroupRows(LensSource, g, 3, 25)
	if len(p3.Group.Rules) != 10 || p3.From != 51 || p3.To != 60 {
		t.Errorf("page 3 = %d rows (%d..%d), want 10 rows 51..60", len(p3.Group.Rules), p3.From, p3.To)
	}
}

// itoaB516 avoids pulling strconv into every fixture line.
func itoaB516(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
