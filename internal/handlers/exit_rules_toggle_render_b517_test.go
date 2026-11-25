// exit_rules_toggle_render_b517_test.go — B517 render contracts.
//
// The shell contract proves the endpoint exists and that the model returns the
// right ids; only an EXECUTING render proves the switch is on the page the
// operator sees, carries the state it must set, and comes back to the page they
// were on. The three things pinned here:
//
//  1. every card in the LIST offers one switch for the whole group, with the
//     blast radius (rules + subnets) in its confirm;
//  2. every ROW in the group page offers its own switch, and a disabled row
//     offers the OPPOSITE state (otherwise the button would be a no-op);
//  3. the outcome banner renders from the query string the handler redirected to.
package handlers

import (
	"bytes"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/feature/exit_rules"
)

// TestB517_ListOffersTheGroupSwitch — the card is switchable without opening it.
func TestB517_ListOffersTheGroupSwitch(t *testing.T) {
	got := renderMyRulesLens(t, url.Values{"lens": []string{"source"}}, "")

	for _, want := range []string{
		`action="/my/exit-rules/toggle"`,
		`name="scope" value="group"`,
		`name="lens" value="source"`,
		`name="group" value="dom:rutracker.org"`,
		`name="enabled" value="0"`, // an all-on card offers «выключить»
		`name="enabled" value="1"`, // the card with a switched-off row offers «включить»
		`name="back" value="/my/exit-rules?lens=source"`,
		"exit_rules.toggle_confirm_group", // the blast radius, named before the click
	} {
		if !strings.Contains(got, want) {
			t.Errorf("group list: rendered page is missing %q", want)
		}
	}
}

// TestB517_DetailOffersTheRowSwitchAndTheGroupSwitch — both verbs, on the right state.
func TestB517_DetailOffersTheRowSwitchAndTheGroupSwitch(t *testing.T) {
	got := renderMyRulesLens(t, url.Values{"lens": []string{"source"}}, "dom:rutracker.org")

	for _, want := range []string{
		`name="scope" value="rule"`,
		`id="toggle-rule-1"`, // the domain row
		`id="toggle-rule-2"`, // an enabled subnet
		`id="toggle-rule-3"`, // the DISABLED row: its switch must offer «on»
		`name="scope" value="group"`,
		`id="toggle-group-btn"`,
		"exit_rules.toggle_off_title",
		"exit_rules.toggle_on_title",
		// The card holds one disabled row (id 3), so the group switch offers «on».
		// That is the point of computing the offer from the counts: an all-on card
		// offers «off» and a partially-off one offers «on».
		"exit_rules.toggle_group_on",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("group detail: rendered page is missing %q", want)
		}
	}

	// The disabled row (id 3) must carry enabled=1 — the OPPOSITE state. Checked on
	// the markup that belongs to THAT row, not on the whole page: every enabled
	// row's form carries enabled=0, so a page-wide search would pass either way and
	// prove nothing.
	rowAt := strings.Index(got, `id="toggle-rule-3"`)
	if rowAt < 0 {
		t.Fatal("the disabled row rendered no switch")
	}
	start := strings.LastIndex(got[:rowAt], `action="/my/exit-rules/toggle"`)
	if start < 0 {
		t.Fatal("could not locate the disabled row's form")
	}
	if form := got[start:rowAt]; !strings.Contains(form, `name="enabled" value="1"`) {
		t.Errorf("the disabled row's switch does not offer «on»: %q", form)
	}
}

// TestB517_OutcomeBannerRenders — the operator is told what happened, from the URL.
func TestB517_OutcomeBannerRenders(t *testing.T) {
	tpl := loadExitRulesBody(t, "templates/exit_rules.html")
	render := func(extra map[string]any) string {
		data := myExitRulesBase()
		data["RulesLens"] = exit_rules.BuildRulesLensView(
			"/my/exit-rules", url.Values{"lens": []string{"source"}}, b516Rows(), "source", "")
		data["SelfURL"] = "/my/exit-rules?lens=source"
		data["toggled"] = ""
		data["toggle_n"] = 0
		data["toggle_err"] = ""
		for k, v := range extra {
			data[k] = v
		}
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "body-exit_rules", data); err != nil {
			t.Fatalf("render body-exit_rules: %v", err)
		}
		return buf.String()
	}

	body := render(map[string]any{"toggled": "off", "toggle_n": 3})
	for _, want := range []string{`id="toggle-banner"`, "exit_rules.toggle_ok_off"} {
		if !strings.Contains(body, want) {
			t.Errorf("an «off» outcome rendered no banner (%q)", want)
		}
	}
	if strings.Contains(body, `id="toggle-error"`) {
		t.Error("a successful toggle rendered the error banner")
	}

	body = render(map[string]any{"toggle_err": "boom"})
	if !strings.Contains(body, `id="toggle-error"`) || !strings.Contains(body, "exit_rules.toggle_err") {
		t.Error("a toggle error rendered no banner")
	}
	// The ARGUMENT ("boom") is deliberately NOT asserted here: this harness uses a
	// stub `tf` that returns the key, so the substitution is the real catalogue's
	// job (TestCatalogsParity + the key's own `%s`). What this test owns is that the
	// banner exists and carries the parameterised key at all.
	if strings.Contains(body, `id="toggle-banner"`) {
		t.Error("an error rendered the success banner too")
	}
}
