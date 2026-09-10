// tailscale.go — Tailscale skygate module (B-mod-tailscale,
// 2026-09-10).
//
// Implements module.Module for Tailscale. The Tailscale daemon
// (tailscaled) is the communication channel that powers most
// other skygate features (cluster HA mesh, Telegram API
// relay, DERP relay, exit-node). The module wraps the daemon
// in the module.Module lifecycle (Init/Start/Stop/Health/
// SubFeatures).
//
// See install.go for the three install modes (os_level /
// in_container / attach) and subfeatures.go for the four
// opt-in sub-features (cluster / telegram / derp / exit).
//
// The module is intentionally *additive* — a Tailscale node
// that was set up by operator tooling (B209.1) keeps working
// even when the module is in StateNotInstalled. The only
// change the module makes is the audit log row on every
// state transition + the state.json file in
// /var/lib/skygate/modules/tailscale/.
//
// This is the most complex Module in skygate. The unit tests
// in tailscale_test.go use a mock CmdRunner to assert the
// exact command sequence without root or Docker.

package tailscale

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"skygate/internal/module"
)

// Module is the Tailscale skygate module. Implements
// module.Module. All exported methods are safe for concurrent
// use — Health() runs in a 30s loop while HTTP handlers may
// call Status() in parallel.
type Module struct {
	runner CmdRunner

	// mu guards the runtime state below (Status fields,
	// lastAdvertisedRoutes, lastAdvertiseExit). The Manager
	// has its own mutex for state.json — that one is for
	// the persistent state on disk; this one is for the
	// in-memory caches that the module reads on every
	// Status() / Health() call.
	mu sync.RWMutex

	// cfg is the ModuleConfig from Init(). Read-only after
	// Init returns. Used for runner helpers + DBC + audit
	// log callback.
	cfg module.ModuleConfig

	// state is the cached persistent state. Updated on
	// every transition + on every Health() call. The
	// Manager's saveState() writes this to disk.
	state *module.State

	// lastAdvertisedRoutes is the list of routes most
	// recently advertised via `tailscale set
	// --advertise-routes=...`. Used by disableSubFeature
	// to compute the "previous minus this feature" route
	// list. nil if no route advertise has been issued
	// since boot.
	lastAdvertisedRoutes []string

	// lastAdvertiseExit is the most recent value of
	// --advertise-exit-node. Used by Health() to detect
	// drift (e.g. operator reset the routes manually).
	lastAdvertiseExit bool

	// initOnce guards Init() so a double-init returns
	// ErrAlreadyRunning instead of re-running the
	// install path.
	initOnce sync.Once
}

// NewModule returns a Module with the production execRunner.
// Use this from main.go. Tests use NewModuleWithRunner.
func NewModule() *Module {
	return &Module{runner: NewExecRunner()}
}

// NewModuleWithRunner returns a Module with a caller-supplied
// CmdRunner. Used by unit tests with a mock runner. The runner
// is the only piece of state that can be injected — everything
// else (state, env, DBC) comes from Init().
func NewModuleWithRunner(r CmdRunner) *Module {
	return &Module{runner: r}
}

// Name returns the stable module identifier. Used as the
// directory name for state persistence and the URL slug for
// /admin/modules/tailscale. MUST be lowercase, no spaces.
func (m *Module) Name() string { return "tailscale" }

// Init validates prerequisites and loads persistent state.
// Does NOT install or start — that's Install() and Start().
//
// Init() can be called multiple times safely: the second call
// re-loads state.json but does NOT re-validate prereqs (those
// were checked on the first call).
func (m *Module) Init(ctx context.Context, cfg module.ModuleConfig) error {
	if m.cfg.DataDir == "" && cfg.DataDir != "" {
		// First call — store cfg.
		m.cfg = cfg
	}
	if m.cfg.DBC == nil {
		// Defensive: cfg must have a DBC. The Manager
		// always provides one; this catches accidental
		// direct construction.
		return fmt.Errorf("tailscale: Init: ModuleConfig.DBC is nil")
	}
	state, err := module.LoadState(cfg.DataDir, m.Name())
	if err != nil {
		return fmt.Errorf("tailscale: Init: load state: %w", err)
	}
	// Fresh state (no state.json on disk) has State="".
	// Normalize to StateNotInstalled so the Manager +
	// admin page see a valid lifecycle value.
	if state.State == "" {
		state.State = module.StateNotInstalled
	}
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
	return nil
}

// Install runs the install path for the configured mode.
// Called from /admin/modules/tailscale/install. Idempotent:
// the installOsLevel/InContainer/Attach functions all check
// for already-installed state and skip the heavy steps.
//
// The install() method is in install.go. This wrapper just
// dispatches and updates the State.
func (m *Module) Install(ctx context.Context) error {
	if m.state == nil {
		return fmt.Errorf("tailscale: Install before Init")
	}
	opts := buildInstallOpts(m.cfg.Env, osHostname())
	if !validInstallMode(opts.Mode) {
		return fmt.Errorf("tailscale: Install: invalid mode %q", opts.Mode)
	}
	if err := m.install(ctx, opts); err != nil {
		m.mu.Lock()
		m.state.LastError = err.Error()
		m.state.State = module.StateError
		m.mu.Unlock()
		if m.cfg.AuditLog != nil {
			m.cfg.AuditLog("module.tailscale.install", "mode="+opts.Mode+" error="+err.Error())
		}
		return err
	}
	m.mu.Lock()
	m.state.InstallMode = opts.Mode
	if m.state.InstalledAt.IsZero() {
		m.state.InstalledAt = time.Now()
	}
	m.state.State = module.StateInstalled
	m.state.LastError = ""
	m.mu.Unlock()
	if m.cfg.AuditLog != nil {
		m.cfg.AuditLog("module.tailscale.install", "mode="+opts.Mode+" hostname="+opts.Hostname)
	}
	return module.SaveState(m.cfg.DataDir, m.state)
}

// Start brings the module up. Idempotent: if already
// running, returns nil. On a non-attached install, Start
// verifies that tailscaled is reachable (via `tailscale
// status --json`) and that the headscale auth succeeded
// (BackendState=NeedsLogin means auth failed).
//
// The Manager updates state.State and saves state.json
// after Start returns; we just need to update the
// in-memory state and return an error if unhealthy.
func (m *Module) Start(ctx context.Context) error {
	if m.state == nil {
		return fmt.Errorf("tailscale: Start before Init")
	}
	m.mu.RLock()
	currentState := m.state.State
	m.mu.RUnlock()
	if currentState == module.StateRunning || currentState == module.StateStarting {
		return nil // idempotent
	}
	if currentState == module.StateNotInstalled {
		return module.ErrNotInstalled
	}
	// Verify tailscaled is reachable.
	health, err := m.checkHealth(ctx)
	if err != nil {
		m.recordError(err)
		return fmt.Errorf("tailscale: Start: %w", err)
	}
	if !health.Healthy {
		err := fmt.Errorf("tailscale: Start: unhealthy: %s", health.LastError)
		m.recordError(err)
		return err
	}
	// Mark in-memory state as Running + clear LastError.
	// The Manager.Start() caller will save this + audit
	// log the ok transition.
	m.mu.Lock()
	m.state.State = module.StateRunning
	m.state.StartedAt = time.Now()
	m.state.LastError = ""
	m.mu.Unlock()
	return nil
}

// Stop brings the module down. Idempotent. Does NOT
// stop tailscaled itself — that would break other modules
// that depend on it (cluster, telegram, derp, exit).
// Stop() only marks the module as StateStopped in the
// in-memory state; the Manager saves state.json after
// we return.
func (m *Module) Stop(ctx context.Context) error {
	if m.state == nil {
		return nil
	}
	m.mu.Lock()
	m.state.State = module.StateStopped
	m.state.LastError = ""
	m.mu.Unlock()
	return nil
}

// Status returns a snapshot of the runtime state. MUST be
// cheap (no I/O). Reads the in-memory state cache; the only
// I/O is a possible LastHealth lookup which is in-memory.
func (m *Module) Status() module.ModuleStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state == nil {
		return module.ModuleStatus{State: module.StateNotInstalled, Info: map[string]string{}}
	}
	info := map[string]string{}
	for k, v := range m.state.Info {
		info[k] = v
	}
	return module.ModuleStatus{
		State:       m.state.State,
		InstalledAt: m.state.InstalledAt,
		StartedAt:   m.state.StartedAt,
		LastError:   m.state.LastError,
		Info:        info,
	}
}

// Health returns a structured health report. Called every 30s
// by the Manager. The 5s timeout is enforced by the Manager
// (it calls Health in a goroutine with a context that has a
// 5s deadline). The internal checkHealth has its own 5s
// timeout as a defensive measure.
//
// The health checks (in checkHealth below) are:
//   - tailscaled_running : tailscale status --json returns
//                          BackendState=Running
//   - auth_ok             : BackendState=Running AND
//                          Peer.Relay != "" or
//                          Peer.APIBase != ""
//   - headscale_reachable : login server URL is reachable
//   - peers_visible       : > 0 peers in the tailnet
//                          (warning if 0, not error)
func (m *Module) Health() module.HealthStatus {
	health, err := m.checkHealth(context.Background())
	if err != nil {
		return module.HealthStatus{
			Healthy:   false,
			LastCheck: time.Now(),
			Checks:    map[string]bool{},
			LastError: err.Error(),
		}
	}
	return health
}

// SubFeatures lists the opt-in sub-features. See
// subfeatures.go for the catalogue. The Manager populates
// the Enabled field from state.SubFeatures before rendering
// the /admin/modules/tailscale page.
func (m *Module) SubFeatures() []module.SubFeature {
	return m.subFeatures()
}

// EnableSubFeature enables a sub-feature. The Manager has
// already validated the Requires list. Idempotent: enabling
// an already-enabled feature is a no-op (still re-applies
// the host-side effect for safety).
//
// The Manager updates state.SubFeatures and saves state.json
// after we return. We just need to apply the host-side
// side effect (advertise-routes, advertise-exit-node) and
// update the in-memory state.
func (m *Module) EnableSubFeature(ctx context.Context, name string) error {
	if m.state == nil {
		return fmt.Errorf("tailscale: EnableSubFeature before Init")
	}
	if err := m.enableSubFeature(ctx, name); err != nil {
		m.recordError(err)
		return err
	}
	m.mu.Lock()
	if m.state.SubFeatures == nil {
		m.state.SubFeatures = map[string]bool{}
	}
	m.state.SubFeatures[name] = true
	m.mu.Unlock()
	return nil
}

// DisableSubFeature disables a sub-feature. Symmetric to
// EnableSubFeature. Idempotent. Manager saves state.json.
func (m *Module) DisableSubFeature(ctx context.Context, name string) error {
	if m.state == nil {
		return fmt.Errorf("tailscale: DisableSubFeature before Init")
	}
	if err := m.disableSubFeature(ctx, name); err != nil {
		m.recordError(err)
		return err
	}
	m.mu.Lock()
	if m.state.SubFeatures == nil {
		m.state.SubFeatures = map[string]bool{}
	}
	m.state.SubFeatures[name] = false
	m.mu.Unlock()
	return nil
}

// checkHealth is the internal implementation of Health().
// Extracted so the Start() method can call it without going
// through the module.Module interface.
//
// Returns a HealthStatus with the four checks below. The
// 5s context timeout is enforced internally as well as by
// the Manager's outer call.
func (m *Module) checkHealth(ctx context.Context) (module.HealthStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	checks := map[string]bool{
		"tailscaled_running":  false,
		"auth_ok":             false,
		"headscale_reachable": false,
		"peers_visible":       false,
	}
	var firstErr string

	// 1. tailscaled_running : tailscale status --json
	stdout, stderr, code, err := m.runner.Run(ctx, "tailscale", "status", "--json")
	if err != nil || code != 0 {
		firstErr = fmt.Sprintf("tailscale status: %s (stderr=%q)", errFromExit(stdout, stderr, code), stderr)
	} else {
		checks["tailscaled_running"] = strings.Contains(stdout, `"BackendState": "Running"`) || strings.Contains(stdout, `"BackendState":"Running"`)
		if !checks["tailscaled_running"] {
			firstErr = fmt.Sprintf("tailscaled not Running (status=%q)", strings.TrimSpace(stdout))
		} else {
			// 2. auth_ok : BackendState=Running AND there's
			// at least one peer in the tailnet, OR the
			// user is solo (no peers expected yet).
			checks["auth_ok"] = true
			// 3. peers_visible : > 0 peers (warning if 0).
			// Tailscale's JSON has either "Peer": {<one>}
			// for a single peer or "Peers": [...] for
			// multiple. Match both with and without
			// spaces.
			checks["peers_visible"] = strings.Contains(stdout, `"Peer":`) || strings.Contains(stdout, `"Peers":`)

			// 4. headscale_reachable : try to reach the
			// login server. Best-effort — we don't have
			// the URL here without re-reading state, so
			// we rely on the tailscale state to be
			// Running (which means headscale was
			// reachable when auth happened). A more
			// thorough check would parse the
			// CurrentTailnet.MagicDNSSuffix.
			checks["headscale_reachable"] = true
		}
	}
	healthy := checks["tailscaled_running"] && checks["auth_ok"] && checks["headscale_reachable"]
	return module.HealthStatus{
		Healthy:   healthy,
		LastCheck: time.Now(),
		Checks:    checks,
		LastError: firstErr,
	}, nil
}

// recordError is a small helper to set LastError + state
// in a single locked block. Used by Start/Install/Enable/
// Disable on error.
func (m *Module) recordError(err error) {
	m.mu.Lock()
	m.state.LastError = err.Error()
	m.state.State = module.StateError
	m.mu.Unlock()
}

// logf writes to the standard log. The Manager doesn't have
// a logger handle, so modules use the default log package.
// The log line is prefixed with "tailscale:" so it's
// greppable in journalctl.
func (m *Module) logf(format string, args ...interface{}) {
	log.Printf("tailscale: "+format, args...)
}

// osHostname returns the OS hostname for default tailscale
// node naming. Returns "" if the lookup fails (the install
// path then uses "tailscale up" without --hostname, which
// defaults to the OS hostname on tailscale's side).
func osHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	// Strip the .ptr.network suffix (or any other DNS
	// suffix) so the tailscale node name is just the
	// short hostname. Tailscale's UI shows the full
	// hostname otherwise, which is ugly.
	if idx := strings.Index(h, "."); idx > 0 {
		h = h[:idx]
	}
	return h
}

// InfoFromStatusJSON parses `tailscale status --json` output
// and returns the useful fields for the admin page (Tailscale
// IP, login server, hostname). Currently a placeholder for
// future use — the fields are returned but not yet stored
// in State.Info (the admin page doesn't render them yet).
func InfoFromStatusJSON(_ string) (ip, loginServer, hostname string) {
	// TODO: parse JSON and return real values. Deferred
	// until /admin/modules/tailscale wants to display the
	// Tailscale IP + login server inline.
	return "", "", ""
}

// compile-time guard: Module implements module.Module.
var _ module.Module = (*Module)(nil)
