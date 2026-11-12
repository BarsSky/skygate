// B465 (2026-11-12) — an expired measurement is not a verdict about the operator's device.
//
// THE MEASUREMENT (operator's installation, the same day): the chat received
//
//	#46 ⚠️⚠️ пин на exit-узел устарел
//	устройство: basic   причина: stale-pref-owner-split   состояние узла: offline
//
// for a relay that was ONLINE at that moment, had never left `online` that day, and whose
// `exit_node_health` row said `offline` only because it came from a restored copy of the
// database (the real nodes never connect to that copy, so their rows never refresh). The
// message turned a hours-old reading into a claim about the operator's laptop — and, when
// the relay IS down, sent him to the wrong place (the pin) instead of the monitor.
//
// The same day produced the other half of the pair: `exitnode.health` (the admin page's
// snapshot) had not been rewritten since 2026-10-30, so the panel reported a thirteen-day-
// old SSH verdict as a current problem. Both are one rule: a verdict must carry its age.
package exit_rules

import (
	"strings"
	"testing"
	"time"

	"skygate/internal/i18n"
)

// b465Catalog installs the real RU catalogue: the alert's relay fact is composed through
// i18n (`relay_state_aged`), and without the catalogue installed `i18n.Tf` returns the
// KEY with no arguments — the assertions would be reading a placeholder, not the message.
func b465Catalog(t *testing.T) {
	t.Helper()
	i18n.SetGlobal(i18n.New())
}

// TestB465_UnfreshVerdictIsNotAVerdict is the core of the block: the exact live shape
// (pin on karolina, prefixes on emilia, the monitor's row says `offline`) must NOT be
// called stale while the row is too old to describe the relay now.
func TestB465_UnfreshVerdictIsNotAVerdict(t *testing.T) {
	s := b356State()
	s.PrefRelayFresh = false
	s.PrefRelayChecked = time.Now().Add(-6 * time.Hour)

	ch, ok := PlanDevicePrefChange(s)
	if !ok {
		t.Fatal("the planner returned nothing — the operator learns nothing about a device it cannot decide on")
	}
	if ch.Reason != "stale-pref-relay-unmeasured" {
		t.Fatalf("reason = %q, want stale-pref-relay-unmeasured (an expired measurement is not staleness)", ch.Reason)
	}
	if ch.Action != "skip" {
		t.Fatalf("action = %q, want skip — the engine must not rewrite a pin on an expired reading", ch.Action)
	}
	if ch.NewTag != "karolina" {
		t.Fatalf("new tag = %q, want the pinned relay carried for the message (no move)", ch.NewTag)
	}
}

// TestB465_FreshVerdictStillActs — the guard must not swallow the real case: a FRESH
// `offline` still repoints a derived preference (B356's behaviour, unchanged).
func TestB465_FreshVerdictStillActs(t *testing.T) {
	s := b356State() // Fresh=true, Usable=false, derived row
	ch, ok := PlanDevicePrefChange(s)
	if !ok {
		t.Fatal("no change planned for a fresh, unusable relay")
	}
	if ch.Reason != "stale-pref-relay-unusable" {
		t.Fatalf("reason = %q, want stale-pref-relay-unusable (a fresh verdict still moves the pin)", ch.Reason)
	}
	if ch.Action != "update" {
		t.Fatalf("action = %q, want update", ch.Action)
	}
}

// TestB465_UnmeasuredAlertSaysWhatItKnows — the message must not say «пин устарел»: the
// operator's next step is to look at the relay and the monitor, not to edit the pin.
func TestB465_UnmeasuredAlertSaysWhatItKnows(t *testing.T) {
	b465Catalog(t)
	a := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionUnmeasured, Hostname: "basic", Username: "michail",
		OldTag: "tag:dev-infra-karolina", Reason: "stale-pref-relay-unmeasured",
		RelayState: "offline", RelayMeasuredAgo: "6h", RelayMeasuredAt: "2026-11-12 12:09:05 UTC",
		SetByUserID: 0,
	})
	if a.TitleKey == "alert.preferred_exit.title_stale" {
		t.Fatal("the unmeasured alert still uses the stale title — it claims the pin is wrong")
	}
	if a.TitleKey != "alert.preferred_exit.title_unmeasured" {
		t.Fatalf("title key = %q, want alert.preferred_exit.title_unmeasured", a.TitleKey)
	}
	var relay, action string
	for _, f := range a.Facts {
		// Match on the VALUE, not the label: the label is a catalogue lookup, and the
		// assertion is about what the operator reads whatever the catalogue state is.
		if strings.Contains(f.Value, "6h") {
			relay = f.Value
		}
		if f.Value == "не измерено" || f.Value == "not measured" {
			action = f.Value
		}
	}
	if relay == "" {
		t.Fatalf("no fact carries the age of the measurement: %+v", a.Facts)
	}
	if action == "" {
		t.Fatalf("no fact says the relay was NOT measured: %+v", a.Facts)
	}
	if !strings.Contains(a.Details, "2026-11-12 12:09:05 UTC") {
		t.Fatalf("details do not say when the measurement was taken: %q", a.Details)
	}
}

// TestB465_FreshStaleAlertAlsoCarriesTheAge — even a legitimate stale alert names when the
// verdict was measured, so "offline" cannot read as "offline right now".
func TestB465_FreshStaleAlertAlsoCarriesTheAge(t *testing.T) {
	b465Catalog(t)
	a := BuildPreferredExitAlert(PreferredExitAlertInput{
		Action: PrefActionStale, Hostname: "laptop", Username: "michail",
		OldTag: "tag:dev-infra-x", NewTag: "tag:dev-infra-y", Reason: "stale-pref-relay-unusable",
		RelayState: "offline", RelayMeasuredAgo: "2m", UserID: 7,
	})
	for _, f := range a.Facts {
		if f.Value == "offline (замер 2m назад)" || f.Value == "offline (measured 2m ago)" {
			return
		}
	}
	t.Fatalf("the relay fact does not carry the age: %+v", a.Facts)
}

// TestB465_ConditionsDoNotRepeatHourly — a standing condition is ONE incident. The live
// chat got #45 at 16:09 and #46 at 17:09: the dedup window (1 h) expired exactly as the
// next hourly pass ran, so the same sentence arrived every hour, forever.
func TestB465_ConditionsDoNotRepeatHourly(t *testing.T) {
	for _, action := range []string{PrefActionStale, PrefActionUnmeasured, PrefActionAmbiguous, PrefActionOrphan} {
		if got := preferredExitDedupTTL(action); got < 4*time.Hour {
			t.Errorf("condition %q dedups for %s — a standing state would repeat hourly", action, got)
		}
	}
	for _, action := range []string{PrefActionCreate, PrefActionUpdate, PrefActionClear, PrefActionMigrate} {
		if got := preferredExitDedupTTL(action); got != time.Hour {
			t.Errorf("event %q dedups for %s, want 1h — an event that happens again IS news", action, got)
		}
	}
	// And the key must differ per action, or suppressing the condition would suppress the
	// repair that follows it.
	if preferredExitDedupKey(PrefActionStale, "basic", "x") == preferredExitDedupKey(PrefActionUnmeasured, "basic", "x") {
		t.Fatal("stale and unmeasured share a dedup key — the fix would silence the follow-up")
	}
}

// TestB465_HumanDurationReadsLikeAChatMessage — the age is user-visible.
func TestB465_HumanDurationReadsLikeAChatMessage(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "30s",
		13 * time.Minute: "13m",
		6 * time.Hour:    "6h",
		50 * time.Hour:   "2d",
		-5 * time.Minute: "0s", // future stamp must not render as "-5m"
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
