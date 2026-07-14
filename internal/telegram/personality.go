package telegram

// 2026-07-14: Этап 14 v9 — butler voice v2.
//
// The v1 voice (Этап 14 v4 + v5) gave the bot a butler-gatekeeper
// register but each command composed its own greeting / closing
// lines inline, which made the feel inconsistent across surfaces
// (/help looked like a codex, /rules looked like a registry dump,
// /start looked like an invitation card). v2 unifies the
// envelope so EVERY reply from the bot reads as if a butler
// handed you a single folded note at the door:
//
//   ┌────────────────────────────────────────────────────────────┐
//   │ 🪶 | Кодекс                                                │  ← header (1 line)
//   │                                                            │
//   │ body text                                                  │  ← body (1..N lines)
//   │ …                                                          │
//   │                                                            │
//   │ ─ Искренне Ваш, Хранитель Порога                          │  ← footer (1 line, only when body > 3 lines)
//   └────────────────────────────────────────────────────────────┘
//
// The header tells you the topic at a glance (the registry, the
// codex, the version page). The footer is a single sign-off that
// we omit on short replies to keep them from feeling padded. The
// body is the actual answer — and it has more vertical breathing
// room now because we removed the per-section header lines that
// v1 was adding to long replies.
//
// v2 is wire-compatible with v1: the function signatures on the
// existing helpers (welcome, greetingForNewChat, …) are kept
// stable. Existing tests in personality_test.go still pass; new
// tests pin the header/footer shape per context.

import (
	"fmt"
	"strings"

	"skygate/internal/i18n"
)

// Sigil and decorative marks. The sigil is the bot's
// monogram. Stable across languages so a user's eye learns
// to recognise the "this came from the butler" marker
// without reading the text.
const (
	// butlerSigil is the butler's mark, used in every reply
	// header. Multi-byte rune so the column width is
	// consistent in monospace renderings. The ✦ is the
	// same shape Tailscale uses for admin tools, which
	// signals "operator" rather than "ornament".
	butlerSigil = "🪶"

	// thinRule is the separator inside long bodies. It is
	// 12 em-dashes wide, which renders as a clean rule in
	// both Telegram's monospace font and in HTML clients.
	thinRule = "————"

	// The four "waxes" are status icons used inside a body
	// line. They are not the header — they are read by a
	// scanning eye, not a peripheral one.
	successWax = "🜍"
	lockWax    = "🜔"
	infoWax    = "✧"
	errorWax   = "🝆"

	// headerFooterSeparator is the single blank line
	// between the header line and the body, and between
	// the body and the footer. Two newlines, no space.
	headerFooterSeparator = "\n\n"
)

// headerFor returns the one-line prefix on every bot
// reply, parameterised by the reply's "context"
// (registry, codex, version, ack, bind, add, del,
// err, welcome). The translation is loaded from
// `bot.header.<context>` in the i18n catalog.
//
// Why parameterise the context: every reply has one
// topic, and the topic lives in the header so the user
// sees it before scrolling. Without the context the
// header would just say "Skygate" for every message,
// which is less useful and feels less personal.
//
// Falls back to the literal "Skygate" string if the
// catalog key is missing — this preserves forward
// compatibility (adding a new context to a new command
// is just a catalog entry, no code change required).
func headerFor(lang, context string) string {
	key := "bot.header." + context
	if i18n.T(lang, key) == key {
		// Fallback: no translation for this context.
		return butlerSigil + "  " + context
	}
	return butlerSigil + "  " + i18n.T(lang, key)
}

// footerFor returns the single-line sign-off that we
// append to replies whose body is more than 3 lines.
// Short replies (≤ 3 lines) get no footer — the absence
// of a footer is itself a signal: "this is the whole
// answer, nothing more to follow up on".
//
// Pass a lang and the reply is rendered in that
// language. The catalogue key is `bot.footer.signoff`
// (and `bot.footer.signoff_short` for one-line replies
// that DO want a footer — e.g. error confirmations).
func footerFor(lang string) string {
	return "— " + i18n.T(lang, "bot.footer.signoff")
}

// Compose is the one helper every reply should call.
// Given a lang, a context, a body, and a "verbose" flag
// it returns the full reply string:
//
//   if verbose:  HEADER\n\nbody\n\n— signoff
//   if not:      HEADER\n\nbody
//
// "verbose" is a hint, not a rule: a reply is verbose if
// it has more than three body lines OR if the body is
// long (we count runes, not bytes, to play nicely with
// Cyrillic). Pass verbose=true to force the footer (for
// error messages, where the sign-off "regretfully yours"
// helps frame the apology); pass verbose=false to
// suppress it (for one-liner confirmations like
// "Rule added").
//
// Compose is the ONLY public envelope builder. v1's
// welcome()/greetingForNewChat()/etc. are kept as thin
// wrappers around Compose for backward compatibility
// with the existing personality tests.
func Compose(lang, context, body string, verbose bool) string {
	header := headerFor(lang, context)
	if body == "" {
		// Defensive: if the caller passed an empty body
		// (which shouldn't happen, but...), just return
		// the header. The header alone is the butler's
		// "I'm here" — a presence announcement.
		return header
	}
	out := header + headerFooterSeparator + body
	if verbose {
		out += headerFooterSeparator + footerFor(lang)
	}
	return out
}

// verboseForBody is the default verbose heuristic. It
// returns true if the body has more than 3 non-empty
// lines or if its total length (in runes) is over 300.
// Both thresholds are below Telegram's 4096-char hard
// cap with headroom for the wrapper.
func verboseForBody(body string) bool {
	if len([]rune(body)) > 300 {
		return true
	}
	nonEmpty := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
	}
	return nonEmpty > 3
}

// ComposeDefault is the most-common call site: caller
// only specifies lang/context/body and ComposeDefault
// picks verbose vs not based on the body length. Saves
// every command from writing
//   return Compose(lang, ctx, body, verboseForBody(body))
// and instead they write
//   return ComposeDefault(lang, ctx, body)
func ComposeDefault(lang, context, body string) string {
	return Compose(lang, context, body, verboseForBody(body))
}

// trimForTelegram is unchanged from v1. The body budget
// is 3800 chars (Telegram's hard cap is 4096; the
// headroom covers the header/footer wrapper, code-block
// backticks, and any inline-keyboard markup the
// notifier might add on top).
//
// The trim happens at the last "\n\n" before the cap so
// we don't cut a sentence in half. If there's no
// double-newline in the budget, we hard-trim at the
// cap. Either way the returned string is at or under
// 3800 chars (the truncation marker is included in that
// budget; we cut the body to make room for it).
//
// 2026-07-14: Этап 14 v9 — the marker now appends a
// single line saying "the rest was cut; ask for a
// narrower slice". The marker is rendered through
// bot.trim.marker (the same key v1 used) so the
// translation still applies, but we no longer treat
// the marker as a header; it's just a trailing line.
func trimForTelegram(s string) string {
	const budget = 3800
	if len(s) <= budget {
		return s
	}
	marker := i18n.T(i18n.LangEN, "bot.trim.marker")
	bodyBudget := budget - len(marker)
	if bodyBudget < 0 {
		bodyBudget = 0
	}
	cut := s[:bodyBudget]
	if idx := strings.LastIndex(cut, "\n\n"); idx > 200 {
		cut = cut[:idx]
	}
	return cut + marker
}

// --- v1 helpers, kept stable for backward compatibility ---

// gatekeeperSign returns the v1 one-line prefix on the
// welcome message. New code should use headerFor("welcome").
// Kept because the existing /start command and its tests
// pin this exact line.
//
// 2026-07-14: Этап 14 v5 — translated. v9 keeps the
// translation but re-routes through the catalog key
// `bot.personality.gatekeeper_sign` (the same one v5
// used). New commands should NOT call this — they
// should call headerFor directly.
func gatekeeperSign(lang string) string {
	return i18n.T(lang, "bot.personality.gatekeeper_sign")
}

// roleWardenOfSelf is the v1 second line of the welcome.
// Kept stable; new commands should use headerFor.
func roleWardenOfSelf(lang string) string {
	return i18n.T(lang, "bot.personality.role_warden_of_self")
}

// welcome formats the first line of every greeting.
// v1 API. Use headerFor("welcome", lang) for new code.
func welcome(lang, name string) string {
	if name == "" {
		return gatekeeperSign(lang)
	}
	return fmt.Sprintf("%s  %s  ◈  %s", gatekeeperSign(lang), name, roleWardenOfSelf(lang))
}

// gateHeader returns the help-codex header line. v1
// API. Use headerFor("codex", lang) for new code.
func gateHeader(lang string) string {
	return fmt.Sprintf("%s  %s", butlerSigil, i18n.T(lang, "bot.personality.gate_header"))
}

// greetingForNewChat builds the v1 welcome card. v9
// wraps the same content in ComposeDefault so the
// reply has the new envelope (header / body / optional
// footer).
func greetingForNewChat(lang, botUsername string) string {
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "bot.welcome.sealed_intro") + "\n")
	sb.WriteString("\n")
	sb.WriteString("*" + i18n.T(lang, "bot.welcome.bind_title") + "*\n")
	sb.WriteString("` 1.`  " + i18n.T(lang, "bot.welcome.bind_step1") + "\n")
	sb.WriteString("` 2.`  " + i18n.T(lang, "bot.welcome.bind_step2") + "\n")
	sb.WriteString("` 3.`  " + i18n.T(lang, "bot.welcome.bind_step3") + "\n")
	sb.WriteString("          " + i18n.T(lang, "bot.welcome.bind_command") + "\n")
	if botUsername != "" {
		sb.WriteString("\n" + i18n.Tf(lang, "bot.welcome.open_on_phone", botUsername) + "\n")
	}
	sb.WriteString("\n*" + i18n.T(lang, "bot.welcome.after_bound_title") + "*\n")
	sb.WriteString(infoWax + "  " + i18n.T(lang, "bot.welcome.after_bound_my_status") + "\n")
	sb.WriteString(infoWax + "  " + i18n.T(lang, "bot.welcome.after_bound_add_rule") + "\n")
	sb.WriteString(infoWax + "  " + i18n.T(lang, "bot.welcome.after_bound_my_rules") + "\n")
	sb.WriteString("\n" + i18n.T(lang, "bot.welcome.key_ttl") + "\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.help_pointer"))
	return ComposeDefault(lang, "welcome", sb.String())
}

// greetingForReturningUser is the v1 short welcome for
// already-bound users. v9 wraps it in ComposeDefault.
func greetingForReturningUser(lang, name string) string {
	var sb strings.Builder
	sb.WriteString(welcome(lang, name) + "\n")
	sb.WriteString("\n")
	sb.WriteString(successWax + "  " + i18n.T(lang, "bot.welcome.known_title") + "\n")
	sb.WriteString("\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.known_help") + "\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.known_help_detail") + "\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.known_version"))
	return ComposeDefault(lang, "welcome", sb.String())
}

// sectionLabel is the v1 bold-prefix used inside long
// bodies. Kept stable. Use sparingly in v2 — the new
// envelope already carries the topic, and double-labelling
// (header + sectionLabel) is loud.
func sectionLabel(icon, title string) string {
	return fmt.Sprintf("*%s %s*", icon, title)
}

// codexLine is the v1 monospace key/value pair.
func codexLine(icon, key, value string) string {
	if value == "" {
		return fmt.Sprintf("%s  `%s`", icon, key)
	}
	return fmt.Sprintf("%s  `%s` — %s", icon, key, value)
}

// iconForCaller picks the v1 codex icon.
func iconForCaller(isAdmin bool) string {
	if isAdmin {
		return "✦"
	}
	return "◈"
}

// ruleBreak is the v1 inter-section rule. Available to
// long bodies that need a divider. Compose() doesn't
// insert these itself; callers add them inside the body
// string before passing it to Compose.
func ruleBreak() string {
	return thinRule + "\n"
}
