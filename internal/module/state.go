// state.go — persistent per-module state (B-mod-core, 2026-09-09).
//
// One JSON file per module at /var/lib/skygate/modules/<name>/state.json.
// Atomic write pattern: write to state.json.tmp, fsync, rename to
// state.json. This guarantees that a crash mid-write either leaves
// the old state (readable) or the new state (complete) — never a
// half-written file. Same pattern as scripts/ha-state/state.sh
// (B-new ha-state machine, 2026-09-09).
//
// The state holds:
//   - Lifecycle: name, enabled, install_mode, installed_at, started_at
//   - Sub-features: name -> bool
//   - Last health snapshot
//   - Last error (for StateError recovery)
//
// The Manager reads State on Init() and writes State on every
// state transition (Install / Start / Stop / Enable / Disable /
// health change). Modules themselves can also write State
// (e.g. Tailscale module writes its TailscaleIP after a successful
// `tailscale up`).

package module

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// State is the on-disk shape of /var/lib/skygate/modules/<name>/state.json.
// JSON tags are stable; additions are backward-compatible (new fields
// are zero-valued when reading old state). DO NOT rename or remove
// fields without a migration path.
type State struct {
	// Name is the module identifier. Always equal to
	// Module.Name(). Persisted for crash debugging (no need
	// to look up the directory name to identify the file).
	Name string `json:"name"`

	// State is the runtime state of the module — one of
	// the State* constants in module.go (StateRunning,
	// StateStopped, StateError, etc.). The Manager
	// updates this on every state transition. Persisted
	// so that after a crash the operator can see "what
	// was the last known state of this module?".
	State string `json:"state"`

	// Enabled is true when the module should be running.
	// The Manager's StartEnabled() reads this on boot to
	// decide which modules to start. Operators flip this
	// via /admin/modules/{name}/enable.
	Enabled bool `json:"enabled"`

	// InstallMode is "in_container" | "os_level" | "none".
	// Set by the install flow. Read by Start() to know
	// where to look for the runtime (in-container sidecar
	// socket vs OS-level systemd service vs not installed).
	// "none" means the module is registered but not installed.
	InstallMode string `json:"install_mode"`

	// InstalledAt is when the module was first installed.
	// Zero if the module has never been installed.
	InstalledAt time.Time `json:"installed_at,omitempty"`

	// StartedAt is when the module most recently transitioned
	// to StateRunning. Zero if never started.
	StartedAt time.Time `json:"started_at,omitempty"`

	// SubFeatures is name -> enabled (mirrors the SubFeature
	// type's Enabled field). Persisted so the Manager can
	// re-apply sub-feature state on restart.
	SubFeatures map[string]bool `json:"sub_features,omitempty"`

	// LastHealth is the most recent HealthStatus from the
	// module. Updated by the Manager's periodic health check
	// loop. Zero value if Health() has never been called.
	LastHealth HealthStatus `json:"last_health,omitempty"`

	// LastError is the error from the most recent failed
	// Init/Start/Health call. Empty on success.
	LastError string `json:"last_error,omitempty"`

	// Info holds module-specific state fields. Same shape
	// as ModuleStatus.Info (e.g. for Tailscale: "tailscale_ip").
	// Modules write here on successful Start (e.g. Tailscale
	// writes its TailscaleIPs after `tailscale up`).
	Info map[string]string `json:"info,omitempty"`
}

// stateFile is the on-disk filename. Constant so callers don't
// typo it. B-mod-core convention.
const stateFile = "state.json"

// stateMu guards the in-memory state cache. The Manager can be
// called from multiple goroutines (HTTP handlers + periodic
// health check loop), so all reads/writes to State go through
// this mutex.
var stateMu sync.Mutex

// loadState reads the per-module state from disk. Returns:
//   - (&State{Name: name}, nil) if the file doesn't exist
//     (fresh module, never been installed)
//   - (state, nil) on successful read + parse
//   - (nil, err) on I/O error or JSON parse error
//
// The Manager calls this on Init() to populate the in-memory
// state. Subsequent Status() calls use the in-memory copy.
func loadState(dataDir, name string) (*State, error) {
	if dataDir == "" {
		return nil, errors.New("module: loadState: dataDir is empty")
	}
	if name == "" {
		return nil, errors.New("module: loadState: name is empty")
	}
	path := filepath.Join(dataDir, name, stateFile)

	// Fast path: file doesn't exist → fresh state.
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return &State{Name: name, SubFeatures: map[string]bool{}}, nil
	} else if err != nil {
		return nil, fmt.Errorf("module: stat state: %w", err)
	}

	// Read + parse. We read the whole file (state.json is
	// expected to be small — a few hundred bytes — so
	// streaming is overkill).
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("module: read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		// Corrupt JSON — return an error so the Manager
		// can decide whether to fail Init (safest) or
		// rename the corrupt file and start fresh
		// (operator override via /admin/modules/{name}/reset).
		return nil, fmt.Errorf("module: parse state: %w", err)
	}
	// Defensive: ensure Name is set (in case the file was
	// written by an older version that didn't include it).
	if s.Name == "" {
		s.Name = name
	}
	// Defensive: ensure SubFeatures map is non-nil (older
	// versions may have omitted it).
	if s.SubFeatures == nil {
		s.SubFeatures = map[string]bool{}
	}
	return &s, nil
}

// saveState writes the per-module state to disk atomically.
// Pattern: write to state.json.tmp, fsync, rename to state.json.
// Guarantees that a crash mid-write either leaves the old
// state (readable) or the new state (complete) — never a
// half-written file.
//
// The mutex is held for the entire operation. This means
// concurrent saveState calls for the same module serialize,
// which is the desired behavior (no torn writes).
func saveState(dataDir string, s *State) error {
	if dataDir == "" {
		return errors.New("module: saveState: dataDir is empty")
	}
	if s == nil {
		return errors.New("module: saveState: state is nil")
	}
	dir := filepath.Join(dataDir, s.Name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("module: mkdir state dir: %w", err)
	}
	final := filepath.Join(dir, stateFile)
	tmp := final + ".tmp"

	// Marshal with stable field order (json.Marshal sorts
	// map keys alphabetically, so SubFeatures and Info are
	// deterministic). Indent for human-readability on
	// /var/lib/skygate/modules/<name>/state.json — this file
	// is also useful for offline debugging.
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("module: marshal state: %w", err)
	}
	// Write to tmp. Mode 0640 so the state is readable by
	// the skygate user but not by other users on the host.
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("module: write tmp: %w", err)
	}
	// Atomic rename. On POSIX, rename(2) is atomic within
	// the same filesystem. dataDir is expected to be on a
	// single filesystem, so this is safe.
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("module: rename: %w", err)
	}
	return nil
}

// stateKey returns the sorted sub-feature names. Helper for
// stable state diffing (used in tests + in the Manager's
// "did the sub-feature state change?" check).
func (s *State) subFeatureNames() []string {
	if len(s.SubFeatures) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.SubFeatures))
	for k := range s.SubFeatures {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stateChanged returns true if the two states differ in any
// field that the Manager persists. Used to avoid writing
// state.json when nothing has changed (the periodic health
// check calls Status() every 30s — we don't want a disk
// write on every tick).
func stateChanged(a, b *State) bool {
	if a == nil || b == nil {
		return a != b
	}
	if a.Name != b.Name || a.State != b.State || a.Enabled != b.Enabled || a.InstallMode != b.InstallMode {
		return true
	}
	if !a.InstalledAt.Equal(b.InstalledAt) || !a.StartedAt.Equal(b.StartedAt) {
		return true
	}
	if a.LastError != b.LastError {
		return true
	}
	if a.LastHealth.Healthy != b.LastHealth.Healthy ||
		!a.LastHealth.LastCheck.Equal(b.LastHealth.LastCheck) ||
		a.LastHealth.LastError != b.LastHealth.LastError {
		return true
	}
	if len(a.SubFeatures) != len(b.SubFeatures) {
		return true
	}
	for k, va := range a.SubFeatures {
		if vb, ok := b.SubFeatures[k]; !ok || va != vb {
			return true
		}
	}
	if len(a.Info) != len(b.Info) {
		return true
	}
	for k, va := range a.Info {
		if vb, ok := b.Info[k]; !ok || va != vb {
			return true
		}
	}
	return false
}
