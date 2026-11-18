// scheduler_native_b492_test.go — TD-42: the SCHEDULE must drive a NATIVE install, not only Docker.
//
// The measured defect: `tick` step 4b and `runScheduled` both returned for every install kind except
// Docker, so an armed schedule on the production host (systemd since the 2026-11-15 migration) did
// nothing and logged "NOT SUPPORTED" once a slot — while the MANUAL native path had worked since
// B261. The operator's ask: «и для нативного варианта сделать автообновление по расписанию».
//
// CI runs `go test -race` (measured: `.github/workflows/ci.yml`), so the recorder below takes its own
// mutex: the upgrader's Run runs in the goroutine runScheduled spawns, and a plain
// `rec.target = target` is a data race that passes on a laptop and fails on every CI run. The first
// version of this file did exactly that, and CI caught it.
package update

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"skygate/internal/localtime"
)

// b492RecordedRun is one call the scheduler made to an upgrader, whichever kind it chose.
type b492RecordedRun struct {
	which  string // "docker" | "native"
	target string
}

// b492Recorder collects upgrader calls from both goroutines the scheduler uses.
type b492Recorder struct {
	mu   sync.Mutex
	runs []b492RecordedRun
}

func (r *b492Recorder) record(which, target string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, b492RecordedRun{which: which, target: target})
}

func (r *b492Recorder) snapshot() []b492RecordedRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]b492RecordedRun, len(r.runs))
	copy(out, r.runs)
	return out
}

// b492Upgrader is the stand-in upgrader: it records the call and returns immediately.
type b492Upgrader struct {
	r     *b492Recorder
	which string
}

func (u b492Upgrader) Run(ctx context.Context, target string) { u.r.record(u.which, target) }

// b492InstallKind points the scheduler's kind seam at a fixed value for one test.
func b492InstallKind(t *testing.T, kind InstallKind) {
	t.Helper()
	prev := detectInstallKindFn
	detectInstallKindFn = func() InstallKind { return kind }
	t.Cleanup(func() { detectInstallKindFn = prev })
}

// TestB492_SchedulerDrivesNativeAndDocker is the whole contract in one table: the predicate that
// gates the schedule must say YES to the native kinds (they are exactly as capable as Docker here —
// both stage a request and let a privileged component swap) and NO only when the host was not
// recognised at all.
func TestB492_SchedulerDrivesNativeAndDocker(t *testing.T) {
	cases := []struct {
		kind InstallKind
		want bool
	}{
		{InstallDocker, true},
		{InstallSystemd, true},
		{InstallOpenRC, true},
		{InstallBare, true},
		{InstallUnknown, false},
	}
	for _, tc := range cases {
		if got := SchedulerDrivesKind(tc.kind); got != tc.want {
			t.Fatalf("SchedulerDrivesKind(%s) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

// TestB492_NativeScheduledRunStagesTheReleaseTag — the native path must hand the privileged applier a
// RELEASE TAG, not the display label: the applier downloads
// `skygate-<tag>-linux-<arch>.tar.gz`, so "v1.5.236+abc1234" would 404 AFTER the service stopped.
func TestB492_NativeScheduledRunStagesTheReleaseTag(t *testing.T) {
	b492InstallKind(t, InstallSystemd)
	d := b375DB(t)
	b375SetSchedule(t, d, true, "03:00")
	deps := b375Deps(t, d, "v1.5.230")

	rec := &b492Recorder{}
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		return b492Upgrader{r: rec, which: "native"}
	}
	deps.NewUpgrader = func(repoPath string, st *StateStore, current string) ScheduledUpgrader {
		return b492Upgrader{r: rec, which: "docker"}
	}

	runScheduled(context.Background(), deps, "v9.9.9", time.Now())
	b375JoinScheduledRun(t)

	runs := rec.snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected exactly one upgrader call, got %d (%+v)", len(runs), runs)
	}
	if runs[0].which != "native" {
		t.Fatalf("a systemd host must go through the NATIVE updater, got %q", runs[0].which)
	}
	if runs[0].target != "v9.9.9" {
		t.Fatalf("the native updater must receive the release tag, got %q", runs[0].target)
	}
}

// TestB492_NativeScheduledRunRefusesAGitDescribeTarget — the failure must land BEFORE the helper is
// asked to stage anything, and it must say what to do. Otherwise the applier stops the service and
// then 404s, which on a scheduled unattended run means a panel that stays down until someone notices.
func TestB492_NativeScheduledRunRefusesAGitDescribeTarget(t *testing.T) {
	b492InstallKind(t, InstallSystemd)
	d := b375DB(t)
	b375SetSchedule(t, d, true, "03:00")
	deps := b375Deps(t, d, "v1.5.230")

	rec := &b492Recorder{}
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		return b492Upgrader{r: rec, which: "native"}
	}

	// A git-describe build label: exactly what BuildVersion looks like on an untagged tree.
	runScheduled(context.Background(), deps, "v1.5.8-36-g50d4c2d", time.Now())
	b375JoinScheduledRun(t)

	if runs := rec.snapshot(); len(runs) != 0 {
		t.Fatalf("the native updater was called %d time(s) for a target that cannot be downloaded", len(runs))
	}
	st := deps.State.Get()
	if st == nil {
		t.Fatal("no state was written")
	}
	if st.Phase != PhaseFailed {
		t.Fatalf("a non-release target must fail the job, phase=%s", st.Phase)
	}
	if !strings.Contains(st.Error, "not a release tag") {
		t.Fatalf("the failure must name the real problem, got %q", st.Error)
	}
}

// TestB492_UnknownKindStillSkipsLoudly — the pre-B492 guard was right for a host whose kind was not
// detected; it must keep saying so instead of staging a request nobody will apply.
func TestB492_UnknownKindStillSkipsLoudly(t *testing.T) {
	b492InstallKind(t, InstallUnknown)
	d := b375DB(t)
	b375SetSchedule(t, d, true, "03:00")
	deps := b375Deps(t, d, "v1.5.230")

	rec := &b492Recorder{}
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		return b492Upgrader{r: rec, which: "native"}
	}
	deps.NewUpgrader = func(repoPath string, st *StateStore, current string) ScheduledUpgrader {
		return b492Upgrader{r: rec, which: "docker"}
	}

	runScheduled(context.Background(), deps, "v9.9.9", time.Now())
	b375JoinScheduledRun(t)
	if runs := rec.snapshot(); len(runs) != 0 {
		t.Fatalf("an undetected install kind must not drive any upgrader, got %d call(s)", len(runs))
	}
}

// TestB492_NativeTickIsNotSkipped is the POSITIVE half of the renegotiated B375 contract: a systemd
// host with an armed schedule must reach the trigger (and stamp the run), not log "NOT SUPPORTED".
//
// It drives tick() rather than runScheduled() on purpose: the pre-B492 defect was the EARLY guard in
// tick step 4b, so a test that only called runScheduled would have passed while the schedule still
// did nothing on every real host.
func TestB492_NativeTickIsNotSkipped(t *testing.T) {
	b492InstallKind(t, InstallSystemd)
	d := b375DB(t)
	b375SetSchedule(t, d, true, b375NowMinute(localtime.Now()))
	deps := b375Deps(t, d, "v1.5.230")

	rec := &b492Recorder{}
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		return b492Upgrader{r: rec, which: kind.String()}
	}

	logPath := t.TempDir() + "/b492.log"
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer f.Close()
	prev := log.Writer()
	log.SetOutput(f)
	tick(context.Background(), deps)
	log.SetOutput(prev)
	b375JoinScheduledRun(t)

	data, rerr := os.ReadFile(logPath)
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if strings.Contains(string(data), "NOT SUPPORTED") {
		t.Fatalf("a systemd host was skipped by the schedule:\n%s", string(data))
	}
	if got := b375LastRunText(t, d); got == "" {
		t.Fatal("a supported native tick must stamp update_schedule_last_run")
	}
	runs := rec.snapshot()
	if len(runs) == 0 {
		t.Fatal("the native updater was never called by the tick")
	}
	if runs[0].which != "systemd" {
		t.Fatalf("the native updater received kind %q, want systemd", runs[0].which)
	}
}
