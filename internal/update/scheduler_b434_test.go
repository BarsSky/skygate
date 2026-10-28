// B434 — «не отработало плановое обновление» (live, 2026-10-28 08:00 MSK).
//
// The operator's Telegram pair:
//
//	⏰ Плановое обновление skygate начинается: v1.5.181+6aea3673 → v1.5.183 (job 150c20bc)
//	❌ Плановое обновление skygate v1.5.181+6aea3673 → v1.5.183 НЕ УДАЛОСЬ:
//	   backup tag: context canceled
//
// runScheduled created a 10-minute context, deferred its cancel(), and THEN spawned the
// goroutine that uses it. `defer` runs when runScheduled returns — one instruction after
// the spawn — so the upgrader's context was already cancelled before its first cancellable
// call, `git tag -f skygate-pre-update-<sha>`. The manual «Обновить» path, whose cancel()
// has always been deferred INSIDE its goroutine, worked on the same tree.
//
// WHY THE EXISTING TESTS WERE BLIND: they assert that a job was CREATED (last_run written,
// a state row with the offered target), not that it was allowed to run — a failed job
// satisfies both. So the failing assertion has to be about the CONTEXT the goroutine was
// handed, which is what NewUpgrader exists for.
package update

import (
	"context"
	"sync"
	"testing"
	"time"

	"skygate/internal/localtime"
)

// b434Recorder is the injected upgrader: it records the context's state at the moment the
// real upgrader would have made its first cancellable call, then finishes the job cleanly.
type b434Recorder struct {
	mu     sync.Mutex
	err    error
	called bool
	st     *StateStore
}

func (r *b434Recorder) Run(ctx context.Context, target string) {
	r.mu.Lock()
	r.called = true
	r.err = ctx.Err()
	st := r.st
	r.mu.Unlock()
	if st != nil {
		// The real upgrader ends a good job at PhaseDone; do the same so the notification
		// path of runScheduled is exercised too.
		st.SetPhase(PhaseDone, "B434 fake upgrader")
	}
}

func (r *b434Recorder) outcome() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called, r.err
}

// TestB434_ScheduledRunHandsALiveContextToTheUpgrader is the regression guard: with the
// cancel() deferred outside the goroutine, ctx.Err() here is context.Canceled and this
// fails — which is precisely the live failure, one level earlier and without a database
// write or a docker call.
func TestB434_ScheduledRunHandsALiveContextToTheUpgrader(t *testing.T) {
	b375ForceDocker(t)
	d := b375DB(t)
	now := localtime.Now()
	b375SetSchedule(t, d, true, b375NowMinute(now))

	deps := b375Deps(t, d, "v1.0.0")
	rec := &b434Recorder{st: deps.State}
	deps.NewUpgrader = func(repoPath string, st *StateStore, current string) ScheduledUpgrader {
		return rec
	}

	runScheduled(context.Background(), deps, "v9.9.9", now)

	// The goroutine runs concurrently with the return of runScheduled; give it a moment
	// (bounded) to record the context it was handed.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if called, _ := rec.outcome(); called {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	called, ctxErr := rec.outcome()
	if !called {
		t.Fatal("the injected upgrader was never called — runScheduled did not spawn the run")
	}
	if ctxErr != nil {
		t.Fatalf("the upgrader's context was already %v when it was called — every cancellable "+
			"step would fail (this is the live «backup tag: context canceled»)", ctxErr)
	}

	waitScheduledRuns(t)
	if st := deps.State.Get(); st == nil || st.Phase != PhaseDone {
		got := "<nil>"
		if st != nil {
			got = string(st.Phase)
		}
		t.Fatalf("the job ended at phase %s, want done — the scheduler's own completion path "+
			"must see a finished run, or it reports a failure it caused", got)
	}
}

// waitScheduledRuns joins runScheduled's goroutine so a test cannot leak it into the next
// one (the waitgroup is package-level because the process, not the caller, owns the run).
func waitScheduledRuns(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		scheduledRunWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the scheduled run did not finish within 10s")
	}
}
