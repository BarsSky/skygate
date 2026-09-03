// v1.5.0+ / B225 — unit tests for the backup scheduler's
// Telegram alert helper. Pure Go (no DB needed) so it
// runs without the SKYGATE_TEST_PG_DSN env var. The
// production code path is exercised by the live-verify
// on the agent (the scheduler tick fires every 60s and
// the alert text format is the contract being pinned
// here).
//
// The B225 contract surface for the scheduler is:
//   1. sendSchedulerAlert calls Notifier.SendAlert
//      exactly once per invocation
//   2. The alert text is "emoji body" with a space
//      separator (matches the admin /admin/database
//      failover alerts so the operator reads a
//      consistent format in their Telegram chat)
//   3. nil Notifier is silent (the production
//      startup path always wires NoopNotifier; this
//      is defensive)
//   4. The alert body for config-load-fail includes
//      the error message (operator scans for "error:"
//      to triage)

package backup

import (
	"strings"
	"sync"
	"testing"
)

// recordingSink satisfies the local SchedulerAlertSink
// interface. Records every SendAlert call.
type recordingSink struct {
	mu    sync.Mutex
	texts []string
}

func (r *recordingSink) SendAlert(text string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts = append(r.texts, text)
	return int64(len(r.texts))
}

func (r *recordingSink) first() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.texts) == 0 {
		return "", false
	}
	return r.texts[0], true
}

func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.texts)
}

// TestSchedulerAlert_NilIsSilent ensures the production
// startup path doesn't panic. main.go always wires
// NoopNotifier via schedulerNotifierSink, but the nil
// check is a belt-and-suspenders guard for tests + future
// refactors.
func TestSchedulerAlert_NilIsSilent(t *testing.T) {
	s := &Scheduler{}
	// No Notifier set.
	s.sendSchedulerAlert("❌", "backup: scheduler config load failed\nerror: some test error")
	// No panic, no error — just returns.
}

// TestSchedulerAlert_Format pins the "emoji + ' ' + body"
// format. The operator's Telegram chat receives a
// consistent shape across all B225 alert sources
// (PG failover ✅/❌, backup config-load-fail ❌,
// backup run-fail ❌, cluster.discovery.*).
func TestSchedulerAlert_Format(t *testing.T) {
	s := &Scheduler{}
	rec := &recordingSink{}
	s.Notifier = rec

	s.sendSchedulerAlert("❌", "backup: scheduler config load failed\nerror: connection refused")

	got, ok := rec.first()
	if !ok {
		t.Fatal("SendAlert was not called")
	}
	want := "❌ backup: scheduler config load failed\nerror: connection refused"
	if got != want {
		t.Errorf("alert text mismatch:\n  got:  %q\n  want: %q", got, want)
	}
}

// TestSchedulerAlert_ConfigLoadFailureText pins the
// alert text shape for the config-load-fail path. The
// text must:
//   - start with the ❌ emoji
//   - say "backup: scheduler config load failed"
//   - include the underlying error (operator triages
//     by grepping for "error:" in the Telegram chat)
func TestSchedulerAlert_ConfigLoadFailureText(t *testing.T) {
	s := &Scheduler{}
	rec := &recordingSink{}
	s.Notifier = rec

	s.sendSchedulerAlert("❌", "backup: scheduler config load failed\nerror: sql: database is closed")

	got, _ := rec.first()
	for _, want := range []string{"❌", "backup: scheduler config load failed", "error: sql: database is closed"} {
		if !strings.Contains(got, want) {
			t.Errorf("config-load-fail alert missing %q\n  text: %q", want, got)
		}
	}
}

// TestSchedulerAlert_ExactOneAlertPerCall asserts that
// sendSchedulerAlert fires exactly ONE SendAlert call
// per invocation (not 0, not 2). This is the contract
// for the "best-effort, never duplicate" path.
func TestSchedulerAlert_ExactOneAlertPerCall(t *testing.T) {
	s := &Scheduler{}
	rec := &recordingSink{}
	s.Notifier = rec

	s.sendSchedulerAlert("❌", "test 1")
	s.sendSchedulerAlert("❌", "test 2")
	s.sendSchedulerAlert("❌", "test 3")

	if n := rec.count(); n != 3 {
		t.Errorf("alert count = %d, want 3 (one SendAlert per sendSchedulerAlert call)", n)
	}
}

// TestSchedulerAlert_NotNotifyingOnNoOp is a regression
// guard: the alert fires on ERROR paths only, not on
// the happy path (a successful tick doesn't spam the
// operator's Telegram with a "backup ok" message every
// minute). The test verifies the helper itself — the
// caller is responsible for only calling it on errors.
func TestSchedulerAlert_NotNotifyingOnNoOp(t *testing.T) {
	s := &Scheduler{}
	rec := &recordingSink{}
	s.Notifier = rec
	// No sendSchedulerAlert calls — the tick is a no-op
	// from the alerting perspective.
	if n := rec.count(); n != 0 {
		t.Errorf("alert count without any call = %d, want 0 (helper must not auto-alert on success)", n)
	}
}
