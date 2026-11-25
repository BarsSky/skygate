// source_actions_b518_test.go — B518: the verbs a SOURCE card was missing.
//
// The operator's sentence was «если потребуется координально менять правила то
// придется все сносить и задавать заново», so the properties pinned here are about
// changing a domain WITHOUT rebuilding it:
//
//  1. «в группу» files exactly the card's rows under the operator's own label —
//     and a re-added rule could never do that (the natural key would make it a
//     duplicate that changes nothing);
//  2. «переразрешить сейчас» runs the real resolver pass and reports its counts,
//     and refuses a domain whose rule is switched off instead of reporting "0";
//  3. both actions are owner-scoped and refuse a card that is not a source.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

// b518Post runs one card action and returns the redirect target.
func b518Post(t *testing.T, s *Service, path string, form url.Values) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	switch path {
	case "/my/exit-rules/assign-group":
		s.PostMyExitRuleAssignGroup(rec, req)
	case "/my/exit-rules/source-resolve":
		s.PostMyExitRuleSourceResolve(rec, req)
	default:
		t.Fatalf("unknown path %s", path)
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("%s answered %d, want 303; body: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location")
}

// b518GroupLabels reads the labels of the given ids ("" when unset).
func b518GroupLabels(t *testing.T, s *Service, ids ...int) map[int]string {
	t.Helper()
	out := map[int]string{}
	for _, id := range ids {
		var label string
		if err := s.dbc().QueryRow(`SELECT COALESCE(group_label,'') FROM device_rules WHERE id = ?`, id).Scan(&label); err != nil {
			t.Fatalf("read group_label of rule %d: %v", id, err)
		}
		out[id] = label
	}
	return out
}

// TestB518_AssignGroupFilesTheCardAndOnlyTheCard — the «собрать группу из
// источника» verb, on the card the operator is looking at.
func TestB518_AssignGroupFilesTheCardAndOnlyTheCard(t *testing.T) {
	s, d := b517Fixture(t)
	a1 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "a.example", "", 1, 0)
	a2 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.1/32", "a.example", 1, 0)
	b1 := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "b.example", "", 1, 0)

	loc := b518Post(t, s, "/my/exit-rules/assign-group", url.Values{
		"scope": {"group"}, "lens": {"source"}, "group": {"dom:a.example"},
		"group_to": {"Игры"}, "back": {"/my/exit-rules?lens=source"},
	})
	if strings.Contains(loc, "source_err=") {
		t.Fatalf("assign-group refused: %s", loc)
	}
	if !strings.Contains(loc, "group_assigned=2") || !strings.Contains(loc, "assigned_to=") {
		t.Errorf("redirect = %q, want group_assigned=2 and the label", loc)
	}
	labels := b518GroupLabels(t, s, a1, a2, b1)
	if labels[a1] != "Игры" || labels[a2] != "Игры" {
		t.Errorf("card rows = %q/%q, want both filed under «Игры»", labels[a1], labels[a2])
	}
	if labels[b1] != "" {
		t.Errorf("another card's rule got labelled %q — the action must act on the card", labels[b1])
	}
}

// TestB518_AssignGroupWithEmptyLabelClears — the same control removes the group.
func TestB518_AssignGroupWithEmptyLabelClears(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "ip", "198.51.100.4", "", 1, 0)
	if _, err := d.Exec(`UPDATE device_rules SET group_label = 'Работа' WHERE id = ?`, id); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	loc := b518Post(t, s, "/my/exit-rules/assign-group", url.Values{
		"scope": {"group"}, "lens": {"source"}, "group": {"t:ip:198.51.100.4:accept"},
		"group_to": {""}, "back": {"/my/exit-rules"},
	})
	if strings.Contains(loc, "source_err=") {
		t.Fatalf("clearing refused: %s", loc)
	}
	if got := b518GroupLabels(t, s, id)[id]; got != "" {
		t.Errorf("group_label = %q, want cleared", got)
	}
	if !strings.Contains(loc, "group_assigned=1") {
		t.Errorf("redirect = %q, want the count reported", loc)
	}
}

// TestB518_AssignGroupRefusesAForeignSelection — the owner scope.
func TestB518_AssignGroupRefusesAForeignSelection(t *testing.T) {
	s, d := b517Fixture(t)
	foreign := b517InsertRule(t, d, 2, 29, "michail", "karolina", "domain", "other.example", "", 1, 0)

	loc := b518Post(t, s, "/my/exit-rules/assign-group", url.Values{
		"scope": {"ids"}, "ids": {itoaB517(foreign)}, "group_to": {"Игры"}, "back": {"/my/exit-rules"},
	})
	if !strings.Contains(loc, "source_err=") {
		t.Errorf("redirect = %q, want a refusal", loc)
	}
	if got := b518GroupLabels(t, s, foreign)[foreign]; got != "" {
		t.Errorf("a foreign rule was labelled %q", got)
	}
}

// TestB518_ResolveNowRunsThePassAndReportsIt — the real pass, through the seam.
func TestB518_ResolveNowRunsThePassAndReportsIt(t *testing.T) {
	s, d := b517Fixture(t)
	b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "a.example", "", 1, 0)
	b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.1/32", "a.example", 1, 0)
	// The resolver pass is the SERVICE's, not a re-implementation: assert it is the
	// one called, and that its numbers reach the flash.
	called := 0
	s.ResolvePass = func() (int, int, error) { called++; return 3, 1, nil }

	loc := b518Post(t, s, "/my/exit-rules/source-resolve", url.Values{
		"scope": {"group"}, "lens": {"source"}, "group": {"dom:a.example"},
		"back": {"/my/exit-rules?lens=source&group=dom:a.example"},
	})
	if called != 1 {
		t.Fatalf("the resolver pass ran %d time(s), want exactly 1", called)
	}
	if strings.Contains(loc, "source_err=") {
		t.Fatalf("resolve refused: %s", loc)
	}
	for _, want := range []string{"source_resolved=a.example", "source_added=3", "source_removed=1"} {
		if !strings.Contains(loc, want) {
			t.Errorf("redirect = %q, missing %q", loc, want)
		}
	}
	// The gate is cleared, so the next scheduled pass treats the domain as due.
	raw, err := skygatedb.GetGlobalSetting(s.dbc(), SettingDomainResolveAtPrefix+"a.example", "")
	if err != nil {
		t.Fatalf("read resolve stamp: %v", err)
	}
	if raw != "" {
		t.Errorf("resolve stamp = %q, want it cleared", raw)
	}
}

// TestB518_ResolveRefusesADisabledDomainRule — «0 added» would be a lie: the
// resolver only follows ENABLED domain rules, so the reason is named.
func TestB518_ResolveRefusesADisabledDomainRule(t *testing.T) {
	s, d := b517Fixture(t)
	b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "domain", "a.example", "", 0 /* disabled */, 0)
	called := 0
	s.ResolvePass = func() (int, int, error) { called++; return 0, 0, nil }

	loc := b518Post(t, s, "/my/exit-rules/source-resolve", url.Values{
		"scope": {"group"}, "lens": {"source"}, "group": {"dom:a.example"}, "back": {"/my/exit-rules"},
	})
	if !strings.Contains(loc, "source_err=") {
		t.Errorf("redirect = %q, want a named refusal", loc)
	}
	if called != 0 {
		t.Error("the resolver pass ran for a switched-off domain rule (it would do nothing)")
	}
}

// TestB518_ResolveRefusesANonSourceCard — a label or device card has no domain.
func TestB518_ResolveRefusesANonSourceCard(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "ip", "198.51.100.9", "", 1, 0)
	if _, err := d.Exec(`UPDATE device_rules SET group_label = 'Игры' WHERE id = ?`, id); err != nil {
		t.Fatalf("seed label: %v", err)
	}
	called := 0
	s.ResolvePass = func() (int, int, error) { called++; return 0, 0, nil }

	loc := b518Post(t, s, "/my/exit-rules/source-resolve", url.Values{
		"scope": {"group"}, "lens": {"groups"}, "group": {"label:игры"}, "back": {"/my/exit-rules?lens=groups"},
	})
	if !strings.Contains(loc, "source_err=") {
		t.Errorf("redirect = %q, want a refusal", loc)
	}
	if called != 0 {
		t.Error("the resolver pass ran for a label card")
	}
}

// TestB518_CardIDsAreEveryRowUnpaged — the set a card action carries.
func TestB518_CardIDsAreEveryRowUnpaged(t *testing.T) {
	rules := []RuleRow{
		{ID: 1, DeviceID: 9, DeviceName: "a", TargetType: "domain", TargetValue: "big.example", Enabled: true},
	}
	for i := int64(2); i <= 61; i++ {
		rules = append(rules, RuleRow{ID: i, DeviceID: 9, DeviceName: "a", TargetType: "subnet",
			TargetValue: "192.0.2." + itoaB517(int(i)) + "/32", ParentDomain: "big.example", Enabled: true})
	}
	ids, ok := RuleIDsOfGroup(rules, LensSource, "dom:big.example")
	if !ok {
		t.Fatal("the card did not resolve")
	}
	if len(ids) != 61 {
		t.Errorf("card carries %d ids, want 61 — the unpaged set, not one page of it", len(ids))
	}
	if _, ok := RuleIDsOfGroup(rules, LensSource, "dom:gone.example"); ok {
		t.Error("a stale key resolved")
	}
}
