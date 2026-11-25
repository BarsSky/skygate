// rules_view_b516_i18n_test.go — the catalogue side of B516.
//
// The kind and lens chips are rendered with `{{t .KindKey}}`, i.e. with a key held
// in a GO constant, not a literal in the template. That is invisible to the B325
// shell ratchet (which greps templates for `{{t "literal"`), and the parity test
// only compares key SETS — so a typo in the constant, or a key added to one
// catalogue only, would reach the page as a raw `exit_rules.kind_domain` and no
// existing check would notice. This test notices.
package exit_rules

import (
	"testing"
	"unicode"

	"skygate/internal/i18n"
)

// TestB516_KindAndLensKeysResolveInBothLanguages asserts every chip a card can
// render has a real translation in RU and EN.
func TestB516_KindAndLensKeysResolveInBothLanguages(t *testing.T) {
	cat := i18n.New()
	keys := append(RuleKindKeys(), RuleLensKeys()...)
	if len(keys) == 0 {
		t.Fatal("no kind/lens keys to check — the helper that lists them is empty")
	}
	for _, key := range keys {
		for _, lang := range []string{i18n.LangRU, i18n.LangEN} {
			got := cat.T(lang, key)
			if got == key || got == "" {
				t.Errorf("%s: key %q has no %s translation (the page would print the raw key)", lang, key, lang)
				continue
			}
			if lang == i18n.LangRU && !hasCyrillicB516(got) {
				t.Errorf("ru: %q = %q — no Cyrillic, so it is not translated (B325's class)", key, got)
			}
		}
	}
}

// TestB516_EveryLensAndKindIsCovered — the two enumerations the selector and the
// chips are built from must cover the constants. A new lens added without a label
// would silently fall back to the "source" wording.
func TestB516_EveryLensAndKindIsCovered(t *testing.T) {
	if got := len(RuleLensOrder()); got != 4 {
		t.Errorf("lens count = %d, want 4 (groups, source, device, relay)", got)
	}
	seen := map[string]bool{}
	for _, k := range RuleKindKeys() {
		if seen[k] {
			t.Errorf("kind key %q listed twice", k)
		}
		seen[k] = true
	}
	for _, kind := range []string{GroupKindLabel, GroupKindDomain, GroupKindCDN, GroupKindDevice, GroupKindRelay, GroupKindTarget} {
		if !seen[groupKindKey(kind)] {
			t.Errorf("kind %q has no i18n key in RuleKindKeys()", kind)
		}
	}
	// The relay card has NO title (the engine decides) — the template names it.
	// That is a design decision, so pin it: a non-empty title there would replace a
	// translated phrase with a bare word.
	rules := []RuleRow{{ID: 1, DeviceID: 1, DeviceName: "a", TargetType: "ip", TargetValue: "1.1.1.1", Enabled: true}}
	g := BuildRuleGroups(rules, LensRelay)
	if len(g) != 1 || g[0].Title != "" || !g[0].Auto {
		t.Fatalf("auto-relay card = %+v, want an empty title and Auto=true", g)
	}
}

func hasCyrillicB516(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
