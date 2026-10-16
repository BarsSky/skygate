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
	"unicode/utf8"

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
		{"welcome", "Gate", "Добро пожаловать"},
		{"registry", "Registry", "Реестр"},
		{"codex", "Codex", "Кодекс"},
		{"version", "Version", "Версия"},
		{"ack", "Acknowledged", "Подтверждение"},
		{"bind", "Binding", "Привязка"},
		{"unbind", "Unbinding", "Отвязка"},
		{"add", "Added", "Готово — добавлено"},
		{"del", "Removed", "Готово — удалено"},
		{"err", "Closed", "Дверь"},
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
	if !strings.Contains(en, "butler") {
		t.Errorf("EN footer missing 'butler' phrase, got: %q", en)
	}
	ru := footerFor(i18n.LangRU)
	if !strings.Contains(ru, "Дворецкий") {
		t.Errorf("RU footer missing 'Дворецкий' phrase, got: %q", ru)
	}
}

// TestComposeEnvelope: the envelope shape. Short body →
// no footer; long body → footer present; empty body →
// header alone (defensive, shouldn't happen in practice).
//
// 2026-07-16: v0.15.2 — gate-style envelope. The header
// is "🪶 ═══ Skygate ═══\n<topic>" and the footer is
// "═══ — Ваш Дворецкий ═══" (EN: "═══ — Your butler ═══").
func TestComposeEnvelope(t *testing.T) {
	short := Compose(i18n.LangEN, "add", "Rule added: 1.2.3.4", true)
	if !strings.HasPrefix(short, "🪶 ═══ Skygate ═══\n") {
		t.Errorf("short reply should start with gate header, got: %q", short[:50])
	}
	if !strings.Contains(short, "Your butler") {
		t.Errorf("forced-verbose reply should still carry the gate footer, got: %q", short)
	}

	long := Compose(i18n.LangEN, "registry", "lots of rules\n\none per line\n\nmany lines", false)
	if !strings.HasPrefix(long, "🪶 ═══ Skygate ═══\n") {
		t.Errorf("long reply should start with gate header, got: %q", long[:50])
	}
	if !strings.Contains(long, "many lines") {
		t.Errorf("long reply should contain body, got: %q", long)
	}
	_ = long

	// Now: short body, verbose=false → no footer.
	tiny := Compose(i18n.LangEN, "add", "Done.", false)
	if strings.Contains(tiny, "Your butler") {
		t.Errorf("short reply (verbose=false) should NOT have a footer, got: %q", tiny)
	}

	// Empty body → header alone.
	empty := Compose(i18n.LangEN, "err", "", true)
	if !strings.HasPrefix(empty, "🪶 ═══ Skygate ═══") {
		t.Errorf("empty-body reply should start with gate header, got: %q", empty)
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
	if strings.Contains(short, "butler") {
		t.Errorf("ComposeDefault should drop the footer for short bodies, got: %q", short)
	}
	long := ComposeDefault(i18n.LangEN, "registry", "rule 1\n\nrule 2\n\nrule 3\n\nrule 4")
	if !strings.Contains(long, "butler") {
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
	// 2026-07-16: v0.15.2 — gate header is "🪶 ═══ Skygate ═══"
	// followed by a topic line ("The Gate" for new-chat).
	if !strings.HasPrefix(got, "🪶 ═══ Skygate ═══\n🚪 The Gate") {
		t.Errorf("v15 new-chat welcome should start with gate header + The Gate topic, got: %q", got[:80])
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
	// Returning-user welcome is short — under 14 lines
	// (the v0.15.2 gate envelope added 2 more lines for
	// the "═══ Skygate ═══" header and the gate footer;
	// before v0.15.2 the limit was 10).
	if strings.Count(got, "\n") > 14 {
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
	if !strings.Contains(got, "пять минут") {
		t.Errorf("Russian new-chat welcome should mention пятиминутный TTL: %q", got)
	}
}

// TestGreetingForReturningUserRussian: the Russian
// returning-user welcome should still name the user.
func TestGreetingForReturningUserRussian(t *testing.T) {
	got := greetingForReturningUser(i18n.LangRU, "alice")
	if !strings.Contains(got, "alice") {
		t.Errorf("Russian returning user welcome should name the user, got: %q", got)
	}
	if !strings.Contains(got, "Рад вас видеть снова") {
		t.Errorf("Russian returning user welcome missing the returning-user line: %q", got)
	}
}

// TestTrimForTelegramStaysWithinBudget: the trim function must fit
// Telegram's reply budget. The budget is counted in RUNES (Telegram's
// own unit — the hard cap is 4096 code points); the byte length is
// checked against the hard cap separately, because the pre-B389
// byte-based implementation silently over-trimmed Cyrillic by half.
func TestTrimForTelegramStaysWithinBudget(t *testing.T) {
	long := strings.Repeat("a", 5000)
	got := trimForTelegram(i18n.LangEN, long)
	const budget = 3800
	if n := len([]rune(got)); n > budget {
		t.Errorf("trim output %d runes > budget %d", n, budget)
	}
	if len(got) > 4096 {
		t.Errorf("trim output %d bytes exceeds Telegram's 4096 hard cap", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("trim output should announce truncation, got tail: %q", got[len(got)-60:])
	}
}

// TestTrimForTelegram_CyrillicStaysValidUTF8 — B389. The byte-based
// implementation could cut a Cyrillic body mid-rune, producing invalid
// UTF-8 that Telegram rejects outright (the whole reply is lost, not
// just shortened).
func TestTrimForTelegram_CyrillicStaysValidUTF8(t *testing.T) {
	long := strings.Repeat("привет мир ", 600) // 6600 bytes, 6600 runes worth of 2-byte chars
	got := trimForTelegram(i18n.LangRU, long)
	if len([]rune(got)) > 3800 {
		t.Errorf("rune length %d > budget 3800", len([]rune(got)))
	}
	if !utf8.ValidString(got) {
		t.Error("trim output is not valid UTF-8 — a rune was cut in half")
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Error("trim output contains a replacement character")
	}
}

// TestTrimForTelegram_MarkerFollowsLanguage — the truncation marker
// used to be fetched in English for every language, so a Russian chat
// ended with an English sentence.
func TestTrimForTelegram_MarkerFollowsLanguage(t *testing.T) {
	long := strings.Repeat("a", 5000)
	ru := trimForTelegram(i18n.LangRU, long)
	en := trimForTelegram(i18n.LangEN, long)
	if ru == en {
		t.Fatal("ru and en trims are identical — the marker is not localised")
	}
	if !strings.Contains(ru, "обрезано") {
		t.Errorf("ru trim missing the Russian marker: %q", ru[len(ru)-80:])
	}
	if !strings.Contains(en, "truncated") {
		t.Errorf("en trim missing the English marker: %q", en[len(en)-80:])
	}
}

// TestTrimForTelegram_NeverEndsInsideATag — a cut inside "<code..." or
// "&am..." leaves wreckage the send boundary would have to escape, and
// the reader would see "<co" at the truncation point. A COMPLETE tag
// in the trimmed body is fine; an unterminated one is not.
func TestTrimForTelegram_NeverEndsInsideATag(t *testing.T) {
	marker := i18n.New().T(i18n.LangEN, "bot.trim.marker")
	assertNoPartialMarkup := func(head string) {
		t.Helper()
		if i := strings.LastIndex(head, "<"); i >= 0 && !strings.Contains(head[i:], ">") {
			t.Errorf("trim left a partial tag: %q", head[i:])
		}
		if i := strings.LastIndex(head, "&"); i >= 0 && !strings.Contains(head[i:], ";") {
			t.Errorf("trim left a partial entity: %q", head[i:])
		}
	}
	body := strings.Repeat("x", 3700) + "<code>" + strings.Repeat("y", 400)
	assertNoPartialMarkup(strings.TrimSuffix(trimForTelegram(i18n.LangEN, body), marker))

	// An entity that lost its ";" is the other half of the same hazard.
	body = strings.Repeat("x", 3700) + "&amp" + strings.Repeat("y", 400)
	assertNoPartialMarkup(strings.TrimSuffix(trimForTelegram(i18n.LangEN, body), marker))

	// A tag that would be cut in half must be dropped entirely. Place
	// it so the cut lands inside it: bodyBudget is the index the body
	// is truncated at.
	bodyBudget := 3800 - len([]rune(marker))
	body = strings.Repeat("x", bodyBudget-2) + "<code>" + strings.Repeat("y", 100)
	got := trimForTelegram(i18n.LangEN, body)
	head := strings.TrimSuffix(got, marker)
	if strings.Contains(head, "<") {
		t.Errorf("trim cut a tag in half: %q", head[len(head)-20:])
	}
	assertNoPartialMarkup(head)
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
