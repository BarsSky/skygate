// exit_rules_render_b449_test.go — B449 (2026-10-30).
//
// Two panel-truth items are render assertions, not greps:
//
//  1. the «Все правила» card folds as a whole and the choice is remembered — a 291-rule page
//     with no fold made the operator scroll for a long time to reach the cards below it;
//  2. the preferred-substitution outcome has its OWN banner. Pre-B449 it travelled as `err=`,
//     which the page renders as «Не удалось добавить правило: …» — so a substitution that
//     partially failed told the operator that ADDING a rule failed: the wrong action, and a
//     cause («Ошибка базы данных, попробуйте позже») that no amount of waiting could fix.
package handlers

import (
	"bytes"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// TestAdminAllRulesCardFolds_B449 pins the whole-card fold and its remembered state.
func TestAdminAllRulesCardFolds_B449(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/admin/exit_rules.html")

	data := b448AdminData(map[string]b448UserGroup{})
	data["pref_msg"] = ""

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-admin-exit_rules", data); err != nil {
		t.Fatalf("render body-admin-exit_rules: %v", err)
	}
	got := buf.String()

	for _, want := range []struct{ needle, why string }{
		{`id="admin-all-rules-fold"`, "the «Все правила» card has no fold — the page is a wall of rows again"},
		{`exit_rules_admin.fold_hint`, "the fold has no hint, so it is invisible to the operator"},
		{`skygate.admin.exit_rules.all_open`, "the fold choice is not remembered across reloads"},
	} {
		if !strings.Contains(got, want.needle) {
			t.Errorf("admin page: %s (missing %q)", want.why, want.needle)
		}
	}
}

// TestMyExitRulesPrefBanner_B449 pins the substitution's own banner: present when the flash
// carries an outcome, absent otherwise, and never reusing the «add rule» wording.
func TestMyExitRulesPrefBanner_B449(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")

	base := myExitRulesBase()
	base["pref_msg"] = ""
	base["err"] = ""
	base["warn"] = ""

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", base); err != nil {
		t.Fatalf("render body-exit_rules: %v", err)
	}
	if strings.Contains(buf.String(), `id="pref-apply-banner"`) {
		t.Fatal("the substitution banner is rendered with no outcome to report")
	}

	base["pref_msg"] = "обновлено 12, пропущено 3 — у устройства skyworker уже есть правило для 1.2.3.4/32 на emilia"
	buf.Reset()
	if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", base); err != nil {
		t.Fatalf("render body-exit_rules (with pref): %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, `id="pref-apply-banner"`) {
		t.Fatal("the substitution outcome is not rendered at all")
	}
	if !strings.Contains(got, "exit_rules.pref_apply_result") {
		t.Error("the banner does not use its own i18n key")
	}
	if strings.Contains(got, "exit_rules.form_error") {
		t.Error("the substitution banner reuses the «Не удалось добавить правило» key — the wrong action")
	}
	_ = exit_rules.RuleRow{}
}
