// B390 (2026-10-25, TD-33 H5) — «Сообщение отправлено» only when it was.
//
// The handler used to count its loop iterations instead of deliveries. These tests
// pin the two halves the operator reads: what is counted, and what the flash says.
package admin

import (
	"errors"
	"strings"
	"testing"
)

func TestB390_SendTestCountsDeliveriesNotAttempts(t *testing.T) {
	// Two targets, one refusal. The old code would have said "2".
	out := sendTestMessages(2, func(i int) error {
		if i == 1 {
			return errors.New("telegram: API refused the message (HTTP 400): chat not found")
		}
		return nil
	}, func(i int) string { return []string{"global chat_id=1", "binding chat_id=2"}[i] })

	if len(out.sent) != 1 {
		t.Errorf("B390 H5: sent = %d, want 1 (only one target accepted the message): %+v", len(out.sent), out)
	}
	if len(out.failed) != 1 || !strings.Contains(out.failed[0], "chat not found") {
		t.Errorf("B390 H5: the refusal is not recorded with its reason: %+v", out.failed)
	}
	flash := testFlashMessage(out)
	if strings.Contains(flash, "Сообщение отправлено (2") {
		t.Errorf("B390 H5: the flash still claims two deliveries: %q", flash)
	}
	if !strings.Contains(flash, "НЕ отправлено 1") {
		t.Errorf("B390 H5: the flash does not name the failed delivery: %q", flash)
	}
	if !strings.Contains(flash, "chat not found") {
		t.Errorf("B390 H5: the flash hides WHY: %q", flash)
	}
}

func TestB390_SendTestAllFailedNeverSaysSent(t *testing.T) {
	out := sendTestMessages(2, func(int) error {
		return errors.New("telegram: no token and chat_id configured")
	}, func(i int) string { return "binding chat_id=" + string(rune('1'+i)) })

	flash := testFlashMessage(out)
	if strings.Contains(flash, "отправлено (") && !strings.Contains(flash, "НЕ отправлено") {
		t.Errorf("B390 H5: a total failure is reported as a success: %q", flash)
	}
	if !strings.Contains(flash, "НЕ отправлено ни одного") {
		t.Errorf("B390 H5: the flash does not say nothing was delivered: %q", flash)
	}
	if !strings.Contains(flash, "token") {
		t.Errorf("B390 H5: the flash does not carry the reason: %q", flash)
	}
}

func TestB390_SendTestAllSucceededSaysSo(t *testing.T) {
	out := sendTestMessages(1, func(int) error { return nil }, func(int) string { return "global chat_id=1" })
	flash := testFlashMessage(out)
	if !strings.Contains(flash, "Сообщение отправлено (1 шт.)") {
		t.Errorf("B390 H5: a real success is not reported as one: %q", flash)
	}
	if strings.Contains(flash, "НЕ отправлено") {
		t.Errorf("B390 H5: a clean success mentions failures: %q", flash)
	}
}
