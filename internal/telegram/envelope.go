package telegram

// 2026-07-16: Этап 14 v15 (v0.15.2) — butler-voice envelope helper.
//
// Every bot reply goes through butlerEnvelope (or one of its
// thin wrappers) so the message shape is consistent across
// commands. The envelope is "gate-style": a line-art divider
// with the Skygate wordmark opens and closes the reply,
// bracketing the actual content. This makes bot messages
// visually distinct from system notifications and from
// user-typed text in the same chat.
//
// Visual structure:
//
//	🪶 ═══ Skygate ═══
//	Добрый вечер, <name>.
//
//	<b>title</b>
//	<blockquote>subheader</blockquote>
//	<body>
//	<i>footer</i>
//
//	[inline_keyboard]
//
//	═══ — Ваш Дворецкий ═══
//
// All replies are parse_mode=HTML. Caller is responsible for
// HTML-escaping any user-controlled string (the
// platform_picker.escapeHTML helper covers the keys/IDs we
// substitute; if a new reply substitutes a username or a
// hostname, run it through escapeHTML first).
//
// Length budget: ≤80 words per reply (best practice for
// mobile Telegram — see docs/bot-message-style-v0.15.2.md).

import (
	"strings"
	"time"

	"skygate/internal/i18n"
)

// Gate envelope constants — single source of truth for the
// "🪶 ═══ Skygate ═══ … ═══ — Ваш Дворецкий ═══" frame
// that wraps every bot reply. Both butlerEnvelope (here in
// envelope.go) and Compose (in personality.go, the v1 path
// for non-HTML commands) use these constants. If the operator
// wants to rebrand (e.g. replace "Skygate" with "Skygate-2"
// or swap the 🪶 for 🚪), it's a one-line change here.
//
// 2026-07-16: v0.15.3 — extracted from the duplicated
// "🪶 ═══ Skygate ═══" string that was hardcoded in
// both envelope.go and personality.go. Now there's a single
// source of truth.
const (
	// gateIcon is the butler feather that opens every
	// bot reply header. Single emoji, single character
	// class (so the column width is consistent in
	// monospace renderings).
	gateIcon = "🪶"

	// gateLine is the wordmark divider that brackets
	// the reply: "═══ Skygate ═══". The "═══" (U+2550
	// box-drawing) is the visual gate metaphor —
	// "Skygate" = "heavenly gate", and every reply
	// walks in through one.
	gateLine = "═══ Skygate ═══"
)

// GateHeader is the full top line of the gate envelope:
// "<gateIcon> <gateLine>\n<topic>". Used by butlerEnvelope
// (the HTML path) and Compose (the plain-text path). The
// topic comes from bot.header.<context> in the i18n catalog
// (e.g. "The Registry", "The Codex"); falls back to the
// literal context string if the catalog key is missing.
//
// 2026-07-16: v0.15.3.
func GateHeader(lang, context string) string {
	topic := GateTopic(lang, context)
	return gateIcon + " " + gateLine + "\n" + topic
}

// GateTopic extracts the per-context topic label from the
// v1 bot.header.<context> catalog keys. Falls back to a
// generic label if the catalog key is missing.
//
// 2026-07-16: v0.15.3 — extracted from the inlined version
// in personality.go so envelope.go can use it too (e.g. for
// /help, /version, and any other plain-text reply that
// doesn't need a full butlerEnvelope).
func GateTopic(lang, context string) string {
	if context == "" {
		return i18n.T(lang, "bot.envelope.greeting.afternoon")
	}
	key := "bot.header." + context
	if v := i18n.T(lang, key); v != key {
		return v
	}
	return context
}

// GateFooter is the matching closing line of the gate
// envelope: "═══ — <signoff> ═══". Signoff comes from
// bot.envelope.signoff in the i18n catalog (RU: "Ваш
// Дворецкий", EN: "Your butler"; pinned in v0.10.12
// signoff D).
//
// 2026-07-16: v0.15.3.
func GateFooter(lang string) string {
	return signoffGateLine(i18n.T(lang, "bot.envelope.signoff"))
}

// signoffGateLine is the matching closing "═══ — Signoff ═══"
// divider. The signoff text is the only thing that varies
// (RU/EN).
func signoffGateLine(signoff string) string {
	return "═══ — " + signoff + " ═══"
}

// butlerEnvelopeOpts is a thin options struct so we can
// extend (WithSignoff, WithTitle, WithNoGreeting, ...) without
// growing the butlerEnvelope signature past 6 positional args.
//
// Today only one field is set in practice (skipGreeting for
// admin /sync_nodes et al. where the recipient is not a
// single person). The rest is future-proofing.
type butlerEnvelopeOpts struct {
	skipGreeting bool
	// skipSignoff is set for trivia acknowledgements
	// (1-line "Готово.") where the signoff would be noise.
	skipSignoff bool
	// icon overrides the default 🪶 (used by security /
	// exit-node / preauth commands that have a more
	// specific glyph). The icon precedes the gate line.
	icon string
}

// ButlerOpt returns the option (functional-options pattern).
// Future: WithIcon("⚙️"), WithNoSignoff(), WithNoGreeting().
type ButlerOpt func(*butlerEnvelopeOpts)

// WithNoSignoff returns an option that drops the closing
// "═══ — Ваш Дворецкий ═══" line. Use only for 1-line
// acknowledgements where the signoff would be visual noise.
func WithNoSignoff() ButlerOpt {
	return func(o *butlerEnvelopeOpts) { o.skipSignoff = true }
}

// WithNoGreeting returns an option that drops the "Добрый
// вечер, <name>." line. Use for admin broadcast commands
// that don't speak to a specific user.
func WithNoGreeting() ButlerOpt {
	return func(o *butlerEnvelopeOpts) { o.skipGreeting = true }
}

// WithIcon returns an option that replaces the default 🪶
// with a more specific glyph (⚙️ settings, 🛡️ security, 🛰️
// exit-node, 🔑 preauth, 📋 plain copy, etc.).
func WithIcon(icon string) ButlerOpt {
	return func(o *butlerEnvelopeOpts) { o.icon = icon }
}

// butlerEnvelope assembles a reply in the Skygate butler
// voice with the gate-style divider. All four body strings
// are optional — an empty string drops that line entirely.
//
// Required for the standard butler-voice envelope:
//   - title: <b>...</b> heading (1 line, what happened)
//   - subheader: <blockquote>...</blockquote> context
// Optional:
//   - body: raw HTML (for <pre>/<code>/lists/etc.)
//   - footer: <i>...</i> next-steps hint
//
// lang should be i18n.LangRU or i18n.LangEN; signoff is
// resolved from lang automatically.
//
// env may be the zero BotEnv (no greeting available); the
// envelope will still produce a valid reply, just without
// the "Добрый вечер, …" line.
func butlerEnvelope(lang, envUsername string, title, subheader, body, footer string, opts ...ButlerOpt) string {
	o := &butlerEnvelopeOpts{icon: "🪶"}
	for _, opt := range opts {
		opt(o)
	}

	var sb strings.Builder

	// Header: 🪶 ═══ Skygate ═══
	// Header: <icon> <gateLine>\n
	// 2026-07-16: v0.15.3 — uses the per-command icon
	// (o.icon) followed by the canonical gateLine from
	// the same constants Compose() uses. Single source
	// of truth for the header shape.
	if o.icon != "" {
		sb.WriteString(o.icon)
		sb.WriteByte(' ')
	}
	sb.WriteString(gateLine)
	sb.WriteByte('\n')

	// Greeting: time-of-day + name (skipped when there's no
	// username or the caller asked WithNoGreeting).
	if !o.skipGreeting && envUsername != "" {
		sb.WriteString(greetingFor(lang, time.Now()))
		sb.WriteString(", ")
		sb.WriteString(escapeHTML(envUsername))
		sb.WriteString(".\n")
	}
	sb.WriteByte('\n')

	// Body. Each line is independent — empty lines are
	// skipped so the envelope stays tight.
	if title != "" {
		sb.WriteString("<b>")
		sb.WriteString(title)
		sb.WriteString("</b>\n")
	}
	if subheader != "" {
		sb.WriteString("<blockquote>")
		sb.WriteString(subheader)
		sb.WriteString("</blockquote>\n")
	}
	if body != "" {
		sb.WriteString(body)
		// Body is followed by a blank line before the
		// footer-hint only when the body doesn't end with
		// one already. Keeps the rendered output compact
		// for short bodies (a single <pre>…</pre>) without
		// a double blank.
		if !strings.HasSuffix(body, "\n") {
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}
	if footer != "" {
		sb.WriteString("<i>")
		sb.WriteString(footer)
		sb.WriteString("</i>\n")
	}

	// Footer: ═══ — Signoff ═══
	// 2026-07-16: v0.15.3 — uses GateFooter() (the same
	// function Compose() uses) so any future change to
	// the closing-line shape lands in one place.
	if !o.skipSignoff {
		sb.WriteByte('\n')
		sb.WriteString(GateFooter(lang))
		sb.WriteByte('\n')
	}

	return sb.String()
}

// greetingFor returns the time-of-day greeting for the
// current hour in lang ("Добрый день, skyadmin" /
// "Good afternoon, skyadmin"). Buckets:
//   05:00–10:59  → morning
//   11:00–16:59  → afternoon
//   17:00–21:59  → evening
//   22:00–04:59  → night
func greetingFor(lang string, t time.Time) string {
	h := t.Hour()
	var key string
	switch {
	case h >= 5 && h < 11:
		key = "bot.envelope.greeting.morning"
	case h >= 11 && h < 17:
		key = "bot.envelope.greeting.afternoon"
	case h >= 17 && h < 22:
		key = "bot.envelope.greeting.evening"
	default:
		key = "bot.envelope.greeting.night"
	}
	// Fall back to EN if the lang's catalog is missing
	// the greeting (covers the edge case of a partial
	// i18n catalog during a deploy).
	if v := i18n.T(lang, key); v != key {
		return v
	}
	return i18n.T(i18n.LangEN, key)
}

// signoffFor returns the butler signoff for lang.
// RU: "Ваш Дворецкий", EN: "Your butler".
// Pinned in v0.10.12 (signoff variant D).
func signoffFor(lang string) string {
	if v := i18n.T(lang, "bot.envelope.signoff"); v != "bot.envelope.signoff" {
		return v
	}
	return i18n.T(i18n.LangEN, "bot.envelope.signoff")
}
