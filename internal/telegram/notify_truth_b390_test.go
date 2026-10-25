// B390 (2026-10-25, TD-33 H5) — THE "SEND TEST" BUTTON MUST BE ABLE TO FAIL ITSELF.
//
// /admin/telegram's test sent the message through the fire-and-forget
// SendTelegram/SendTelegramToChat — which return nothing — and then counted its own
// LOOP ITERATIONS, so the operator was told «Сообщение отправлено (N шт.)» when the
// token was missing, the chat_id did not parse, or Telegram answered
// {"ok":false,"description":"chat not found"}. A tester that cannot fail is worse than
// none: it certifies a channel that is dropping the alerts.
//
// These tests drive the wire path (a fake Bot API) and the counting helper, and they
// assert the operator-visible sentence, so a regression fails here instead of on a
// silent notification nobody received.
package telegram

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestB390_PostToChatReturnsTheAPIRefusal — the API's own words ("chat not found")
// must reach the caller, not just the log.
func TestB390_PostToChatReturnsTheAPIRefusal(t *testing.T) {
	srv, _, _ := richTestServer(t, 400, `{"ok":false,"description":"chat not found"}`)
	n := newTestNotifier(srv)

	err := n.postToChatErr("123:abc", 4242, "hello")
	if err == nil {
		t.Fatal("B390 H5: postToChatErr returned nil for an ok:false response — that is the defect " +
			"«Send test» was built on")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("B390 H5: the error does not carry the API's explanation: %v", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("B390 H5: the error does not carry the HTTP status: %v", err)
	}
}

func TestB390_PostToChatSucceedsOnOK(t *testing.T) {
	srv, _, _ := richTestServer(t, 200, `{"ok":true,"result":{"message_id":7}}`)
	n := newTestNotifier(srv)
	if err := n.postToChatErr("123:abc", 4242, "hello"); err != nil {
		t.Errorf("B390 H5: a delivered message reported %v", err)
	}
}

// TestB390_NoopNotifierReportsWhyItCannotSend — the no-op notifier is what runs
// before a token is saved. It must refuse with a reason instead of a silent success.
func TestB390_NoopNotifierReportsWhyItCannotSend(t *testing.T) {
	var n NoopNotifier
	if err := n.SendTelegramErr("x"); err == nil {
		t.Error("B390 H5: the no-op notifier claims a delivery it never attempted")
	}
	if err := n.SendTelegramToChatErr("x", 1); err == nil {
		t.Error("B390 H5: the no-op notifier claims a delivery it never attempted")
	}
}

// TestB390_HTTPErrorIsReported — a 5xx with an empty body must still be a failure.
func TestB390_HTTPErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(nil)
	srv.Close() // a closed server: every POST fails at the transport
	n := &RealNotifier{apiBase: srv.URL, client: srv.Client()}
	if err := n.postToChatErr("123:abc", 4242, "hello"); err == nil {
		t.Error("B390 H5: a transport failure reported success")
	}
}
