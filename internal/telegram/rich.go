// Package telegram — rich.go implements the B186 Bot API 10.1
// "Rich Messages" adapter. Telegram released Bot API 10.1 on
// 2026-06-11 with a new sendRichMessage method that accepts
// structured HTML/markdown/blocks (headings, lists, tables,
// <details>, <aside>, <tg-time>, <tg-map>, etc.) up to 32768
// chars / 500 blocks / 16 nesting levels. The butler-voice
// messages we currently send via parse_mode=HTML look the
// same in the old format (flat text with <b>/<i>/<code>/
// <pre>), but in 10.1 they can become proper structured
// documents — section headings, real lists, aligned tables,
// collapsible technical detail blocks.
//
// 2026-08-25 (B186): the operator asked to "adapt the bot
// messages to the new format" while keeping the butler
// style. The approach is a small, opt-in builder that
// produces the structured HTML the new endpoint expects,
// plus a SendRich helper that posts via sendRichMessage with
// a graceful fallback to sendMessage when the bot is
// talking to a client that doesn't support rich messages
// (older Telegram app versions). Existing call sites that
// don't need the new structure keep using parse_mode=HTML
// via the existing sendPlain path — no behaviour change for
// the simple case.
//
// Limits (from the Bot API docs, #rich-message-limits):
//   - 32768 UTF-8 characters total
//   - 500 blocks (incl. nested blocks, list items, table rows)
//   - 16 nesting levels
//   - 50 media attachments (we don't use media yet)
//   - 20 columns in a table
//
// ---------------------------------------------------------------------
// 2026-10-16 (B389) — THE WIRE FORMAT, MEASURED AGAINST THE LIVE API.
//
// The B186 implementation was wrong in two independent ways and every
// rich send therefore fell back to plain sendMessage, unnoticed for two
// months. Both were reproduced against api.telegram.org on the reference
// host, with the bot's own token and a bound chat:
//
//  1. WRONG ENVELOPE. `sendRichMessage` takes the message under
//     `rich_message`; the pre-B389 code posted `blocks` at the TOP
//     level, together with `chat_id`. Unknown top-level fields are
//     silently IGNORED, so the API answered
//     400 "Bad Request: rich message must be non-empty" — and
//     SendRich's own fallback logged the rejection and re-sent the
//     message as flat parse_mode=HTML. The rich path had never
//     delivered a single message.
//
//     `{"chat_id": 1, "blocks": [...]}`                  -> 400
//     `{"chat_id": 1, "rich_message": {"blocks": [...]}}` -> parsed
//     `{"chat_id": 1, "rich_message": {"html": "<h2>x</h2>"}}` -> 200
//
//  2. THE BLOCK VOCABULARY IS NOT THE API'S. `InputRichBlock` does not
//     use the type names this file invented from the HTML tag list.
//     Verified type-by-type on the live parser (the API names the first
//     offending type, which is how this list was obtained):
//
//     accepted: paragraph, heading (numeric `size`, NOT `level`),
//     list (items are OBJECTS, not strings),
//     table (field is `cells` of PageBlockTableCell objects,
//     NOT `rows` of RichText),
//     details, footer, divider
//     rejected: section_heading, aside, inline `text` (a plain inline
//     node must be a bare JSON string), mark/sub/sup;
//     `url` needs the field `url`, not `href`
//
//     So the RichBlock values below are NOT the wire format of
//     sendRichMessage — they are skygate's own internal IR, and
//     `renderBlocksAsRichHTML` is the single serialiser to the wire.
//     Do not "fix" the type names back into the payload: the payload
//     carries HTML.
//
// The HTML form was chosen deliberately over the blocks form: it needs
// no reverse-engineered struct schema, it reuses the renderer that
// already existed for the old-client fallback, and the accepted tag
// vocabulary was enumerated in one shot (all 31 of h1-h6, p, b, i, u,
// s, code, pre, a, br, hr, blockquote (plain + expandable), ul, ol,
// details/summary, footer, table (bordered striped + caption + th),
// tg-spoiler, span.tg-spoiler, mark, sub, sup, tg-time, divider and
// input[type=checkbox] were accepted in ONE message). `reply_markup`
// (inline keyboards) is accepted alongside `rich_message` too, so a
// rich reply is not restricted to button-less messages.
//
// Guarded by `scripts/check_b389_rich_message_wire.sh` and
// `internal/telegram/rich_b389_test.go`.
//   - "Show more" button folds long messages after ~8000 chars
package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"skygate/internal/i18n"
)

// RichBlock is skygate's INTERNAL rich-message IR — it is NOT the
// wire format of sendRichMessage (see the B389 note at the top of
// this file: the API's InputRichBlock uses different type names and
// different field names). renderBlocksAsRichHTML is the single place
// that turns this IR into what the API actually accepts.
//
// The discriminated union key is "type". Values:
//
//	"section_heading" — <h1>..<h6> with optional "level" (1..6)
//	"paragraph"       — plain text block
//	"list"            — bullet or ordered list of items
//	"table"           — header row + body rows
//	"details"         — collapsible <details> block
//	"aside"           — pull-quote (used for warnings / alerts)
//	"footer"          — small-text footer line
//	"divider"         — horizontal rule
//	"preformatted"    — monospace block
type RichBlock = map[string]any

// RichText is the inline-text node inside a block. The Bot
// API accepts either a plain string OR a list of styled
// nodes. We use the list form when we need <b>/<i>/<code>/
// <tg-time> styling inside the block.
type RichText = []any

// Heading returns a section_heading block. The level arg
// is 1..4 (mapped to <h1>..<h4> on the client).
func Heading(text string, level int) RichBlock {
	if level < 1 {
		level = 2
	}
	if level > 4 {
		level = 4
	}
	return RichBlock{
		"type":  "section_heading",
		"text":  text,
		"level": level,
	}
}

// Paragraph returns a plain paragraph block. Use
// ParagraphStyled when you need inline <b>/<i>/<code>/
// <tg-time> nodes.
func Paragraph(text string) RichBlock {
	return RichBlock{"type": "paragraph", "text": text}
}

// ParagraphStyled returns a paragraph with styled inline
// nodes. Pass a RichText (list of styled nodes) — see
// Bold/Code/Time helpers below.
func ParagraphStyled(nodes RichText) RichBlock {
	return RichBlock{"type": "paragraph", "text": nodes}
}

// List returns a bullet list. Pass items as plain strings
// (or RichText for styled items).
func List(items []RichText, ordered bool) RichBlock {
	out := RichBlock{
		"type":  "list",
		"items": items,
	}
	if ordered {
		out["style"] = "ordered"
	} else {
		out["style"] = "unordered"
	}
	return out
}

// Table returns a table block. The first row of `rows` is
// rendered as <thead> on the client; subsequent rows are
// <tbody>. Use a row of plain strings for header labels
// and rows of styled RichText for body cells.
//
// Note: the Bot API limits tables to 20 columns. Table
// returns an error block (visible "table too wide" cell)
// when the input exceeds the limit rather than truncating
// silently — better to alert the operator than to drop
// data.
func Table(rows [][]RichText) RichBlock {
	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	if maxCols > 20 {
		// Return a single-row "table" with the error
		// message so the operator sees the problem
		// instead of the silent truncation the API
		// would otherwise do.
		return RichBlock{
			"type": "table",
			"rows": [][]RichText{[]RichText{
				Plain(fmt.Sprintf("Table too wide (%d columns, max 20)", maxCols)),
			}},
		}
	}
	return RichBlock{
		"type": "table",
		"rows": rows,
	}
}

// Details returns a collapsible <details> block. Use this
// for long technical content (audit trail, rule dump,
// full health check report) that the operator can fold
// away with a tap. The summary is the always-visible
// single-line title; the body is the list of inner blocks.
func Details(summary string, body []RichBlock) RichBlock {
	return RichBlock{
		"type":    "details",
		"summary": summary,
		"body":    body,
	}
}

// Aside returns a pull-quote block (the <aside> tag on
// the client). Use this for warnings / alerts / important
// callouts — it visually separates from the regular
// paragraph flow. Background colour is set client-side.
func Aside(text string) RichBlock {
	return RichBlock{
		"type": "aside",
		"text": text,
	}
}

// Footer returns a small-text footer line. Used for the
// closing line of a message (timestamps, "— Ваш
// Дворецкий", etc.).
func Footer(text string) RichBlock {
	return RichBlock{
		"type": "footer",
		"text": text,
	}
}

// Divider returns a horizontal rule.
func Divider() RichBlock {
	return RichBlock{"type": "divider"}
}

// Bold returns a styled bold inline node. Use inside a
// RichText passed to ParagraphStyled / Table / List.
func Bold(text string) RichText {
	return RichText{RichBlock{"type": "bold", "text": text}}
}

// Italic returns a styled italic inline node.
func Italic(text string) RichText {
	return RichText{RichBlock{"type": "italic", "text": text}}
}

// CodeInline returns a styled inline-code node for use
// inside a RichText list. The plain-text Code() helper in
// format.go returns a <code>…</code> string suitable for
// old parse_mode=HTML — this CodeInline returns a RichText
// node suitable for the new sendRichMessage blocks.
func CodeInline(text string) RichText {
	return RichText{RichBlock{"type": "code", "text": text}}
}

// Link returns an inline link node.
func Link(text, href string) RichText {
	return RichText{RichBlock{"type": "url", "text": text, "href": href}}
}

// Time returns a styled timestamp node. The Bot API
// renders this as a localised date/time on the client
// (the user's locale, not the server's). Pass the ISO
// 8601 string in `iso` (e.g. "2026-08-25T16:30:00Z") and
// the visible label in `text`. Falls back to `text` on
// clients that don't render <tg-time>.
func Time(text, iso string) RichText {
	return RichText{RichBlock{"type": "date_time", "text": text, "iso": iso}}
}

// Spoiler returns a hidden-until-tap inline node. Use for
// secrets / tokens / addresses the operator might want
// to glance at but not have in the chat scrollback.
func Spoiler(text string) RichText {
	return RichText{RichBlock{"type": "spoiler", "text": text}}
}

// Plain returns a plain-text inline node. The Bot API
// accepts bare strings too, but using Plain() inside a
// RichText list makes the structure explicit.
func Plain(text string) RichText {
	return RichText{RichBlock{"type": "text", "text": text}}
}

// SendRich posts a rich message to the bot's chat_id via
// the sendRichMessage API. On any error (bot version <
// 10.1, network failure, rate limit) it falls back to
// sendMessage with parse_mode=HTML so the operator still
// sees the body — never silently drops a notification.
//
// `blocks` is skygate's internal IR (see RichBlock). It is
// serialised by renderBlocksAsRichHTML into the `rich_message.html`
// form the API accepts — 2026-10-16 (B389): the previous version
// posted the IR itself as a top-level `blocks` field, which the API
// ignores, so EVERY rich send was answered
// `400 rich message must be non-empty` and silently degraded to the
// flat fallback. Both the envelope and the empty-body guard below are
// pinned by rich_b389_test.go.
//
// `opts` is a free-form map of extra top-level fields (reply_markup,
// is_rtl, skip_entity_detection, media, ...). An inline keyboard
// passed here survives the fallback path, so a rich reply with
// buttons never loses them.
func (n *RealNotifier) SendRich(token string, chatID int64, blocks []RichBlock, opts map[string]any) error {
	if n == nil || token == "" || chatID == 0 {
		return fmt.Errorf("SendRich: missing token or chatID")
	}
	if len(blocks) == 0 {
		return fmt.Errorf("SendRich: empty blocks")
	}
	html := renderBlocksAsRichHTML(blocks)
	if strings.TrimSpace(html) == "" {
		// A body that renders to nothing would be rejected by the API
		// with the same "rich message must be non-empty" answer that
		// hid the B389 envelope bug. Fail loudly instead.
		return fmt.Errorf("SendRich: %d block(s) rendered to an empty body", len(blocks))
	}
	// 1. Try sendRichMessage first.
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/sendRichMessage"
	payload := map[string]any{
		"chat_id": chatID,
		"rich_message": map[string]any{
			"html": html,
		},
	}
	for k, v := range opts {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 200 {
			var ack struct {
				OK          bool   `json:"ok"`
				Description string `json:"description"`
				ErrorCode   int    `json:"error_code"`
			}
			if json.Unmarshal(rb, &ack) == nil && ack.OK {
				return nil
			}
			// Fall through to fallback on Telegram API error
			// (e.g. method not found for old bot version).
			log.Printf("telegram: sendRichMessage rejected: code=%d %s — falling back to sendMessage",
				ack.ErrorCode, ack.Description)
		} else {
			log.Printf("telegram: sendRichMessage HTTP %d — falling back to sendMessage: %s",
				resp.StatusCode, string(rb))
		}
	} else {
		// B356.1: the transport error carries the full URL, token included.
		log.Printf("telegram: sendRichMessage transport err: %v — falling back to sendMessage", redactError(err, token))
	}
	// 2. Fallback: render blocks as flat HTML and post via
	// sendMessage. The rendering is "good enough" — the
	// structured <h2>/<ul>/<table> tags degrade gracefully
	// to bold + bullets on older clients.
	flat := renderBlocksAsHTML(blocks)
	_, _ = n.sendPlain(token, chatID, flat, pendingFromRichOpts(opts))
	return nil
}

// pendingFromRichOpts rebuilds the PendingReply for the sendMessage
// fallback from the options handed to SendRich, so an inline keyboard
// survives a rich rejection. Only reply_markup is understood; every
// other option is a rich-only field with no sendMessage equivalent.
func pendingFromRichOpts(opts map[string]any) *PendingReply {
	pending := &PendingReply{ParseMode: "HTML"}
	if len(opts) == 0 {
		return pending
	}
	rm, ok := opts["reply_markup"].(map[string]any)
	if !ok {
		return pending
	}
	if kb, ok := rm["inline_keyboard"].([][]map[string]any); ok {
		pending.InlineKeyboard = kb
	}
	return pending
}

// RichReplyMarkup builds the SendRich `opts` map for a reply that
// carries an inline keyboard. Returns nil when there is no keyboard,
// so the common case adds nothing to the payload.
func RichReplyMarkup(pending *PendingReply) map[string]any {
	if pending == nil || len(pending.InlineKeyboard) == 0 {
		return nil
	}
	return map[string]any{
		"reply_markup": map[string]any{
			"inline_keyboard": pending.InlineKeyboard,
		},
	}
}

// renderBlocksAsRichHTML serialises the internal IR into the
// `rich_message.html` body. This is the ONLY encoder to the
// sendRichMessage wire format (see the B389 note at the top of the
// file). Every value passes through escapeHTML, so user-controlled
// text (hostnames, rule targets, mesh names) cannot inject markup.
//
// Tag choice is not arbitrary: every tag emitted here was verified
// accepted by the live API in a single message on 2026-10-16.
func renderBlocksAsRichHTML(blocks []RichBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		renderBlockAsRichHTML(&b, blk)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func renderBlockAsRichHTML(b *strings.Builder, blk RichBlock) {
	t, _ := blk["type"].(string)
	switch t {
	case "section_heading":
		level, _ := blk["level"].(int)
		if level < 1 || level > 6 {
			level = 3
		}
		fmt.Fprintf(b, "<h%d>%s</h%d>", level, escapeHTML(getString(blk, "text")), level)
	case "paragraph":
		b.WriteString("<p>")
		b.WriteString(renderRichTextRich(getRichText(blk)))
		b.WriteString("</p>")
	case "list":
		items := getRawSlice(blk, "items")
		ordered, _ := blk["style"].(string)
		tag := "ul"
		if ordered == "ordered" {
			tag = "ol"
		}
		fmt.Fprintf(b, "<%s>", tag)
		for _, raw := range items {
			b.WriteString("<li>")
			b.WriteString(renderRichTextRich(toRichText(raw)))
			b.WriteString("</li>")
		}
		fmt.Fprintf(b, "</%s>", tag)
	case "table":
		rows := getRawSlice(blk, "rows")
		if len(rows) == 0 {
			return
		}
		b.WriteString("<table bordered striped>")
		for i, raw := range rows {
			b.WriteString("<tr>")
			cell := "td"
			if i == 0 {
				cell = "th"
			}
			for _, c := range toRichTextArray(raw) {
				fmt.Fprintf(b, "<%s>%s</%s>", cell, renderRichTextRich(c), cell)
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</table>")
	case "details":
		b.WriteString("<details><summary>")
		b.WriteString(escapeHTML(getString(blk, "summary")))
		b.WriteString("</summary>")
		body := getRawSlice(blk, "body")
		for _, inner := range body {
			if ib, ok := inner.(RichBlock); ok {
				renderBlockAsRichHTML(b, ib)
				b.WriteString("\n")
			}
		}
		b.WriteString("</details>")
	case "aside":
		b.WriteString("<blockquote>")
		b.WriteString(escapeHTML(getString(blk, "text")))
		b.WriteString("</blockquote>")
	case "footer":
		b.WriteString("<footer>")
		b.WriteString(renderRichTextRich(getRichText(blk)))
		b.WriteString("</footer>")
	case "divider":
		b.WriteString("<hr>")
	case "preformatted":
		b.WriteString("<pre>")
		b.WriteString(escapeHTML(getString(blk, "text")))
		b.WriteString("</pre>")
	}
}

// renderBlocksAsHTML is the fallback for clients that
// don't support sendRichMessage. The output is a flat
// HTML body that uses the old parse_mode=HTML tag subset
// (<b>, <i>, <u>, <code>, <pre>, <a>) so it still renders
// on Telegram v9.x and earlier. It's not pretty —
// tables become monospace <pre> blocks, lists become
// "• " prefixed lines — but it conveys the same info.
//
// 2026-08-25 (B186): the trade-off is intentional. The
// fallback is the LAST-RESORT path; the rich path
// (sendRichMessage) is what 99% of clients will see.
func renderBlocksAsHTML(blocks []RichBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		renderBlockAsHTML(&b, blk)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func renderBlockAsHTML(b *strings.Builder, blk RichBlock) {
	t, _ := blk["type"].(string)
	switch t {
	case "section_heading":
		// Fall back to bold text — we don't have <h2>
		// in old parse_mode=HTML.
		text := getString(blk, "text")
		b.WriteString("<b>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</b>")
	case "paragraph":
		b.WriteString(renderRichText(getRichText(blk)))
	case "list":
		items := getRawSlice(blk, "items")
		ordered, _ := blk["style"].(string)
		for i, raw := range items {
			if ordered == "ordered" {
				fmt.Fprintf(b, "%d. ", i+1)
			} else {
				b.WriteString("• ")
			}
			b.WriteString(renderRichText(toRichText(raw)))
			b.WriteString("\n")
		}
	case "table":
		rows := getRawSlice(blk, "rows")
		for _, raw := range rows {
			row := toRichTextArray(raw)
			for _, cell := range row {
				b.WriteString(renderRichText(cell))
				b.WriteString("\t")
			}
			b.WriteString("\n")
		}
	case "details":
		summary := getString(blk, "summary")
		b.WriteString("<b>")
		b.WriteString(escapeHTML(summary))
		b.WriteString("</b>\n")
		body := getRawSlice(blk, "body")
		for _, inner := range body {
			if ib, ok := inner.(RichBlock); ok {
				renderBlockAsHTML(b, ib)
				b.WriteString("\n")
			}
		}
	case "aside":
		text := getString(blk, "text")
		b.WriteString("<i>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</i>")
	case "footer":
		text := getString(blk, "text")
		b.WriteString("<i>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</i>")
	case "preformatted":
		// B389: this case was missing, so a preformatted block
		// rendered as NOTHING on the fallback path (the rich path
		// had no preformatted case either). Both now emit <pre>.
		b.WriteString("<pre>")
		b.WriteString(escapeHTML(getString(blk, "text")))
		b.WriteString("</pre>")
	case "divider":
		b.WriteString("———")
	}
}

// renderRichTextRich is the rich-HTML twin of renderRichText: same
// RichText input, but it may emit the tags only the rich vocabulary
// accepts (<tg-time>) and prefers the native <tg-spoiler> over
// <span class="tg-spoiler">. Both forms were verified accepted in
// rich_message.html, so the substitution is cosmetic.
func renderRichTextRich(rt RichText) string {
	var b strings.Builder
	for _, raw := range rt {
		if m, ok := raw.(RichBlock); ok {
			renderInlineNodeRich(&b, m)
		} else if s, ok := raw.(string); ok {
			b.WriteString(escapeHTML(s))
		}
	}
	return b.String()
}

func renderInlineNodeRich(b *strings.Builder, m RichBlock) {
	t, _ := m["type"].(string)
	text := getString(m, "text")
	switch t {
	case "text":
		b.WriteString(escapeHTML(text))
	case "bold":
		b.WriteString("<b>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</b>")
	case "italic":
		b.WriteString("<i>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</i>")
	case "code":
		b.WriteString("<code>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</code>")
	case "spoiler":
		b.WriteString("<tg-spoiler>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</tg-spoiler>")
	case "url":
		href := getString(m, "href")
		fmt.Fprintf(b, `<a href="%s">%s</a>`, escapeHTML(href), escapeHTML(text))
	case "date_time":
		// <tg-time unix="..."> lets the client render the moment in
		// the reader's own locale and timezone. The IR carries the
		// visible label plus an RFC3339 stamp; without a parseable
		// stamp we fall back to the label alone rather than lying
		// about the instant.
		if unix, ok := timeUnixFromISO(getString(m, "iso")); ok {
			fmt.Fprintf(b, `<tg-time unix="%d">%s</tg-time>`, unix, escapeHTML(text))
		} else {
			b.WriteString(escapeHTML(text))
		}
	default:
		b.WriteString(escapeHTML(text))
	}
}

// timeUnixFromISO converts an RFC3339 stamp into a Unix second count.
// Returns ok=false for an empty or unparseable value.
func timeUnixFromISO(iso string) (int64, bool) {
	if iso == "" {
		return 0, false
	}
	ts, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return 0, false
	}
	return ts.Unix(), true
}

func renderRichText(rt RichText) string {
	var b strings.Builder
	for _, raw := range rt {
		if m, ok := raw.(RichBlock); ok {
			renderInlineNode(&b, m)
		} else if s, ok := raw.(string); ok {
			b.WriteString(escapeHTML(s))
		}
	}
	return b.String()
}

func renderInlineNode(b *strings.Builder, m RichBlock) {
	t, _ := m["type"].(string)
	text := getString(m, "text")
	switch t {
	case "text":
		b.WriteString(escapeHTML(text))
	case "bold":
		b.WriteString("<b>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</b>")
	case "italic":
		b.WriteString("<i>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</i>")
	case "code":
		b.WriteString("<code>")
		b.WriteString(escapeHTML(text))
		b.WriteString("</code>")
	case "spoiler":
		b.WriteString(`<span class="tg-spoiler">`)
		b.WriteString(escapeHTML(text))
		b.WriteString("</span>")
	case "url":
		href := getString(m, "href")
		fmt.Fprintf(b, `<a href="%s">%s</a>`, escapeHTML(href), escapeHTML(text))
	case "date_time":
		// <tg-time> isn't supported in old parse_mode=HTML
		// — fall back to the visible label.
		b.WriteString(escapeHTML(text))
	default:
		// Unknown inline type: best-effort plain text.
		b.WriteString(escapeHTML(text))
	}
}

// getString safely extracts a string field from a
// RichBlock (map). Returns "" if the key is missing or
// the value isn't a string.
func getString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// getRawSlice extracts a slice field from a RichBlock and
// normalises both `[]any` (the JSON-decoded form) and
// concrete typed slices like `[][]RichText` to a single
// `[]any` the renderer can iterate. Go's type system
// refuses a direct `[]T → []any` cast, so the call site
// would otherwise need a per-type loop; this helper does
// that loop once.
func getRawSlice(m map[string]any, key string) []any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch arr := v.(type) {
	case []any:
		return arr
	case []RichBlock:
		out := make([]any, 0, len(arr))
		for _, x := range arr {
			out = append(out, x)
		}
		return out
	case []RichText:
		out := make([]any, 0, len(arr))
		for _, x := range arr {
			out = append(out, x)
		}
		return out
	case [][]RichText:
		out := make([]any, 0, len(arr))
		for _, x := range arr {
			out = append(out, x)
		}
		return out
	}
	return nil
}

// getRichText extracts the "text" field as a RichText
// (a list of styled nodes). If the field is a plain
// string, wraps it in a Plain() node. Returns an empty
// RichText if the field is missing.
func getRichText(m map[string]any) RichText {
	raw, ok := m["text"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil
		}
		return Plain(v)
	case []any:
		return toRichText(v)
	}
	return Plain(fmt.Sprint(raw))
}

func toRichText(raw any) RichText {
	switch v := raw.(type) {
	case string:
		return Plain(v)
	case []any:
		// Flatten a nested []any into a single RichText
		// by treating each element as an inline node.
		out := make(RichText, 0, len(v))
		for _, item := range v {
			if rb, ok := item.(RichBlock); ok {
				out = append(out, rb)
			} else if s, ok := item.(string); ok {
				out = append(out, Plain(s))
			}
		}
		return out
	case RichBlock:
		return RichText{v}
	}
	return nil
}

func toRichTextArray(raw any) []RichText {
	switch arr := raw.(type) {
	case []any:
		out := make([]RichText, 0, len(arr))
		for _, v := range arr {
			out = append(out, toRichText(v))
		}
		return out
	case []RichText:
		return arr
	}
	return nil
}

// KVRow is a typed alternative to passing the table rows
// as [][]RichText. It's a tiny convenience for the common
// case of "bold label, monospace value" rows. The first
// row of a Table is rendered as <thead> on the client.
type KVRow struct {
	Label string
	Value string
}

// KeyValueTable builds a 2-column Table block from a list
// of (label, value) pairs. The header row is bold "label /
// value" and the body rows are label-bold / value-code.
// Empty label or value is allowed (renders as a single
// spanned cell).
//
// 2026-08-25 (B186): the common skygate reply shape is a
// key/value list (Field() lines under a Section()). The
// old render used flat <b>label:</b> <code>value</code>
// lines. The new render uses a proper table so mobile
// Telegram aligns the columns even on narrow viewports
// (the old flat lines were impossible to align without
// a <pre> block + manual padding). This helper makes the
// migration mechanical — replace "list of Field lines"
// with "one call to KeyValueTable(rows)".
func KeyValueTable(rows []KVRow) RichBlock {
	return KeyValueTableLang(i18n.LangEN, rows)
}

// KeyValueTableLang is KeyValueTable with the header row localised.
// B389: the header used to be the hardcoded English "Field / Value"
// for every language, so a Russian chat rendered an English header
// above Russian labels (and the same fix applies to /version and
// /status). Keys: bot.table.field / bot.table.value.
func KeyValueTableLang(lang string, rows []KVRow) RichBlock {
	if len(rows) == 0 {
		return Paragraph("") // safer than an empty table
	}
	tableRows := make([][]RichText, 0, len(rows)+1)
	tableRows = append(tableRows, []RichText{
		Bold(i18n.T(lang, "bot.table.field")), Bold(i18n.T(lang, "bot.table.value")),
	})
	for _, r := range rows {
		tableRows = append(tableRows, []RichText{
			Bold(r.Label), CodeInline(r.Value),
		})
	}
	return Table(tableRows)
}

// PreformattedBlock returns a <pre>-style block (the
// Bot API calls it "preformatted") for monospace columnar
// data — for example the rule-dump table that used to
// render as a Telegram <pre> block. Lines is a list of
// already-formatted strings (caller is responsible for
// width-padding). Max 4096 chars per block; the caller
// is expected to slice long output.
func PreformattedBlock(text string) RichBlock {
	return RichBlock{"type": "preformatted", "text": text}
}
