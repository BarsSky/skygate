package telegram

// 2026-07-14: Этап 14 v4 — bot UX redesign.
//
// The bot was previously speaking in terse, system-y prose
// ("Skygate status\nrules: 12\nusers: 1") which felt like a
// tool dump, not a personality. This file introduces a
// shared "butler gatekeeper" voice and the visual formatting
// primitives every reply should use. The voice is set in
// the project context: a magic portal that lets its
// "warden" operate the tailnet on the warden's behalf.
//
// Conventions applied throughout:
//
//   * Every reply is wrapped in a single backtick fence by
//     the notifier (see internal/telegram/notify.go
//     postToChat), so we DON'T add our own ``` — single
//     inline backticks for monospace values (chat ids,
//     rule ids, code snippets), bold for emphasis.
//
//   * 3800-char budget per reply (Telegram's 4096 minus
//     the wrapper + headroom). Replies longer than that get
//     trimmed via trimForTelegram in the per-reply helper.
//
//   * Sections separated by a thin rule "─────────────────"
//     so the bot's output looks like a magical codex entry,
//     not a CLI dump.
//
//   * Emojis are sparse but deliberate: ✦ for "warden
//     commands" (admin), ◈ for "warden's own data",
//     🜍 for "the gate stays silent" (errors), ⟡ for
//     "the gate opens" (success). The warden's sigil
//     appears in /help to anchor the brand.
//
// All formatters are public so tests can pin the exact
// text; if you change a string, expect a test failure in
// personality_test.go.

import (
	"fmt"
	"strings"
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

// gatekeeperSign is the one-line prefix on the welcome
// message. Once per session the user sees it; thereafter the
// "welcome back" line is used (see welcomeBack).
const gatekeeperSign = "⟡  Warden of the Threshold"

// welcome formats the first line of every greeting. Pass the
// user's portal name (without @) for a personalised sign-on.
// When the caller is anonymous (pre-login) we just print the
// sigil + role; the full welcome is built by welcomeGreeting
// below.
func welcome(name string) string {
	if name == "" {
		return gatekeeperSign
	}
	return fmt.Sprintf("%s  %s  ◈  %s", gatekeeperSign, name, roleWardenOfSelf)
}

// roleWardenOfSelf is the second line of the welcome for
// already-bound users. It's a one-line signature that the
// reply system prints immediately after `welcome(name)` so
// every bound chat sees the same opening frame and the
// "warden" is the consistent brand.
const roleWardenOfSelf = "you are Warden of your own devices"

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
func gateHeader() string {
	return fmt.Sprintf("%s  The Threshold's Codex", gateSigil)
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
func trimForTelegram(s string) string {
	const budget = 3800
	if len(s) <= budget {
		return s
	}
	const marker = "\n\n…(truncated — too much; ask for a narrower slice)"
	// Reserve room for the marker so the final string stays
	// at or under the budget.
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
// args:
//   - botUsername: the @handle of the bot (e.g.
//     "skygatepj_bot"), used to give the user a deep link
//     they can tap on mobile. Empty string drops the line.
//
// The number is intentionally short — three real steps and
// a one-line "what to expect when you bind" preview. The
// prior welcome message was the same length and the user
// said it felt thin; this one trades some of the long
// explanation for a couple of command examples the user can
// try once they bind, so the welcome doubles as a teaser of
// what /help will reveal.
func greetingForNewChat(botUsername string) string {
	var sb strings.Builder
	sb.WriteString(gatekeeperSign + "\n")
	sb.WriteString("\n")
	sb.WriteString("The gate is sealed. Speak your name into the\n")
	sb.WriteString("embers and the wardens will know you.\n")
	sb.WriteString("\n")
	sb.WriteString("*To bind this chat to your skygate account:*\n")
	sb.WriteString("` 1.`  Open skygate → `/my/telegram`\n")
	sb.WriteString("` 2.`  Click *Generate login key* — copy the key\n")
	sb.WriteString("` 3.`  Send it here:\n")
	sb.WriteString("          `/login <key>`\n")
	sb.WriteString("\n")
	if botUsername != "" {
		sb.WriteString(fmt.Sprintf("Or open me on your phone: @%s\n", botUsername))
		sb.WriteString("\n")
	}
	sb.WriteString("*Once bound* you can:\n")
	sb.WriteString(infoWax + "  `/my_status` — your own summary\n")
	sb.WriteString(infoWax + "  `/my_rules` — your own exit-rules\n")
	sb.WriteString(infoWax + "  `/add_rule <domain>` — let a domain through\n")
	sb.WriteString("\n")
	sb.WriteString("The key lives 5 minutes and burns on first use.\n")
	sb.WriteString("`/help` shows the full codex.")
	return sb.String()
}

// greetingForReturningUser is the message returned by /start
// when env.Username is non-empty (i.e. the chat is already
// bound). Short — the binding is the contract, no need to
// re-explain. Directs at /help for the full command surface.
func greetingForReturningUser(name string) string {
	var sb strings.Builder
	sb.WriteString(welcome(name) + "\n")
	sb.WriteString("\n")
	sb.WriteString(successWax + "  The gate knows you.\n")
	sb.WriteString("\n")
	sb.WriteString("`/help` — full codex of every command\n")
	sb.WriteString("`/help <command>` — details + examples for one command\n")
	sb.WriteString("`/version` — build, Go runtime, schema level")
	return sb.String()
}
