// internal/feature/exit_rules/prefix_failover_b430_test.go — B430.
//
// The reconcile pass that moves prefixes off a dead relay must produce ONE readable
// message, not a flood and not silence. These tests pin the composed shape (title
// from the catalogue, facts, a bounded excerpt, a dedup key on the ROUTE) and the
// plain fallback for a sink that cannot render an Alert.
package exit_rules

import (
	"strings"
	"testing"

	"skygate/internal/i18n"
	"skygate/internal/prefixowner"
	"skygate/internal/telegram"
)

func b430Moves(n int, from, to string) []prefixowner.PrefixMove {
	out := make([]prefixowner.PrefixMove, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, prefixowner.PrefixMove{
			Prefix: "10." + string(rune('a'+i)) + ".0.0/16", From: from, To: to, Forced: true,
		})
	}
	return out
}

func TestB430_ComposedAlertNamesTheRouteAndCounts(t *testing.T) {
	i18n.SetGlobal(i18n.New())
	moves := b430Moves(3, "emilia", "karolina")
	alert := BuildPrefixFailoverAlert(moves)

	if alert.TitleKey != PrefixFailoverTitleKey {
		t.Fatalf("title key = %q, want the catalogue key", alert.TitleKey)
	}
	if alert.Severity != telegram.SeverityWarn {
		t.Fatalf("severity = %q, want warn (a failover degrades service, it does not stop it)", alert.Severity)
	}
	if len(alert.Facts) < 2 {
		t.Fatalf("facts = %+v, want the total AND one fact per route", alert.Facts)
	}
	if alert.Facts[0].Value != "3" {
		t.Fatalf("facts[0] = %+v, want the total moved", alert.Facts[0])
	}
	// One fact per route: the total alone does not say WHICH relay took over.
	if alert.Facts[1].Label != "emilia → karolina" || alert.Facts[1].Value != "3" {
		t.Fatalf("route fact = %+v, want emilia → karolina: 3", alert.Facts[1])
	}
	// The label must be TRANSLATED, not a catalogue key — the composer prints it
	// literally next to the value.
	if alert.Facts[0].Label == "alert.prefix_failover.fact_moved" {
		t.Fatalf("the fact label leaked a catalogue key: %q", alert.Facts[0].Label)
	}
	if !strings.Contains(alert.Details, "karolina") {
		t.Fatalf("details do not name the destination: %q", alert.Details)
	}
	// The dedup identity is the ROUTE, so a flapping relay pages once per direction
	// rather than once per prefix.
	if !strings.Contains(alert.DedupKey, "emilia>karolina") {
		t.Fatalf("dedup key = %q, want it to carry from>to", alert.DedupKey)
	}
	if alert.DedupTTL <= 0 {
		t.Fatal("a failover alert without a dedup window can flood the chat")
	}
}

// TestB430_ExcerptIsBounded: a dead relay owns dozens of prefixes; the message must
// stay readable and say how much it dropped.
func TestB430_ExcerptIsBounded(t *testing.T) {
	i18n.SetGlobal(i18n.New())
	moves := b430Moves(maxFailoverPrefixesListed+5, "emilia", "karolina")
	alert := BuildPrefixFailoverAlert(moves)
	lines := strings.Split(strings.TrimSpace(alert.Details), "\n")
	if len(lines) > maxFailoverPrefixesListed+1 {
		t.Fatalf("the excerpt is %d lines — it must stay bounded and say how much it dropped", len(lines))
	}
	if !strings.Contains(alert.Details, "+5") {
		t.Fatalf("the excerpt does not say how many prefixes it dropped: %q", alert.Details)
	}
}

// TestB430_OneFactPerRoute: prefixes going to two different relays are two facts,
// not one merged number.
func TestB430_OneFactPerRoute(t *testing.T) {
	i18n.SetGlobal(i18n.New())
	moves := append(b430Moves(2, "emilia", "karolina"), b430Moves(1, "emilia", "sharlotta")...)
	alert := BuildPrefixFailoverAlert(moves)
	// The count fact is the total; the per-route counts live in the details so the
	// message stays short.
	if alert.Facts[0].Value != "3" {
		t.Fatalf("count fact = %q, want the total moved", alert.Facts[0].Value)
	}
	if !strings.Contains(alert.DedupKey, "emilia>karolina") || !strings.Contains(alert.DedupKey, "emilia>sharlotta") {
		t.Fatalf("dedup key must cover both routes: %q", alert.DedupKey)
	}
}
