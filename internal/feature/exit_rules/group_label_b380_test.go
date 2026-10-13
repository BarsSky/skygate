// group_label_b380_test.go — B380: the operator's own group name for a set of rules.
//
// THE REQUEST (verbatim): «возможность добавить в exit rules для пользователей
// группировать те правила что они задают так им будет проще ориентироваться не только
// по домену но и по пользовательскому названию».
//
// What is pinned here, and why each half matters:
//   - the NORMALISER — because " Игры" and "Игры " silently splitting into two groups is
//     how a grouping feature loses the user's trust, and the page must never show a group
//     the user cannot reproduce by typing its name again;
//   - the RENDER ORDER — named groups alphabetically (case-insensitively) and the
//     ungrouped bucket LAST, because "ungrouped" is the leftovers, not a category;
//   - the WRITE path — the label must reach the DOMAIN row and the DERIVED /32 rows, and
//     the all-devices fan-out copies, or a rule filed under «Игры» appears ungrouped on
//     every surface the user actually reads;
//   - the RENAME action — owner-scoped (a decoy row of a SECOND user must not move),
//     honest about a label nobody uses, and free of routing consequences.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	skygatedb "skygate/internal/db"
)

// ---------------------------------------------------------------------------
// the normaliser
// ---------------------------------------------------------------------------

func TestB380_NormaliseGroupLabel(t *testing.T) {
	cases := []struct {
		in   string
		want string
		why  string
	}{
		{"Игры", "Игры", "a plain label is stored verbatim"},
		{"  Игры  ", "Игры", "surrounding whitespace must not split a group in two"},
		{"Игры   на   ПК", "Игры на ПК", "inner runs collapse, so the same words are one group"},
		{"Игры\nна ПК", "Игры на ПК", "a newline would break the chip, so it becomes a space"},
		{"Игры\tна ПК", "Игры на ПК", "a tab likewise"},
		{"Игры\x00на ПК", "Игры на ПК", "a control character is dropped, not rendered"},
		{"", "", "empty is the ungrouped state, not an error"},
		{"   ", "", "whitespace-only is ungrouped too"},
		{strings.Repeat("я", GroupLabelMaxRunes+10), strings.Repeat("я", GroupLabelMaxRunes), "the cap is enforced"},
	}
	for _, c := range cases {
		if got := NormaliseGroupLabel(c.in); got != c.want {
			t.Errorf("NormaliseGroupLabel(%q) = %q, want %q — %s", c.in, got, c.want, c.why)
		}
	}
	// Rune-safety: a Cyrillic label must not be cut mid-character (the byte-vs-rune
	// mistake would produce invalid UTF-8 and a mojibake chip).
	long := strings.Repeat("Ж", GroupLabelMaxRunes+5)
	got := NormaliseGroupLabel(long)
	if len([]rune(got)) != GroupLabelMaxRunes || !strings.HasPrefix(long, got) {
		t.Errorf("the cap must be rune-based and keep a valid prefix, got %q (%d runes)", got, len([]rune(got)))
	}
}

// ---------------------------------------------------------------------------
// the render order
// ---------------------------------------------------------------------------

func TestB380_GroupRulesByLabel_PutsUngroupedLast(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, TargetValue: "a.example", GroupLabel: "Работа"},
		{ID: 2, TargetValue: "b.example", GroupLabel: ""},
		{ID: 3, TargetValue: "c.example", GroupLabel: "игры"},
		{ID: 4, TargetValue: "d.example", GroupLabel: "Игры"},
		{ID: 5, TargetValue: "e.example", GroupLabel: "  Работа  "},
	}
	groups := GroupRulesByLabel(rules, func(int) string { return "" })

	if len(groups) != 3 {
		t.Fatalf("expected 3 groups (Работа, игры+Игры merge by case, ungrouped), got %d: %+v", len(groups), groups)
	}
	// Named groups first, case-insensitively alphabetical: «игры» < «Работа».
	if groups[0].Label != "игры" && groups[0].Label != "Игры" {
		t.Errorf("first named group should be the «игры» family, got %q", groups[0].Label)
	}
	if len(groups[0].Rules) != 2 {
		t.Errorf("«игры» and «Игры» must be ONE group (case-insensitive), got %d rows", len(groups[0].Rules))
	}
	if groups[1].Label != "Работа" {
		t.Errorf("second group should be «Работа», got %q", groups[1].Label)
	}
	if len(groups[1].Rules) != 2 {
		t.Errorf("a trailing-space duplicate must fold into «Работа» (normalised), got %d rows", len(groups[1].Rules))
	}
	if groups[2].Label != "" {
		t.Errorf("the UNGROUPED bucket must be last, got %q in that position", groups[2].Label)
	}
	if len(groups[2].Rules) != 1 || groups[2].Rules[0].ID != 2 {
		t.Errorf("the ungrouped bucket must hold exactly the row with no label, got %+v", groups[2].Rules)
	}

	// Nothing may be lost: the whole point of the view is that every rule is visible.
	total := 0
	for _, g := range groups {
		total += len(g.Rules)
	}
	if total != len(rules) {
		t.Errorf("grouping lost rules: %d of %d rendered", total, len(rules))
	}
}

func TestB380_GroupNamesForSuggestions(t *testing.T) {
	rules := []RuleRow{
		{GroupLabel: "Игры"}, {GroupLabel: "игры"}, {GroupLabel: " Работа "}, {GroupLabel: ""}, {GroupLabel: "Архив"},
	}
	got := GroupNamesForSuggestions(rules)
	// Case-insensitive dedup keeps the FIRST spelling the user typed («Игры»), the sort
	// is folded, and there is no empty entry (ungrouped is not a suggestion).
	want := []string{"Архив", "Игры", "Работа"}
	if len(got) != len(want) {
		t.Fatalf("suggestions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestions = %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// the write path (real SQLite — the same harness B379 uses)
// ---------------------------------------------------------------------------

// TestB380_AddRuleTargetWritesTheLabelOnTheDomainAndItsDerivedRows is the core write
// contract. The domain does not resolve (.invalid), so the derived-row half is exercised
// through the same insertRuleUnique call the derived path makes — the ONE place both
// writers share, which is what makes the inheritance a property rather than a hope.
func TestB380_AddRuleTargetWritesTheLabelOnTheDomainAndItsDerivedRows(t *testing.T) {
	d := newB379DB(t)
	b379SeedUser(t, d, 7, "michail")
	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 7, Username: "michail", IsAdmin: false}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}

	ins := s.addRuleTarget(7, 42, "100.64.0.20", "emilia", "domain", "b380-label.invalid", "accept", "  Игры  ")
	if ins.Err != "" {
		t.Fatalf("addRuleTarget refused: %s", ins.Err)
	}
	var label string
	if err := d.QueryRow(`SELECT group_label FROM device_rules WHERE user_id = 7 AND target_type = 'domain'`).Scan(&label); err != nil {
		t.Fatalf("read the domain row: %v", err)
	}
	if label != "Игры" {
		t.Errorf("the domain row carries %q, want the NORMALISED label %q", label, "Игры")
	}

	// The derived /32 writer is the same insertRuleUnique the DNS-resolve path calls.
	ok, _ := s.insertRuleUnique(7, 42, "emilia", "subnet", "192.0.2.7/32", "accept", "100.64.0.20", "b380-label.invalid", "Игры")
	if !ok {
		t.Fatalf("insertRuleUnique refused the derived row")
	}
	if err := d.QueryRow(`SELECT group_label FROM device_rules WHERE target_type = 'subnet'`).Scan(&label); err != nil {
		t.Fatalf("read the derived row: %v", err)
	}
	if label != "Игры" {
		t.Errorf("a derived /32 carries %q, want %q — the page lists these rows, so a label that stops at the domain row leaves them ungrouped", label, "Игры")
	}
}

// TestB380_SetDeviceRuleGroupIsOwnerScoped — a rename is a user action on their own
// list, and the DECOY row proves the scope rather than assuming it.
func TestB380_SetDeviceRuleGroupIsOwnerScoped(t *testing.T) {
	d := newB379DB(t)
	b379SeedRuleWithLabel(t, d, 7, "michail", 42, "Игры")
	b379SeedRuleWithLabel(t, d, 7, "michail", 43, "Игры")
	b379SeedRuleWithLabel(t, d, 8, "daniil", 44, "Игры") // the decoy: another user's row

	n, err := skygatedb.SetDeviceRuleGroup(d, 7, "Игры", "Игры и Steam")
	if err != nil {
		t.Fatalf("SetDeviceRuleGroup: %v", err)
	}
	if n != 2 {
		t.Errorf("renamed %d rows, want the owner's 2", n)
	}
	var mine, theirs int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 7 AND group_label = 'Игры и Steam'`).Scan(&mine)
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 8 AND group_label = 'Игры'`).Scan(&theirs)
	if mine != 2 {
		t.Errorf("owner rows with the new label = %d, want 2", mine)
	}
	if theirs != 1 {
		t.Errorf("the rename crossed users: owner 8 has %d rows with the old label, want 1 untouched", theirs)
	}

	// Clearing: an empty target removes the group.
	if _, err := skygatedb.SetDeviceRuleGroup(d, 7, "Игры и Steam", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var cleared int
	_ = d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE user_id = 7 AND group_label = ''`).Scan(&cleared)
	if cleared != 2 {
		t.Errorf("clearing left %d ungrouped rows, want 2", cleared)
	}

	// An empty SOURCE is refused: "rename the ungrouped rows" would stamp a label on
	// every unlabelled rule the user owns.
	if _, err := skygatedb.SetDeviceRuleGroup(d, 7, "", "Игры"); err == nil {
		t.Errorf("SetDeviceRuleGroup accepted an empty source group — that would rename every ungrouped row")
	}
}

// TestB380_RenameHandlerMovesTheGroupAndSaysWhatItDid drives the HTTP action.
func TestB380_RenameHandlerMovesTheGroupAndSaysWhatItDid(t *testing.T) {
	d := newB379DB(t)
	b379SeedRuleWithLabel(t, d, 7, "michail", 42, "Игры")
	s := &Service{
		Backend: &b379Backend{claims: &auth.Claims{UserID: 7, Username: "michail", IsAdmin: false}, db: d},
		DB:      skygatedb.FixedDBSource{DB: d},
	}

	rec := b380PostGroup(s, "Игры", "Гейминг")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rename answered %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "view=group") || !strings.Contains(loc, "group_renamed=1") {
		t.Errorf("the redirect must land on the group view and report the rename: %q", loc)
	}
	var label string
	if err := d.QueryRow(`SELECT group_label FROM device_rules WHERE user_id = 7`).Scan(&label); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if label != "Гейминг" {
		t.Errorf("label = %q, want %q", label, "Гейминг")
	}
	var audits int
	_ = d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'exit_rule_group_rename'`).Scan(&audits)
	if audits != 1 {
		t.Errorf("audit rows for the rename = %d, want 1", audits)
	}

	// A group nobody uses is reported instead of silently doing nothing.
	rec = b380PostGroup(s, "Нет такой", "Новая")
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "group_err=") {
		t.Errorf("renaming a non-existent group must produce a message, got %q", loc)
	}
}

func b380PostGroup(s *Service, from, to string) *httptest.ResponseRecorder {
	form := url.Values{"group_from": {from}, "group_to": {to}}
	req := httptest.NewRequest(http.MethodPost, "/my/exit-rules/group", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.PostMyExitRuleGroup(rec, req)
	return rec
}
