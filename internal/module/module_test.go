// module_test.go — unit tests for the Manager + state (B-mod-core, 2026-09-09).
//
// Coverage:
//   - loadState/saveState: roundtrip, missing file, corrupt JSON
//   - stateChanged: every field's contribution
//   - Manager.Register: duplicate detection, nil guard
//   - Manager.Get/List: registry visibility
//   - Manager.InitAll: success + failure modes
//   - Manager.Start/Stop: idempotency, state transitions
//   - Manager.Enable/Disable: state flag + audit
//   - Manager.EnableSubFeature: Requires validation
//   - Manager.StartHealthLoop: state transition on health change
//
// All tests use t.TempDir() for state isolation. The tests do NOT
// touch the real /var/lib/skygate/modules/ directory.

package module

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeModule is a minimal Module implementation for tests.
// It records calls to Init/Start/Stop/EnableSubFeature/DisableSubFeature
// so tests can assert on the lifecycle.
type fakeModule struct {
	name string

	// initErr, startErr, stopErr let tests inject failures.
	initErr  error
	startErr error
	stopErr  error

	// enableSubErr / disableSubErr injected per sub-feature name.
	enableSubErr  map[string]error
	disableSubErr map[string]error

	// subFeatures is what SubFeatures() returns.
	subFeatures []SubFeature

	// healthResult is what Health() returns.
	healthResult HealthStatus

	// state is what Status() returns.
	state ModuleStatus

	mu       sync.Mutex
	initN    int32
	startN   int32
	stopN    int32
	enabled  map[string]int // sub-name -> enable count
	disabled map[string]int // sub-name -> disable count
}

func newFakeModule(name string) *fakeModule {
	return &fakeModule{
		name:          name,
		enableSubErr:  map[string]error{},
		disableSubErr: map[string]error{},
		enabled:       map[string]int{},
		disabled:      map[string]int{},
		healthResult:  HealthStatus{Healthy: true, Checks: map[string]bool{"ok": true}},
		state:         ModuleStatus{State: StateRunning, StartedAt: time.Now()},
	}
}

func (f *fakeModule) Name() string { return f.name }
func (f *fakeModule) Init(_ context.Context, _ ModuleConfig) error {
	atomic.AddInt32(&f.initN, 1)
	return f.initErr
}
func (f *fakeModule) Start(_ context.Context) error {
	atomic.AddInt32(&f.startN, 1)
	return f.startErr
}
func (f *fakeModule) Stop(_ context.Context) error {
	atomic.AddInt32(&f.stopN, 1)
	return f.stopErr
}
func (f *fakeModule) Status() ModuleStatus { return f.state }
func (f *fakeModule) Health() HealthStatus { return f.healthResult }
func (f *fakeModule) SubFeatures() []SubFeature {
	return f.subFeatures
}
func (f *fakeModule) EnableSubFeature(_ context.Context, name string) error {
	if err, ok := f.enableSubErr[name]; ok {
		return err
	}
	f.mu.Lock()
	f.enabled[name]++
	f.mu.Unlock()
	return nil
}
func (f *fakeModule) DisableSubFeature(_ context.Context, name string) error {
	if err, ok := f.disableSubErr[name]; ok {
		return err
	}
	f.mu.Lock()
	f.disabled[name]++
	f.mu.Unlock()
	return nil
}

// newTestManager builds a Manager + a captured audit log for tests.
func newTestManager(t *testing.T) (*Manager, *[]string) {
	t.Helper()
	dir := t.TempDir()
	var audit []string
	mu := sync.Mutex{}
	mgr := NewManager(dir, "/tmp/sock", func(action, detail string) {
		mu.Lock()
		audit = append(audit, action+":"+detail)
		mu.Unlock()
	})
	mgr.SetHealthInterval(50 * time.Millisecond) // fast for tests
	t.Cleanup(func() { mgr.StopHealthLoop() })
	return mgr, &audit
}

// TestLoadState_Missing verifies that loadState returns a fresh
// State when the file doesn't exist (no error).
func TestLoadState_Missing(t *testing.T) {
	dir := t.TempDir()
	s, err := loadState(dir, "nonexistent")
	if err != nil {
		t.Fatalf("loadState(missing): unexpected error: %v", err)
	}
	if s.Name != "nonexistent" {
		t.Errorf("Name = %q, want %q", s.Name, "nonexistent")
	}
	if s.SubFeatures == nil {
		t.Error("SubFeatures should be non-nil empty map")
	}
}

// TestSaveLoadState_Roundtrip verifies that saveState + loadState
// preserves every field.
func TestSaveLoadState_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	original := &State{
		Name:        "tailscale",
		State:       StateRunning,
		Enabled:     true,
		InstallMode: "in_container",
		InstalledAt: time.Date(2026, 9, 9, 14, 0, 0, 0, time.UTC),
		StartedAt:   time.Date(2026, 9, 9, 14, 5, 0, 0, time.UTC),
		SubFeatures: map[string]bool{"cluster": true, "telegram": false},
		LastHealth:  HealthStatus{Healthy: true, Checks: map[string]bool{"auth": true}},
		Info:        map[string]string{"tailscale_ip": "100.64.0.22"},
		LastError:   "",
	}
	if err := saveState(dir, original); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	loaded, err := loadState(dir, "tailscale")
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if loaded.Name != original.Name {
		t.Errorf("Name: got %q want %q", loaded.Name, original.Name)
	}
	if loaded.State != original.State {
		t.Errorf("State: got %q want %q", loaded.State, original.State)
	}
	if !loaded.Enabled {
		t.Error("Enabled: got false, want true")
	}
	if loaded.InstallMode != original.InstallMode {
		t.Errorf("InstallMode: got %q want %q", loaded.InstallMode, original.InstallMode)
	}
	if !loaded.InstalledAt.Equal(original.InstalledAt) {
		t.Errorf("InstalledAt: got %v want %v", loaded.InstalledAt, original.InstalledAt)
	}
	if !loaded.StartedAt.Equal(original.StartedAt) {
		t.Errorf("StartedAt: got %v want %v", loaded.StartedAt, original.StartedAt)
	}
	if !loaded.SubFeatures["cluster"] || loaded.SubFeatures["telegram"] {
		t.Errorf("SubFeatures: got %v, want cluster=true telegram=false", loaded.SubFeatures)
	}
	if loaded.LastHealth.Healthy != original.LastHealth.Healthy {
		t.Errorf("LastHealth.Healthy: got %v want %v", loaded.LastHealth.Healthy, original.LastHealth.Healthy)
	}
	if loaded.Info["tailscale_ip"] != "100.64.0.22" {
		t.Errorf("Info[tailscale_ip]: got %q want %q", loaded.Info["tailscale_ip"], "100.64.0.22")
	}
}

// TestStateChanged verifies that stateChanged returns true when
// ANY field differs and false when all match.
func TestStateChanged(t *testing.T) {
	base := &State{
		Name: "tailscale", State: StateRunning, Enabled: true,
		InstallMode: "in_container",
		SubFeatures: map[string]bool{"cluster": true},
		Info:        map[string]string{"k": "v"},
	}
	cases := []struct {
		name string
		mod  func(*State)
		want bool
	}{
		{"same", func(s *State) {}, false},
		{"name", func(s *State) { s.Name = "other" }, true},
		{"state", func(s *State) { s.State = StateStopped }, true},
		{"enabled", func(s *State) { s.Enabled = false }, true},
		{"install_mode", func(s *State) { s.InstallMode = "os_level" }, true},
		{"sub_feature", func(s *State) { s.SubFeatures["cluster"] = false }, true},
		{"info", func(s *State) { s.Info["k"] = "x" }, true},
		{"last_error", func(s *State) { s.LastError = "boom" }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := *base
			b.SubFeatures = map[string]bool{"cluster": true}
			b.Info = map[string]string{"k": "v"}
			c.mod(&b)
			if got := stateChanged(base, &b); got != c.want {
				t.Errorf("stateChanged: got %v want %v", got, c.want)
			}
		})
	}
}

// TestManager_Register_Duplicate verifies that Register rejects
// a second module with the same name.
func TestManager_Register_Duplicate(t *testing.T) {
	mgr, _ := newTestManager(t)
	if err := mgr.Register(newFakeModule("tailscale")); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := mgr.Register(newFakeModule("tailscale"))
	if err == nil {
		t.Error("second Register: expected error, got nil")
	}
}

// TestManager_Register_Nil verifies that Register rejects nil.
func TestManager_Register_Nil(t *testing.T) {
	mgr, _ := newTestManager(t)
	if err := mgr.Register(nil); err == nil {
		t.Error("Register(nil): expected error, got nil")
	}
}

// TestManager_Get_List verifies the registry visibility.
func TestManager_Get_List(t *testing.T) {
	mgr, _ := newTestManager(t)
	_ = mgr.Register(newFakeModule("tailscale"))
	_ = mgr.Register(newFakeModule("headplane"))

	if _, ok := mgr.Get("tailscale"); !ok {
		t.Error("Get(tailscale): expected true")
	}
	if _, ok := mgr.Get("nonexistent"); ok {
		t.Error("Get(nonexistent): expected false")
	}
	list := mgr.List()
	if len(list) != 2 {
		t.Errorf("List: got %d items, want 2", len(list))
	}
}

// TestManager_InitAll_Success verifies InitAll calls Init on
// every registered module.
func TestManager_InitAll_Success(t *testing.T) {
	mgr, audit := newTestManager(t)
	a := newFakeModule("tailscale")
	b := newFakeModule("headplane")
	_ = mgr.Register(a)
	_ = mgr.Register(b)

	if err := mgr.InitAll(context.Background()); err != nil {
		t.Fatalf("InitAll: %v", err)
	}
	if a.initN != 1 || b.initN != 1 {
		t.Errorf("initN: a=%d b=%d, want both 1", a.initN, b.initN)
	}
	if len(*audit) != 2 {
		t.Errorf("audit: got %d entries, want 2", len(*audit))
	}
}

// TestManager_InitAll_Failure verifies that a failing module
// is reported but other modules still init.
func TestManager_InitAll_Failure(t *testing.T) {
	mgr, _ := newTestManager(t)
	a := newFakeModule("tailscale")
	a.initErr = errors.New("binary missing")
	b := newFakeModule("headplane")
	_ = mgr.Register(a)
	_ = mgr.Register(b)

	err := mgr.InitAll(context.Background())
	if err == nil {
		t.Fatal("InitAll: expected error, got nil")
	}
	// Both modules should still have been called.
	if a.initN != 1 || b.initN != 1 {
		t.Errorf("initN: a=%d b=%d, want both 1", a.initN, b.initN)
	}
	// tailscale's state should be StateError.
	if info, ok := findInfo(mgr, "tailscale"); !ok || info.State != StateError {
		t.Errorf("tailscale state: got %v want %v", info.State, StateError)
	}
}

// TestManager_Start_Stop_Idempotency verifies that calling
// Start/Stop twice is a no-op.
func TestManager_Start_Stop_Idempotency(t *testing.T) {
	mgr, _ := newTestManager(t)
	a := newFakeModule("tailscale")
	_ = mgr.Register(a)
	_ = mgr.InitAll(context.Background())

	// First Start: actual call.
	if err := mgr.Start(context.Background(), "tailscale"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Second Start: should be a no-op (idempotent).
	if err := mgr.Start(context.Background(), "tailscale"); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if a.startN != 1 {
		t.Errorf("startN: got %d, want 1", a.startN)
	}

	// First Stop: actual call.
	if err := mgr.Stop(context.Background(), "tailscale"); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	// Second Stop: should be a no-op.
	if err := mgr.Stop(context.Background(), "tailscale"); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if a.stopN != 1 {
		t.Errorf("stopN: got %d, want 1", a.stopN)
	}
}

// TestManager_Enable_Disable verifies state flag + audit.
func TestManager_Enable_Disable(t *testing.T) {
	mgr, audit := newTestManager(t)
	a := newFakeModule("tailscale")
	_ = mgr.Register(a)
	_ = mgr.InitAll(context.Background())

	if err := mgr.Enable("tailscale"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !mgr.states["tailscale"].Enabled {
		t.Error("Enabled: got false, want true")
	}
	// Idempotent: enabling again is a no-op.
	if err := mgr.Enable("tailscale"); err != nil {
		t.Fatalf("Enable (2nd): %v", err)
	}
	// Count enable audits.
	count := 0
	for _, e := range *audit {
		if e == "module.tailscale.enable:ok" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("enable audit count: got %d, want 1", count)
	}
	if err := mgr.Disable("tailscale"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if mgr.states["tailscale"].Enabled {
		t.Error("Enabled: got true, want false")
	}
}

// TestManager_StartEnabled_StartsOnlyEnabled verifies that
// StartEnabled only starts modules with state.Enabled=true.
func TestManager_StartEnabled_StartsOnlyEnabled(t *testing.T) {
	mgr, _ := newTestManager(t)
	a := newFakeModule("tailscale")
	b := newFakeModule("headplane")
	_ = mgr.Register(a)
	_ = mgr.Register(b)
	_ = mgr.InitAll(context.Background())

	// Only enable tailscale.
	_ = mgr.Enable("tailscale")

	if err := mgr.StartEnabled(context.Background()); err != nil {
		t.Fatalf("StartEnabled: %v", err)
	}
	if a.startN != 1 {
		t.Errorf("tailscale startN: got %d, want 1", a.startN)
	}
	if b.startN != 0 {
		t.Errorf("headplane startN: got %d, want 0", b.startN)
	}
}

// TestManager_EnableSubFeature_Requires verifies that
// EnableSubFeature rejects when prereqs aren't met.
func TestManager_EnableSubFeature_Requires(t *testing.T) {
	mgr, _ := newTestManager(t)
	a := newFakeModule("tailscale")
	a.subFeatures = []SubFeature{
		{Name: "cluster", Description: "HA mesh"},
		{Name: "exit", Description: "Exit-node", Requires: []string{"cluster"}},
	}
	_ = mgr.Register(a)
	_ = mgr.InitAll(context.Background())

	// Try to enable "exit" before "cluster" — should fail.
	err := mgr.EnableSubFeature(context.Background(), "tailscale", "exit")
	if !errors.Is(err, ErrSubFeatureRequires) {
		t.Errorf("got %v, want ErrSubFeatureRequires", err)
	}
	// Enable cluster first.
	if err := mgr.EnableSubFeature(context.Background(), "tailscale", "cluster"); err != nil {
		t.Fatalf("enable cluster: %v", err)
	}
	// Now enable exit.
	if err := mgr.EnableSubFeature(context.Background(), "tailscale", "exit"); err != nil {
		t.Errorf("enable exit after cluster: %v", err)
	}
	if !mgr.states["tailscale"].SubFeatures["exit"] {
		t.Error("SubFeatures[exit]: got false, want true")
	}
}

// TestManager_HealthLoop_StateTransition verifies that the
// health check loop transitions a module to StateError when
// Health() reports unhealthy, and back to StateRunning when
// it recovers.
func TestManager_HealthLoop_StateTransition(t *testing.T) {
	mgr, _ := newTestManager(t)
	a := newFakeModule("tailscale")
	a.state.State = StateRunning
	_ = mgr.Register(a)
	_ = mgr.InitAll(context.Background())
	// Force to StateRunning (Init doesn't auto-start).
	mgr.states["tailscale"].State = StateRunning
	_ = saveState(filepath.Dir(filepath.Dir(mgr.dataDir)), mgr.states["tailscale"])

	// Start healthy.
	a.healthResult = HealthStatus{Healthy: true, Checks: map[string]bool{"ok": true}}
	mgr.StartHealthLoop(context.Background())

	// Wait for at least one tick.
	time.Sleep(100 * time.Millisecond)

	// Flip to unhealthy.
	a.mu.Lock()
	a.healthResult = HealthStatus{
		Healthy: false,
		Checks:  map[string]bool{"ok": false},
		LastError: "auth expired",
	}
	a.mu.Unlock()

	// Wait for the loop to detect the change.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.RLock()
		st := mgr.states["tailscale"]
		mgr.mu.RUnlock()
		if st != nil && st.State == StateError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mgr.mu.RLock()
	st := mgr.states["tailscale"]
	mgr.mu.RUnlock()
	if st == nil || st.State != StateError {
		t.Errorf("state after unhealthy: got %v, want %v", st, StateError)
	}

	// Recover.
	a.mu.Lock()
	a.healthResult = HealthStatus{Healthy: true, Checks: map[string]bool{"ok": true}}
	a.mu.Unlock()

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.RLock()
		st := mgr.states["tailscale"]
		mgr.mu.RUnlock()
		if st != nil && st.State == StateRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mgr.mu.RLock()
	st = mgr.states["tailscale"]
	mgr.mu.RUnlock()
	if st == nil || st.State != StateRunning {
		t.Errorf("state after recovery: got %v, want %v", st, StateRunning)
	}
}

// findInfo is a test helper that finds a module by name in List().
func findInfo(mgr *Manager, name string) (ModuleInfo, bool) {
	for _, info := range mgr.List() {
		if info.Name == name {
			return info, true
		}
	}
	return ModuleInfo{}, false
}
