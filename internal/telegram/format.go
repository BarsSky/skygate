package telegram

import (
	"fmt"
	"strings"
)

// format.go — HTML formatting helpers for bot replies.
//
// 2026-07-16: v0.16.x — "more HTML" pass. Bot messages were
// either plain prose or one big <pre> block; the operator
// asked for more structure (visual hierarchy, aligned
// key/value pairs, tabular monospace, section dividers)
// without sacrificing the butler-voice envelope.
//
// Telegram's HTML subset is small but enough:
//   <b>, <i>, <u>, <s>, <strike>, <del>
//   <code>          — inline monospace
//   <pre>           — block monospace (preserves spaces)
//   <a href="...">  — link
//   <tg-spoiler>    — hidden until tapped
// NO div/span, NO CSS, NO tables, NO class= attrs. So
// "alignment" must be done by padding strings in <pre>
// blocks (Telegram's <pre> uses a fixed-pitch font).
//
// This file is a tiny helper layer so individual reply
// functions don't reinvent the same HTML escaping +
// spacing logic. The output is parse_mode=HTML-safe —
// user-controlled strings (usernames, keys, hostnames)
// are run through escapeHTML before being interpolated.

// Field renders a "Label: value" pair as a single line.
// The label is bold, the value is inline-monospace, the
// pair is separated by ": ". Used for one-key-per-line
// lists like /my_status, /version, /exit_nodes_health
// state rows.
//
// Example:
//
//	Field("rules",   "12 / ∞")
//	Field("devices", "3")
//	Field("last acl", "#5")
//
// → (in HTML):
//
//	<b>rules:</b> <code>12 / ∞</code>
//	<b>devices:</b> <code>3</code>
//	<b>last acl:</b> <code>#5</code>
//
// (the caller joins them with "\n" to render multiple
// fields as separate lines in the reply body).
func Field(label, value string) string {
	return "<b>" + escapeHTML(label) + ":</b> " + Code(value)
}

// Fieldf is Field with a fmt-style value (handy for
// numbers, durations, %v-style rendering).
func Fieldf(label, format string, args ...any) string {
	return "<b>" + escapeHTML(label) + ":</b> " + Code(fmt.Sprintf(format, args...))
}

// Code wraps a value in <code>…</code> for inline
// monospace. The value is HTML-escaped (so e.g. a
// hostname with a "&" doesn't break the surrounding
// <code>…</code>).
func Code(value string) string {
	return "<code>" + escapeHTML(value) + "</code>"
}

// Pre renders one or more lines in a <pre> block so the
// font is monospace and the column alignment survives
// (Telegram's <pre> uses a fixed-pitch font on all
// clients). Empty lines are preserved (so a 2-line
// table with a blank between groups renders with a
// blank row).
//
// The body is HTML-escaped (so e.g. a value with a "<"
// or "&" doesn't break the surrounding <pre>). If you
// need to embed <b> / <i> tags inside a <pre> (a header
// row, a separator line), use PreRaw with a
// pre-escaped body, or build the <pre>…</pre> string
// yourself.
func Pre(body string) string {
	return "<pre>" + escapeHTML(body) + "</pre>"
}

// PreRaw is the un-escaped variant of Pre. Pass an
// already-escaped body when you need to embed inline
// HTML tags inside the <pre> block (e.g. "<b>ID</b>"
// header row, "<i>────</i>" separator). The caller is
// responsible for HTML-escaping any user-controlled
// strings before passing them in.
//
// 2026-07-16: v0.16.x — added so /audit can render a
// bold header row + an italic rule line inside a
// monospace block, giving a "real table" feel that
// just <pre> alone doesn't.
func PreRaw(body string) string {
	return "<pre>" + body + "</pre>"
}

// PreLines is the join-of-lines variant of Pre. Each
// argument becomes one line; an empty string becomes a
// blank line. Newlines inside a single arg are NOT
// touched (caller can use \n explicitly). The body
// IS HTML-escaped (use PreLinesRaw if you need inline
// HTML).
func PreLines(lines ...string) string {
	return Pre(strings.Join(lines, "\n"))
}

// PreLinesRaw is the un-escaped variant of PreLines.
// Same join semantics as PreLines but no escapeHTML
// pass on the body.
func PreLinesRaw(lines ...string) string {
	return PreRaw(strings.Join(lines, "\n"))
}

// Section renders a section divider with a title. The
// title is italic + a horizontal rule, so it reads as
// "──────── section title ────────" in the bot output.
// Used to break a long reply into named parts (e.g.
// /my_status: "Summary" → "Devices" → "Recent ACL").
//
// Example:
//
//	Section("Summary")
//	Field("rules",   "12 / ∞")
//	Section("Recent ACL")
//	Field("last acl", "#5")
//
// → (in HTML):
//
//	<i>──────── Summary ────────</i>
//	<b>rules:</b> <code>12 / ∞</code>
//	<i>──────── Recent ACL ────────</i>
//	<b>last acl:</b> <code>#5</code>
func Section(title string) string {
	// 7 em-dashes on each side = a visible horizontal
	// rule in Telegram's proportional font (roughly
	// 35-40 chars wide on a phone, which fits a
	// 3-4 word section title).
	const dashes = "───────"
	return "<i>" + dashes + " " + escapeHTML(title) + " " + dashes + "</i>"
}

// Header renders a top-level heading for a reply. Bold
// + uppercase, the way a section title looks in
// Markdown. Used at the top of a multi-section reply.
//
// Example:
//
//	Header("Devices")
//	PreLines(...)
//
// → <b>DEVICES</b>
func Header(title string) string {
	return "<b>" + escapeHTML(strings.ToUpper(title)) + "</b>"
}

// BulletList renders each arg as a bullet item, with a
// Unicode "•" prefix. Used for short lists where a
// <pre> block would be overkill. The value is escaped.
//
// Example:
//
//	BulletList("karolina (relay, emilia)",
//	          "sharlotta (relay, RU)")
//
// → • karolina (relay, emilia)
//	 • sharlotta (relay, RU)
func BulletList(items ...string) string {
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString("• ")
		sb.WriteString(escapeHTML(it))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

// HeaderLine is a single-line section divider that
// reads as "── Section name ──". Useful when the reply
// is one-paragraph and a full Section() with a second
// line would be too heavy. Italic + 2 em-dashes per
// side.
func HeaderLine(title string) string {
	return "<i>── " + escapeHTML(title) + " ──</i>"
}
