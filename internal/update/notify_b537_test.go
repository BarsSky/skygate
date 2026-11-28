// notify_b537_test.go — B537 (2026-11-28)
//
// The live case these tests pin: «⏰ Плановое обновление skygate начинается …» and then
// silence, because the goroutine that owns BOTH messages dies when the Docker swap
// recreates the container. The verdict therefore has to survive the process that
// composed it, and the tests check that property — not the formatting of the message.
package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// b537Sink records what would have been sent. rc == 0 means "the notifier refused"
// (bot disabled, chat unbound), which must NOT count as delivered.
type b537Sink struct {
	texts []string
	rc    int64
}

func (s *b537Sink) SendAlert(text string) int64 {
	s.texts = append(s.texts, text)
	return s.rc
}

// b537ScheduledStore is a Docker job in the given phase, started by the SCHEDULER.
func b537ScheduledStore(t *testing.T, phase Phase) (*StateStore, time.Time) {
	t.Helper()
	store, started := b428Store(t, phase)
	store.MarkScheduled()
	return store, started
}

// TestB537_ScheduledVerdictSurvivesItsReporter is the block's whole point: the resolver
// decides "this swap completed", the verdict is written to the STATE FILE (no sink
// exists in that process yet), and a store loaded from the same file — i.e. the next
// process — still owes it.
func TestB537_ScheduledVerdictSurvivesItsReporter(t *testing.T) {
	SetAlertSink(nil) // none registered yet: exactly the boot order
	defer SetAlertSink(nil)

	logPath := filepath.Join(t.TempDir(), "swap.log")
	store, started := b537ScheduledStore(t, PhaseVerify)
	if !resolvePendingSwap(context.Background(), store, "v1.5.173+abc12345",
		b428Fingerprint("newcontainer0123456"), logPath, started.Add(time.Minute)) {
		t.Fatal("the verdict did not resolve a job whose target build is serving")
	}
	if got := store.Get().Phase; got != PhaseDone {
		t.Fatalf("phase = %s, want done", got)
	}
	p := store.Get().PendingNotify
	if p == nil {
		t.Fatal("no verdict owed: the operator would again see «начинается» and nothing else")
	}
	if p.Kind != NotifyOK {
		t.Errorf("kind = %q, want %q", p.Kind, NotifyOK)
	}
	if p.From != "v1.5.170+4ec90a26" || p.To != "v1.5.173" {
		t.Errorf("the verdict must name both ends of the job: %q -> %q", p.From, p.To)
	}
	if p.DurationSeconds <= 0 {
		t.Errorf("an OK verdict must carry how long it took: %v", p.DurationSeconds)
	}

	reloaded := NewStateStore(store.path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := reloaded.Get()
	if got == nil || got.PendingNotify == nil {
		t.Fatal("the verdict did not survive a fresh store over the same file — a restart would erase it")
	}
	if got.PendingNotify.To != p.To || got.PendingNotify.JobID != p.JobID {
		t.Errorf("reloaded verdict = %+v, want the same job %q -> %q", got.PendingNotify, p.JobID, p.To)
	}
}

// TestB537_DeliveryIsAtLeastOnceAndAtMostOnce: a refused message stays pending; an
// accepted one is never sent twice.
func TestB537_DeliveryIsAtLeastOnceAndAtMostOnce(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "status.json"))
	store.Start("b537job", InstallDocker.String(), "v1.5.265+4d594e2e", "v1.5.270", nil, nil, "")
	store.MarkPendingNotify(PendingNotification{
		JobID: "b537job", Kind: NotifyOK, From: "v1.5.265+4d594e2e", To: "v1.5.270", DurationSeconds: 42,
	})

	sink := &b537Sink{rc: 0}
	SetAlertSink(sink)
	defer SetAlertSink(nil)

	if AlertPendingScheduledVerdict(store) {
		t.Fatal("a refused message was reported as delivered")
	}
	if store.Get().PendingNotify == nil {
		t.Fatal("a refused message was consumed — the operator would never see it")
	}

	sink.rc = 1
	if !AlertPendingScheduledVerdict(store) {
		t.Fatal("an accepted message was not reported as delivered")
	}
	if store.Get().PendingNotify != nil {
		t.Fatal("the marker was not cleared after delivery")
	}
	if AlertPendingScheduledVerdict(store) {
		t.Fatal("the same verdict was delivered twice")
	}
	if len(sink.texts) != 2 {
		t.Fatalf("SendAlert calls = %d, want 2 (one refused, one accepted)", len(sink.texts))
	}
	if !strings.Contains(sink.texts[1], "alert.update.scheduled_ok") && !strings.Contains(sink.texts[1], "v1.5.270") {
		t.Errorf("the delivered text names neither the key nor the target: %q", sink.texts[1])
	}
}

// TestB537_ManualJobOwesNoMessage: a manual update is watched by the operator's own
// browser; messaging it as well would be noise, and a second message about a job nobody
// asked to be notified about is exactly the sort of "helpful" alert this project keeps
// removing.
func TestB537_ManualJobOwesNoMessage(t *testing.T) {
	SetAlertSink(nil)
	defer SetAlertSink(nil)

	logPath := filepath.Join(t.TempDir(), "swap.log")
	store, started := b428Store(t, PhaseVerify) // NOT marked scheduled
	if !resolvePendingSwap(context.Background(), store, "v1.5.173+abc12345",
		b428Fingerprint("newcontainer0123456"), logPath, started.Add(time.Minute)) {
		t.Fatal("the verdict did not resolve the manual job")
	}
	if got := store.Get(); got.PendingNotify != nil {
		t.Fatalf("a manual job produced a pending notification: %+v", got.PendingNotify)
	}
	if AlertPendingScheduledVerdict(store) {
		t.Fatal("AlertPendingScheduledVerdict sent something for a manual job")
	}
}

// TestB537_FailedScheduledVerdictNamesTheReason: the failure verdict must carry the
// reason the resolver recorded, because "не удалось" without a reason is the message
// class this project keeps replacing with a named one.
func TestB537_FailedScheduledVerdictNamesTheReason(t *testing.T) {
	SetAlertSink(nil)
	defer SetAlertSink(nil)

	logPath := filepath.Join(t.TempDir(), "swap.log")
	store, started := b537ScheduledStore(t, PhaseSwap)
	// A swap log that reports its own failure short-circuits the grace period.
	if err := os.WriteFile(logPath, []byte("skygate-swap: FAILED: compose up exited 1\n"), 0o600); err != nil {
		t.Fatalf("write swap log: %v", err)
	}
	if !resolvePendingSwap(context.Background(), store, "v1.5.270", nil, logPath, started.Add(30*time.Second)) {
		t.Fatal("a swap log naming a failure must resolve the job immediately")
	}
	p := store.Get().PendingNotify
	if p == nil || p.Kind != NotifyFailed {
		t.Fatalf("pending = %+v, want a failed verdict", p)
	}
	if !strings.Contains(p.Error, "FAILED") {
		t.Errorf("the failed verdict must name the failure, got error=%q", p.Error)
	}
	if p.From != "v1.5.170+4ec90a26" || p.To != "v1.5.173" {
		t.Errorf("the verdict must name both ends of the job: %q -> %q", p.From, p.To)
	}
}

// TestB537_StalledScheduledJobAlsoOwesAVerdict: a run that dies before the swap is just
// as silent as a successful one, so the stalled branch must record the verdict too.
func TestB537_StalledScheduledJobAlsoOwesAVerdict(t *testing.T) {
	SetAlertSink(nil)
	defer SetAlertSink(nil)

	logPath := filepath.Join(t.TempDir(), "swap.log")
	store, started := b537ScheduledStore(t, PhasePullBuild)
	// Past the pre-swap grace with the old container still serving: a dead build.
	if !resolvePendingSwap(context.Background(), store, "v1.5.170+4ec90a26",
		b428Fingerprint("oldcontainer0123456789"), logPath, started.Add(PreSwapGracePeriod+time.Minute)) {
		t.Fatal("a job past its grace must be resolved")
	}
	p := store.Get().PendingNotify
	if p == nil || p.Kind != NotifyFailed {
		t.Fatalf("pending = %+v, want a failed verdict for the stalled scheduled run", p)
	}
}
