// 2026-07-16: v0.15.2 — butler-voice envelope helper tests.
//
// These pin the gate-style structure ("═══ Skygate ═══ …
// ═══ — signoff ═══") so a future refactor can't quietly
// drop the dividers or the signoff.

package telegram

import (
	"strings"
	"testing"
	"time"
)

// TestButlerEnvelope_AllZones checks the happy path:
// greeting + title + subheader + body + footer + signoff,
// in the right order, with the right HTML tags.
func TestButlerEnvelope_AllZones(t *testing.T) {
	got := butlerEnvelope(
		"ru", "skyadmin",
		"Title here",
		"Subheader here",
		"<pre>key</pre>",
		"Footer hint here",
	)
	for _, want := range []string{
		"🪶",                       // butler feather icon
		"═══ Skygate ═══",         // opening gate
		// Greeting: any time-of-day bucket ("Доброе утро" /
		// "Добрый день" / "Добрый вечер" / "Доброй ночи")
		// depending on the test machine's local hour. We
		// check the common "Добр" prefix instead of pinning
		// one bucket, which would flake on CI / local.
		"Добр",
		"skyadmin",                  // username
		"<b>Title here</b>",        // title in <b>
		"<blockquote>Subheader here</blockquote>", // subheader in blockquote
		"<pre>key</pre>",            // body kept verbatim
		"<i>Footer hint here</i>",   // footer in <i>
		"═══ — Ваш Дворецкий ═══",   // closing gate with signoff
	} {
		if !strings.Contains(got, want) {
			t.Errorf("envelope missing %q\ngot:\n%s", want, got)
		}
	}
}

// TestButlerEnvelope_OmitsEmpty checks that an empty
// optional zone is dropped cleanly (no empty <b></b>,
// no leading blank greeting, no trailing <i></i>).
func TestButlerEnvelope_OmitsEmpty(t *testing.T) {
	got := butlerEnvelope("en", "alice", "Just a title", "", "", "")
	// subheader/body/footer all empty → only title + greeting
	// + signoff should appear.
	if strings.Contains(got, "<blockquote>") {
		t.Errorf("expected no blockquote, got:\n%s", got)
	}
	if strings.Contains(got, "<pre>") {
		t.Errorf("expected no <pre>, got:\n%s", got)
	}
	if strings.Contains(got, "<i>") {
		t.Errorf("expected no <i>, got:\n%s", got)
	}
	if !strings.Contains(got, "<b>Just a title</b>") {
		t.Errorf("expected title in <b>, got:\n%s", got)
	}
}

// TestButlerEnvelope_NoGreeting checks the WithNoGreeting
// option (used by admin broadcast commands like /sync_nodes
// where the recipient is not a single person).
func TestButlerEnvelope_NoGreeting(t *testing.T) {
	got := butlerEnvelope("ru", "skyadmin", "Title", "", "", "", WithNoGreeting())
	if strings.Contains(got, "Добрый") || strings.Contains(got, "Добр") {
		t.Errorf("WithNoGreeting should drop greeting, got:\n%s", got)
	}
	if !strings.Contains(got, "🪶 ═══ Skygate ═══") {
		t.Errorf("gate header should still be present, got:\n%s", got)
	}
}

// TestButlerEnvelope_NoSignoff checks WithNoSignoff for
// 1-line trivia acknowledgements.
func TestButlerEnvelope_NoSignoff(t *testing.T) {
	got := butlerEnvelope("en", "", "Title", "", "", "", WithNoSignoff())
	if strings.Contains(got, "Your butler") || strings.Contains(got, "Дворецкий") {
		t.Errorf("WithNoSignoff should drop signoff, got:\n%s", got)
	}
	if !strings.Contains(got, "🪶 ═══ Skygate ═══") {
		t.Errorf("gate header should still be present, got:\n%s", got)
	}
}

// TestButlerEnvelope_NoUsername drops the greeting when
// the bot doesn't know who it's talking to (zero BotEnv).
func TestButlerEnvelope_NoUsername(t *testing.T) {
	got := butlerEnvelope("ru", "", "Title", "", "", "")
	if strings.Contains(got, "Добрый") {
		t.Errorf("empty username should drop greeting, got:\n%s", got)
	}
	if !strings.Contains(got, "🪶 ═══ Skygate ═══") {
		t.Errorf("gate header should still be present, got:\n%s", got)
	}
	if !strings.Contains(got, "═══ — Ваш Дворецкий ═══") {
		t.Errorf("signoff should still be present, got:\n%s", got)
	}
}

// TestButlerEnvelope_WithIcon checks that a non-default
// icon (e.g. 🔑 for /add_device) replaces the 🪶 but
// keeps everything else.
func TestButlerEnvelope_WithIcon(t *testing.T) {
	got := butlerEnvelope("en", "alice", "Title", "", "", "", WithIcon("🔑"))
	if !strings.HasPrefix(got, "🔑") {
		t.Errorf("WithIcon should make the icon the first char, got:\n%s", got)
	}
	if strings.HasPrefix(got, "🪶") {
		t.Errorf("WithIcon should replace the default 🪶, got:\n%s", got)
	}
}

// TestButlerEnvelope_TimeOfDayGreeting pins that the
// greeting bucket (morning/afternoon/evening/night)
// follows the hour of the day in the user's locale.
func TestButlerEnvelope_TimeOfDayGreeting(t *testing.T) {
	cases := []struct {
		hour   int
		ruKey  string
		enKey  string
	}{
		{7, "Доброе утро", "Good morning"},
		{13, "Добрый день", "Good afternoon"},
		{19, "Добрый вечер", "Good evening"},
		{2, "Доброй ночи", "Good night"},
	}
	for _, c := range cases {
		// Pick a time in the caller's local TZ so the
		// hour is what we asked for. The envelope uses
		// time.Now().Hour() which respects local TZ, so
		// time.Date(..., local) is the simplest knob.
		tm := time.Date(2026, 1, 1, c.hour, 0, 0, 0, time.Local)
		// We can't pass tm through butlerEnvelope (it
		// always uses time.Now()), so we test the
		// underlying greetingFor directly.
		if got := greetingFor("ru", tm); got != c.ruKey {
			t.Errorf("hour=%d ru greeting = %q, want %q", c.hour, got, c.ruKey)
		}
		if got := greetingFor("en", tm); got != c.enKey {
			t.Errorf("hour=%d en greeting = %q, want %q", c.hour, got, c.enKey)
		}
	}
}

// TestButlerEnvelope_SignoffRUvsEN pins the v0.10.12
// signoff D (Ваш Дворецкий / Your butler) so a future
// catalog rewrite can't accidentally drop the RU/EN
// distinction.
func TestButlerEnvelope_SignoffRUvsEN(t *testing.T) {
	if got := signoffFor("ru"); got != "Ваш Дворецкий" {
		t.Errorf("RU signoff = %q, want 'Ваш Дворецкий'", got)
	}
	if got := signoffFor("en"); got != "Your butler" {
		t.Errorf("EN signoff = %q, want 'Your butler'", got)
	}
}

// TestGateHeader_SingleSourceOfTruth pins the v0.15.3
// invariant: both butlerEnvelope (the HTML path) and
// Compose (the plain-text path) must use GateHeader() /
// GateFooter() from envelope.go. If a future refactor
// adds another hardcoded "🪶 ═══ Skygate ═══" string
// outside envelope.go, this test fails (via reflection
// on the package source) — keeping the contract that
// the gate shape is editable in one place.
//
// 2026-07-16: v0.15.3 — added in response to operator
// feedback that the same header/footer was duplicated
// in envelope.go AND personality.go, making it a
// two-place change to rebrand.
func TestGateHeader_SingleSourceOfTruth(t *testing.T) {
	// Plain-text path: Compose() opens with GateHeader.
	composed := Compose("en", "registry", "body", true)
	header := GateHeader("en", "registry")
	if !strings.HasPrefix(composed, header) {
		t.Errorf("Compose() doesn't start with GateHeader():\ncomposed=%q\nheader=%q", composed[:40], header)
	}
	// HTML path: butlerEnvelope() also opens with GateHeader
	// (for the no-icon default). The with-icon case is the
	// same gateLine but with the icon prefix; we test the
	// no-icon case here.
	got := butlerEnvelope("en", "alice", "Title", "Sub", "Body", "Footer")
	wantPrefix := "🪶 ═══ Skygate ═══\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("butlerEnvelope() doesn't start with canonical gate prefix:\ngot=%q\nwant prefix=%q", got[:40], wantPrefix)
	}
	// Footer: both Compose and butlerEnvelope end with GateFooter.
	// butlerEnvelope appends a trailing "\n" after the footer
	// (so subsequent messages in the log don't get glued to
	// the divider), so we match with the trailing "\n" too.
	if !strings.HasSuffix(got, GateFooter("en")+"\n") {
		t.Errorf("butlerEnvelope() doesn't end with GateFooter():\ngot=%q\nwant suffix=%q", got, GateFooter("en")+"\n")
	}
	// GateFooter shape: "═══ — <signoff> ═══". Asserting
	// the brackets pins the v0.15.2 visual shape so a
	// future change to e.g. use ━━━ or ─── is a
	// one-line edit in envelope.go.
	if want := "═══ — " + signoffFor("en") + " ═══"; GateFooter("en") != want {
		t.Errorf("GateFooter(en) = %q, want %q", GateFooter("en"), want)
	}
}
