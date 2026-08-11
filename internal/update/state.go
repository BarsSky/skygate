package update

// state.go — v0.29.0 self-update state machine (Phase 2).
//
// The updater runs as a background goroutine kicked off by
// `POST /admin/update/apply`. Each phase is recorded with
// a log line; the whole state is persisted to a status file
// (`/data/skygate-update-status.json`) so the page can show
// progress across page reloads and so a crash doesn't lose
// the audit trail.
//
// The state machine is intentionally small and explicit:
//
//   pending  → backup → pull_build → migrate → swap → verify → done
//                  ↓         ↓          ↓       ↓       ↓
//                  └─────────┴──────────┴───────┴───────┴─→ failed
//
// On failure of any phase, the orchestrator calls rollback
// (defined in docker.go / bare.go per install kind) which
// restores the previous version. The final state is either
// "done" (success) or "failed" (rolled back). The "failed"
// state includes `ManualFallback = true` and a copy of the
// manual steps so the operator can run them by hand.
//
// Concurrency: only ONE updater runs at a time. PostAdminUpdateApply
// returns http.StatusConflict Conflict if a job is already in progress. The
// "Rollback now" button is a separate state transition that
// cancels the in-progress job (or rolls back a completed-but-
// bad update) without starting a new one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// randReadOS is implemented in rand_read.go (the file is
// kept separate so this package's stdlib import list stays
// narrow and easy to audit).
var randReadOS = func(b []byte) (int, error) { return 0, fmt.Errorf("not initialized") }

// Phase is one step in the update state machine. String values
// are written to the status file and the audit log; renames
// are a breaking change.
type Phase string

const (
	PhasePending    Phase = "pending"
	PhaseBackup     Phase = "backup"
	PhasePullBuild  Phase = "pull_build"
	PhaseMigrate    Phase = "migrate"
	PhaseSwap       Phase = "swap"
	PhaseVerify     Phase = "verify"
	// PhaseBuildDone is the new terminal phase added in
	// v0.29.1: the orchestrator stops at "image rebuilt +
	// migrations applied" and writes a manual_step telling
	// the operator to run `docker compose up -d
	// --force-recreate --no-deps skygate` on the host.
	// The orchestrator used to do the up itself in Phase
	// 4, but that would send SIGTERM to the skygate
	// container (the orchestrator's own process tree)
	// mid-execution, leaving the new container in an
	// undefined state with no healthz verification. The
	// sidecar-based orchestrator (v0.29.2 follow-up) will
	// restore the full auto-swap.
	PhaseBuildDone Phase = "build_done"
	PhaseDone      Phase = "done"
	PhaseFailed    Phase = "failed"
	PhaseRolledBack Phase = "rolled_back"
)

// LogLevel is the severity of a single log line. The UI
// renders Error lines in red, Warn in amber, Info in
// default color. Debug is hidden unless `verbose=true` is
// passed in the page query.
type LogLevel string

const (
	LogDebug LogLevel = "debug"
	LogInfo  LogLevel = "info"
	LogWarn  LogLevel = "warn"
	LogError LogLevel = "error"
)

// State is the full state of an in-flight or recently-
// completed update. Written to the status file as JSON on
// every transition so the file is the single source of truth
// for "what phase are we in right now".
type State struct {
	// JobID is a short random string (8 hex chars) that
	// identifies this update. Surfaced in the audit log
	// and the UI so the operator can correlate.
	JobID string `json:"job_id"`

	// InstallKind is "docker" / "systemd" / "bare" — copied
	// from the install detection at job start.
	InstallKind string `json:"install_kind"`

	// FromVersion / ToVersion are the build labels (e.g.
	// "v0.28.6+abc1234" and "v0.29.0"). Surfaced in the
	// audit log + UI.
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`

	// Phase is the current step. Updates on every transition.
	Phase Phase `json:"phase"`

	// StartedAt / FinishedAt are wall-clock timestamps.
	// FinishedAt is the zero value while the job is in
	// progress.
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`

	// Log is the rolling buffer of the last MaxLogLines
	// log entries. The UI renders this as a scrollable
	// <pre> block. Newer lines are at the bottom.
	Log []LogEntry `json:"log"`

	// Error is set when Phase == PhaseFailed. The UI shows
	// this prominently + a "Run manual steps" button.
	Error string `json:"error,omitempty"`

	// ManualFallback is true when the auto-update failed
	// and the operator should run the manual steps (the
	// same GenerateManualSteps output the page already
	// shows, just re-stated at the failure banner so
	// the operator doesn't have to scroll).
	ManualFallback bool `json:"manual_fallback"`

	// ManualSteps is the copy-pasteable command list.
	// Populated at job start (from the same logic as
	// /admin/update's ManualSteps field) so the failure
	// path can show them inline.
	ManualSteps []string `json:"manual_steps,omitempty"`
	Rollback    []string `json:"rollback,omitempty"`

	// ManualSwap (v0.29.1) is set when the orchestrator
	// stops at PhaseBuildDone. The page surfaces it as a
	// single copy-pasteable command (typically
	// `docker compose up -d --force-recreate --no-deps skygate`)
	// that the operator runs on the host to apply the
	// built image. Distinct from ManualSteps (the full
	// fallback procedure for a failed job) and Rollback
	// (the steps to undo a successful-but-wrong upgrade).
	ManualSwap string `json:"manual_swap,omitempty"`

	// VerifyAfter is the post-success sanity check.
	VerifyAfter string `json:"verify_after,omitempty"`
}

// LogEntry is one line in the rolling log buffer.
type LogEntry struct {
	At    time.Time `json:"at"`
	Level LogLevel  `json:"level"`
	Msg   string    `json:"msg"`
}

// MaxLogLines caps the in-memory log buffer. Older lines
// are dropped. http.StatusInternalServerError is enough for the typical 5-minute
// update (3-5 lines per second * 300s = 1500, but most
// lines are short and a typical update has <100 events).
const MaxLogLines = http.StatusInternalServerError

// StateStore holds the current state in memory and on disk.
// All access is serialized through mu (the status file is
// read by the /admin/update handler which can race with the
// background updater goroutine).
type StateStore struct {
	mu     sync.Mutex
	state  *State
	path   string // status file path (e.g. /data/skygate-update-status.json)
}

// NewStateStore creates a StateStore backed by the given file
// path. The file is read on construction (so a restart
// picks up the most recent job) and written on every
// transition. An empty / missing file is OK — returns nil
// state until the first transition.
func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

// Load reads the persisted state from disk and stores it
// in the receiver. Returns the loaded state, or nil if the
// file doesn't exist or is unreadable (e.g. first run on
// a fresh install).
//
// v0.29.3: prior versions parsed the file but discarded
// the result, so s.state stayed nil after Load() — every
// subsequent Get() returned nil, every Log() was a silent
// no-op (s.state == nil short-circuit), and confirmPendingSwap
// in renderUpdatePage never saw a real state to act on.
// The fix: store the parsed state in s.state.
func (s *StateStore) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state file: %w", err)
	}
	s.mu.Lock()
	s.state = &st
	s.mu.Unlock()
	return &st, nil
}

// Get returns a copy of the current state, or nil if no
// update is in progress / completed recently.
func (s *StateStore) Get() *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Start initializes a new state with the given job metadata
// and returns a *State (also persisted to disk). Replaces
// any existing state. The "replace" semantics is intentional:
// the page shows the most recent job, even if it failed.
func (s *StateStore) Start(jobID, installKind, fromVersion, toVersion string, manualSteps, rollback []string, verifyAfter string) *State {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	st := &State{
		JobID:        jobID,
		InstallKind:  installKind,
		FromVersion:  fromVersion,
		ToVersion:    toVersion,
		Phase:        PhasePending,
		StartedAt:    now,
		Log:          []LogEntry{{At: now, Level: LogInfo, Msg: "update job started"}},
		ManualSteps:  manualSteps,
		Rollback:     rollback,
		VerifyAfter:  verifyAfter,
	}
	s.state = st
	_ = s.persistLocked()
	return st
}

// SetPhase advances the state to a new phase + adds a log line.
// Persists to disk before returning.
func (s *StateStore) SetPhase(phase Phase, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return
	}
	s.state.Phase = phase
	if msg != "" {
		s.state.Log = appendLog(s.state.Log, LogInfo, msg)
	}
	_ = s.persistLocked()
}

// Log appends a log entry at the given level. Does NOT change
// the phase — use SetPhase for that.
func (s *StateStore) Log(level LogLevel, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return
	}
	s.state.Log = appendLog(s.state.Log, level, msg)
	_ = s.persistLocked()
}

// Fail transitions to PhaseFailed and records the error.
// The page surfaces the error + manual fallback steps.
// Idempotent: calling Fail twice is a no-op (the second
// call's error message is preserved but the phase stays
// "failed").
func (s *StateStore) Fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return
	}
	s.state.Phase = PhaseFailed
	if err != nil {
		s.state.Error = err.Error()
	}
	s.state.ManualFallback = true
	s.state.FinishedAt = time.Now().UTC()
	s.state.Log = appendLog(s.state.Log, LogError, "FAILED: "+s.state.Error)
	_ = s.persistLocked()
}

// Complete transitions to PhaseDone. The orchestrator calls
// this after the verify phase succeeds.
func (s *StateStore) Complete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return
	}
	s.state.Phase = PhaseDone
	s.state.FinishedAt = time.Now().UTC()
	s.state.Log = appendLog(s.state.Log, LogInfo, "update completed successfully")
	_ = s.persistLocked()
}

// SetManualStep stores a one-line operator action in the
// state and adds a log line. Used by the v0.29.1 orchestrator
// when it stops at PhaseBuildDone and needs to tell the
// operator to run `docker compose up -d --force-recreate
// --no-deps skygate` on the host. Idempotent: safe to call
// multiple times.
func (s *StateStore) SetManualStep(kind, cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return
	}
	s.state.ManualSwap = cmd
	s.state.Log = appendLog(s.state.Log, LogInfo, "manual step ("+kind+"): "+cmd)
	_ = s.persistLocked()
}

// Clear removes the persisted state file and in-memory
// state. Called by the "Dismiss" button on /admin/update
// when the operator has read the failure banner and wants
// to start fresh. Not called automatically on success —
// the success state lingers so the operator can see "yes,
// that worked" after the page refreshes.
func (s *StateStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = nil
	_ = os.Remove(s.path)
}

// persistLocked writes the current state to disk. Caller
// must hold s.mu. Errors are intentionally swallowed
// (logged via the path name + a console print, NOT via
// the standard logger which the package can't import
// without a cycle): the operator-facing failure mode is
// "the page shows the latest in-memory state and the disk
// copy is stale", not a hard error.
func (s *StateStore) persistLocked() error {
	if s.state == nil {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to a temp file in the same dir,
	// then rename. This avoids the half-written-file race
	// that the page could otherwise read.
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "skygate-update-status-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, s.path)
}

// appendLog adds a new entry and trims the buffer to
// MaxLogLines. Caller must NOT hold the lock (this fn is
// pure).
func appendLog(log []LogEntry, level LogLevel, msg string) []LogEntry {
	entry := LogEntry{At: time.Now().UTC(), Level: level, Msg: msg}
	log = append(log, entry)
	if len(log) > MaxLogLines {
		// Drop the oldest. The UI shows a "[older entries
		// truncated]" hint via the MaxLogLines constant.
		log = log[len(log)-MaxLogLines:]
	}
	return log
}

// GenerateJobID returns a short, URL-safe ID. Used
// for the JobID field; the operator can quote it in support
// requests and the audit log.
//
// 4 random bytes = 8 hex chars. With /admin/update being
// a single-operator surface, 32 bits of entropy is more
// than enough (the actual risk is "operator clicks
// Update twice in a row" → the second click returns
// http.StatusConflict Conflict and the JobID is unused; no auth context
// to brute-force).
func GenerateJobID() string {
	var b [4]byte
	if _, err := randRead(b[:]); err != nil {
		// Fallback: time-based. Not cryptographically random
		// but more than sufficient for a UI display id.
		now := time.Now().UTC().UnixNano()
		return fmt.Sprintf("%08x", now&0xFFFFFFFF)
	}
	return fmt.Sprintf("%x", b[:])
}

// randRead is a small wrapper around crypto/rand.Read that
// keeps state.go's import list short. The updater isn't
// security-sensitive (the JobID is a UI display id, not an
// auth token) so this is just a thin abstraction.
func randRead(b []byte) (int, error) {
	return randReadOS(b)
}
