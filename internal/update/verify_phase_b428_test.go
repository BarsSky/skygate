// internal/update/verify_phase_b428_test.go — B428 (2026-10-27).
//
// Operator report: «с версии 1.5.70 не могу обновить через панель обновления,
// окно появляется но обновление не начинается и не показывает никаких ошибок».
//
// Measured on the live install: the panel was serving v1.5.170, the last
// `update_apply` audit row was 2026-10-26 07:40:12 (job bf263082, target
// v1.5.170), and that job's state file said
//
//	"phase": "verify", "started_at": "2026-10-26T07:40:12Z", "finished_at": "0001-01-01…"
//
// with ~150 render-noise lines an hour for 22 hours. Two defects held each other
// up: (1) resolvePendingSwap admitted ONLY build_done/rolled_back, so a job whose
// orchestrator died at `verify` — the normal outcome of a swap that recreates the
// container, because the polling goroutine dies with the old process — was never
// resolved; (2) the template had its own hardcoded "finished" list that omitted
// `verify`, so that stale job armed a location.reload() every 5 s, and the queued
// reload cancelled the operator's own form submission — no POST, no audit row, no
// error, every time.
//
// These tests pin the phase matrix and the two grace periods, so neither edge can
// drift back: every phase a job can die in is resolved (by the same evidence), and
// a pre-swap phase that still has real work to do is given the orchestrator's own
// budget before it is declared dead.
package update

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestB428_PhaseIsTerminal(t *testing.T) {
	terminal := map[Phase]bool{
		PhaseDone:       true,
		PhaseFailed:     true,
		PhaseRolledBack: true,
		// not terminal: a Docker job stops here and waits for the swap verdict
		PhaseBuildDone: false,
		PhasePending:   false,
		PhaseBackup:    false,
		PhasePullBuild: false,
		PhaseMigrate:   false,
		PhaseSwap:      false,
		PhaseVerify:    false,
	}
	for p, want := range terminal {
		if got := p.IsTerminal(); got != want {
			t.Errorf("Phase(%q).IsTerminal() = %v, want %v", p, got, want)
		}
	}
}

// b428Store starts a Docker job in the given phase, with a known start time.
func b428Store(t *testing.T, phase Phase) (*StateStore, time.Time) {
	t.Helper()
	store := NewStateStore(filepath.Join(t.TempDir(), "status.json"))
	store.Start("b428job", InstallDocker.String(), "v1.5.170+4ec90a26", "v1.5.173", nil, nil, "")
	store.SetPhase(phase, "drill")
	store.SetSwapFromContainer("oldcontainer0123456789")
	started := time.Now().UTC()
	store.mu.Lock()
	store.state.StartedAt = started
	store.mu.Unlock()
	return store, started
}

func b428Fingerprint(id string) func(context.Context) (*ContainerFingerprint, error) {
	return func(context.Context) (*ContainerFingerprint, error) {
		return &ContainerFingerprint{ID: id}, nil
	}
}

// TestB428_EveryNonTerminalPhaseIsResolved is the heart of the block: the phase a
// job died in must not decide whether it is ever resolved.
func TestB428_EveryNonTerminalPhaseIsResolved(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "swap.log")
	for _, phase := range []Phase{
		PhasePending, PhaseBackup, PhasePullBuild, PhaseMigrate,
		PhaseSwap, PhaseVerify, PhaseBuildDone, PhaseRolledBack,
	} {
		// (a) the build this phase is waiting for is serving → the job completed,
		// whatever the phase string says. A rolled-back job waits for the FROM
		// version (the rollback restored it), every other phase for the target.
		store, started := b428Store(t, phase)
		grace := stalledJobGrace(phase)
		serving := "v1.5.173+abc12345"
		wantPhase := PhaseDone
		if phase == PhaseRolledBack {
			serving = "v1.5.170+4ec90a26"
			wantPhase = PhaseRolledBack
		}
		if !resolvePendingSwap(context.Background(), store, serving,
			b428Fingerprint("newcontainer0123456"), logPath, started.Add(grace+time.Minute)) {
			t.Fatalf("%s: the verdict did not resolve a job whose target build is serving", phase)
		}
		if got := store.Get().Phase; got != wantPhase {
			t.Errorf("%s: phase = %s, want %s", phase, got, wantPhase)
		}

		// (b) the build this job is NOT waiting for is serving and the grace is
		// gone → failed, and the error must say WHICH phase the orchestrator died
		// in. A rolled-back job waits for the FROM version, so its negative case
		// needs a third build — using the FROM version here would (correctly)
		// confirm the rollback instead.
		store, started = b428Store(t, phase)
		wrongBuild := "v1.5.170+4ec90a26"
		if phase == PhaseRolledBack {
			wrongBuild = "v1.5.99+deadbeef"
		}
		if !resolvePendingSwap(context.Background(), store, wrongBuild,
			b428Fingerprint("oldcontainer0123456789"), logPath, started.Add(grace+time.Minute)) {
			t.Fatalf("%s: after the grace period the job must be resolved", phase)
		}
		st := store.Get()
		if st.Phase != PhaseFailed {
			t.Errorf("%s: phase = %s, want failed", phase, st.Phase)
		}
		if !strings.Contains(st.Error, "stopped in phase "+string(phase)) {
			t.Errorf("%s: the error must name the phase it died in: %q", phase, st.Error)
		}
	}
}

// TestB428_PreSwapGraceIsLongerThanSwapGrace: a job that is legitimately still
// fetching/building must not be declared dead after the 4-minute swap grace — the
// orchestrator that owns it has a 10-minute budget.
func TestB428_PreSwapGraceIsLongerThanSwapGrace(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "swap.log")
	hardWork := []Phase{PhasePending, PhaseBackup, PhasePullBuild, PhaseMigrate}
	for _, phase := range hardWork {
		store, started := b428Store(t, phase)
		if got := stalledJobGrace(phase); got <= SwapGracePeriod {
			t.Fatalf("%s: grace = %s, want more than the post-swap grace (%s) — a cold build takes minutes",
				phase, got, SwapGracePeriod)
		}
		// 6 minutes in: past the swap grace, well inside the orchestrator's budget.
		if resolvePendingSwap(context.Background(), store, "v1.5.170+4ec90a26",
			b428Fingerprint("oldcontainer0123456789"), logPath, started.Add(6*time.Minute)) {
			t.Fatalf("%s: a healthy 6-minute-old build was declared dead", phase)
		}
		if got := store.Get().Phase; got != phase {
			t.Errorf("%s: phase changed to %s while the job could still be working", phase, got)
		}
		// ...and past its own grace it is resolved.
		if !resolvePendingSwap(context.Background(), store, "v1.5.170+4ec90a26",
			b428Fingerprint("oldcontainer0123456789"), logPath, started.Add(PreSwapGracePeriod+time.Minute)) {
			t.Fatalf("%s: a job past its pre-swap grace was not resolved", phase)
		}
	}
}

// TestB428_TheLiveCase: the exact state the operator's install was stuck in —
// a job at `verify` left behind by a container swap, whose target build is now
// serving. It must be resolved on the first page render / boot, not left forever.
func TestB428_TheLiveCase(t *testing.T) {
	store, started := b428Store(t, PhaseVerify)
	logPath := filepath.Join(t.TempDir(), "swap.log")
	// 22 hours later, the new build (v1.5.170 was the target of that job) is serving.
	if !resolvePendingSwap(context.Background(), store, "v1.5.170+4ec90a26",
		b428Fingerprint("recreatedcontainer01"), logPath, started.Add(22*time.Hour)) {
		t.Fatal("the job stuck at verify was not resolved — this is the operator's 22-hour hang")
	}
	if got := store.Get().Phase; got != PhaseDone {
		t.Fatalf("phase = %s, want done", got)
	}
}
