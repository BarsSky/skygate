// Package telegram — rich_b389_test.go pins the ACTUAL wire payload of
// sendRichMessage. This is the test whose absence let B389 ship: the
// pre-B389 suite (rich_test.go) marshalled its own map, asserted its
// own field names, and never once looked at what the notifier puts on
// the wire — so it stayed green for two months while every rich send
// was answered `400 rich message must be non-empty` and silently
// degraded to plain text.
//
// Live measurements this file encodes (reference host, 2026-10-16,
// bot token from the DB, bound chat):
//
//	{"chat_id":N,"blocks":[...]}                          -> 400 "rich message must be non-empty"
//	{"chat_id":N,"rich_message":{"blocks":[...]}}         -> 400 `type "section_heading" is unsupported`
//	{"chat_id":N,"rich_message":{"html":"<h2>x</h2>"}}    -> 200 delivered
//	reply_markup alongside rich_message                    -> 200, keyboard echoed back
package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// richTestServer records every request body it receives and answers
// with the given JSON status/body.
func richTestServer(t *testing.T, status int, response string) (*httptest.Server, *[]map[string]any, *[]string) {
	t.Helper()
	var payloads []map[string]any
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		payloads = append(payloads, m)
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, &payloads, &paths
}

func newTestNotifier(srv *httptest.Server) *RealNotifier {
	return &RealNotifier{apiBase: srv.URL, client: srv.Client()}
}

func sampleBlocks() []RichBlock {
	return []RichBlock{
		Heading("Сводка", 2),
		KeyValueTable([]KVRow{{Label: "rules", Value: "12"}}),
		List([]RichText{Plain("first"), Plain("second")}, false),
		Aside("⚠ предупреждение"),
		Divider(),
		Details("Подробности", []RichBlock{Paragraph("внутри")}),
		PreformattedBlock("col1\tcol2"),
		Footer("— Ваш Дворецкий"),
	}
}

// TestSendRich_WireEnvelopeIsNested — the headline B389 contract: the
// body travels under rich_message.html, and the old top-level `blocks`
// field is gone for good.
func TestSendRich_WireEnvelopeIsNested(t *testing.T) {
	srv, payloads, paths := richTestServer(t, 200, `{"ok":true,"result":{"message_id":1}}`)
	n := newTestNotifier(srv)

	if err := n.SendRich("123:abc", 4242, sampleBlocks(), nil); err != nil {
		t.Fatalf("SendRich returned %v, want nil", err)
	}
	if len(*payloads) != 1 {
		t.Fatalf("requests = %d, want 1", len(*payloads))
	}
	if got := (*paths)[0]; !strings.HasSuffix(got, "/sendRichMessage") {
		t.Errorf("path = %q, want .../sendRichMessage", got)
	}
	p := (*payloads)[0]
	if p["chat_id"] != float64(4242) {
		t.Errorf("chat_id = %v, want 4242", p["chat_id"])
	}
	// The pre-B389 bug in one assertion: unknown top-level fields are
	// silently ignored by Telegram, so a top-level `blocks` sends an
	// EMPTY rich message and the API answers 400.
	if _, bad := p["blocks"]; bad {
		t.Errorf("payload carries the pre-B389 top-level `blocks` field: %v", p)
	}
	rm, ok := p["rich_message"].(map[string]any)
	if !ok {
		t.Fatalf("rich_message is %T, want object — payload: %v", p["rich_message"], p)
	}
	html, ok := rm["html"].(string)
	if !ok || strings.TrimSpace(html) == "" {
		t.Fatalf("rich_message.html = %q, want a non-empty string (the API rejects an empty body with "+
			"`rich message must be non-empty`)", rm["html"])
	}
}

// TestSendRich_RendererEmitsVerifiedVocabulary — every tag the
// renderer can emit was accepted by the live API in one message
// (2026-10-16). The assertions also fail if a rich-only tag leaks into
// the flat sendMessage fallback, which Telegram would reject.
func TestSendRich_RendererEmitsVerifiedVocabulary(t *testing.T) {
	srv, payloads, _ := richTestServer(t, 200, `{"ok":true,"result":{"message_id":1}}`)
	n := newTestNotifier(srv)
	if err := n.SendRich("123:abc", 1, sampleBlocks(), nil); err != nil {
		t.Fatalf("SendRich: %v", err)
	}
	p := (*payloads)[0]
	rm := p["rich_message"].(map[string]any)
	html := rm["html"].(string)

	for _, want := range []string{
		"<h2>Сводка</h2>",
		"<table bordered striped>",
		"<th>", "<td>",
		"<ul><li>first</li><li>second</li></ul>",
		"<blockquote>",
		"<hr>",
		"<details><summary>Подробности</summary>",
		"<pre>col1\tcol2</pre>",
		"<footer>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rich body missing %q\nfull body:\n%s", want, html)
		}
	}
}

// TestSendRich_FallbackKeepsBodyAndKeyboard — a rich rejection must
// still deliver the message, through the flat parse_mode=HTML body,
// and must not lose the inline keyboard on the way.
func TestSendRich_FallbackKeepsBodyAndKeyboard(t *testing.T) {
	srv, payloads, paths := richTestServer(t, 400,
		`{"ok":false,"error_code":400,"description":"Bad Request: method not found"}`)
	n := newTestNotifier(srv)

	opts := RichReplyMarkup(&PendingReply{InlineKeyboard: [][]map[string]any{
		{{"text": "→ karolina", "callback_data": "setexitnode:11"}},
	}})
	if err := n.SendRich("123:abc", 4242, sampleBlocks(), opts); err != nil {
		t.Fatalf("SendRich returned %v, want nil after a successful fallback", err)
	}
	if len(*payloads) != 2 {
		t.Fatalf("requests = %d, want 2 (rich attempt + fallback)", len(*payloads))
	}
	if got := (*paths)[1]; !strings.HasSuffix(got, "/sendMessage") {
		t.Errorf("fallback path = %q, want .../sendMessage", got)
	}
	fb := (*payloads)[1]
	if fb["parse_mode"] != "HTML" {
		t.Errorf("fallback parse_mode = %v, want HTML", fb["parse_mode"])
	}
	text, _ := fb["text"].(string)
	if strings.TrimSpace(text) == "" {
		t.Fatal("fallback body is empty — the message would be lost")
	}
	// The flat vocabulary only: a rich-only tag here is a 400 that
	// would drop the fallback too.
	for _, forbidden := range []string{"<h2>", "<table", "<details>", "<footer>", "<blockquote>", "<hr>"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("fallback body carries the rich-only tag %q:\n%s", forbidden, text)
		}
	}
	markup, ok := fb["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("fallback lost reply_markup: %v", fb)
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("fallback reply_markup = %v, want the original keyboard", markup)
	}
}

// TestSendRich_EmptyRenderIsAnError — a body that renders to nothing
// is exactly the symptom that hid the envelope bug, so it is reported
// instead of being posted and rejected.
func TestSendRich_EmptyRenderIsAnError(t *testing.T) {
	srv, payloads, _ := richTestServer(t, 200, `{"ok":true}`)
	n := newTestNotifier(srv)

	if err := n.SendRich("123:abc", 1, nil, nil); err == nil {
		t.Error("SendRich(nil blocks) = nil, want an error")
	}
	if err := n.SendRich("123:abc", 1, []RichBlock{{"type": "unknown_kind"}}, nil); err == nil {
		t.Error("SendRich(unknown block type) = nil, want an error")
	}
	if len(*payloads) != 0 {
		t.Errorf("a request was sent for an unrenderable body: %v", *payloads)
	}
	if err := n.SendRich("", 1, sampleBlocks(), nil); err == nil {
		t.Error("SendRich(no token) = nil, want an error")
	}
}

// TestSendRich_HTMLIsEscaped — user-controlled values reach the rich
// body through escapeHTML, so a hostname or mesh name cannot inject
// markup that Telegram would either render or reject.
func TestSendRich_HTMLIsEscaped(t *testing.T) {
	srv, payloads, _ := richTestServer(t, 200, `{"ok":true,"result":{"message_id":1}}`)
	n := newTestNotifier(srv)
	blocks := []RichBlock{
		Heading("evil <script> & <b>", 2),
		Paragraph("target: <username>"),
		PreformattedBlock("x < y"),
	}
	if err := n.SendRich("123:abc", 1, blocks, nil); err != nil {
		t.Fatalf("SendRich: %v", err)
	}
	html := (*payloads)[0]["rich_message"].(map[string]any)["html"].(string)
	if strings.Contains(html, "<script>") {
		t.Errorf("script tag reached the wire: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("script tag not escaped: %s", html)
	}
	if strings.Contains(html, "<username>") {
		t.Errorf("placeholder not escaped in the rich body: %s", html)
	}
}

// TestRichReplyMarkup_NoKeyboardMeansNoPayload — the common case must
// not add an empty reply_markup object (Telegram rejects
// `reply_markup: {}` with "object expected").
func TestRichReplyMarkup_NoKeyboardMeansNoPayload(t *testing.T) {
	if got := RichReplyMarkup(nil); got != nil {
		t.Errorf("RichReplyMarkup(nil) = %v, want nil", got)
	}
	if got := RichReplyMarkup(&PendingReply{ParseMode: "HTML"}); got != nil {
		t.Errorf("RichReplyMarkup(no keyboard) = %v, want nil", got)
	}
	got := RichReplyMarkup(&PendingReply{InlineKeyboard: [][]map[string]any{{{"text": "x"}}}})
	if got == nil || got["reply_markup"] == nil {
		t.Errorf("RichReplyMarkup(keyboard) = %v, want reply_markup", got)
	}
}
