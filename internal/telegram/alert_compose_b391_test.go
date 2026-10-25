// B391 (2026-10-25, TD-34) — the alert composer and the alert transport.
//
// What these tests protect, in the operator's terms:
//
//   - an alert arrives with parse_mode=HTML (before B391 it arrived as plain text in a
//     ``` fence, so every backtick the author wrote showed up as a character);
//   - a hostname or an error string containing "<" cannot make Telegram reject the
//     message (the sanitiser at the wire, plus escaping in the composer);
//   - a long alert is TRIMMED with a marker instead of being refused outright and LOST
//     (Telegram answers 400 above 4096 characters — the pre-B391 behaviour dropped it);
//   - a flapping component does not flood the chat (dedup key + window).
package telegram

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

func b391DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := db.OpenWithDialect("sqlite:" + t.TempDir() + "/b391.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	// The bot is configured: token + chat_id, the pair LoadTelegramSendTarget wants.
	for k, v := range map[string]string{"telegram.bot_token": "123:abc", "telegram.chat_id": "4242"} {
		if _, err := d.Exec(`INSERT INTO global_settings (key, value) VALUES ($1, $2)`, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	return d
}

// b391CapturingNotifier wires a RealNotifier to a fake Bot API that records payloads.
func b391CapturingNotifier(t *testing.T, d *sql.DB, status int, response string) (*RealNotifier, *[]map[string]any) {
	t.Helper()
	var payloads []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(newCaptureHandler(&payloads, status, response)))
	t.Cleanup(srv.Close)
	return &RealNotifier{apiBase: srv.URL, client: srv.Client(), db: d}, &payloads
}

func newCaptureHandler(payloads *[]map[string]any, status int, response string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		*payloads = append(*payloads, m)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}
}

// --- the composer ----------------------------------------------------------

func TestB391_ComposerSetsHTMLSeverityAndEscapes(t *testing.T) {
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	body := ComposeAlert(Alert{
		Severity: SeverityCritical,
		Title:    "disk <almost> full & slow",
		Facts: []AlertFact{
			{Label: "path", Value: "/var/lib/<x>"},
		},
		Details: "rm -rf /tmp/a&b",
		Lang:    "en",
	})
	if !strings.Contains(body, severityEmoji(SeverityCritical)) {
		t.Errorf("B391: the severity glyph is missing — a critical alert must LOOK critical\nbody: %s", body)
	}
	if !strings.Contains(body, "&lt;almost&gt;") || !strings.Contains(body, "&amp;") {
		t.Errorf("B391: the title is not HTML-escaped — a '<' in a message makes Telegram answer 400\nbody: %s", body)
	}
	if !strings.Contains(body, "&lt;x&gt;") {
		t.Errorf("B391: a fact value is not escaped\nbody: %s", body)
	}
	if !strings.Contains(body, "<b>") || !strings.Contains(body, "<pre>") {
		t.Errorf("B391: the composer did not use the markup it is allowed to write\nbody: %s", body)
	}
}

func TestB391_ComposerTrimsWithAMarkerInsteadOfDropping(t *testing.T) {
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	long := strings.Repeat("x", 12000)
	body := ComposeAlert(Alert{Severity: SeverityWarn, Title: "big", Details: long, Lang: "en"})
	if n := len([]rune(body)); n > maxAlertBody+200 {
		t.Errorf("B391: the composed body is %d runes — Telegram rejects >4096 and the old path lost the alert", n)
	}
	if !strings.Contains(body, "trimmed") {
		t.Errorf("B391: the truncated body does not SAY it was truncated — half a stack trace reads as the whole story\nbody tail: %s", body[len(body)-120:])
	}
}

func TestB391_ComposerUsesTheCatalogueTitle(t *testing.T) {
	if i18n.GlobalCatalog == nil {
		i18n.SetGlobal(i18n.New())
	}
	ru := ComposeAlert(Alert{Severity: SeverityWarn, TitleKey: "alert.disk.title_warn", Lang: "ru"})
	en := ComposeAlert(Alert{Severity: SeverityWarn, TitleKey: "alert.disk.title_warn", Lang: "en"})
	if ru == en {
		t.Errorf("B391: the title is not localized (RU == EN): %s", ru)
	}
	if !strings.Contains(ru, i18n.T("ru", "alert.disk.title_warn")) {
		t.Errorf("B391: the RU title does not come from the catalogue: %s", ru)
	}
}

// --- the transport ---------------------------------------------------------

func TestB391_AlertGoesOutAsHTMLWithoutAFence(t *testing.T) {
	d := b391DB(t)
	n, payloads := b391CapturingNotifier(t, d, 200, `{"ok":true,"result":{"message_id":1}}`)

	id := n.SendAlert("legacy <plain> text with `backticks`")
	if id == 0 {
		t.Fatal("B391: SendAlert returned no id for a configured bot")
	}
	if len(*payloads) != 1 {
		t.Fatalf("B391: requests = %d, want 1", len(*payloads))
	}
	p := (*payloads)[0]
	if got, _ := p["parse_mode"].(string); got != "HTML" {
		t.Errorf("B391: parse_mode = %q, want HTML — the pre-B391 alert path set no parse mode at all", got)
	}
	text, _ := p["text"].(string)
	if strings.HasPrefix(text, "```") {
		t.Errorf("B391: the body is wrapped in a literal fence again — that is what made every backtick show\nbody: %s", text)
	}
	if !strings.Contains(text, "<plain>") {
		// The sanitiser escapes an unsupported tag; the WORD must still be there.
		if !strings.Contains(text, "&lt;plain&gt;") {
			t.Errorf("B391: the body lost its content in escaping: %s", text)
		}
	}
	if !strings.HasPrefix(text, "[#") {
		t.Errorf("B391: the #[id] prefix /ack depends on is missing: %s", text)
	}
}

func TestB391_ComposedAlertKeepsItsMarkupAndNumbersTheRow(t *testing.T) {
	d := b391DB(t)
	n, payloads := b391CapturingNotifier(t, d, 200, `{"ok":true,"result":{"message_id":1}}`)

	id := n.SendAlertEvent(Alert{
		Severity: SeverityCritical,
		TitleKey: "alert.disk.title_critical",
		Facts:    []AlertFact{{Label: "used", Value: "97%"}},
		Lang:     "ru",
	})
	if id == 0 {
		t.Fatal("B391: SendAlertEvent returned no id")
	}
	text, _ := (*payloads)[0]["text"].(string)
	if !strings.Contains(text, "<b>") || !strings.Contains(text, "97%") {
		t.Errorf("B391: the composed markup did not survive the wire: %s", text)
	}
	if !strings.Contains(text, "[#1]") {
		t.Errorf("B391: the numbered prefix is missing: %s", text)
	}
	// /ack reads telegram_alerts.body: it must be the PLAIN rendering, not the HTML.
	var body string
	if err := d.QueryRow(`SELECT body FROM telegram_alerts WHERE id = $1`, id).Scan(&body); err != nil {
		t.Fatalf("read telegram_alerts: %v", err)
	}
	if strings.Contains(body, "<b>") {
		t.Errorf("B391: /ack would show raw markup: %q", body)
	}
	if !strings.Contains(body, "critical") {
		t.Errorf("B391: the stored row does not carry the severity: %q", body)
	}
}

func TestB391_DedupSuppressesRepeatsInsideTheWindow(t *testing.T) {
	d := b391DB(t)
	n, payloads := b391CapturingNotifier(t, d, 200, `{"ok":true,"result":{"message_id":1}}`)
	a := Alert{Severity: SeverityWarn, Title: "flap", DedupKey: "test-flap", DedupTTL: 10 * time.Minute, Lang: "en"}
	if id := n.SendAlertEvent(a); id == 0 {
		t.Fatal("B391: the first alert of a key must be sent")
	}
	if id := n.SendAlertEvent(a); id != 0 {
		t.Errorf("B391: a repeat inside the dedup window was sent again (id=%d)", id)
	}
	if len(*payloads) != 1 {
		t.Errorf("B391: requests = %d, want 1 — the flap flooded the chat", len(*payloads))
	}
	// A different key is a different event.
	other := a
	other.DedupKey = "test-flap-2"
	if id := n.SendAlertEvent(other); id == 0 {
		t.Error("B391: a different dedup key was suppressed")
	}
}

func TestB391_AlertTransportReportsRefusal(t *testing.T) {
	d := b391DB(t)
	n, _ := b391CapturingNotifier(t, d, 400, `{"ok":false,"description":"chat not found"}`)
	// The alert path must not panic and must log the refusal; the id is still
	// returned because the ROW exists (that is the /ack numbering contract).
	if id := n.SendAlert("boom"); id == 0 {
		t.Error("B391: a refused send must still return the alert id (the row exists)")
	}
}
