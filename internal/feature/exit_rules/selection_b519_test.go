// selection_b519_test.go — B519: acting on a SELECTION of rows.
//
// The properties pinned here are the ones a multi-select bar can get wrong:
//
//  1. it changes exactly the ticked rows — nothing else on the card, nothing of
//     another user;
//  2. it goes through the SAME writers as the single-row verbs (one label writer,
//     one state-change tail), so the audit/ACL behaviour cannot differ;
//  3. an empty selection and an unknown action are refused with a reason, not
//     silently redirected.
package exit_rules

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// b519Post runs the selection handler and returns the redirect target.
func b519Post(t *testing.T, s *Service, form url.Values) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/my/exit-rules/selection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.PostMyExitRuleSelection(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("selection answered %d, want 303; body: %s", rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location")
}

// TestB519_DisableSelectionSwitchesOnlyTheTickedRows is property 1.
func TestB519_DisableSelectionSwitchesOnlyTheTickedRows(t *testing.T) {
	s, d := b517Fixture(t)
	a := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.1/32", "big.example", 1, 0)
	b := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.2/32", "big.example", 1, 0)
	untouched := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.3/32", "big.example", 1, 0)

	loc := b519Post(t, s, url.Values{
		"op": {"disable"}, "ids": {itoaB517(a), itoaB517(b)},
		"back": {"/my/exit-rules?lens=source&group=dom:big.example"},
	})
	if strings.Contains(loc, "source_err=") {
		t.Fatalf("selection refused: %s", loc)
	}
	if !strings.Contains(loc, "selection_op=off") || !strings.Contains(loc, "selection_n=2") {
		t.Errorf("redirect = %q, want selection_op=off&selection_n=2", loc)
	}
	if !strings.Contains(loc, "group=dom%3Abig.example") {
		t.Errorf("redirect = %q, the operator must land back on the card", loc)
	}
	for _, id := range []int{a, b} {
		if got := b517Enabled(t, d, id); got != 0 {
			t.Errorf("ticked rule %d enabled = %d, want 0", id, got)
		}
	}
	if got := b517Enabled(t, d, untouched); got != 1 {
		t.Errorf("an unticked rule was switched (enabled=%d)", got)
	}
}

// TestB519_EnableSelection — the opposite verb, same writer.
func TestB519_EnableSelection(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.9/32", "big.example", 0, 0)

	loc := b519Post(t, s, url.Values{"op": {"enable"}, "ids": {itoaB517(id)}, "back": {"/my/exit-rules"}})
	if !strings.Contains(loc, "selection_op=on") {
		t.Errorf("redirect = %q, want selection_op=on", loc)
	}
	if got := b517Enabled(t, d, id); got != 1 {
		t.Errorf("enabled = %d, want 1", got)
	}
}

// TestB519_GroupSelectionFilesOnlyTheTickedRows is property 2 for the label writer.
func TestB519_GroupSelectionFilesOnlyTheTickedRows(t *testing.T) {
	s, d := b517Fixture(t)
	a := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.11/32", "big.example", 1, 0)
	b := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.12/32", "big.example", 1, 0)

	loc := b519Post(t, s, url.Values{
		"op": {"group"}, "ids": {itoaB517(a)}, "group_to": {"  Игры  "}, "back": {"/my/exit-rules"},
	})
	if strings.Contains(loc, "source_err=") {
		t.Fatalf("filing refused: %s", loc)
	}
	if !strings.Contains(loc, "group_assigned=1") || !strings.Contains(loc, "assigned_to=") {
		t.Errorf("redirect = %q, want the count and the label", loc)
	}
	labels := b518GroupLabels(t, s, a, b)
	// The label is NORMALISED on the way in (B380): the surrounding spaces the
	// operator typed are not part of the group name.
	if labels[a] != "Игры" {
		t.Errorf("ticked row label = %q, want «Игры» (normalised)", labels[a])
	}
	if labels[b] != "" {
		t.Errorf("an unticked row was filed under %q", labels[b])
	}
}

// TestB519_ForeignIdsAreIgnoredButTheRestStillApplies is the owner scope: a
// crafted id must not reach another account's rule, and it must not abort the
// action the operator actually asked for either.
func TestB519_ForeignIdsAreIgnoredButTheRestStillApplies(t *testing.T) {
	s, d := b517Fixture(t)
	mine := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.21/32", "big.example", 1, 0)
	foreign := b517InsertRule(t, d, 2, 29, "michail", "karolina", "subnet", "192.0.2.22/32", "big.example", 1, 0)

	loc := b519Post(t, s, url.Values{
		"op": {"disable"}, "ids": {itoaB517(mine), itoaB517(foreign)}, "back": {"/my/exit-rules"},
	})
	if strings.Contains(loc, "source_err=") {
		t.Fatalf("the own row should still be switched: %s", loc)
	}
	if got := b517Enabled(t, d, mine); got != 0 {
		t.Errorf("my rule enabled = %d, want 0", got)
	}
	if got := b517Enabled(t, d, foreign); got != 1 {
		t.Errorf("a foreign rule was switched (enabled=%d)", got)
	}
}

// TestB519_EmptyAndUnknownAreRefused — property 3.
func TestB519_EmptyAndUnknownAreRefused(t *testing.T) {
	s, d := b517Fixture(t)
	id := b517InsertRule(t, d, 1, 9, "skyadmin", "karolina", "subnet", "192.0.2.31/32", "big.example", 1, 0)

	loc := b519Post(t, s, url.Values{"op": {"disable"}, "back": {"/my/exit-rules"}})
	if !strings.Contains(loc, "source_err=") {
		t.Errorf("an empty selection = %q, want a refusal", loc)
	}
	loc = b519Post(t, s, url.Values{"op": {"nuke"}, "ids": {itoaB517(id)}, "back": {"/my/exit-rules"}})
	if !strings.Contains(loc, "source_err=") {
		t.Errorf("an unknown op = %q, want a refusal", loc)
	}
	if got := b517Enabled(t, d, id); got != 1 {
		t.Errorf("a refused action changed the rule (enabled=%d)", got)
	}
}
