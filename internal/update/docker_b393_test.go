package update

// B393 — regression tests for the swap-helper handoff and the "did the swap
// happen?" verdict.
//
// Every case here is a real defect observed on a live host on 2026-10-16/17:
//
//   - the helper mounted `skygate-data`, an empty legacy volume, while the
//     orchestrator wrote its script into the checkout's ./data bind mount, so
//     the helper died with "can't open '/data/skygate-swap-helper.sh'" and
//     nothing said so;
//   - both probes accepted a 200 from the OLD container, so the job was
//     promoted to `done` within one page view while the panel kept serving
//     the previous build.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseDataMountInspect pins the resolver's reading of `docker inspect`:
// a named volume resolves to the VOLUME NAME, a bind to the HOST PATH, and a
// missing /data mount resolves to nothing (so the caller falls back instead of
// inventing a mount).
func TestParseDataMountInspect(t *testing.T) {
	cases := []struct {
		name, in, want, kind string
	}{
		{"named volume", "volume|skygate-data|/var/lib/docker/volumes/skygate-data/_data", "skygate-data", "volume"},
		{"bind", "bind||/home/sky/admin/skygate/data", "/home/sky/admin/skygate/data", "bind"},
		{"no /data mount", "", "", ""},
		{"garbage", "not-a-mount-line", "", ""},
		{"bind without source", "bind||", "", ""},
		{"volume without name falls back to source", "volume||/var/lib/docker/volumes/x/_data", "/var/lib/docker/volumes/x/_data", "volume"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, kind := parseDataMountInspect(tc.in)
			if got != tc.want {
				t.Errorf("parseDataMountInspect(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if tc.want != "" && kind != tc.kind {
				t.Errorf("kind = %q, want %q", kind, tc.kind)
			}
		})
	}
}

// TestSwapScripts_UseResolvedMount is the contract that closes the actual
// production bug: neither script may hardcode the legacy volume, both must go
// through the resolved placeholder, and both must pre-flight it.
func TestSwapScripts_UseResolvedMount(t *testing.T) {
	for name, script := range map[string]string{
		"success":  swapSubprocessScript,
		"rollback": rollbackSwapScriptTemplate,
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(script, "skygate-data:/data") {
				t.Error("script still hardcodes the legacy volume skygate-data:/data — the exact bug B393 fixes")
			}
			if !strings.Contains(script, swapDataMountPlaceholder) {
				t.Errorf("script does not carry the %s placeholder, so the resolved mount never reaches it", swapDataMountPlaceholder)
			}
			if !strings.Contains(script, `-v "$DATA_MOUNT:/data"`) {
				t.Error(`script does not mount "$DATA_MOUNT:/data"`)
			}
			// The pre-flight is what turns a wrong mount into a named failure
			// instead of a 127 buried in the log.
			if !strings.Contains(script, "test -s /data/skygate-swap-helper.sh") {
				t.Error("script has no pre-flight check that the helper can see its script")
			}
			if !strings.Contains(script, "FAILED:") {
				t.Error("script does not write a FAILED marker the verdict can find")
			}
			// Rendering must leave no placeholder behind.
			rendered := renderSwapScript(script, "/srv/skygate/data")
			if strings.Contains(rendered, swapDataMountPlaceholder) {
				t.Error("placeholder survived rendering")
			}
			if !strings.Contains(rendered, `DATA_MOUNT='/srv/skygate/data'`) {
				t.Error("rendered script does not assign the resolved path to DATA_MOUNT")
			}
		})
	}
}

// TestShQuote covers the operator override path: SKYGATE_DATA_MOUNT is
// interpolated into a shell script, so it must not be able to break out.
func TestShQuote(t *testing.T) {
	cases := map[string]string{
		"/srv/skygate/data": `'/srv/skygate/data'`,
		"it's":              `'it'\''s'`,
		"":                  `''`,
		"/a b/c":            `'/a b/c'`,
		"$(reboot)":         `'$(reboot)'`,
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildSatisfiesTarget pins the comparison used by both probes.
func TestBuildSatisfiesTarget(t *testing.T) {
	cases := []struct {
		name           string
		build, target  string
		previous, want string // want is "true"/"false" for readability
	}{
		{"exact tag with commit suffix", "v1.5.131+b9b22468", "v1.5.131", "v1.5.130+aae95368", "true"},
		{"target without v prefix", "v1.5.131+b9b22468", "1.5.131", "v1.5.130+aae95368", "true"},
		{"old build is not the target", "v1.5.130+aae95368", "v1.5.131", "v1.5.130+aae95368", "false"},
		{"empty build proves nothing", "", "v1.5.131", "v1.5.130", "false"},
		{"branch target, label changed", "v1.5.132-2-gabc1234+abc1234", "main", "v1.5.131+b9b22468", "true"},
		{"branch target, label unchanged", "v1.5.131+b9b22468", "main", "v1.5.131+b9b22468", "false"},
		{"release target is not satisfied by a describe-ahead label", "v1.5.131-3-gabc1234", "v1.5.131", "v1.5.131-3-gabc1234", "false"},
		{"no target, label unchanged", "v1.5.131+b9b22468", "", "v1.5.131+b9b22468", "false"},
		{"no target, label changed", "v1.5.131+b9b22468", "", "v1.5.130+aae95368", "true"},
		{"short sha target", "abc1234+abc1234", "abc1234", "v1.5.131+b9b22468", "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildSatisfiesTarget(tc.build, tc.target, tc.previous)
			want := tc.want == "true"
			if got != want {
				t.Errorf("BuildSatisfiesTarget(%q, %q, %q) = %v, want %v",
					tc.build, tc.target, tc.previous, got, want)
			}
		})
	}
}

// TestEvaluateSwap pins the two witnesses and their combination.
func TestEvaluateSwap(t *testing.T) {
	base := &State{
		Phase:               PhaseBuildDone,
		FromVersion:         "v1.5.130+aae95368",
		ToVersion:           "v1.5.131",
		SwapFromContainerID: "oldcontainer0123456789",
	}
	newContainer := &ContainerFingerprint{ID: "newcontainer0123456789"}
	oldContainer := &ContainerFingerprint{ID: "oldcontainer0123456789"}

	t.Run("label matches", func(t *testing.T) {
		v := EvaluateSwap(base, "v1.5.131+b9b22468", oldContainer)
		if !v.OK {
			t.Fatalf("expected OK, got %q", v.Reason)
		}
		if !strings.Contains(v.Reason, "unchanged") {
			t.Errorf("the verdict must note that the container id did not change: %q", v.Reason)
		}
	})

	t.Run("container recreated", func(t *testing.T) {
		st := *base
		st.ToVersion = "main"
		v := EvaluateSwap(&st, "v1.5.131-7-gdeadbee+deadbee", newContainer)
		if !v.OK {
			t.Fatalf("a recreated container proves the swap ran; got %q", v.Reason)
		}
	})

	t.Run("old build in the old container", func(t *testing.T) {
		v := EvaluateSwap(base, "v1.5.130+aae95368", oldContainer)
		if v.OK {
			t.Fatal("a 200 from the OLD build in the OLD container must not be accepted")
		}
		if !strings.Contains(v.Reason, "STILL") {
			t.Errorf("the reason must say the container is unchanged: %q", v.Reason)
		}
	})

	t.Run("no fingerprint, old label", func(t *testing.T) {
		v := EvaluateSwap(base, "v1.5.130+aae95368", nil)
		if v.OK {
			t.Fatal("with no fingerprint the label must decide, and it does not match")
		}
	})

	t.Run("rollback expects the previous build", func(t *testing.T) {
		st := *base
		st.Phase = PhaseRolledBack
		v := EvaluateSwap(&st, "v1.5.130+aae95368", newContainer)
		if !v.OK {
			t.Fatalf("a rollback is confirmed by the previous build serving again; got %q", v.Reason)
		}
	})
}

// TestSwapLogFailure pins the "the script already said it cannot work" path:
// only lines at or after the job start count, so an old FATAL cannot fail a
// new job.
func TestSwapLogFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "swap.log")
	old := "[2026-10-16T20:08:31Z] FAILED: an earlier job gave up\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	jobStart := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	if failed, _ := swapLogFailure(path, jobStart); failed {
		t.Error("a FAILED line from a previous job must not fail the current one")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[2026-10-17T08:00:30Z] FAILED: the helper cannot see the script through '/srv/data'\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	failed, line := swapLogFailure(path, jobStart)
	if !failed {
		t.Fatal("a FAILED line written after the job start must fail the job")
	}
	if !strings.Contains(line, "cannot see the script") {
		t.Errorf("the marker text must reach the operator, got %q", line)
	}
}

// TestResolvePendingSwap covers the state transitions of the verdict, with the
// fingerprint, the log path and the clock injected.
func TestResolvePendingSwap(t *testing.T) {
	newStore := func(t *testing.T) *StateStore {
		t.Helper()
		store := NewStateStore(filepath.Join(t.TempDir(), "status.json"))
		store.Start("job1", InstallDocker.String(), "v1.5.130+aae95368", "v1.5.131", nil, nil, "")
		store.SetPhase(PhaseBuildDone, "image rebuilt")
		store.SetSwapFromContainer("oldcontainer0123456789")
		return store
	}
	started := time.Now().UTC()
	setStarted := func(store *StateStore) {
		store.mu.Lock()
		store.state.StartedAt = started
		store.mu.Unlock()
	}
	fp := func(id string) func(context.Context) (*ContainerFingerprint, error) {
		return func(context.Context) (*ContainerFingerprint, error) {
			return &ContainerFingerprint{ID: id}, nil
		}
	}
	logPath := filepath.Join(t.TempDir(), "swap.log")

	t.Run("matching build promotes to done", func(t *testing.T) {
		store := newStore(t)
		setStarted(store)
		if !resolvePendingSwap(context.Background(), store, "v1.5.131+b9b22468", fp("new"), logPath, started.Add(time.Minute)) {
			t.Fatal("expected the verdict to resolve the job")
		}
		if got := store.Get().Phase; got != PhaseDone {
			t.Errorf("phase = %s, want done", got)
		}
	})

	t.Run("old build in the same container waits", func(t *testing.T) {
		store := newStore(t)
		setStarted(store)
		if resolvePendingSwap(context.Background(), store, "v1.5.130+aae95368", fp("oldcontainer0123456789"), logPath, started.Add(30*time.Second)) {
			t.Fatal("nothing has happened yet, so the verdict must not resolve")
		}
		if got := store.Get().Phase; got != PhaseBuildDone {
			t.Errorf("phase = %s, want build_done", got)
		}
	})

	t.Run("old build after the grace period fails with a diagnostic", func(t *testing.T) {
		store := newStore(t)
		setStarted(store)
		if !resolvePendingSwap(context.Background(), store, "v1.5.130+aae95368", fp("oldcontainer0123456789"), logPath, started.Add(SwapGracePeriod+time.Minute)) {
			t.Fatal("after the grace period the job must be resolved")
		}
		st := store.Get()
		if st.Phase != PhaseFailed {
			t.Fatalf("phase = %s, want failed", st.Phase)
		}
		if !strings.Contains(st.Error, "never swapped") {
			t.Errorf("the error must say the swap never ran: %q", st.Error)
		}
		if !strings.Contains(st.Error, "docker compose up") {
			t.Errorf("the error must carry the break-glass command: %q", st.Error)
		}
	})

	t.Run("a FAILED marker in the swap log fails immediately", func(t *testing.T) {
		store := newStore(t)
		setStarted(store)
		markerPath := filepath.Join(t.TempDir(), "swap.log")
		if err := os.WriteFile(markerPath,
			[]byte("[2099-01-01T00:00:00Z] FAILED: the helper cannot see the script\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !resolvePendingSwap(context.Background(), store, "v1.5.130+aae95368", fp("oldcontainer0123456789"), markerPath, started.Add(5*time.Second)) {
			t.Fatal("the script's own failure marker must resolve the job at once")
		}
		st := store.Get()
		if st.Phase != PhaseFailed {
			t.Fatalf("phase = %s, want failed", st.Phase)
		}
		if !strings.Contains(st.Error, "cannot see the script") {
			t.Errorf("the marker text must be in the error: %q", st.Error)
		}
		// The tail of the swap log must also be in the job log, so the page
		// explains itself without a shell.
		found := false
		for _, e := range st.Log {
			if strings.Contains(e.Msg, "swap log:") {
				found = true
			}
		}
		if !found {
			t.Error("the swap log tail was not folded into the job log")
		}
	})

	t.Run("native jobs are left to ConfirmNativeSwap", func(t *testing.T) {
		store := NewStateStore(filepath.Join(t.TempDir(), "status.json"))
		store.Start("job1", InstallSystemd.String(), "v1.5.130", "v1.5.131", nil, nil, "")
		store.SetPhase(PhaseBuildDone, "")
		if resolvePendingSwap(context.Background(), store, "v1.5.130", fp("old"), logPath, started.Add(time.Hour)) {
			t.Fatal("a native job must not be resolved by the docker verdict")
		}
	})

	t.Run("terminal states are untouched", func(t *testing.T) {
		store := newStore(t)
		setStarted(store)
		store.Complete()
		if resolvePendingSwap(context.Background(), store, "v1.5.130+aae95368", fp("old"), logPath, started.Add(time.Hour)) {
			t.Fatal("a completed job must not be re-resolved")
		}
		if got := store.Get().Phase; got != PhaseDone {
			t.Errorf("phase = %s, want done", got)
		}
	})
}

// TestPendingSwapSurvivesPersistence pins that the pre-update container id is
// actually on disk: the verdict runs in the NEW process, which only knows what
// the state file says.
func TestPendingSwapSurvivesPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	store := NewStateStore(path)
	store.Start("job1", InstallDocker.String(), "v1.5.130", "v1.5.131", nil, nil, "")
	store.SetSwapFromContainer("abc123def456")

	reloaded := NewStateStore(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	st := reloaded.Get()
	if st == nil {
		t.Fatal("no state after reload")
	}
	if st.SwapFromContainerID != "abc123def456" {
		t.Errorf("SwapFromContainerID = %q, want it persisted", st.SwapFromContainerID)
	}
}

// TestHealthzProbeParsing pins that a booting process is not mistaken for a
// finished swap (B269's provisional /healthz already serves the NEW build
// string, so the label alone is not enough) and that an unparseable body is
// "no proof" rather than a match.
func TestHealthzProbeParsing(t *testing.T) {
	if got := healthzBuild(`{"build":"v1.5.131+b9b22468","status":"ok"}`); got != "v1.5.131+b9b22468" {
		t.Errorf("healthzBuild = %q", got)
	}
	if got := healthzBuild(`not json`); got != "" {
		t.Errorf("healthzBuild on a non-JSON body = %q, want empty", got)
	}
	if !healthzBootInProgress(`{"status":"ok","build":"v1.5.131","ready":false,"phase":"db-open+migrate"}`) {
		t.Error("a body with ready:false must count as still booting")
	}
	if !healthzBootInProgress(`{"status":"ok","phase":"routes"}`) {
		t.Error("a body with a phase must count as still booting")
	}
	if healthzBootInProgress(`{"build":"v1.5.131+b9b22468","status":"ok"}`) {
		t.Error("a final /healthz body must not count as booting")
	}
}
