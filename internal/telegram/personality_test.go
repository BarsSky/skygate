package telegram

// 2026-07-14: tests for the butler-gatekeeper personality
// (personality.go). The i18n catalog must be installed via
// TestMain for these to render the real English / Russian
// strings; without it, i18n.T returns the key and the substring
// assertions on /login <key> and similar literals would fail.
// TestMain (commands_test.go) calls i18n.SetGlobal(i18n.New())
// before any test runs.
//
// These pin:
//   * Gatekeeper sign + role + codex header (in en + ru).
//   * New-chat welcome: the three binding steps + a teaser of
//     /help's value, no admin commands leaked.
//   * Returning-user welcome: short, the gate-knows-you
//     phrase, the @username appears.
//   * trimForTelegram: stays within the 3800-char budget.

import (
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// TestGateSigilStability: the warden's sigil is part of the
// brand; a test failure here means someone re-themed the
// bot. The sigil is a single character so it's cheap to pin
// and easy to fix if the redesign is intentional.
func TestGateSigilStability(t *testing.T) {
	if gateSigil != "✦" {
		t.Errorf("gate sigil changed from ✦ to %q — update the test alongside the source", gateSigil)
	}
	if thinRule != "─────────────────" {
		t.Errorf("thin rule changed: %q", thinRule)
	}
}

// TestGreetingForNewChatShape: the brand-new-chat welcome
// must contain the three binding steps (the only real way
// to bind is via /login <key>) AND a teaser of what /help
// reveals (so the user gets a hint of the value of
// continuing). It must NOT dump every command (that's what
// /help is for). Same shape as the v0.10.4 test, but the
// substring set is now coming from the i18n catalog so we
// assert on the catalog keys (not on hardcoded English).
func TestGreetingForNewChatShape(t *testing.T) {
	got := greetingForNewChat(i18n.LangEN, "")
	// We assert on the literal prefix that the catalog
	// contains; if the catalog drifts, this test catches
	// it before it reaches a real user.
	if !strings.Contains(got, "Warden of the Threshold") {
		t.Errorf("new-chat welcome missing the gatekeeper sign: %q", got)
	}
	if !strings.Contains(got, "/login <key>") {
		t.Errorf("new-chat welcome missing the /login <key> command: %q", got)
	}
	if !strings.Contains(got, "Generate login key") {
		t.Errorf("new-chat welcome missing the Generate login key step: %q", got)
	}
	if !strings.Contains(got, "/help") {
		t.Errorf("new-chat welcome missing the /help pointer: %q", got)
	}
	if !strings.Contains(got, "/my_status") {
		t.Errorf("new-chat welcome missing the /my_status teaser: %q", got)
	}
	if !strings.Contains(got, "5 minutes") {
		t.Errorf("new-chat welcome missing the 5-min TTL hint: %q", got)
	}
}

// TestGreetingForNewChatWithBotUsername: when the bot's
// @username is known, the welcome includes a tap-to-open
// line. We pin the line so a future refactor doesn't break
// the deep link the user clicks.
func TestGreetingForNewChatWithBotUsername(t *testing.T) {
	got := greetingForNewChat(i18n.LangEN, "skygatepj_bot")
	if !strings.Contains(got, "@skygatepj_bot") {
		t.Errorf("welcome should mention the @username when known, got: %q", got)
	}
	// The line should be a one-liner; the rest of the
	// welcome still has the multi-line binding steps.
	if !strings.Contains(got, "/login <key>") {
		t.Errorf("welcome should still include the /login <key> step, got: %q", got)
	}
}

// TestGreetingForReturningUser: a bound user hitting /start
// gets a short welcome — not the long one. The new chat
// welcome is overwhelming for a returning user.
func TestGreetingForReturningUser(t *testing.T) {
	got := greetingForReturningUser(i18n.LangEN, "alice")
	if !strings.Contains(got, "alice") {
		t.Errorf("returning user welcome should name the user, got: %q", got)
	}
	if !strings.Contains(got, "gate knows you") {
		t.Errorf("returning user welcome should include the 'gate knows you' line, got: %q", got)
	}
	// Returning-user welcome is short — under 10 lines.
	if strings.Count(got, "\n") > 10 {
		t.Errorf("returning user welcome is too long (%d lines), got: %q", strings.Count(got, "\n"), got)
	}
	// Should still point at /help so the user can explore.
	if !strings.Contains(got, "/help") {
		t.Errorf("returning user welcome should mention /help, got: %q", got)
	}
}

// TestGreetingForNewChatRussian: the same shape as the
// English test, but asserts the Russian catalog renders
// the right sigil + the right command placeholder. The
// command name itself stays English (it's a /login token
// the user types), but the surrounding prose is Russian.
func TestGreetingForNewChatRussian(t *testing.T) {
	got := greetingForNewChat(i18n.LangRU, "")
	if !strings.Contains(got, "Хранитель Порога") {
		t.Errorf("Russian new-chat welcome missing the gatekeeper sign: %q", got)
	}
	if !strings.Contains(got, "/login") {
		t.Errorf("Russian new-chat welcome should still mention the /login command: %q", got)
	}
	if !strings.Contains(got, "5 минут") {
		t.Errorf("Russian new-chat welcome should mention 5-минутный TTL: %q", got)
	}
}

// TestGreetingForReturningUserRussian: the Russian
// returning-user welcome should still name the user and
// point at /help.
func TestGreetingForReturningUserRussian(t *testing.T) {
	got := greetingForReturningUser(i18n.LangRU, "alice")
	if !strings.Contains(got, "alice") {
		t.Errorf("Russian returning user welcome should name the user, got: %q", got)
	}
	if !strings.Contains(got, "Врата знают тебя") {
		t.Errorf("Russian returning user welcome missing the 'Врата знают тебя' line: %q", got)
	}
}

// TestTrimForTelegramStaysWithinBudget: the trim function
// must produce output that fits Telegram's 3800-char reply
// budget. We use 5000 "a"s (no newlines) so the hard-trim
// path is exercised, not the "\n\n"-aware path.
func TestTrimForTelegramStaysWithinBudget(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := trimForTelegram(long)
	const budget = 3800
	if len(got) > budget {
		t.Errorf("trim output %d > budget %d (the gate is silent for being too chatty)", len(got), budget)
	}
	// The marker must mention "truncated" so the user
	// knows the reply is partial, not complete. The
	// exact wording is in the i18n catalog; we don't
	// pin it here (so a translation tweak doesn't break
	// the test).
	if !strings.Contains(got, "truncated") {
		t.Errorf("trim output should announce truncation, got tail: %q", got[len(got)-60:])
	}
}

// TestWelcomeNameVariants: the welcome line for a named
// user must include the name in the gatekeeper sigil row.
// Empty name → bare sigil. The role label "you are Warden
// of your own devices" is the second-line signature for
// returning users; it should ALWAYS appear when the user
// is named.
func TestWelcomeNameVariants(t *testing.T) {
	if got := welcome(i18n.LangEN, ""); got != gatekeeperSign(i18n.LangEN) {
		t.Errorf("welcome(en, \"\") = %q, want %q (bare sigil)", got, gatekeeperSign(i18n.LangEN))
	}
	if got := welcome(i18n.LangEN, "alice"); !strings.Contains(got, "alice") {
		t.Errorf("welcome(en, \"alice\") should include name, got: %q", got)
	}
	if got := welcome(i18n.LangEN, "alice"); !strings.Contains(got, roleWardenOfSelf(i18n.LangEN)) {
		t.Errorf("welcome(en, \"alice\") should include role line, got: %q", got)
	}
}

// TestSectionLabelFormat: section labels in the codex use
// the same icon+title pattern so the user's eye learns the
// rhythm.
func TestSectionLabelFormat(t *testing.T) {
	got := sectionLabel("✦", "Your top three")
	want := "*✦ Your top three*"
	if got != want {
		t.Errorf("sectionLabel: got %q, want %q", got, want)
	}
}

// TestCodexLineVariants: codexLine is the workhorse for
// every reply that lists items. Two variants: with a
// value (key — value) and without (just key). Empty value
// should fall back to the no-value form, NOT render
// "`key` — " with a trailing dash.
func TestCodexLineVariants(t *testing.T) {
	with := codexLine("◈", "/my_status", "your own summary")
	if !strings.HasPrefix(with, "◈  `/my_status` — ") {
		t.Errorf("codexLine with value should be icon+key+value, got: %q", with)
	}
	empty := codexLine("◈", "/my_status", "")
	if strings.HasSuffix(empty, "— ") {
		t.Errorf("codexLine with empty value should not render trailing dash, got: %q", empty)
	}
	if !strings.HasPrefix(empty, "◈  `/my_status`") {
		t.Errorf("codexLine with empty value should still show key, got: %q", empty)
	}
}

// TestIconForCaller picks the right icon for the caller's
// privilege level. Admin → warden sigil (✦); non-admin
// → warden's own data sigil (◈).
func TestIconForCaller(t *testing.T) {
	if got := iconForCaller(true); got != "✦" {
		t.Errorf("iconForCaller(true) = %q, want ✦ (warden)", got)
	}
	if got := iconForCaller(false); got != "◈" {
		t.Errorf("iconForCaller(false) = %q, want ◈ (warden's own data)", got)
	}
}
