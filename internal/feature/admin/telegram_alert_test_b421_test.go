// telegram_alert_test_b421_test.go — B421.
//
// The "test alert" button must tell the operator WHAT happened, and the ~40 legacy
// triggers (which still call SendAlert with a plain sentence) must reach the same
// transport and the same ledger — otherwise the page would report a healthy alert path
// while most notifications take a different one.
package admin

import (
	"strings"
	"testing"

	"skygate/internal/telegram"
)

// TestB421_AlertTestFlashNamesTheOutcome pins the sentence the operator reads. The B390
// lesson: a tester whose message cannot distinguish success from failure is worse than no
// tester, and here it must also distinguish "delivered" from "delivered through the
// admin-binding fallback" and from "the target was never resolved".
func TestB421_AlertTestFlashNamesTheOutcome(t *testing.T) {
	delivered := alertTestFlash(17, telegram.AlertDeliveryStats{
		LastOutcome:  telegram.AlertSent,
		LastPath:     "relay:karolina",
		TargetSource: telegram.TargetAdminBind,
		TargetMasked: "55****11",
	})
	if !strings.Contains(delivered, "#17") {
		t.Errorf("delivered flash does not name the alert id: %q", delivered)
	}
	if !strings.Contains(delivered, "relay:karolina") {
		t.Errorf("delivered flash does not name the transport: %q", delivered)
	}
	if !strings.Contains(delivered, telegram.TargetAdminBind) {
		t.Errorf("delivered flash does not name the recipient source: %q", delivered)
	}
	if !strings.Contains(delivered, "ДОСТАВЛЕНО") && !strings.Contains(delivered, "доставлено") {
		t.Errorf("delivered flash does not say delivered: %q", delivered)
	}

	notDelivered := alertTestFlash(0, telegram.AlertDeliveryStats{
		LastOutcome:  telegram.AlertNoChat,
		LastReason:   "telegram.chat_id is empty and no chat is bound",
		TargetSource: telegram.TargetNotResolved,
	})
	if !strings.Contains(notDelivered, "НЕ доставлено") {
		t.Errorf("failure flash does not say NOT delivered: %q", notDelivered)
	}
	if !strings.Contains(notDelivered, string(telegram.AlertNoChat)) {
		t.Errorf("failure flash does not name the outcome: %q", notDelivered)
	}
	if !strings.Contains(notDelivered, "chat_id") {
		t.Errorf("failure flash does not carry the reason: %q", notDelivered)
	}

	suppressed := alertTestFlash(0, telegram.AlertDeliveryStats{LastOutcome: telegram.AlertSuppressed})
	if !strings.Contains(suppressed, "дедупликац") {
		t.Errorf("suppressed flash does not explain the dedup window: %q", suppressed)
	}
	if strings.Contains(suppressed, "НЕ доставлено") {
		t.Errorf("a dedup suppression must not read as a delivery failure: %q", suppressed)
	}
}

// TestB421_AlertTestFlashNeverClaimsSuccessOnAnEmptyOutcome guards the default branch:
// an outcome this block did not think about must read as a failure, never as success.
func TestB421_AlertTestFlashNeverClaimsSuccessOnAnEmptyOutcome(t *testing.T) {
	msg := alertTestFlash(0, telegram.AlertDeliveryStats{})
	if !strings.Contains(msg, "НЕ доставлено") {
		t.Fatalf("empty outcome flash = %q, want a failure", msg)
	}
}
