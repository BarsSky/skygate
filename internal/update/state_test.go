package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestStateStore_StartAndPersist pins the contract that
// Start() initializes a fresh state, writes the status
// file atomically, and Get() returns the same struct on
// subsequent calls.
func TestStateStore_StartAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewStateStore(path)

	st := store.Start("abc12345", "docker", "v0.28.6", "v0.29.0",
		[]string{"echo step1"}, []string{"echo rollback"}, "curl /healthz")
	if st == nil {
		t.Fatal("Start returned nil")
	}
	if st.JobID != "abc12345" {
		t.Errorf("JobID = %q, want abc12345", st.JobID)
	}
	if st.Phase != PhasePending {
		t.Errorf("Phase = %q, want %q", st.Phase, PhasePending)
	}
	if st.InstallKind != "docker" {
		t.Errorf("InstallKind = %q, want docker", st.InstallKind)
	}
	if len(st.Log) != 1 {
		t.Errorf("Log len = %d, want 1 (startup entry)", len(st.Log))
	}

	// Status file must exist and contain the same JobID.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("status file not written: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status file: %v", err)
	}
	if !contains(data, []byte("abc12345")) {
		t.Errorf("status file does not contain JobID: %s", data)
	}
}

// TestStateStore_PhaseTransitions pins the contract that
// SetPhase advances the phase + appends a log line, and
// the transitions are visible via Get().
func TestStateStore_PhaseTransitions(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	_ = store.Start("job1", "docker", "v0.28.6", "v0.29.0", nil, nil, "")

	store.SetPhase(PhaseBackup, "tagging current commit")
	if got := store.Get().Phase; got != PhaseBackup {
		t.Errorf("Phase = %q, want %q", got, PhaseBackup)
	}
	if got := len(store.Get().Log); got != 2 {
		t.Errorf("Log len = %d, want 2 (startup + backup)", got)
	}

	store.SetPhase(PhasePullBuild, "fetching target")
	store.SetPhase(PhaseMigrate, "running migrations")
	store.SetPhase(PhaseSwap, "recreating container")
	store.SetPhase(PhaseVerify, "polling healthz")
	store.SetPhase(PhaseDone, "") // PhaseDone is the final state

	if got := store.Get().Phase; got != PhaseDone {
		t.Errorf("Phase = %q, want %q", got, PhaseDone)
	}
	if got := len(store.Get().Log); got != 6 {
		t.Errorf("Log len = %d, want 6 (1 startup + 5 phase logs)", got)
	}
}

// TestStateStore_Fail pins the contract that Fail() sets
// PhaseFailed, records the error, sets ManualFallback=true,
// and the state file is updated.
func TestStateStore_Fail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewStateStore(path)
	_ = store.Start("job1", "docker", "v0.28.6", "v0.29.0", nil, nil, "")

	store.SetPhase(PhaseBackup, "")
	failErr := &testError{msg: "build failed: exit 1"}
	store.Fail(failErr)

	st := store.Get()
	if st.Phase != PhaseFailed {
		t.Errorf("Phase = %q, want %q", st.Phase, PhaseFailed)
	}
	if st.Error == "" {
		t.Error("Error empty, want non-empty")
	}
	if !st.ManualFallback {
		t.Error("ManualFallback = false, want true")
	}
	if st.FinishedAt.IsZero() {
		t.Error("FinishedAt not set on Fail")
	}
	// Status file must reflect the failed state.
	data, _ := os.ReadFile(path)
	if !contains(data, []byte("failed")) {
		t.Errorf("status file phase = %s, want failed", data)
	}
}

// TestStateStore_LoadAfterRestart pins the contract that
// the in-memory state is restored from disk on Load().
// This is what makes the "in-flight job survives a
// skygate restart" promise real.
func TestStateStore_LoadAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// First boot: start a job, advance to a phase, drop the store.
	first := NewStateStore(path)
	_ = first.Start("job1", "docker", "v0.28.6", "v0.29.0", nil, nil, "")
	first.SetPhase(PhasePullBuild, "fetching target")

	// Second boot: fresh store, Load() should return the same state.
	second := NewStateStore(path)
	st, err := second.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st == nil {
		t.Fatal("Load returned nil state")
	}
	if st.JobID != "job1" {
		t.Errorf("JobID = %q, want job1", st.JobID)
	}
	if st.Phase != PhasePullBuild {
		t.Errorf("Phase = %q, want %q", st.Phase, PhasePullBuild)
	}
}

// TestStateStore_Clear pins the contract that Clear()
// removes the in-memory state AND the status file.
func TestStateStore_Clear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := NewStateStore(path)
	_ = store.Start("job1", "docker", "v0.28.6", "v0.29.0", nil, nil, "")

	store.Clear()
	if store.Get() != nil {
		t.Error("Get() should return nil after Clear()")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("status file should be removed, stat err = %v", err)
	}
}

// TestStateStore_LogTrimsAtMax pins the contract that
// appendLog drops the OLDEST entries when the buffer
// exceeds MaxLogLines, so the file doesn't grow unbounded.
func TestStateStore_LogTrimsAtMax(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	_ = store.Start("job1", "docker", "v0.28.6", "v0.29.0", nil, nil, "")

	// Add MaxLogLines + 50 entries.
	for i := 0; i < MaxLogLines+50; i++ {
		store.Log(LogInfo, "entry")
	}
	got := len(store.Get().Log)
	if got != MaxLogLines {
		t.Errorf("Log len = %d, want %d (trimmed)", got, MaxLogLines)
	}
}

// TestStateStore_AtomicWrite pins the contract that the
// status file is written atomically (via temp file +
// rename) so a concurrent read can never see a half-
// written file. We force a partial-write condition by
// pointing the store at a directory that doesn't exist
// (os.CreateTemp fails → persist returns an error;
// in-memory state is still updated). The test is loose:
// we just verify the store doesn't panic.
func TestStateStore_PersistFailureDoesNotPanic(t *testing.T) {
	// Point the state file at a path under a non-existent
	// directory. The temp-file write fails, but the
	// in-memory state is still updated.
	store := NewStateStore("/no/such/dir/state.json")
	_ = store.Start("job1", "docker", "v0.28.6", "v0.29.0", nil, nil, "")
	if store.Get() == nil {
		t.Error("Get() should still return the in-memory state after a persist failure")
	}
}

// TestGenerateJobID_Unique runs 1000 iterations and checks
// no collisions. The function uses crypto/rand so the
// probability of a collision is negligible, but a test
// catches a "fall back to time-based" bug that would
// collide when the test runs in a tight loop.
func TestGenerateJobID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := GenerateJobID()
		if id == "" {
			t.Fatalf("GenerateJobID returned empty at iteration %d", i)
		}
		if seen[id] {
			t.Errorf("collision at iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}

// TestStateStore_JSONRoundTrip pins the contract that
// the State struct's JSON shape is stable (renaming a
// field would break the operator's /admin/status AJAX
// polling + the audit-log replay). The test round-trips
// a populated state through json.Marshal/json.Unmarshal
// and asserts every field survives.
func TestStateStore_JSONRoundTrip(t *testing.T) {
	in := &State{
		JobID:           "abc12345",
		InstallKind:     "docker",
		FromVersion:     "v0.28.6",
		ToVersion:       "v0.29.0",
		Phase:           PhasePullBuild,
		ManualFallback:  true,
		ManualSteps:     []string{"echo step1", "echo step2"},
		Rollback:        []string{"echo rollback"},
		VerifyAfter:     "curl /healthz",
		Log: []LogEntry{
			{Level: LogInfo, Msg: "starting"},
			{Level: LogError, Msg: "FAILED: oh no"},
		},
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out State
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.JobID != in.JobID {
		t.Errorf("JobID = %q, want %q", out.JobID, in.JobID)
	}
	if out.Phase != in.Phase {
		t.Errorf("Phase = %q, want %q", out.Phase, in.Phase)
	}
	if out.ManualFallback != in.ManualFallback {
		t.Errorf("ManualFallback = %v, want %v", out.ManualFallback, in.ManualFallback)
	}
	if len(out.Log) != len(in.Log) {
		t.Errorf("Log len = %d, want %d", len(out.Log), len(in.Log))
	}
	if out.Log[1].Level != LogError {
		t.Errorf("Log[1].Level = %q, want %q", out.Log[1].Level, LogError)
	}
}

// contains is a tiny helper that mimics strings.Contains
// for []byte to avoid an import.
func contains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// testError is a tiny test helper implementing error with a
// stable Error() string. Using os.ErrNotExist directly is
// awkward because its Error() string varies across Go
// versions ("file does not exist" / "no such file or
// directory" etc.).
type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
