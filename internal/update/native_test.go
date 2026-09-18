package update

// native_test.go — §12.15 native (systemd / bare) self-update.
//
// These tests cover the parts that are pure Go: request-file rendering
// + validation (the security boundary, since the file is written by an
// unprivileged process and read by a root helper), the "helper is
// missing → fail with an actionable message" pre-flight, and the
// verdict folding (result.* → StateStore).
//
// The privileged apply script itself is NOT exercised here — it cannot
// be, it needs root. It is covered by scripts/check_b261_native_self_update.sh
// (syntax + data-only parsing + dry-run smoke) and by the live canary
// run recorded in the plan (§12.16).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newNativeTestStore(t *testing.T) (*StateStore, string) {
	t.Helper()
	dir := t.TempDir()
	store := NewStateStore(filepath.Join(dir, "state.json"))
	store.Start("deadbeef", InstallSystemd.String(), "v1.5.8", "v1.5.9", nil, nil, "")
	return store, dir
}

// stubHelper writes an executable helper stub and returns its path.
func stubHelper(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "skygate-apply-update.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNativeReleaseTagFor(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"v1.5.9", "v1.5.9"},
		{"v1.5.9+abc1234", "v1.5.9"},
		{"1.5.9", "v1.5.9"}, // normalize leaves it; GitRefForBuildLabel only strips
		{"v1.5.8-36-g50d4c2d", ""},
		{"v1.5.8-36-g50d4c2d+50d4c2d", ""},
		{"ve2d0b9e+e2d0b9e", ""}, // bare SHA is not a release tag
		{"", ""},
		{"v1.5.9'; rm -rf /", ""},
	}
	for _, c := range cases {
		got := NativeReleaseTagFor(c.in)
		// "1.5.9" passes through GitRefForBuildLabel unchanged (no "+"
		// and not all-hex), which is what the helper needs — the form
		// is normalized by the caller, not here.
		if c.in == "1.5.9" {
			if got != "1.5.9" {
				t.Errorf("NativeReleaseTagFor(%q) = %q, want %q", c.in, got, "1.5.9")
			}
			continue
		}
		if got != c.want {
			t.Errorf("NativeReleaseTagFor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderRequestRejectsHostileTarget(t *testing.T) {
	store, dir := newNativeTestStore(t)
	u := NewNativeUpgrader(InstallSystemd, dir, dir, "skygate", "", store, "v1.5.8")
	for _, bad := range []string{
		"v1.5.9'; rm -rf /",
		"v1.5.9$(id)",
		"v1.5.9 `id`",
		"v1.5.9\nTARGET='evil'",
		"v1.5.8-36-g50d4c2d",
		"",
	} {
		if _, err := u.renderRequest(bad); err == nil {
			t.Errorf("renderRequest(%q) accepted a hostile/invalid target", bad)
		}
	}
}

func TestRenderRequestContainsOnlyDataKeys(t *testing.T) {
	store, dir := newNativeTestStore(t)
	u := NewNativeUpgrader(InstallSystemd, dir, dir, "skygate", "", store, "v1.5.8-36-g50d4c2d")
	body, err := u.renderRequest("v1.5.9")
	if err != nil {
		t.Fatalf("renderRequest: %v", err)
	}
	for _, key := range []string{"JOB_ID=", "TARGET='v1.5.9'", "FROM_VERSION='v1.5.8-36-g50d4c2d'", "RUNTIME_PID=", "INSTALL_KIND='systemd'"} {
		if !strings.Contains(body, key) {
			t.Errorf("request file is missing %q:\n%s", key, body)
		}
	}
	// Paths must NOT be handed to the privileged side — they come from
	// the root-owned helper config.
	for _, forbidden := range []string{"BINARY=", "SERVICE=", "HEALTH_URL=", "UPDATE_DIR=", "RUN_USER="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("request file must not carry %q:\n%s", forbidden, body)
		}
	}
}

func TestRunRefusesWithoutHelper(t *testing.T) {
	store, dir := newNativeTestStore(t)
	u := NewNativeUpgrader(InstallSystemd, dir, dir, "skygate", "", store, "v1.5.8")
	u.HelperPath = filepath.Join(dir, "missing-helper.sh")
	u.Run(context.Background(), "v1.5.9")

	st := store.Get()
	if st == nil || st.Phase != PhaseFailed {
		t.Fatalf("phase = %v, want failed", st)
	}
	if !strings.Contains(st.Error, "helper") {
		t.Errorf("error should point at the missing helper, got %q", st.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, NativeRequestFile)); err == nil {
		t.Error("no request must be staged when the helper is absent")
	}
}

func TestRunRefusesWhenPathUnitInactive(t *testing.T) {
	store, dir := newNativeTestStore(t)
	u := NewNativeUpgrader(InstallSystemd, dir, dir, "skygate", "", store, "v1.5.8")
	u.HelperPath = stubHelper(t, dir)
	u.LookPath = func(string) (string, error) { return "/usr/bin/systemctl", nil }
	u.RunSystemctl = func(context.Context, ...string) (string, error) { return "inactive", nil }

	u.Run(context.Background(), "v1.5.9")

	st := store.Get()
	if st == nil || st.Phase != PhaseFailed {
		t.Fatalf("phase = %v, want failed", st)
	}
	if !strings.Contains(st.Error, "skygate-update.path") {
		t.Errorf("error should mention the path unit, got %q", st.Error)
	}
}

func TestRunStagesRequestAndStopsAtSwap(t *testing.T) {
	store, dir := newNativeTestStore(t)
	u := NewNativeUpgrader(InstallSystemd, dir, dir, "skygate", "", store, "v1.5.8-36-g50d4c2d")
	u.HelperPath = stubHelper(t, dir)
	u.LookPath = func(string) (string, error) { return "/usr/bin/systemctl", nil }
	u.RunSystemctl = func(context.Context, ...string) (string, error) { return "active", nil }

	u.Run(context.Background(), "v1.5.9")

	if st := store.Get(); st != nil {
		t.Logf("phase=%s err=%q log=%v", st.Phase, st.Error, logMsgs(st))
	}

	raw, err := os.ReadFile(filepath.Join(dir, NativeRequestFile))
	if err != nil {
		t.Fatalf("request file: %v", err)
	}
	if !strings.Contains(string(raw), "TARGET='v1.5.9'") {
		t.Errorf("request file missing target:\n%s", raw)
	}
	st := store.Get()
	if st == nil || st.Phase != PhaseSwap {
		t.Fatalf("phase = %v, want %s (the helper owns the rest)", st, PhaseSwap)
	}
}

func TestConfirmNativeSwapDone(t *testing.T) {
	store, dir := newNativeTestStore(t)
	writeResult(t, dir, "done", "", "v1.5.9+abc1234", []string{"installed the new binary", "healthz reports build 'v1.5.9+abc1234'"})

	if !ConfirmNativeSwap(store, dir) {
		t.Fatal("ConfirmNativeSwap returned false, want true")
	}
	st := store.Get()
	if st.Phase != PhaseDone {
		t.Errorf("phase = %v, want done", st.Phase)
	}
	if !strings.Contains(strings.Join(logMsgs(st), "\n"), "helper: healthz reports build") {
		t.Errorf("helper log was not folded into the state: %v", logMsgs(st))
	}
	for _, n := range []string{NativeResultStatusFile, NativeResultErrorFile, NativeResultBuildFile} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s should be consumed (removed) after folding", n)
		}
	}
}

func TestConfirmNativeSwapRolledBack(t *testing.T) {
	store, dir := newNativeTestStore(t)
	writeResult(t, dir, "rolled_back", "healthz did not report v1.5.9 within 90s", "v1.5.8+old", []string{"ROLLBACK: restoring the previous binary"})

	if !ConfirmNativeSwap(store, dir) {
		t.Fatal("ConfirmNativeSwap returned false, want true")
	}
	st := store.Get()
	if st.Phase != PhaseRolledBack {
		t.Errorf("phase = %v, want rolled_back", st.Phase)
	}
	if !strings.Contains(st.Error, "healthz did not report") {
		t.Errorf("rollback reason lost: %q", st.Error)
	}
}

func TestConfirmNativeSwapFailed(t *testing.T) {
	store, dir := newNativeTestStore(t)
	writeResult(t, dir, "failed", "SHA256 mismatch", "", []string{"ERROR: SHA256 mismatch"})

	ConfirmNativeSwap(store, dir)
	st := store.Get()
	if st.Phase != PhaseFailed {
		t.Errorf("phase = %v, want failed", st.Phase)
	}
	if st.Error != "SHA256 mismatch" {
		t.Errorf("error = %q", st.Error)
	}
	if !st.ManualFallback {
		t.Error("a failed native update must set ManualFallback so the page shows the manual steps")
	}
}

func TestConfirmNativeSwapIgnoresStaleResult(t *testing.T) {
	store, dir := newNativeTestStore(t)
	writeResult(t, dir, "done", "", "v1.5.9", nil)
	// Backdate the result to before the job started.
	p := filepath.Join(dir, NativeResultStatusFile)
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if ConfirmNativeSwap(store, dir) {
		t.Error("a result older than the job start must not be folded in")
	}
	if store.Get().Phase == PhaseDone {
		t.Error("stale result promoted the job to done")
	}
}

func TestConfirmNativeSwapSkipsDockerJobs(t *testing.T) {
	dir := t.TempDir()
	store := NewStateStore(filepath.Join(dir, "state.json"))
	store.Start("cafebabe", InstallDocker.String(), "v1.5.8", "v1.5.9", nil, nil, "")
	writeResult(t, dir, "done", "", "v1.5.9", nil)

	if ConfirmNativeSwap(store, dir) {
		t.Error("ConfirmNativeSwap must ignore Docker jobs (they use the detached-subprocess protocol)")
	}
	if store.Get().Phase == PhaseDone {
		t.Error("docker job was finalized by the native path")
	}
}

func TestConfirmNativeSwapNoopWithoutResult(t *testing.T) {
	store, dir := newNativeTestStore(t)
	if ConfirmNativeSwap(store, dir) {
		t.Error("no result file → nothing to fold")
	}
	if store.Get().Phase != PhasePending {
		t.Errorf("phase changed without a result: %v", store.Get().Phase)
	}
}

func writeResult(t *testing.T, dir, status, errMsg, build string, logLines []string) {
	t.Helper()
	must := func(name, body string) {
		if body == "" {
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body+"\n"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	must(NativeResultStatusFile, status)
	must(NativeResultErrorFile, errMsg)
	must(NativeResultBuildFile, build)
	if len(logLines) > 0 {
		must(NativeApplyLogFile, strings.Join(logLines, "\n"))
	}
}

func logMsgs(st *State) []string {
	out := make([]string, 0, len(st.Log))
	for _, e := range st.Log {
		out = append(out, e.Msg)
	}
	return out
}

func TestDetectPlatformSanity(t *testing.T) {
	dir := t.TempDir()
	info := DetectPlatform(dir)
	if info.OS == "" || info.Arch == "" {
		t.Errorf("OS/Arch must be reported, got %q/%q", info.OS, info.Arch)
	}
	if info.HelperPath == "" {
		t.Error("HelperPath must default to the installed helper path")
	}
	if !info.StateDirWritable {
		t.Error("a temp dir must be reported writable")
	}
	if info.InContainer {
		// Not an assertion about the host — just documents that the
		// flag tracks the container markers.
		t.Log("running inside a container")
	}
}
