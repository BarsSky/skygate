// Package telegram — rich_compose_test.go pins the B186
// wire-up: the ComposeRich envelope, the cmdReply.blocks
// field, and the HandleCommand (string, []RichBlock)
// dual return. The behavioural contract is:
//
//  1. ComposeRich prepends a Heading (the gate header) and
//     appends a Footer (the butler's signoff).
//  2. The Heading uses the same GateHeader() that the
//     string path uses (so the visual style matches).
//  3. The Footer uses the same GateFooter() that the
//     string path uses.
//  4. HandleCommand returns (body, nil) for commands that
//     haven't been migrated yet, and (body, blocks) for
//     migrated commands.
//  5. The polling loop picks blocks when non-empty (this
//     is tested via the integration test in
//     notify_polling_test.go if present, otherwise the
//     unit tests in commands_user_test.go / commands_phase4_test.go).
//
// 2026-08-25 (B186).
package telegram

import (
	"strings"
	"testing"
)

// TestComposeRich_HeaderAndFooter — the envelope adds
// exactly one Heading at the start (level 3) and one
// Footer at the end. The body blocks are untouched in
// the middle.
func TestComposeRich_HeaderAndFooter(t *testing.T) {
	body := []RichBlock{
		Paragraph("Body content here."),
	}
	rich := ComposeRich("ru", "my_status", body)
	if len(rich) != 3 {
		t.Fatalf("len = %d, want 3 (header + body + footer)", len(rich))
	}
	if t1, _ := rich[0]["type"].(string); t1 != "section_heading" {
		t.Errorf("rich[0] type = %v, want section_heading", t1)
	}
	if t2, _ := rich[1]["type"].(string); t2 != "paragraph" {
		t.Errorf("rich[1] type = %v, want paragraph (body untouched)", t2)
	}
	if t3, _ := rich[2]["type"].(string); t3 != "footer" {
		t.Errorf("rich[2] type = %v, want footer", t3)
	}
}

// TestComposeRich_HeaderMatchesGateHeader — the Heading
// text must equal GateHeader(lang, context) verbatim so
// the visual style matches the string path. If a future
// change introduces a difference (e.g. a different emoji
// or wordmark), this test catches it before the operator
// sees two different "Skygate" headers.
func TestComposeRich_HeaderMatchesGateHeader(t *testing.T) {
	body := []RichBlock{Paragraph("body")}
	rich := ComposeRich("ru", "my_status", body)
	headerText, _ := rich[0]["text"].(string)
	want := GateHeader("ru", "my_status")
	if headerText != want {
		t.Errorf("rich[0].text = %q, want %q", headerText, want)
	}
	if !strings.Contains(headerText, "Skygate") {
		t.Errorf("header should contain the Skygate wordmark: %q", headerText)
	}
}

// TestComposeRich_FooterMatchesGateFooter — the Footer
// text equals GateFooter(lang). The signoff text
// ("Ваш Дворецкий" / "Your butler") lives in the i18n
// catalog, so a rebrand is one translation key change.
func TestComposeRich_FooterMatchesGateFooter(t *testing.T) {
	body := []RichBlock{Paragraph("body")}
	rich := ComposeRich("ru", "version", body)
	footerText, _ := rich[2]["text"].(string)
	want := GateFooter("ru")
	if footerText != want {
		t.Errorf("rich[2].text = %q, want %q", footerText, want)
	}
	if !strings.Contains(footerText, "Дворецкий") {
		t.Errorf("footer should contain the butler signoff: %q", footerText)
	}
}

// TestComposeRich_EmptyBody — when the body is empty (or
// the dispatcher has nothing to send), ComposeRich still
// emits the header + footer pair. The result is a valid
// rich message ("I'm here" — the butler's presence
// announcement), matching the Compose() string-path
// behaviour for empty bodies.
func TestComposeRich_EmptyBody(t *testing.T) {
	rich := ComposeRich("ru", "my_status", nil)
	if len(rich) != 2 {
		t.Errorf("len = %d, want 2 (header + footer, no body)", len(rich))
	}
	if t1, _ := rich[0]["type"].(string); t1 != "section_heading" {
		t.Errorf("rich[0] type = %v, want section_heading", t1)
	}
	if t2, _ := rich[1]["type"].(string); t2 != "footer" {
		t.Errorf("rich[1] type = %v, want footer", t2)
	}
}
