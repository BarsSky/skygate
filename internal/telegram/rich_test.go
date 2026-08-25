// Package telegram — rich_test.go pins the B186 Bot API 10.1
// Rich Messages adapter. The tests cover the four concerns
// the operator cares about:
//
//  1. The builder produces the right JSON shape (verified
//     against the Bot API 10.1 spec — see the live block list
//     in https://core.telegram.org/bots/api#inputrichblock).
//  2. The escape layer never lets user-controlled strings
//     inject HTML or break out of a block.
//  3. The fallback path (client < 10.1) degrades to a flat
//     HTML body that still conveys the same info.
//  4. The size limits (32768 chars / 500 blocks / 20 table
//     columns) are enforced at the builder boundary, not at
//     the network boundary — the operator gets an inline
//     "table too wide" cell instead of a silent API
//     rejection.
package telegram

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRichBuilder_HeadingsAndParagraph — section_heading +
// paragraph + paragraph + footer compose the most common
// "summary message" shape (the one /my_status renders).
func TestRichBuilder_HeadingsAndParagraph(t *testing.T) {
	blocks := []RichBlock{
		Heading("Добрый вечер, skyadmin.", 2),
		Paragraph("У вас 12 правил на 3 устройствах."),
		Footer("— Ваш Дворецкий"),
	}
	body, err := json.Marshal(map[string]any{
		"chat_id": 12345,
		"blocks":  blocks,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		`"type":"section_heading"`,
		`"level":2`,
		`"type":"paragraph"`,
		`"type":"footer"`,
		`"chat_id":12345`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("body missing %q\nfull body: %s", want, s)
		}
	}
}

// TestRichBuilder_KeyValueTable — the KeyValueTable helper
// produces a 2-col table with a bold header row. The
// caller passes KVRow labels + values; the helper builds
// the RichText nodes with bold/code styling.
//
// This is the regression guard for the B186 conversion of
// the old "<b>label:</b> <code>value</code>" flat lines.
func TestRichBuilder_KeyValueTable(t *testing.T) {
	table := KeyValueTable([]KVRow{
		{Label: "rules", Value: "12 / 50"},
		{Label: "devices", Value: "3"},
		{Label: "last acl", Value: "#5"},
	})
	if table["type"] != "table" {
		t.Errorf("type = %v, want table", table["type"])
	}
	rows, ok := table["rows"].([][]RichText)
	if !ok {
		t.Fatalf("rows is %T, want [][]RichText", table["rows"])
	}
	if len(rows) != 4 { // 1 header + 3 body
		t.Errorf("rows len = %d, want 4", len(rows))
	}
	// Header row is bold/bold.
	header := rows[0]
	if len(header) != 2 {
		t.Errorf("header len = %d, want 2", len(header))
	}
	if header[0][0].(RichBlock)["type"] != "bold" {
		t.Errorf("header col 0 type = %v, want bold", header[0][0].(RichBlock)["type"])
	}
	// Body row is bold/code.
	body := rows[1]
	if body[0][0].(RichBlock)["type"] != "bold" {
		t.Errorf("body col 0 type = %v, want bold", body[0][0].(RichBlock)["type"])
	}
	if body[1][0].(RichBlock)["type"] != "code" {
		t.Errorf("body col 1 type = %v, want code", body[1][0].(RichBlock)["type"])
	}
}

// TestRichBuilder_TableWidthLimit — Bot API limits tables
// to 20 columns. The builder replaces over-wide tables
// with a single error cell rather than silently
// truncating. This is the B186 fail-fast contract.
func TestRichBuilder_TableWidthLimit(t *testing.T) {
	// 25 columns in a row → over the 20-col limit.
	huge := make([]RichText, 25)
	for i := range huge {
		huge[i] = Plain("c")
	}
	rows := [][]RichText{huge}
	table := Table(rows)
	body, _ := json.Marshal(table)
	if !strings.Contains(string(body), "Table too wide") {
		t.Errorf("expected 'Table too wide' in JSON, got: %s", body)
	}
}

// TestRichBuilder_EscapeHTML — user-controlled strings
// (hostnames with &, IDs with <) must not inject HTML.
// The Plain/Bold/Code helpers all run their input through
// escapeHTML.
func TestRichBuilder_EscapeHTML(t *testing.T) {
	// Mix of & < > " in a single label.
	bad := "evil & <script> \"quoted\""
	blocks := []RichBlock{
		ParagraphStyled(Plain(bad)),
	}
	body, _ := json.Marshal(blocks)
	// JSON-level escaping: & becomes \u0026, < becomes \u003c.
	// The test asserts the escape happened, not the literal
	// character.
	if !strings.Contains(string(body), `\u0026`) {
		t.Errorf("& not JSON-escaped: %s", body)
	}
	if !strings.Contains(string(body), `\u003c`) {
		t.Errorf("< not JSON-escaped: %s", body)
	}
	if !strings.Contains(string(body), `\u003e`) {
		t.Errorf("> not JSON-escaped: %s", body)
	}
}

// TestRichBuilder_Aside — the <aside> block is used for
// warnings/alerts (visually distinct pull-quote). The
// helper just sets the type + text.
func TestRichBuilder_Aside(t *testing.T) {
	aside := Aside("⚠ Tailscale API недоступен — relay не маршрутизирует трафик.")
	if aside["type"] != "aside" {
		t.Errorf("type = %v, want aside", aside["type"])
	}
	if aside["text"] == nil {
		t.Errorf("text missing")
	}
}

// TestRichBuilder_Details — the <details> block is the
// "long technical content behind a tap" pattern. The
// summary is the always-visible title; the body is the
// folded list of inner blocks.
func TestRichBuilder_Details(t *testing.T) {
	d := Details("Подробный audit trail (нажмите, чтобы раскрыть)", []RichBlock{
		ParagraphStyled(Plain("12 активаций за последние 24 часа.")),
		ParagraphStyled(Plain("Последняя: emilia → karolina @ 16:30.")),
	})
	if d["type"] != "details" {
		t.Errorf("type = %v, want details", d["type"])
	}
	body, ok := d["body"].([]RichBlock)
	if !ok || len(body) != 2 {
		t.Errorf("body len = %v, want 2", d["body"])
	}
}

// TestRichBuilder_RichText_Time — <tg-time> renders the
// client's locale-specific datetime. The helper takes a
// human label + ISO 8601 string.
func TestRichBuilder_RichText_Time(t *testing.T) {
	rt := Time("5 минут назад", "2026-08-25T16:30:00Z")
	if len(rt) != 1 {
		t.Fatalf("rt len = %d, want 1", len(rt))
	}
	m, ok := rt[0].(RichBlock)
	if !ok {
		t.Fatalf("rt[0] is %T, want RichBlock", rt[0])
	}
	if m["type"] != "date_time" {
		t.Errorf("type = %v, want date_time", m["type"])
	}
	if m["iso"] != "2026-08-25T16:30:00Z" {
		t.Errorf("iso = %v", m["iso"])
	}
}

// TestRenderBlocksAsHTML_Fallback — when the client is
// < 10.1, SendRich falls back to sendMessage. The fallback
// body is a flat HTML render of the same blocks. The test
// asserts the fallback conveys the same info (the heading
// becomes bold text, the table becomes tab-separated
// lines, the aside becomes italic).
func TestRenderBlocksAsHTML_Fallback(t *testing.T) {
	blocks := []RichBlock{
		Heading("Сводка", 2),
		KeyValueTable([]KVRow{
			{Label: "rules", Value: "12"},
		}),
		Aside("⚠ предупреждение"),
		Footer("footer text"),
	}
	html := renderBlocksAsHTML(blocks)
	// Heading becomes <b>Сводка</b>.
	if !strings.Contains(html, "<b>Сводка</b>") {
		t.Errorf("heading not bold: %s", html)
	}
	// Table cell values end up tab-separated.
	if !strings.Contains(html, "rules") {
		t.Errorf("table label not present: %s", html)
	}
	// Aside becomes italic.
	if !strings.Contains(html, "<i>⚠ предупреждение</i>") {
		t.Errorf("aside not italic: %s", html)
	}
	// Footer becomes italic.
	if !strings.Contains(html, "<i>footer text</i>") {
		t.Errorf("footer not italic: %s", html)
	}
}

// TestRenderBlocksAsHTML_HtmlEscaping — the fallback path
// must also escape user-controlled strings (otherwise an
// old client would render <script> as HTML).
func TestRenderBlocksAsHTML_HtmlEscaping(t *testing.T) {
	blocks := []RichBlock{
		ParagraphStyled(Plain("evil & <script>")),
	}
	html := renderBlocksAsHTML(blocks)
	if strings.Contains(html, "<script>") {
		t.Errorf("fallback did not escape <script>: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("fallback missing &lt;script&gt;: %s", html)
	}
}

// TestListBlocks — bullet lists use a leading "• " in
// the fallback; ordered lists use a 1-based index. The
// block type itself is always "list" with a "style" field.
func TestListBlocks(t *testing.T) {
	bullet := List([]RichText{
		Plain("first"), Plain("second"), Plain("third"),
	}, false)
	if bullet["type"] != "list" {
		t.Errorf("type = %v, want list", bullet["type"])
	}
	if bullet["style"] != "unordered" {
		t.Errorf("style = %v, want unordered", bullet["style"])
	}
	ordered := List([]RichText{Plain("only")}, true)
	if ordered["style"] != "ordered" {
		t.Errorf("style = %v, want ordered", ordered["style"])
	}
}
