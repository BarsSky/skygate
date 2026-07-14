package telegram

// 2026-07-14: tests for the butler-gatekeeper personality.
//
// These pin the EXACT text of every visible string in
// personality.go so a redesign of the voice / layout has to
// update both the source and the tests. They're also
// regression guards against:
//   * Silent emoji/whitespace drift (the tests use
//     strings.Contains on specific phrases).
//   * Renders that lose critical content (the test names
//     map to specific behaviours: "gate knows you" for
//     returning users, "the gate is sealed" for new chats).
//   * The trim budget (one test constructs a 5000-char reply
//     and asserts the trim stays within the 3800-char budget).

import (
	"strings"
	"testing"
)

// TestGateSigilStability: the warden's sigil is part of the
// brand; a test failure here means someone re-themed the
// bot. The sigil is a single character so it's cheap to pin
// and easy to fix if the redesign is intentional.
func TestGateSigilStability(t *testing.T) {
	if gateSigil != "✦" {
		t.Errorf("gate sigil changed from ✦ to %q — update the test alongside the source", gateSigil)
	}
	if gatekeeperSign != "⟡  Warden of the Threshold" {
		t.Errorf("gatekeeper sign changed: %q", gatekeeperSign)
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
// /help is for).
func TestGreetingForNewChatShape(t *testing.T) {
	got := greetingForNewChat("")
	mustContain := []string{
		"Warden of the Threshold",        // header
		"Open skygate → `/my/telegram`",   // step 1
		"Generate login key",              // step 2
		"/login <key>",                     // step 3
		"The key lives 5 minutes",          // TTL hint
		"/help",                            // pointer
		"/my_status",                       // top-3 teaser
		"/add_rule",                        // top-3 teaser (placeholder form)
		"/my_rules",                        // top-3 teaser
	}
	mustNotContain := []string{
		"/nodes",     // admin-only — would leak
		"/status",    // admin-only
		"/audit",     // admin-only
		"/restart",   // admin-only
		"set userpic", // BotFather-only command; not part of the bot's voice
	}
	for _, s := range mustContain {
		if !strings.Contains(got, s) {
			t.Errorf("new-chat welcome missing %q\ngot: %q", s, got)
		}
	}
	for _, s := range mustNotContain {
		if strings.Contains(got, s) {
			t.Errorf("new-chat welcome leaks %q (should not be in a brand-new-chat welcome)\ngot: %q", s, got)
		}
	}
}

// TestGreetingForNewChatWithBotUsername: the welcome
// includes a tap-to-open line when the bot's @username is
// known. We pin the line format so a future refactor
// doesn't break the deep link the user clicks.
func TestGreetingForNewChatWithBotUsername(t *testing.T) {
	got := greetingForNewChat("skygatepj_bot")
	if !strings.Contains(got, "@skygatepj_bot") {
		t.Errorf("welcome should mention the @username when known, got: %q", got)
	}
	// The line should be a one-liner (no command examples in
	// it) — the rest of the welcome already has the examples.
	if strings.Count(got, "\n") < 5 {
		t.Errorf("welcome should still be multi-line, got: %q", got)
	}
}

// TestGreetingForReturningUser: a bound user hitting /start
// gets a short welcome — not the long one. The new chat
// welcome is overwhelming for a returning user.
func TestGreetingForReturningUser(t *testing.T) {
	got := greetingForReturningUser("alice")
	if !strings.Contains(got, "alice") {
		t.Errorf("returning user welcome should name the user, got: %q", got)
	}
	if !strings.Contains(got, "the gate knows you") &&
		!strings.Contains(got, "The gate knows you") {
		t.Errorf("returning user welcome should include the 'gate knows you' line, got: %q", got)
	}
	// Returning-user welcome is short — under 6 lines.
	if strings.Count(got, "\n") > 6 {
		t.Errorf("returning user welcome is too long (%d lines), got: %q", strings.Count(got, "\n"), got)
	}
	// Should still point at /help so the user can explore.
	if !strings.Contains(got, "/help") {
		t.Errorf("returning user welcome should mention /help, got: %q", got)
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
	// The marker must be present so the user knows the reply
	// is partial, not complete.
	if !strings.Contains(got, "truncated") {
		t.Errorf("trim output should announce truncation, got: %q", got[len(got)-60:])
	}
}

// TestTrimForTelegramRespectsParagraphBoundary: when the
// input has "\n\n" inside the budget, the trim should
// prefer to cut at the last paragraph boundary rather than
// mid-sentence. We construct a 5000-char input with a clear
// "\n\n" boundary well inside the budget and assert the
// trim lands at that boundary.
func TestTrimForTelegramRespectsParagraphBoundary(t *testing.T) {
	// Build a string with two clear paragraphs:
	//   paragraph 1: 1000 chars of "a"
	//   "\n\n"          (separator)
	//   paragraph 2: 4000 chars of "b" (this is the part
	//                              that should be truncated)
	// Total ~ 5002 chars.
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString("a")
	}
	sb.WriteString("\n\n")
	for i := 0; i < 4000; i++ {
		sb.WriteString("b")
	}
	got := trimForTelegram(sb.String())

	// The trim should keep paragraph 1 (1000 "a"s + "\n\n")
	// and append the truncation marker, NOT include any "b".
	if strings.Contains(got, "b") {
		t.Errorf("trim should have cut at the paragraph boundary and dropped the second paragraph; got %d 'b' chars in output", strings.Count(got, "b"))
	}
	// The body must include the first paragraph.
	if !strings.Contains(got, strings.Repeat("a", 100)) {
		t.Errorf("trim should have kept the first paragraph (100 'a' chars), got: %q", got[:min(120, len(got))])
	}
	// The marker is appended.
	if !strings.Contains(got, "narrower slice)") {
		t.Errorf("trim should end with the gatekeeper marker, got tail: %q", got[len(got)-40:])
	}
	// Total stays under budget.
	if len(got) > 3800 {
		t.Errorf("trim output %d > budget 3800", len(got))
	}
}

// TestWelcomeNameVariants: the welcome line for a named
// user must include the name in the gatekeeper sigil row.
// Empty name → bare sigil. The role label "you are Warden
// of your own devices" is the second-line signature for
// returning users; it should ALWAYS appear when the user
// is named, even if the future redesign changes other
// formatting.
func TestWelcomeNameVariants(t *testing.T) {
	if got := welcome(""); got != gatekeeperSign {
		t.Errorf("welcome(\"\") = %q, want %q (bare sigil)", got, gatekeeperSign)
	}
	if got := welcome("alice"); !strings.Contains(got, "alice") {
		t.Errorf("welcome(\"alice\") should include name, got: %q", got)
	}
	if got := welcome("alice"); !strings.Contains(got, roleWardenOfSelf) {
		t.Errorf("welcome(\"alice\") should include role line %q, got: %q", roleWardenOfSelf, got)
	}
}

// TestSectionLabelFormat: section labels in the codex use
// the same icon+title pattern so the user's eye learns
// the rhythm. The bold marker in the rendered Telegram
// client is the surrounding "**"; we use ASCII asterisks
// because not every Telegram client renders markdown and
// we want a graceful degradation to literal "**"
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
// → warden's own data sigil (◈). The bot uses this to
// colour /status and /version differently for the two
// scopes.
func TestIconForCaller(t *testing.T) {
	if got := iconForCaller(true); got != "✦" {
		t.Errorf("iconForCaller(true) = %q, want ✦ (warden)", got)
	}
	if got := iconForCaller(false); got != "◈" {
		t.Errorf("iconForCaller(false) = %q, want ◈ (warden's own data)", got)
	}
}
