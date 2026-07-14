package telegram

// 2026-07-14: Этап 14 v4 — bot UX redesign.
// 2026-07-14: Этап 14 v5 — bot i18n. The translatable strings
// (gatekeeper sign, role line, codex header, all the
// greeting lines) now live in the i18n catalog. The
// language-independent symbols (sigil, thin rule, the four
// wax emojis) stay as Go constants.

import (
	"fmt"
	"strings"

	"skygate/internal/i18n"
)

// Sigil and decorative rules used across the bot. Defining
// them as constants (not literals scattered through reply
// functions) means a future redesign changes one place.
const (
	// gateSigil is the warden's mark, used in the help codex
	// header and on the welcome line. It's a single
	// multi-byte rune so the column width stays consistent
	// across monospace renderings.
	gateSigil = "✦"

	// thinRule is the separator between sections of a long
	// reply. It is exactly 18 characters wide, which is
	// wide enough to look like a rule in Telegram's
	// monospace rendering without wrapping on phones.
	thinRule = "─────────────────"

	// successWax, lockWax, infoWax, errorWax — small emoji
	// used to colour the leading line of a reply so the
	// reader can see at a glance whether this is a "yes"
	// ("the gate opens"), "no" ("the gate stays shut"),
	// or "look closer" message.
	successWax = "🜍"
	lockWax    = "🜔"
	infoWax    = "✧"
	errorWax   = "🝆"
)

// gatekeeperSign returns the one-line prefix on the welcome
// message in the given language. Once per session the user
// sees it; thereafter the "welcome back" line is used.
//
// 2026-07-14: Этап 14 v5 — was a constant, now a function
// of lang. The English version is "⟡  Warden of the
// Threshold"; the Russian is "⟡  Хранитель Порога".
func gatekeeperSign(lang string) string {
	return i18n.T(lang, "bot.personality.gatekeeper_sign")
}

// roleWardenOfSelf is the second line of the welcome for
// already-bound users. It's a one-line signature that the
// reply system prints immediately after `welcome(name)` so
// every bound chat sees the same opening frame and the
// "warden" is the consistent brand.
//
// 2026-07-14: Этап 14 v5 — translated.
func roleWardenOfSelf(lang string) string {
	return i18n.T(lang, "bot.personality.role_warden_of_self")
}

// welcome formats the first line of every greeting. Pass the
// user's portal name (without @) for a personalised sign-on.
// When the caller is anonymous (pre-login) we just print the
// sigil + role; the full welcome is built by welcomeGreeting
// below.
func welcome(lang, name string) string {
	if name == "" {
		return gatekeeperSign(lang)
	}
	return fmt.Sprintf("%s  %s  ◈  %s", gatekeeperSign(lang), name, roleWardenOfSelf(lang))
}

// sectionLabel is the bold line that starts a section in a
// multi-section reply (e.g. /help, /nodes, /audit). The double
// asterisk in the Telegram sense renders as bold in clients
// that support markdown; clients without markdown show the
// asterisks literally. We accept that as a graceful
// degradation — the structure is still readable.
func sectionLabel(icon, title string) string {
	return fmt.Sprintf("*%s %s*", icon, title)
}

// ruleBreak returns a single thinRule on its own line, used
// between sections of a long reply. The trailing newline
// gives a blank line after the rule, which is what the
// codex layout calls for.
func ruleBreak() string {
	return thinRule + "\n"
}

// gateHeader returns the help-codex header line. We use
// this in /help and in /version (the only reply that
// shouldn't open with the warden sign).
func gateHeader(lang string) string {
	return fmt.Sprintf("%s  %s", gateSigil, i18n.T(lang, "bot.personality.gate_header"))
}

// trimForTelegram trims a reply to the 3800-char budget.
// Telegram's hard cap is 4096; we leave ~300 chars of
// headroom for the wrapping ``` and any future
// inline-keyboard markup that the notifier might add on
// top of the reply text.
//
// The trim happens at the last "\n\n" before the cap so we
// don't cut a sentence in half. If there's no double-newline
// in the budget, we hard-trim at the cap. Either way the
// returned string is at or under 3800 chars (the truncation
// marker is included in that budget; we cut the body to
// make room for it).
//
// 2026-07-14: Этап 14 v5 — the marker is now a catalog key
// (bot.trim.marker) so RU/EN users see a "truncated" line
// in their own language. The signature is still
// parameterless; the catalog default (ru) is used. Callers
// that need a language-specific marker should compose
// their own. trimForTelegram is called from per-reply
// helpers that already returned a fully-translated string;
// the marker is the only thing that doesn't carry lang,
// and "we cut you off, ask for a narrower slice" is a
// meta-message, not a domain string.
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

// codexLine renders a key/value pair in the codex style
// used by /help, /version, /status, etc. The key is in
// monospace, the value follows after a dash and a space.
// The icon parameter is a small symbol (✦, ◈, ✧, 🜍, 🜔) that
// hints at the section's role. Keeping the format uniform
// means the user's eye learns to scan a codex line
// regardless of which reply they got.
func codexLine(icon, key, value string) string {
	if value == "" {
		return fmt.Sprintf("%s  `%s`", icon, key)
	}
	return fmt.Sprintf("%s  `%s` — %s", icon, key, value)
}

// isAdmin decides which icon to lead a codex line with,
// given whether the caller is an admin. Used by /status and
// /version where the same data line can be "warden scope"
// (admin-only) or "warden-of-self scope" (user).
//
// Returns a single rune. Stable so tests can pin it.
func iconForCaller(isAdmin bool) string {
	if isAdmin {
		return "✦" // warden commands
	}
	return "◈" // warden's own data
}

// greetingForNewChat is the message returned by /start with
// no args (i.e. a brand-new chat that has never bound). The
// voice is "butler at the gate" — formal, slightly
// theatrical, but the steps are still concrete enough to
// follow.
//
// Kept here (not in commands_login.go) so the voice lives in
// one file and the test in personality_test.go can pin every
// line of the message.
//
// 2026-07-14: Этап 14 v5 — the entire body is composed from
// i18n.T(lang, "bot.welcome.*") calls. The lang is the
// dispatcher's resolution (env.Lang); for an unbound chat
// the dispatcher falls back to LangFromTelegramCode(env's
// TelegramLangCode) so the first /start greets the user in
// their Telegram client language.
//
// args:
//   - lang: the bot's active language for this chat
//     ("ru" or "en")
//   - botUsername: the @handle of the bot (e.g.
//     "skygatepj_bot"), used to give the user a deep link
//     they can tap on mobile. Empty string drops the line.
func greetingForNewChat(lang, botUsername string) string {
	var sb strings.Builder
	sb.WriteString(gatekeeperSign(lang) + "\n")
	sb.WriteString("\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.sealed_intro") + "\n")
	sb.WriteString("\n")
	sb.WriteString("*" + i18n.T(lang, "bot.welcome.bind_title") + "*\n")
	sb.WriteString("` 1.`  " + i18n.T(lang, "bot.welcome.bind_step1") + "\n")
	sb.WriteString("` 2.`  " + i18n.T(lang, "bot.welcome.bind_step2") + "\n")
	sb.WriteString("` 3.`  " + i18n.T(lang, "bot.welcome.bind_step3") + "\n")
	sb.WriteString("          " + i18n.T(lang, "bot.welcome.bind_command") + "\n")
	sb.WriteString("\n")
	if botUsername != "" {
		sb.WriteString(i18n.Tf(lang, "bot.welcome.open_on_phone", botUsername) + "\n")
		sb.WriteString("\n")
	}
	sb.WriteString("*" + i18n.T(lang, "bot.welcome.after_bound_title") + "*\n")
	sb.WriteString(infoWax + "  " + i18n.T(lang, "bot.welcome.after_bound_my_status") + "\n")
	sb.WriteString(infoWax + "  " + i18n.T(lang, "bot.welcome.after_bound_add_rule") + "\n")
	sb.WriteString(infoWax + "  " + i18n.T(lang, "bot.welcome.after_bound_my_rules") + "\n")
	sb.WriteString("\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.key_ttl") + "\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.help_pointer"))
	return sb.String()
}

// greetingForReturningUser is the message returned by /start
// when env.Username is non-empty (i.e. the chat is already
// bound). Short — the binding is the contract, no need to
// re-explain. Directs at /help for the full command surface.
//
// 2026-07-14: Этап 14 v5 — translated. welcome() now takes
// the lang as the first arg so the gatekeeper sigil + role
// line render in the user's language.
func greetingForReturningUser(lang, name string) string {
	var sb strings.Builder
	sb.WriteString(welcome(lang, name) + "\n")
	sb.WriteString("\n")
	sb.WriteString(successWax + "  " + i18n.T(lang, "bot.welcome.known_title") + "\n")
	sb.WriteString("\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.known_help") + "\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.known_help_detail") + "\n")
	sb.WriteString(i18n.T(lang, "bot.welcome.known_version"))
	return sb.String()
}
