// B391 (2026-10-25, TD-34) — ONE COMPOSER FOR EVERY OUTGOING ALERT.
//
// Measured before this file existed: all 61 outgoing triggers built their text with
// `fmt.Sprintf` at the CALL SITE, in English only, and sent it through the
// reply-shaped transport, which had two consequences nobody had noticed:
//
//   - no `parse_mode` was set, so the body arrived as literal text and any markdown
//     the author wrote showed up as characters;
//   - the body was wrapped in a ``` fence "so it renders monospaced" — which for a
//     multi-line alert with backticks inside (the `sudo docker prune` hint, the
//     `rule_ids=[…]` line) produced broken, unreadable output.
//
// The composer gives an alert three things it never had:
//
//	severity   one vocabulary (info / warn / critical / recovery) → one emoji, so a
//	           critical alert LOOKS different from a routine one in a chat list;
//	i18n       the title comes from the catalogue (`TitleKey`), in the operator's
//	           language, instead of an English sentence baked into a handler;
//	a length   Telegram rejects >4096 characters, and the old path simply let the
//	policy     API refuse the message — a long alert was LOST. Here the details are
//	           truncated with an explicit marker that says how much was dropped.
//
// Callers that still pass a raw string keep working (SendAlert escapes and carries
// it), and the translation of the remaining trigger texts is tracked as a ratchet in
// scripts/check_b391_alert_composer.sh — a count that may only go down.
package telegram

import (
	"fmt"
	"html"
	"strings"
	"time"

	"skygate/internal/i18n"
)

// Severity is the one vocabulary an outgoing alert may use.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarn     Severity = "warn"
	SeverityCritical Severity = "critical"
	SeverityRecovery Severity = "recovery"
)

// severityEmoji maps the severity to the glyph the operator sees first. It is
// deliberately the SAME emoji the legacy texts used ad hoc (❌ / ⚠️ / ✅) so a chat
// that already filters on them keeps working, plus ℹ️ for the neutral case.
func severityEmoji(s Severity) string {
	switch s {
	case SeverityWarn:
		return "⚠️"
	case SeverityCritical:
		return "❌"
	case SeverityRecovery:
		return "✅"
	default:
		return "ℹ️"
	}
}

// SeverityEmoji is the exported view for callers that build a title themselves.
func SeverityEmoji(s Severity) string { return severityEmoji(s) }

// AlertFact is one label/value pair under the title. Values are escaped, so a
// hostname or a rule list cannot inject markup.
type AlertFact struct {
	Label string
	Value string
}

// Alert is a composed outgoing message.
//
// TitleKey wins over Title: the catalogue is the source of truth for a translated
// title, and a caller that passes a key gets the operator's language for free.
type Alert struct {
	Severity Severity
	// TitleKey is a catalogue key (`alert.disk.title_warn`, …). When set, the
	// title is i18n.Tf(lang, TitleKey, TitleArgs...) and Title is ignored.
	TitleKey  string
	TitleArgs []any
	// Title is a literal fallback (already localized by the caller, or a name).
	Title string
	Facts []AlertFact
	// Details is free-form context (a stack trace, a list of ids, a command to
	// run). Rendered as a <pre> block and truncated first when the body is long.
	Details string
	// DedupKey suppresses a repeat of the same event inside DedupTTL (default
	// 10 minutes). Empty = never dedup. Use it for anything a flapping component
	// can emit on every tick — the disk monitor keys on the severity level.
	DedupKey string
	DedupTTL time.Duration
	Lang     string
}

// maxAlertBody is the budget for ONE alert body, in runes.
//
// Telegram's hard limit is 4096 characters for a text message; we keep a margin for
// the "[#N] " prefix, the severity word and the truncation marker so the API never
// refuses a payload we could have trimmed ourselves (the pre-B391 behaviour lost
// exactly those messages).
const maxAlertBody = 3500

// alertTruncationMarker appends the "what was dropped" note. It is part of the
// length policy: a truncated alert must SAY it was truncated, or an operator reads
// half a stack trace as the whole story.
func alertTruncationMarker(lang string, dropped int) string {
	return i18n.Tf(lang, "alert.truncated", dropped)
}

// ComposeAlert renders an Alert as Telegram HTML.
//
// Every caller-supplied string (title, labels, values, details) is ESCAPED; the only
// tags in the output are the ones this function writes. The body is then bounded by
// maxAlertBody, trimming Details first (the part whose loss is least surprising) and
// SAYING how much was dropped — a silently truncated alert reads as the whole story.
func ComposeAlert(a Alert) string {
	lang := a.Lang
	title := a.Title
	if a.TitleKey != "" {
		title = i18n.Tf(lang, a.TitleKey, a.TitleArgs...)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = i18n.T(lang, "alert.untitled")
	}

	var head strings.Builder
	fmt.Fprintf(&head, "<b>%s %s</b>", severityEmoji(a.Severity), html.EscapeString(title))
	for _, f := range a.Facts {
		if f.Label == "" && f.Value == "" {
			continue
		}
		fmt.Fprintf(&head, "\n<b>%s:</b> <code>%s</code>",
			html.EscapeString(f.Label), html.EscapeString(f.Value))
	}

	details := a.Details
	dropped := 0
	for {
		body := head.String()
		if details != "" {
			body += "\n<pre>" + html.EscapeString(details) + "</pre>"
		}
		if dropped > 0 {
			body += "\n" + alertTruncationMarker(lang, dropped)
		}
		if n := len([]rune(body)); n <= maxAlertBody {
			return body
		}
		if details != "" {
			// Trim the details by the overrun plus a margin for the marker that
			// will replace them; at least one rune so the loop converges.
			dr := []rune(details)
			over := len([]rune(head.String())) + len(dr) + 7 - maxAlertBody
			cut := over + 48
			if cut < 1 {
				cut = 1
			}
			if cut >= len(dr) {
				dropped += len(dr)
				details = ""
				continue
			}
			details = string(dr[:len(dr)-cut])
			dropped += cut
			continue
		}
		// Even the title+facts are over budget: cut the whole body and say so.
		r := []rune(body)
		over := len(r) - maxAlertBody + 48
		return string(r[:len(r)-over]) + "\n" + alertTruncationMarker(lang, over)
	}
}

// ComposeAlertPlain is the same alert without markup, for a sink that cannot carry
// HTML (schedulers that only know the legacy SendAlert-shaped interface, a log line,
// the audit row). It carries the severity word so the information is not lost.
func ComposeAlertPlain(a Alert) string {
	lang := a.Lang
	title := a.Title
	if a.TitleKey != "" {
		title = i18n.Tf(lang, a.TitleKey, a.TitleArgs...)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = i18n.T(lang, "alert.untitled")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %s", severityEmoji(a.Severity), a.Severity, title)
	for _, f := range a.Facts {
		if f.Label == "" && f.Value == "" {
			continue
		}
		fmt.Fprintf(&b, "\n  %s: %s", f.Label, f.Value)
	}
	if a.Details != "" {
		b.WriteString("\n" + a.Details)
	}
	out := b.String()
	if r := []rune(out); len(r) > maxAlertBody {
		dropped := len(r) - maxAlertBody
		out = string(r[:maxAlertBody]) + "\n" + alertTruncationMarker(lang, dropped)
	}
	return out
}
