// v1.5.0+ / B225 — unit tests for the Phase 4.4
// Telegram alerting on the Patroni failover / rollback
// paths. The tests use a recording notifier (a tiny
// test double that satisfies telegram.Notifier and
// captures every SendAlert call) so we can assert
// the alert text is operator-readable.
//
// The B225 contract surface is:
//   1. Patroni /switchover SUCCESS → ✅ alert with
//      "candidate (now primary)" + the timestamp
//   2. Patroni /switchover ERROR → ❌ alert with
//      the error message + the leader/candidate
//   3. Patroni rollback SUCCESS → ✅ alert
//      referencing the reversed failover
//   4. Patroni rollback ERROR → ❌ alert with the
//      original failover context
//   5. nil notifier → silent (the production
//      startup path always has NoopNotifier; this
//      is defensive)

package admin

import (
	"strings"
	"sync"
	"testing"
)

// recordingNotifier satisfies telegram.Notifier for the
// B225 tests. It records every SendAlert text and is
// goroutine-safe (the production handler might fire
// from a goroutine — though the current code path is
// synchronous; the mutex is future-proofing).
type recordingNotifier struct {
	mu     sync.Mutex
	texts  []string
	ids    []int64
}

func (r *recordingNotifier) SendTelegram(text string) {}
func (r *recordingNotifier) SendTelegramToChat(text string, chatID int64) {
	// unused
}
func (r *recordingNotifier) SendAlert(text string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	r.ids = append(r.ids, int64(len(r.texts)))
	return r.ids[len(r.ids)-1]
}
// Noop methods (other Notifier interface methods that
// B225 doesn't exercise) — return zero values.
func (r *recordingNotifier) SendUserMessage(userID int64, text string) int64 {
	return 0
}
func (r *recordingNotifier) BotUsernameCached() string  { return "" }
func (r *recordingNotifier) Stop()                      {}

func (r *recordingNotifier) first() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.texts) == 0 {
		return "", false
	}
	return r.texts[0], true
}

func (r *recordingNotifier) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.texts)
}

// TestSendFailoverAlert_SuccessFormat pins the ✅ alert
// text for a successful Patroni /switchover. The text
// must include the candidate (the new primary), the
// previous leader, the reason, and the timestamp.
func TestSendFailoverAlert_SuccessFormat(t *testing.T) {
	s := &Service{}
	rec := &recordingNotifier{}
	s.Notifier = rec

	body := "PG failover OK\ncandidate: skygate-standby (now primary)\nleader: skygate-primary (was primary)\nreason: planned maintenance\ntimestamp: 2026-09-03T10:00:00Z"
	s.sendFailoverAlert("✅", body)

	got, ok := rec.first()
	if !ok {
		t.Fatal("SendAlert was not called")
	}
	// Format: emoji + " " + body
	want := "✅ PG failover OK\ncandidate: skygate-standby (now primary)\nleader: skygate-primary (was primary)\nreason: planned maintenance\ntimestamp: 2026-09-03T10:00:00Z"
	if got != want {
		t.Errorf("alert text mismatch:\n  got:  %q\n  want: %q", got, want)
	}
	// Required fields the operator scans for.
	for _, field := range []string{"PG failover OK", "candidate: skygate-standby", "leader: skygate-primary", "reason: planned maintenance", "timestamp:"} {
		if !strings.Contains(got, field) {
			t.Errorf("alert missing field %q\n  text: %q", field, got)
		}
	}
}

// TestSendFailoverAlert_ErrorFormat pins the ❌ alert
// text for a failed Patroni /switchover. The text
// must surface the candidate + the underlying error
// so the operator can diagnose (Patroni said no, PG
// unreachable, etc).
func TestSendFailoverAlert_ErrorFormat(t *testing.T) {
	s := &Service{}
	rec := &recordingNotifier{}
	s.Notifier = rec

	body := "PG failover FAILED\ncandidate: skygate-standby\nleader: skygate-primary\nreason: planned maintenance\nerror: connection refused"
	s.sendFailoverAlert("❌", body)

	got, ok := rec.first()
	if !ok {
		t.Fatal("SendAlert was not called")
	}
	for _, field := range []string{"PG failover FAILED", "candidate:", "error: connection refused"} {
		if !strings.Contains(got, field) {
			t.Errorf("error alert missing field %q\n  text: %q", field, got)
		}
	}
}

// TestSendFailoverAlert_RollbackSuccessFormat pins the
// rollback ✅ alert. The text must reference the
// original failover so the operator can see "this
// rollback undid the B223 failover from X to Y".
func TestSendFailoverAlert_RollbackSuccessFormat(t *testing.T) {
	s := &Service{}
	rec := &recordingNotifier{}
	s.Notifier = rec

	body := "PG rollback OK\ncandidate: skygate-primary (now primary)\noriginal failover: skygate-standby → skygate-primary (now reversed)\nreason: rollback of failover skygate-standby → skygate-primary\ntimestamp: 2026-09-03T10:30:00Z"
	s.sendFailoverAlert("✅", body)

	got, ok := rec.first()
	if !ok {
		t.Fatal("SendAlert was not called")
	}
	for _, field := range []string{"PG rollback OK", "now primary", "original failover:", "now reversed", "timestamp:"} {
		if !strings.Contains(got, field) {
			t.Errorf("rollback alert missing field %q\n  text: %q", field, got)
		}
	}
}

// TestSendFailoverAlert_RollbackErrorFormat pins the
// rollback ❌ alert. The text must include the
// original failover context so the operator knows
// the cluster is in a partially-failed state.
func TestSendFailoverAlert_RollbackErrorFormat(t *testing.T) {
	s := &Service{}
	rec := &recordingNotifier{}
	s.Notifier = rec

	body := "PG rollback FAILED\ncandidate: skygate-primary (rollback target)\noriginal failover: skygate-standby → skygate-primary\nreason: rollback\nerror: patroni timeout"
	s.sendFailoverAlert("❌", body)

	got, ok := rec.first()
	if !ok {
		t.Fatal("SendAlert was not called")
	}
	for _, field := range []string{"PG rollback FAILED", "rollback target", "original failover:", "error: patroni timeout"} {
		if !strings.Contains(got, field) {
			t.Errorf("rollback error alert missing field %q\n  text: %q", field, got)
		}
	}
}

// TestSendFailoverAlert_NilNotifierIsSilent ensures
// the production startup path (Notifier not wired)
// doesn't panic. main.go always wires NoopNotifier,
// so this is defensive — but the explicit nil check
// in the helper makes the contract visible.
func TestSendFailoverAlert_NilNotifierIsSilent(t *testing.T) {
	s := &Service{}
	// No Notifier set.
	s.sendFailoverAlert("✅", "anything")
	// No panic, no error — just returns.
}

// TestSendFailoverAlert_ExactOneAlertPerCall asserts
// that sendFailoverAlert fires exactly ONE SendAlert
// call (not 0, not 2). This is the contract for the
// "best-effort, never duplicate" path.
func TestSendFailoverAlert_ExactOneAlertPerCall(t *testing.T) {
	s := &Service{}
	rec := &recordingNotifier{}
	s.Notifier = rec

	s.sendFailoverAlert("✅", "test 1")
	s.sendFailoverAlert("❌", "test 2")
	s.sendFailoverAlert("✅", "test 3")

	if n := rec.count(); n != 3 {
		t.Errorf("alert count = %d, want 3 (one SendAlert per sendFailoverAlert call)", n)
	}
}
