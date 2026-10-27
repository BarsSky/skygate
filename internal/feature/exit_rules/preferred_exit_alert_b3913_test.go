// B391.3 — the last eight English-only alerts, pinned as composed alerts.
//
// The two properties that matter, and that a translation alone would not give:
//
//  1. every variant carries the SAME five facts (action, device, user, pin, reason) plus
//     the one fact specific to it — so the operator reads one shape, not seven;
//  2. the text is Russian and the dedup key identifies the INCIDENT (action + device +
//     pin), so a state that persists across ticks is one alert per window, not one per
//     pass.
package exit_rules

import (
	"strings"
	"testing"
	"unicode"

	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

func b3913Catalog(t *testing.T) {
	t.Helper()
	if i18n.GlobalCatalog == nil {
		i18n.GlobalCatalog = i18n.New()
	}
}

// hasCyrillic is the guard against "translated" by copying the English value into the RU
// map — the exact hole B325.1 identified (the parity test compares key SETS only).
func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

func factValue(a telegram.Alert, label string) (string, bool) {
	for _, f := range a.Facts {
		if f.Label == label {
			return f.Value, true
		}
	}
	return "", false
}

func TestB3913_EveryVariantCarriesTheFiveCoreFacts(t *testing.T) {
	b3913Catalog(t)
	lang := i18n.LangRU
	cases := []struct {
		action   string
		in       PreferredExitAlertInput
		severity telegram.Severity
	}{
		{PrefActionCreate, PreferredExitAlertInput{Hostname: "laptop", Username: "michail", NewTag: "tag:dev-infra-x", Reason: "rule-pref", Rules: 3, UserID: 7, ViaEnabled: true}, telegram.SeverityInfo},
		{PrefActionUpdate, PreferredExitAlertInput{Hostname: "laptop", Username: "michail", OldTag: "a", NewTag: "b", Reason: "relay-died", UserID: 7}, telegram.SeverityInfo},
		{PrefActionClear, PreferredExitAlertInput{Hostname: "laptop", Username: "michail", OldTag: "tag:public", Reason: "class-tag"}, telegram.SeverityInfo},
		{PrefActionMigrate, PreferredExitAlertInput{Hostname: "old", NewHostname: "new", Username: "michail", NewTag: "tag:dev-infra-x", Reason: "hostname-rename", Rules: 2, UserID: 7}, telegram.SeverityInfo},
		{PrefActionAmbiguous, PreferredExitAlertInput{Hostname: "laptop", Username: "michail", NewTag: "tag:dev-infra-x", Reason: "several", Candidates: "a, b"}, telegram.SeverityWarn},
		{PrefActionOrphan, PreferredExitAlertInput{Hostname: "gone", Username: "michail", NewTag: "tag:dev-infra-x", Reason: "no device", UserID: 7}, telegram.SeverityWarn},
		{PrefActionStale, PreferredExitAlertInput{Hostname: "laptop", Username: "michail", OldTag: "tag:dev-infra-x", NewTag: "tag:dev-infra-y", Reason: "stale-pref-relay-down", RelayState: "down", UserID: 7}, telegram.SeverityWarn},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			c.in.Action = c.action
			a := BuildPreferredExitAlert(c.in)
			if a.Lang != lang {
				t.Fatalf("Lang = %q, want %q", a.Lang, lang)
			}
			if a.TitleKey == "" {
				t.Fatal("no TitleKey — the title would fall back to a literal")
			}
			if a.Severity != c.severity {
				t.Fatalf("Severity = %v, want %v (a warning for bookkeeping teaches an operator to ignore warnings)", a.Severity, c.severity)
			}
			for _, k := range []string{
				"alert.preferred_exit.fact_action",
				"alert.preferred_exit.fact_device",
				"alert.preferred_exit.fact_user",
				"alert.preferred_exit.fact_pin",
				"alert.preferred_exit.fact_reason",
			} {
				label := i18n.T(lang, k)
				if v, ok := factValue(a, label); !ok || v == "" {
					t.Fatalf("fact %q missing or empty (facts=%v)", k, a.Facts)
				}
			}
			// The action value must be a REAL translation, not the key and not English.
			actionValue, _ := factValue(a, i18n.T(lang, "alert.preferred_exit.fact_action"))
			if !hasCyrillic(actionValue) {
				t.Fatalf("action value %q carries no Cyrillic — the RU catalogue is English here", actionValue)
			}
			if !strings.HasPrefix(a.DedupKey, "preferred_exit."+c.action+".") {
				t.Fatalf("DedupKey = %q, want the preferred_exit.<action>.<device> shape", a.DedupKey)
			}
			if a.DedupTTL <= 0 {
				t.Fatal("DedupTTL is zero — the dedup window would never suppress anything")
			}
		})
	}
}

func TestB3913_StaleNamesTheRelayAndTheHuman(t *testing.T) {
	b3913Catalog(t)
	lang := i18n.LangRU

	// A human-set preference: the engine refuses to rewrite it, and the message must say
	// so — 64 silent skips in a row is what made the operator file this family.
	a := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionStale, Hostname: "laptop", Username: "michail",
		OldTag: "tag:dev-infra-x", NewTag: "tag:dev-infra-y",
		Reason: "stale-pref-relay-down", RelayState: "down",
		UserID: 7, SetByUserID: 42,
	})
	if a.TitleKey != "alert.preferred_exit.title_stale" {
		t.Fatalf("TitleKey = %q, want the stale family", a.TitleKey)
	}
	if v, ok := factValue(a, i18n.T(lang, "alert.preferred_exit.fact_relay")); !ok || v != "down" {
		t.Fatalf("relay-state fact = %q (present=%v), want down", v, ok)
	}
	if !strings.Contains(a.Details, "42") {
		t.Fatalf("details do not name set_by_user_id=42: %q", a.Details)
	}
	if !strings.Contains(a.Details, "/admin/exit-nodes") {
		t.Fatalf("details do not say where to fix it: %q", a.Details)
	}
	if !hasCyrillic(a.Details) {
		t.Fatalf("details are not translated: %q", a.Details)
	}

	// Not human-set: the sentence about a human must NOT appear.
	b := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionStale, Hostname: "laptop", Username: "michail",
		OldTag: "tag:dev-infra-x", Reason: "stale-pref-relay-down", RelayState: "down",
	})
	if strings.Contains(b.Details, i18n.T(lang, "alert.preferred_exit.detail_human")[:20]) && strings.Contains(b.Details, "set_by_user_id") {
		t.Fatalf("a non-human preference carries the human note: %q", b.Details)
	}
}

func TestB3913_OrphanAndAmbiguousAreActionable(t *testing.T) {
	b3913Catalog(t)
	lang := i18n.LangRU

	orphan := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionOrphan, Hostname: "gone", Username: "michail",
		NewTag: "tag:dev-infra-x", Reason: "no device", UserID: 7,
	})
	if !strings.Contains(orphan.Details, "DELETE FROM device_exit_node_prefs") {
		t.Fatalf("the orphan details lose the cleanup statement: %q", orphan.Details)
	}
	if !strings.Contains(orphan.Details, "7") || !strings.Contains(orphan.Details, "gone") {
		t.Fatalf("the orphan SQL hint does not name the row (user_id/hostname): %q", orphan.Details)
	}

	amb := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionAmbiguous, Hostname: "laptop", Username: "michail",
		NewTag: "tag:dev-infra-x", Reason: "several", Candidates: "a, b",
	})
	if v, ok := factValue(amb, i18n.T(lang, "alert.preferred_exit.fact_candidates")); !ok || v != "a, b" {
		t.Fatalf("candidates fact = %q (present=%v), want 'a, b'", v, ok)
	}
	if !strings.Contains(amb.Details, "/my/devices") {
		t.Fatalf("the ambiguous details do not say where to resolve it: %q", amb.Details)
	}
	if amb.TitleKey != "alert.preferred_exit.title_review" {
		t.Fatalf("TitleKey = %q, want the manual-decision family", amb.TitleKey)
	}
}

func TestB3913_MigrateShowsBothHostnames(t *testing.T) {
	b3913Catalog(t)
	lang := i18n.LangRU
	a := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionMigrate, Hostname: "cyborg", NewHostname: "cyborg-v2",
		Username: "michail", NewTag: "tag:dev-infra-x", Reason: "hostname-rename", Rules: 2,
	})
	device, _ := factValue(a, i18n.T(lang, "alert.preferred_exit.fact_device"))
	if device != "cyborg → cyborg-v2" {
		t.Fatalf("device fact = %q, want 'cyborg → cyborg-v2' (the rename PAIR is the incident)", device)
	}
	if v, ok := factValue(a, i18n.T(lang, "alert.preferred_exit.fact_rules")); !ok || v != "2" {
		t.Fatalf("rules fact = %q (present=%v), want 2", v, ok)
	}
}

func TestB3913_ComposedTextIsRussianAndEscaped(t *testing.T) {
	b3913Catalog(t)
	a := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionUpdate, Hostname: `lab<script>`, Username: "michail",
		OldTag: "tag:dev-infra-a", NewTag: "tag:dev-infra-b",
		Reason: "relay-died", UserID: 7,
	})
	out := telegram.ComposeAlert(a)
	if !hasCyrillic(out) {
		t.Fatalf("the composed message carries no Cyrillic:\n%s", out)
	}
	if strings.Contains(out, "<script>") {
		t.Fatalf("the device name was not escaped:\n%s", out)
	}
	if strings.Contains(out, "preferred-exit reconciled (B229)") {
		t.Fatalf("the old English sentence is still in the message:\n%s", out)
	}
	if len([]rune(out)) > 3500 {
		t.Fatalf("the composed message exceeds the composer budget: %d runes", len([]rune(out)))
	}
}

func TestB3913_PlainFallbackKeepsTheSameIncident(t *testing.T) {
	b3913Catalog(t)
	in := PreferredExitAlertInput{
		Action: PrefActionUpdate, Hostname: "laptop", Username: "michail",
		OldTag: "tag:dev-infra-a", NewTag: "tag:dev-infra-b", Reason: "relay-died",
	}
	plain := preferredExitPlainFallback(in)
	if !hasCyrillic(plain) {
		t.Fatalf("the plain fallback is not translated: %q", plain)
	}
	if !strings.Contains(plain, "laptop") || !strings.Contains(plain, "tag:dev-infra-a → tag:dev-infra-b") {
		t.Fatalf("the plain fallback loses the device or the pin transition: %q", plain)
	}
}

func TestB3913_DiscoveryAlertIsComposedAndDeduped(t *testing.T) {
	b3913Catalog(t)
	lang := i18n.LangRU
	a := BuildDiscoveryAlert(3, "tag:dev")
	if a.TitleKey != "alert.discovery_new_nodes.title" {
		t.Fatalf("TitleKey = %q", a.TitleKey)
	}
	if v, ok := factValue(a, i18n.T(lang, "alert.discovery_new_nodes.fact_found")); !ok || v != "3" {
		t.Fatalf("nodes-found fact = %q (present=%v), want 3", v, ok)
	}
	if v, ok := factValue(a, i18n.T(lang, "alert.discovery_new_nodes.fact_filter")); !ok || v != "tag:dev" {
		t.Fatalf("tag-filter fact = %q (present=%v), want tag:dev", v, ok)
	}
	if a.DedupKey == "" || a.DedupTTL <= 0 {
		t.Fatalf("the discovery alert is not deduped (key=%q ttl=%v)", a.DedupKey, a.DedupTTL)
	}
	if !hasCyrillic(telegram.ComposeAlert(a)) {
		t.Fatal("the discovery alert is not translated")
	}
	// An empty filter must not produce an empty fact cell.
	if v, _ := factValue(BuildDiscoveryAlert(1, ""), i18n.T(lang, "alert.discovery_new_nodes.fact_filter")); v == "" {
		t.Fatal("an empty tag filter renders as an empty fact value")
	}
}
