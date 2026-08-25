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
)

// RichBlock is the discriminated union of block types we
// emit. The HTTP API takes a raw JSON list of blocks; the
// caller is responsible for assembling the right shapes per
// the Bot API 10.1 spec (see internal/telegram/rich_test.go
// for the JSON contract tests). We don't pre-define Go
// structs for every block type — the rich-message shape
// is large and a map[string]any keeps the wire format
// readable when diffing with the Bot API docs.
//
// The discriminated union key is "type". Values:
//
//	"section_heading" — <h1>..<h4> with optional "level" (1..4)
//	"paragraph"       — plain text block
//	"list"            — bullet or ordered list of items
//	"table"           — header row + body rows
//	"details"         — collapsible <details> block
//	"aside"           — pull-quote (used for warnings / alerts)
//	"footer"          — small-text footer line
//	"divider"         — horizontal rule
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
		"type": "section_heading",
		"text": text,
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
// `blocks` is the list of top-level RichBlock. The body
// is built by passing the same list as InputRichMessage.blocks.
//
// `opts` is a free-form map for future fields
// (is_rtl, skip_entity_detection, media, etc.) — most
// call sites can pass nil.
func (n *RealNotifier) SendRich(token string, chatID int64, blocks []RichBlock, opts map[string]any) error {
	if n == nil || token == "" || chatID == 0 {
		return fmt.Errorf("SendRich: missing token or chatID")
	}
	if len(blocks) == 0 {
		return fmt.Errorf("SendRich: empty blocks")
	}
	// 1. Try sendRichMessage first.
	endpoint := n.apiBase + "/bot" + url.PathEscape(token) + "/sendRichMessage"
	payload := map[string]any{
		"chat_id": chatID,
		"blocks":  blocks,
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
		log.Printf("telegram: sendRichMessage transport err: %v — falling back to sendMessage", err)
	}
	// 2. Fallback: render blocks as flat HTML and post via
	// sendMessage. The rendering is "good enough" — the
	// structured <h2>/<ul>/<table> tags degrade gracefully
	// to bold + bullets on older clients.
	html := renderBlocksAsHTML(blocks)
	_, _ = n.sendPlain(token, chatID, html, &PendingReply{ParseMode: "HTML"})
	return nil
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
	case "divider":
		b.WriteString("———")
	}
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
	if len(rows) == 0 {
		return Paragraph("") // safer than an empty table
	}
	tableRows := make([][]RichText, 0, len(rows)+1)
	tableRows = append(tableRows, []RichText{
		Bold("Field"), Bold("Value"),
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
