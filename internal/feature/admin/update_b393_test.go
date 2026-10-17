package admin

// B393 — the half of the false "done" that lived in this package.
//
// confirmPendingSwap used to promote any /admin/update job to `done` on the
// first 200 from /healthz, and the OLD container answers that. These tests pin
// that the verdict is now evidence-based: a stale build in the unchanged
// container fails the job (with a diagnostic), a matching build completes it.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"skygate/internal/update"
)

// withSwapFingerprint points the verdict at a fake container for the
// duration of one test.
func withSwapFingerprint(t *testing.T, id string) {
	t.Helper()
	old := update.SwapFingerprint
	update.SwapFingerprint = func(context.Context) (*update.ContainerFingerprint, error) {
		return &update.ContainerFingerprint{ID: id}, nil
	}
	oldLog := update.SwapLogFile
	update.SwapLogFile = filepath.Join(t.TempDir(), "swap.log")
	t.Cleanup(func() {
		update.SwapFingerprint = old
		update.SwapLogFile = oldLog
	})
}

func pendingDockerState(t *testing.T, age time.Duration) (*update.StateStore, *update.State) {
	t.Helper()
	// Write the status file the OLD process would have left behind and load
	// it: the verdict reads the store, and StateStore.Get returns a copy, so
	// the job's age has to be in the file to be observed (exactly as in
	// production, where the new container loads it from disk).
	path := filepath.Join(t.TempDir(), "status.json")
	seed := &update.State{
		JobID:               "job-b393",
		InstallKind:         update.InstallDocker.String(),
		FromVersion:         "v1.5.130+aae95368",
		ToVersion:           "v1.5.131",
		Phase:               update.PhaseBuildDone,
		StartedAt:           time.Now().UTC().Add(-age),
		SwapFromContainerID: "oldcontainer0123456789",
	}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	store := update.NewStateStore(path)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load state: %v", err)
	}
	st := store.Get()
	if st == nil {
		t.Fatal("no state after Load")
	}
	return store, st
}

// TestConfirmPendingSwap_RejectsStaleBuild is the regression for the live
// 2026-10-16 defect: the panel kept serving v1.5.130, the job targeted
// v1.5.131, nothing swapped — and the page said "done".
func TestConfirmPendingSwap_RejectsStaleBuild(t *testing.T) {
	withSwapFingerprint(t, "oldcontainer0123456789")
	// Older than the grace period, so the verdict is decided on its first
	// call and the test does not sit through the grace window.
	store, st := pendingDockerState(t, update.SwapGracePeriod+time.Minute)
	s := &Service{UpdateState: store, BuildVersion: "v1.5.130+aae95368"}

	s.confirmPendingSwap(context.Background(), st, store)

	got := store.Get()
	if got.Phase != update.PhaseFailed {
		t.Fatalf("phase = %s, want failed (a stale build must never be reported as done)", got.Phase)
	}
	if !strings.Contains(got.Error, "never swapped") {
		t.Errorf("error = %q, want it to say the swap never ran", got.Error)
	}
	if got.ManualFallback != true {
		t.Error("ManualFallback must be set so the page shows the manual steps")
	}
}

// TestConfirmPendingSwap_AcceptsMatchingBuild pins the happy path is not lost.
func TestConfirmPendingSwap_AcceptsMatchingBuild(t *testing.T) {
	withSwapFingerprint(t, "newcontainer0123456789")
	store, st := pendingDockerState(t, time.Minute)
	s := &Service{UpdateState: store, BuildVersion: "v1.5.131+b9b22468"}

	s.confirmPendingSwap(context.Background(), st, store)

	if got := store.Get().Phase; got != update.PhaseDone {
		t.Fatalf("phase = %s, want done", got)
	}
}

// TestConfirmPendingSwap_StillWaitingKeepsTheJobOpen pins the grace window:
// while the helper may still be working, the phase must stay build_done (not
// "done", not "failed").
func TestConfirmPendingSwap_StillWaitingKeepsTheJobOpen(t *testing.T) {
	withSwapFingerprint(t, "oldcontainer0123456789")
	store, st := pendingDockerState(t, 5*time.Second)
	s := &Service{UpdateState: store, BuildVersion: "v1.5.130+aae95368"}

	// The in-render wait is bounded (confirmSwapBudget); the state must be
	// untouched after it.
	s.confirmPendingSwap(context.Background(), st, store)

	if got := store.Get().Phase; got != update.PhaseBuildDone {
		t.Fatalf("phase = %s, want build_done while the grace window is open", got)
	}
}
