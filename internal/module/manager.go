// manager.go — lifecycle orchestration for skygate modules
// (B-mod-core, 2026-09-09).
//
// The Manager owns the registry of modules + the per-module state.
// On boot, the caller (cmd/skygate/main.go) does:
//
//	manager := module.NewManager(auditLog)
//	manager.Register(tailscale.NewModule())
//	manager.InitAll(ctx, cfg)
//	manager.StartEnabled(ctx)
//
// The Manager then runs a periodic health check (every 30s) that
// calls Module.Health() on every running module, persists the
// result to state.json, and triggers a state transition to
// StateError if Health() reports unhealthy.
//
// On shutdown, the caller calls Manager.StopAll() to gracefully
// stop every running module.

package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// Manager owns the module registry + per-module state. The zero
// value is NOT usable; callers MUST use NewManager().
type Manager struct {
	// modules is the registered modules, keyed by Name().
	// Populated by Register() (called from main.go before
	// InitAll) and by Register() itself.
	modules map[string]Module

	// states is the per-module persistent state, keyed by
	// Name(). Populated by InitAll (via loadState) and
	// mutated by every state transition. The Manager's
	// mutex guards both this map and the underlying
	// state files.
	states map[string]*State

	// dataDir is the per-module state directory
	// (/var/lib/skygate/modules/<name>/). Set by
	// NewManager and never changed.
	dataDir string

	// socketDir is the per-module runtime socket directory
	// (/var/run/skygate/modules/<name>/). Set by
	// NewManager and never changed.
	socketDir string

	// auditLog is the audit row callback. Set by
	// NewManager. Called on every state transition +
	// health change.
	auditLog func(action, detail string)

	// env is the SKYGATE_* env vars loaded at boot. Passed
	// to each module's Init() so modules can read their
	// specific env vars (e.g. SKYGATE_TS_CLUSTER).
	env map[string]string

	// mu guards modules, states, and the underlying state
	// files. RWMutex because most operations are reads
	// (Status, Get, List); only Init/Start/Stop/Enable/Disable
	// take the write lock.
	mu sync.RWMutex

	// healthTicker is the periodic health check loop. Started
	// by StartHealthLoop, stopped by StopHealthLoop.
	healthStop chan struct{}
	healthDone chan struct{}

	// healthInterval is how often the health check fires.
	// Default 30s; overridable via SetHealthInterval (for
	// tests that want faster checks).
	healthInterval time.Duration

	// dbc returns the *sql.DB for skygate's database.
	// Set via SetDBC. Passed to every module's
	// ModuleConfig.DBC so modules can read/write the
	// audit_log + applied_migrations + their own tables
	// without each one re-opening a pool. nil = no DB
	// access (modules that don't need DB still work).
	dbc func() *sql.DB
}

// NewManager creates a Manager with the given data dir + audit log
// callback. The data dir is the per-module state directory
// (/var/lib/skygate/modules/<name>/). If the dir doesn't exist,
// it's created on the first InitAll() call (MkdirAll).
//
// auditLog is called on every state transition. It MUST be
// non-nil — pass a no-op (func(string, string) {}) if you don't
// want audit rows.
func NewManager(dataDir, socketDir string, auditLog func(action, detail string)) *Manager {
	if auditLog == nil {
		auditLog = func(string, string) {}
	}
	return &Manager{
		modules:        map[string]Module{},
		states:         map[string]*State{},
		dataDir:        dataDir,
		socketDir:      socketDir,
		auditLog:       auditLog,
		env:            map[string]string{},
		healthInterval: 30 * time.Second,
	}
}

// SetEnv sets the SKYGATE_* env vars that InitAll passes to each
// module's Init(). Call this BEFORE InitAll. Modules read their
// specific env vars (e.g. SKYGATE_TS_CLUSTER) from this map.
//
// The map is copied (defensive copy — modules must not mutate
// the manager's internal state).
func (m *Manager) SetEnv(env map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.env = make(map[string]string, len(env))
	for k, v := range env {
		m.env[k] = v
	}
}

// SetDBC wires a database accessor that initOne will
// pass to every module's ModuleConfig.DBC. nil disables
// DB access (modules that don't need the DB still work,
// but their Init() can no longer call cfg.DBC()).
//
// v1.5.2+ / B-mod-core re-merge (2026-09-10): the
// original 3d80f573 wiring did not set DBC because the
// B-mod-core Tailscale stub didn't need it. The real
// B-mod-tailscale tailscale.Module.Init() now requires
// DBC (so the module can read audit_log + write its own
// audit rows + check applied_migrations). This setter
// is the wire for that requirement.
func (m *Manager) SetDBC(dbc func() *sql.DB) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dbc = dbc
}

// SetHealthInterval overrides the default 30s health check interval.
// Use this in tests; production code should leave the default.
func (m *Manager) SetHealthInterval(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d > 0 {
		m.healthInterval = d
	}
}

// Register adds a module to the manager. Must be called BEFORE
// InitAll. Returns an error if a module with the same name is
// already registered (duplicate registration is a programming bug,
// not a runtime condition we should silently accept).
func (m *Manager) Register(mod Module) error {
	if mod == nil {
		return errors.New("module: Register: module is nil")
	}
	name := mod.Name()
	if name == "" {
		return errors.New("module: Register: module name is empty")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.modules[name]; exists {
		return fmt.Errorf("module: Register: %q already registered", name)
	}
	m.modules[name] = mod
	return nil
}

// InitAll initializes every registered module. For each module:
//   1. Read persistent state from /var/lib/skygate/modules/<name>/state.json
//   2. Build ModuleConfig (data dir, socket dir, env, dbc, audit log)
//   3. Call Module.Init(ctx, cfg)
//
// Modules that fail Init are marked as StateError in their state
// and the error is returned (the caller decides whether to abort
// boot or continue with the failed module).
//
// InitAll is safe to call multiple times (it re-initializes
// modules that haven't been initialized yet, but doesn't
// re-initialize successfully-initialized ones).
func (m *Manager) InitAll(ctx context.Context) error {
	m.mu.Lock()
	// Snapshot the module names under the lock so we can
	// release it before calling Init (which may take time).
	names := make([]string, 0, len(m.modules))
	for name := range m.modules {
		names = append(names, name)
	}
	sort.Strings(names)
	m.mu.Unlock()

	var initErrors []error
	for _, name := range names {
		if err := m.initOne(ctx, name); err != nil {
			initErrors = append(initErrors, fmt.Errorf("init %s: %w", name, err))
		}
	}
	if len(initErrors) > 0 {
		return fmt.Errorf("module: InitAll: %d module(s) failed: %v", len(initErrors), initErrors)
	}
	return nil
}

// initOne initializes a single module. Helper for InitAll.
func (m *Manager) initOne(ctx context.Context, name string) error {
	m.mu.Lock()
	mod, ok := m.modules[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module: %q not registered", name)
	}
	dataDir := m.dataDir
	socketDir := m.socketDir
	env := m.env
	dbc := m.dbc
	m.mu.Unlock()

	// Load persistent state (or create a fresh one).
	state, err := loadState(dataDir, name)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if state == nil {
		state = &State{Name: name, SubFeatures: map[string]bool{}}
	}

	// Build ModuleConfig. Defensive copy of env so the
	// module can't mutate the manager's internal map.
	envCopy := make(map[string]string, len(env))
	for k, v := range env {
		envCopy[k] = v
	}
	auditLog := func(action, detail string) {
		m.auditLog(action, detail)
	}
	cfg := ModuleConfig{
		DataDir:   dataDir + "/" + name,
		SocketDir: socketDir + "/" + name,
		Env:       envCopy,
		DBC:       dbc,
		AuditLog:  auditLog,
	}

	// Call Init. Modules may take time (e.g. validate
	// binaries on disk, read auth key, etc.) — wrap in
	// a context with a reasonable timeout.
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := mod.Init(initCtx, cfg); err != nil {
		state.State = StateError
		state.LastError = err.Error()
		_ = saveState(dataDir, state)
		m.mu.Lock()
		m.states[name] = state
		m.mu.Unlock()
		m.auditLog("module."+name+".init", "error: "+err.Error())
		return err
	}

	// Init succeeded. Persist the state. The module may
	// have updated state.Info or state.SubFeatures during
	// Init; saveState will write whatever's there.
	if err := saveState(dataDir, state); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	m.mu.Lock()
	m.states[name] = state
	m.mu.Unlock()
	m.auditLog("module."+name+".init", "ok")
	return nil
}

// StartEnabled starts every module whose state.Enabled=true.
// Called once on boot after InitAll. Modules that fail to
// start are marked as StateError; the loop continues to the
// next module (so one broken module doesn't prevent others
// from starting).
func (m *Manager) StartEnabled(ctx context.Context) error {
	m.mu.Lock()
	names := make([]string, 0, len(m.modules))
	for name, mod := range m.modules {
		_ = mod
		state, ok := m.states[name]
		if ok && state.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	m.mu.Unlock()

	var startErrors []error
	for _, name := range names {
		if err := m.Start(ctx, name); err != nil {
			startErrors = append(startErrors, fmt.Errorf("start %s: %w", name, err))
		}
	}
	if len(startErrors) > 0 {
		return fmt.Errorf("module: StartEnabled: %d module(s) failed: %v", len(startErrors), startErrors)
	}
	return nil
}

// Start brings a single module up. Idempotent: returns nil
// immediately if the module is already in StateRunning or
// StateStarting.
func (m *Manager) Start(ctx context.Context, name string) error {
	m.mu.Lock()
	mod, ok := m.modules[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module: Start: %q not registered", name)
	}
	state, ok := m.states[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module: Start: %q not initialized", name)
	}
	if state.State == StateRunning || state.State == StateStarting {
		m.mu.Unlock()
		return nil // idempotent
	}
	m.mu.Unlock()

	// Set state to Starting BEFORE calling Start so that
	// concurrent Status() calls see the in-progress state.
	m.mu.Lock()
	state.State = StateStarting
	state.LastError = ""
	m.mu.Unlock()
	if err := saveState(m.dataDir, state); err != nil {
		log.Printf("module: %s: save state (starting): %v", name, err)
	}
	m.auditLog("module."+name+".start", "starting")

	// Call Start. 60s timeout — most modules should be up
	// in seconds; the long timeout is for slow OS-level
	// installs (apt install tailscale + systemctl start).
	startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := mod.Start(startCtx); err != nil {
		m.mu.Lock()
		state.State = StateError
		state.LastError = err.Error()
		m.mu.Unlock()
		_ = saveState(m.dataDir, state)
		m.auditLog("module."+name+".start", "error: "+err.Error())
		return err
	}

	// Start succeeded. Update state from the module's Status().
	status := mod.Status()
	m.mu.Lock()
	state.State = status.State
	if !status.StartedAt.IsZero() {
		state.StartedAt = status.StartedAt
	}
	if status.LastError != "" {
		state.LastError = status.LastError
	} else {
		state.LastError = ""
	}
	if status.Info != nil {
		if state.Info == nil {
			state.Info = map[string]string{}
		}
		for k, v := range status.Info {
			state.Info[k] = v
		}
	}
	m.mu.Unlock()
	if err := saveState(m.dataDir, state); err != nil {
		log.Printf("module: %s: save state (started): %v", name, err)
	}
	m.auditLog("module."+name+".start", "ok")
	return nil
}

// Stop brings a single module down. Idempotent.
func (m *Manager) Stop(ctx context.Context, name string) error {
	m.mu.Lock()
	mod, ok := m.modules[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module: Stop: %q not registered", name)
	}
	state, ok := m.states[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module: Stop: %q not initialized", name)
	}
	if state.State == StateStopped || state.State == StateNotInstalled {
		m.mu.Unlock()
		return nil // idempotent
	}
	m.mu.Unlock()

	m.mu.Lock()
	state.State = StateStopping
	m.mu.Unlock()
	_ = saveState(m.dataDir, state)
	m.auditLog("module."+name+".stop", "stopping")

	stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := mod.Stop(stopCtx); err != nil {
		m.mu.Lock()
		state.State = StateError
		state.LastError = err.Error()
		m.mu.Unlock()
		_ = saveState(m.dataDir, state)
		m.auditLog("module."+name+".stop", "error: "+err.Error())
		return err
	}

	m.mu.Lock()
	state.State = StateStopped
	state.LastError = ""
	m.mu.Unlock()
	_ = saveState(m.dataDir, state)
	m.auditLog("module."+name+".stop", "ok")
	return nil
}

// StopAll stops every running module. Called on shutdown.
// Modules are stopped in reverse registration order (so
// modules that depend on others stop first).
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	names := make([]string, 0, len(m.modules))
	for name := range m.modules {
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	m.mu.Unlock()

	var stopErrors []error
	for _, name := range names {
		if err := m.Stop(ctx, name); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop %s: %w", name, err))
		}
	}
	// Stop the health loop if it's running.
	m.StopHealthLoop()
	if len(stopErrors) > 0 {
		return fmt.Errorf("module: StopAll: %d module(s) failed: %v", len(stopErrors), stopErrors)
	}
	return nil
}

// Get returns a module by name. The returned Module is the
// live registered instance (callers can call Status/Health
// on it concurrently — the Module interface is required to
// be safe for concurrent use).
func (m *Manager) Get(name string) (Module, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mod, ok := m.modules[name]
	return mod, ok
}

// ModuleInfo is a snapshot of a module's registration +
// state, for the /admin/modules list view.
type ModuleInfo struct {
	Name        string
	State       string
	Enabled     bool
	InstallMode string
	StartedAt   time.Time
	LastError   string
	Info        map[string]string
	SubFeatures []SubFeature
}

// List returns a snapshot of every registered module + its
// current state. Sorted by name for stable rendering.
func (m *Manager) List() []ModuleInfo {
	m.mu.RLock()
	names := make([]string, 0, len(m.modules))
	for name := range m.modules {
		names = append(names, name)
	}
	sort.Strings(names)
	infos := make([]ModuleInfo, 0, len(names))
	for _, name := range names {
		mod := m.modules[name]
		state := m.states[name]
		info := ModuleInfo{
			Name: name,
		}
		if state != nil {
			info.State = state.State
			info.Enabled = state.Enabled
			info.InstallMode = state.InstallMode
			info.StartedAt = state.StartedAt
			info.LastError = state.LastError
			info.Info = state.Info
		} else {
			info.State = StateNotInstalled
		}
		if mod != nil {
			info.SubFeatures = mod.SubFeatures()
		}
		infos = append(infos, info)
	}
	m.mu.RUnlock()
	return infos
}

// Enable marks a module as enabled (will be started on the next
// StartEnabled call). Does NOT start the module immediately —
// the caller decides whether to start now or wait.
func (m *Manager) Enable(name string) error {
	m.mu.Lock()
	state, ok := m.states[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module: Enable: %q not initialized", name)
	}
	if state.Enabled {
		m.mu.Unlock()
		return nil // idempotent
	}
	state.Enabled = true
	m.mu.Unlock()
	if err := saveState(m.dataDir, state); err != nil {
		return err
	}
	m.auditLog("module."+name+".enable", "ok")
	return nil
}

// Disable marks a module as disabled (will NOT be started on
// the next StartEnabled call). Does NOT stop a running module —
// the caller decides whether to stop now.
func (m *Manager) Disable(name string) error {
	m.mu.Lock()
	state, ok := m.states[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("module: Disable: %q not initialized", name)
	}
	if !state.Enabled {
		m.mu.Unlock()
		return nil // idempotent
	}
	state.Enabled = false
	m.mu.Unlock()
	if err := saveState(m.dataDir, state); err != nil {
		return err
	}
	m.auditLog("module."+name+".disable", "ok")
	return nil
}

// EnableSubFeature enables a sub-feature on a module. Validates
// the Requires list (other sub-features must be enabled first).
// Does NOT trigger a state change in the module itself — the
// module's EnableSubFeature method is responsible for any
// side effects (e.g. advertising a subnet route).
func (m *Manager) EnableSubFeature(ctx context.Context, moduleName, subName string) error {
	mod, ok := m.Get(moduleName)
	if !ok {
		return fmt.Errorf("module: EnableSubFeature: %q not registered", moduleName)
	}
	// Find the sub-feature in the module's list.
	var sub *SubFeature
	for i := range mod.SubFeatures() {
		if mod.SubFeatures()[i].Name == subName {
			s := mod.SubFeatures()[i]
			sub = &s
			break
		}
	}
	if sub == nil {
		return fmt.Errorf("module: %w: %s/%s", ErrSubFeatureNotFound, moduleName, subName)
	}
	// Validate Requires.
	m.mu.RLock()
	state := m.states[moduleName]
	m.mu.RUnlock()
	if state == nil {
		return fmt.Errorf("module: %q not initialized", moduleName)
	}
	for _, req := range sub.Requires {
		if !state.SubFeatures[req] {
			return fmt.Errorf("module: %w: %s/%s requires %s", ErrSubFeatureRequires, moduleName, subName, req)
		}
	}
	// Call the module's EnableSubFeature.
	if err := mod.EnableSubFeature(ctx, subName); err != nil {
		return err
	}
	// Update state.
	m.mu.Lock()
	state.SubFeatures[subName] = true
	m.mu.Unlock()
	if err := saveState(m.dataDir, state); err != nil {
		return err
	}
	m.auditLog("module."+moduleName+".subfeature.enable", "sub="+subName)
	return nil
}

// DisableSubFeature disables a sub-feature. Symmetric to
// EnableSubFeature. The module's DisableSubFeature method
// is responsible for cleanup (e.g. un-advertising routes).
func (m *Manager) DisableSubFeature(ctx context.Context, moduleName, subName string) error {
	mod, ok := m.Get(moduleName)
	if !ok {
		return fmt.Errorf("module: DisableSubFeature: %q not registered", moduleName)
	}
	// Check the sub-feature exists.
	found := false
	for _, s := range mod.SubFeatures() {
		if s.Name == subName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("module: %w: %s/%s", ErrSubFeatureNotFound, moduleName, subName)
	}
	if err := mod.DisableSubFeature(ctx, subName); err != nil {
		return err
	}
	m.mu.Lock()
	if state, ok := m.states[moduleName]; ok {
		state.SubFeatures[subName] = false
	}
	m.mu.Unlock()
	if state, ok := m.states[moduleName]; ok {
		_ = saveState(m.dataDir, state)
	}
	m.auditLog("module."+moduleName+".subfeature.disable", "sub="+subName)
	return nil
}

// StartHealthLoop starts the periodic health check. Every
// healthInterval, the loop calls Module.Health() on every
// running module, persists the result, and triggers a state
// transition to StateError if Health() reports unhealthy.
//
// Call StopHealthLoop on shutdown.
func (m *Manager) StartHealthLoop(ctx context.Context) {
	m.mu.Lock()
	if m.healthStop != nil {
		m.mu.Unlock()
		return // already running
	}
	m.healthStop = make(chan struct{})
	m.healthDone = make(chan struct{})
	interval := m.healthInterval
	m.mu.Unlock()

	go m.healthLoop(ctx, interval)
}

// StopHealthLoop stops the health check loop. Idempotent.
func (m *Manager) StopHealthLoop() {
	m.mu.Lock()
	if m.healthStop == nil {
		m.mu.Unlock()
		return
	}
	close(m.healthStop)
	m.healthStop = nil
	m.mu.Unlock()
	if m.healthDone != nil {
		<-m.healthDone
		m.healthDone = nil
	}
}

// healthLoop is the body of the periodic health check. It
// ticks every `interval` and checks every running module.
func (m *Manager) healthLoop(ctx context.Context, interval time.Duration) {
	defer close(m.healthDone)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.healthStop:
			return
		case <-t.C:
			m.checkAllHealth(ctx)
		}
	}
}

// checkAllHealth calls Health() on every running module and
// persists the result. Runs without holding the manager's
// lock (Health() may take up to 5s, and we don't want to
// block HTTP handlers calling Status() during the check).
func (m *Manager) checkAllHealth(ctx context.Context) {
	m.mu.RLock()
	names := make([]string, 0, len(m.modules))
	for name, mod := range m.modules {
		_ = mod
		if state, ok := m.states[name]; ok && (state.State == StateRunning || state.State == StateError) {
			names = append(names, name)
		}
	}
	m.mu.RUnlock()

	for _, name := range names {
		m.checkOneHealth(ctx, name)
	}
}

// checkOneHealth runs Health() on a single module and
// persists the result. Triggers a state transition to
// StateError if Health() reports unhealthy.
func (m *Manager) checkOneHealth(ctx context.Context, name string) {
	m.mu.RLock()
	mod, ok := m.modules[name]
	state, stateOK := m.states[name]
	m.mu.RUnlock()
	if !ok || !stateOK {
		return
	}

	// Health() does not take a context (per the Module
	// interface). Modules are expected to return within
	// 5s; if a module is slow, the periodic tick will
	// overlap but the mutex prevents torn state writes.
	_ = ctx
	hs := mod.Health()

	// Update state.LastHealth.
	m.mu.Lock()
	state.LastHealth = hs
	// If the module is in StateRunning but health is bad,
	// transition to StateError. If in StateError but health
	// recovered, transition back to StateRunning.
	prev := state.State
	if state.State == StateRunning && !hs.Healthy {
		state.State = StateError
		state.LastError = "health check failed: " + hs.LastError
	} else if state.State == StateError && hs.Healthy {
		state.State = StateRunning
		state.LastError = ""
	}
	m.mu.Unlock()

	// Persist + audit only if the state actually changed
	// (avoids disk writes on every 30s tick).
	if stateChanged(m.states[name], state) || prev != state.State {
		_ = saveState(m.dataDir, state)
		if prev != state.State {
			if state.State == StateError {
				m.auditLog("module."+name+".health", "error: "+state.LastError)
			} else {
				m.auditLog("module."+name+".health", "recovered")
			}
		}
	}
}

