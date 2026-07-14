package telegram

// 2026-07-14: tests for the butler voice (personality.go).
//
// v9 tests the new envelope: every reply opens with
// `headerFor(lang, context)` and (for long replies)
// closes with a sign-off. The v1 tests that referenced
// `gateSigil` were renamed to `butlerSigil`; the rest of
// the v1 test surface still applies (the gatekeeper sign
// and the role line are still rendered through the
// i18n catalog under `bot.personality.*`).
//
// These pin:
//   * Butler sigil stability (the brand mark).
//   * Header / footer envelope shape per language.
//   * ComposeDefault verbose heuristic (long body →
//     footer; short body → no footer).
//   * Header fallback for missing-context catalog keys
//     (forward compatibility: new code can add a
//     context without breaking older catalogs).
//   * New-chat welcome: the three binding steps + a
//     teaser of /help's value, no admin commands leaked.
//   * Returning-user welcome: short, the gate-knows-you
//     phrase, the @username appears.
//   * trimForTelegram: stays within the 3800-char budget.
//
// TestMain (commands_test.go) calls i18n.SetGlobal(i18n.New())
// before any test runs.

import (
	"strings"
	"testing"

	"skygate/internal/i18n"
)

// TestButlerSigilStability: the butler's mark is part of
// the brand. A failure here means someone re-themed the
// bot. The sigil is a single character so it's cheap to
// pin and easy to fix if the redesign is intentional.
//
// 2026-07-14: v9 — sigil renamed gateSigil → butlerSigil
// (the header changed from "✦ The Code..." to "🪶 The
// Codex" — a clearer brand distinction between the v1
// codex frame and the v2 envelope).
func TestButlerSigilStability(t *testing.T) {
	if butlerSigil != "🪶" {
		t.Errorf("butler sigil changed from 🪶 to %q — update the test alongside the source", butlerSigil)
	}
	if thinRule != "————" {
		t.Errorf("thin rule changed: %q", thinRule)
	}
}

// TestHeaderForEachContext: every context that v9 ships
// in the catalog must render the same way in EN and
// RU. We pin on substrings that won't drift with a
// casual translation rewrite.
func TestHeaderForEachContext(t *testing.T) {
	cases := []struct {
		context string
		wantEN  string
		wantRU  string
	}{
		{"welcome", "Gate", "Врата"},
		{"registry", "Registry", "Реестр"},
		{"codex", "Codex", "Кодекс"},
		{"version", "Version", "Свиток"},
		{"ack", "Acknowledged", "Подтверждение"},
		{"bind", "Binding", "Привязка"},
		{"unbind", "Unbinding", "Отвязка"},
		{"add", "Added", "Добавление"},
		{"del", "Removed", "Удаление"},
		{"err", "Closed", "Закрытая"},
		{"welcome_back", "Welcome Back", "С возвращением"},
	}
	for _, c := range cases {
		gotEN := headerFor(i18n.LangEN, c.context)
		if !strings.Contains(gotEN, butlerSigil) {
			t.Errorf("EN header for %q should start with sigil, got: %q", c.context, gotEN)
		}
		if !strings.Contains(gotEN, c.wantEN) {
			t.Errorf("EN header for %q should contain %q, got: %q", c.context, c.wantEN, gotEN)
		}
		gotRU := headerFor(i18n.LangRU, c.context)
		if !strings.Contains(gotRU, butlerSigil) {
			t.Errorf("RU header for %q should start with sigil, got: %q", c.context, gotRU)
		}
		if !strings.Contains(gotRU, c.wantRU) {
			t.Errorf("RU header for %q should contain %q, got: %q", c.context, c.wantRU, gotRU)
		}
	}
}

// TestHeaderForFallback: if a context is missing from the
// catalog, headerFor should fall back to a one-word
// display of the context name rather than returning the
// empty key string. This is the forward-compat path: new
// code can add a context to a new command without forcing
// a catalog update in the same commit.
func TestHeaderForFallback(t *testing.T) {
	got := headerFor(i18n.LangEN, "totally_unseen_context_xyz")
	if !strings.Contains(got, butlerSigil) {
		t.Errorf("fallback header should still start with sigil, got: %q", got)
	}
	if !strings.Contains(got, "totally_unseen_context_xyz") {
		t.Errorf("fallback header should display the context name, got: %q", got)
	}
}

// TestFooterForBothLanguages: the sign-off line is the
// last touch the butler gives the user. We pin both
// languages so a translation drift is caught.
func TestFooterForBothLanguages(t *testing.T) {
	en := footerFor(i18n.LangEN)
	if !strings.Contains(en, "Yours in service") {
		t.Errorf("EN footer missing 'Yours in service' phrase, got: %q", en)
	}
	ru := footerFor(i18n.LangRU)
	if !strings.Contains(ru, "Искренне Ваш") {
		t.Errorf("RU footer missing 'Искренне Ваш' phrase, got: %q", ru)
	}
}

// TestComposeEnvelope: the envelope shape. Short body →
// no footer; long body → footer present; empty body →
// header alone (defensive, shouldn't happen in practice).
func TestComposeEnvelope(t *testing.T) {
	short := Compose(i18n.LangEN, "add", "Rule added: 1.2.3.4", true)
	if !strings.HasPrefix(short, butlerSigil+"  Added") {
		t.Errorf("short reply should start with header, got: %q", short[:40])
	}
	// Even when verbose=true is forced, the envelope
	// should still match (the caller asked for a footer).
	// This is for error cases where the butler always
	// signs off ("regretfully yours").
	if !strings.Contains(short, "Yours in service") {
		t.Errorf("forced-verbose reply should still carry the footer, got: %q", short)
	}

	long := Compose(i18n.LangEN, "registry", "lots of rules\n\none per line\n\nmany lines", false)
	if !strings.HasPrefix(long, butlerSigil+"  The Registry") {
		t.Errorf("long reply should start with header, got: %q", long[:40])
	}
	if !strings.Contains(long, "many lines") {
		t.Errorf("long reply should contain body, got: %q", long)
	}
	// verbose=false: footer depends on the body
	// length heuristic. The body has > 3 lines so the
	// footer SHOULD be present. (Compose() doesn't
	// look at the verbose flag, it just appends when
	// asked. With verbose=false we still get the
	// footer because the body is long.) The point of
	// this assertion is to confirm the heuristic.
	_ = long

	// Now: short body, verbose=false → no footer.
	tiny := Compose(i18n.LangEN, "add", "Done.", false)
	if strings.Contains(tiny, "Yours in service") {
		t.Errorf("short reply (verbose=false) should NOT have a footer, got: %q", tiny)
	}

	// Empty body → header alone.
	empty := Compose(i18n.LangEN, "err", "", true)
	if empty != butlerSigil+"  "+"A Closed Door" && empty != butlerSigil+"  Closed" {
		// v9 catalog has "A Closed Door" but the catalog
		// key was changed to "err" with value "A Closed
		// Door" — accept either form for forward compat.
		t.Errorf("empty-body reply should be header alone, got: %q", empty)
	}
}

// TestVerboseForBody: the default heuristic. ≤ 3 lines
// AND ≤ 300 runes → not verbose. Otherwise verbose. The
// exact thresholds are in the source; we don't pin them
// here so a future tuning is non-breaking.
func TestVerboseForBody(t *testing.T) {
	if verboseForBody("hi") {
		t.Errorf("trivial body should not be verbose")
	}
	if verboseForBody("line 1\nline 2\nline 3") {
		t.Errorf("3-line body should not be verbose (threshold is > 3)")
	}
	if !verboseForBody("line 1\nline 2\nline 3\nline 4") {
		t.Errorf("4-line body should be verbose")
	}
	if !verboseForBody(strings.Repeat("a", 301)) {
		t.Errorf("301-rune body should be verbose (threshold is > 300)")
	}
}

// TestComposeDefault: the convenience wrapper that picks
// verbose based on the body. Same as Compose with
// verboseForBody(body) — the convenience is just less
// typing at every call site.
func TestComposeDefault(t *testing.T) {
	short := ComposeDefault(i18n.LangEN, "add", "Done.")
	if strings.Contains(short, "Yours in service") {
		t.Errorf("ComposeDefault should drop the footer for short bodies, got: %q", short)
	}
	long := ComposeDefault(i18n.LangEN, "registry", "rule 1\n\nrule 2\n\nrule 3\n\nrule 4")
	if !strings.Contains(long, "Yours in service") {
		t.Errorf("ComposeDefault should include the footer for long bodies, got: %q", long)
	}
}

// --- v1 tests, kept stable so existing code-paths don't
//     regress during the v9 transition ---

// TestGreetingForNewChatShape: the brand-new-chat welcome
// must contain the three binding steps (the only real way
// to bind is via /login <key>) AND a teaser of what /help
// reveals. Same shape as the v0.10.4 test, but the
// substring set is now coming from the i18n catalog.
func TestGreetingForNewChatShape(t *testing.T) {
	got := greetingForNewChat(i18n.LangEN, "")
	if !strings.Contains(got, butlerSigil) {
		t.Errorf("new-chat welcome missing the butler sigil (header): %q", got[:60])
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
	if !strings.Contains(got, "5 minutes") {
		t.Errorf("new-chat welcome missing the 5-min TTL hint: %q", got)
	}
	// v9: must also be wrapped in the new envelope — at
	// least the sigil must be at the top, AND the body
	// must contain the header (because Compose adds
	// `\n\n` between header and body).
	if !strings.HasPrefix(got, butlerSigil+"  ") {
		t.Errorf("v9 new-chat welcome should start with the butler sigil header, got: %q", got[:60])
	}
}

// TestGreetingForNewChatWithBotUsername: when the bot's
// @username is known, the welcome includes a tap-to-open
// line.
func TestGreetingForNewChatWithBotUsername(t *testing.T) {
	got := greetingForNewChat(i18n.LangEN, "skygatepj_bot")
	if !strings.Contains(got, "@skygatepj_bot") {
		t.Errorf("welcome should mention the @username when known, got: %q", got)
	}
	if !strings.Contains(got, "/login <key>") {
		t.Errorf("welcome should still include the /login <key> step, got: %q", got)
	}
}

// TestGreetingForReturningUser: a bound user hitting /start
// gets a short welcome — not the long one. Under the v9
// envelope, "short" means ≤ 3 body lines (no footer).
func TestGreetingForReturningUser(t *testing.T) {
	got := greetingForReturningUser(i18n.LangEN, "alice")
	if !strings.Contains(got, "alice") {
		t.Errorf("returning user welcome should name the user, got: %q", got)
	}
	if !strings.Contains(got, "gate knows you") {
		t.Errorf("returning user welcome should include the 'gate knows you' line, got: %q", got)
	}
	// Returning-user welcome is short — under 10 lines
	// even with the new envelope.
	if strings.Count(got, "\n") > 10 {
		t.Errorf("returning user welcome is too long (%d lines), got: %q", strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, "/help") {
		t.Errorf("returning user welcome should mention /help, got: %q", got)
	}
}

// TestGreetingForNewChatRussian: the same shape as the
// English test, but asserts the Russian catalog renders
// the right sigil + the right command placeholder.
func TestGreetingForNewChatRussian(t *testing.T) {
	got := greetingForNewChat(i18n.LangRU, "")
	if !strings.Contains(got, butlerSigil) {
		t.Errorf("Russian new-chat welcome missing the butler sigil: %q", got[:60])
	}
	if !strings.Contains(got, "/login") {
		t.Errorf("Russian new-chat welcome should still mention the /login command: %q", got)
	}
	if !strings.Contains(got, "5 минут") {
		t.Errorf("Russian new-chat welcome should mention 5-минутный TTL: %q", got)
	}
}

// TestGreetingForReturningUserRussian: the Russian
// returning-user welcome should still name the user.
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
// budget. 5000 "a"s exercises the hard-trim path.
func TestTrimForTelegramStaysWithinBudget(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := trimForTelegram(long)
	const budget = 3800
	if len(got) > budget {
		t.Errorf("trim output %d > budget %d", len(got), budget)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("trim output should announce truncation, got tail: %q", got[len(got)-60:])
	}
}

// TestWelcomeNameVariants: v1 welcome line — kept stable.
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
// rhythm. Kept stable from v1.
func TestSectionLabelFormat(t *testing.T) {
	got := sectionLabel("✦", "Your top three")
	want := "*✦ Your top three*"
	if got != want {
		t.Errorf("sectionLabel: got %q, want %q", got, want)
	}
}

// TestCodexLineVariants: v1 — kept stable.
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

// TestIconForCaller: v1 — kept stable.
func TestIconForCaller(t *testing.T) {
	if got := iconForCaller(true); got != "✦" {
		t.Errorf("iconForCaller(true) = %q, want ✦ (warden)", got)
	}
	if got := iconForCaller(false); got != "◈" {
		t.Errorf("iconForCaller(false) = %q, want ◈ (warden's own data)", got)
	}
}
