// scheduler_native_b492_test.go — TD-42: the SCHEDULE must drive a NATIVE install, not only Docker.
//
// The measured defect: `tick` step 4b and `runScheduled` both returned for every install kind except
// Docker, so an armed schedule on the production host (systemd since the 2026-11-15 migration) did
// nothing and logged "NOT SUPPORTED" once a slot — while the MANUAL native path had worked since
// B261. The operator's ask: «и для нативного варианта сделать автообновление по расписанию».
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

// b492InstallKind points the scheduler's kind seam at a fixed value for one test.
func b492InstallKind(t *testing.T, kind InstallKind) {
	t.Helper()
	prev := detectInstallKindFn
	detectInstallKindFn = func() InstallKind { return kind }
	t.Cleanup(func() { detectInstallKindFn = prev })
}

// b492Upgrader records the target and returns immediately (the scheduler only calls Run).
type b492Upgrader struct {
	rec *b492RecordedRun
}

func (u b492Upgrader) Run(ctx context.Context, target string) { u.rec.target = target }

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

	var mu sync.Mutex
	var calls []b492RecordedRun
	rec := &calls
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		mu.Lock()
		defer mu.Unlock()
		*rec = append(*rec, b492RecordedRun{which: "native", target: ""})
		return b492Upgrader{rec: &(*rec)[len(*rec)-1]}
	}
	deps.NewUpgrader = func(repoPath string, st *StateStore, current string) ScheduledUpgrader {
		mu.Lock()
		defer mu.Unlock()
		*rec = append(*rec, b492RecordedRun{which: "docker"})
		return b492Upgrader{rec: &(*rec)[len(*rec)-1]}
	}

	runScheduled(context.Background(), deps, "v9.9.9", time.Now())
	b375JoinScheduledRun(t)

	mu.Lock()
	defer mu.Unlock()
	if len(*rec) != 1 {
		t.Fatalf("expected exactly one upgrader call, got %d (%+v)", len(*rec), *rec)
	}
	if (*rec)[0].which != "native" {
		t.Fatalf("a systemd host must go through the NATIVE updater, got %q", (*rec)[0].which)
	}
	if (*rec)[0].target != "v9.9.9" {
		t.Fatalf("the native updater must receive the release tag, got %q", (*rec)[0].target)
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

	nativeCalls := 0
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		nativeCalls++
		return b492Upgrader{rec: &b492RecordedRun{}}
	}

	// A git-describe build label: exactly what BuildVersion looks like on an untagged tree.
	target := "v1.5.8-36-g50d4c2d"
	runScheduled(context.Background(), deps, target, time.Now())
	b375JoinScheduledRun(t)

	if nativeCalls != 0 {
		t.Fatalf("the native updater was called %d time(s) for a target that cannot be downloaded", nativeCalls)
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

	calls := 0
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		calls++
		return b492Upgrader{rec: &b492RecordedRun{}}
	}
	deps.NewUpgrader = func(repoPath string, st *StateStore, current string) ScheduledUpgrader {
		calls++
		return b492Upgrader{rec: &b492RecordedRun{}}
	}

	runScheduled(context.Background(), deps, "v9.9.9", time.Now())
	b375JoinScheduledRun(t)
	if calls != 0 {
		t.Fatalf("an undetected install kind must not drive any upgrader, got %d call(s)", calls)
	}
}

// TestB492_NativeTickIsNotSkipped is the POSITIVE half of the renegotiated B375 contract: a systemd
// host with an armed schedule must reach the trigger (and stamp the run), not log "NOT SUPPORTED".
//
// It drives tick() rather than runScheduled() on purpose: the pre-B492 defect was the EARLY guard in
// tick step 4b, so a test that only called runScheduled would have passed while the schedule still did
// nothing on every real host.
func TestB492_NativeTickIsNotSkipped(t *testing.T) {
	b492InstallKind(t, InstallSystemd)
	d := b375DB(t)
	b375SetSchedule(t, d, true, b375NowMinute(localtime.Now()))
	deps := b375Deps(t, d, "v1.5.230")

	ran := make(chan string, 1)
	deps.NewNativeUpdater = func(kind InstallKind, updateDir string, st *StateStore, current string) ScheduledUpgrader {
		select {
		case ran <- kind.String():
		default:
		}
		return b492Upgrader{rec: &b492RecordedRun{}}
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
	select {
	case kind := <-ran:
		if kind != "systemd" {
			t.Fatalf("the native updater received kind %q, want systemd", kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the native updater was never called by the tick")
	}
}
