// Package module — plugin API for skygate v1.5.2+ (B-mod-core, 2026-09-09).
//
// A Module is an opt-in component that skygate can install, start,
// stop, and monitor. The classic example is Tailscale (module #1):
// it provides a communication channel for other skygate features
// (cluster HA mesh, Telegram API relay, DERP relay, exit-node)
// without requiring the operator to give skygate root access to
// the host OS.
//
// Lifecycle (managed by Manager):
//
//	Register(mod)         // static registration in main.go
//	Init(ctx, cfg)        // validate prereqs + load state
//	Start(ctx)            // idempotent — bring the module up
//	Status()              // snapshot of runtime state
//	Health()              // structured health report
//	SubFeatures()         // list opt-in sub-features
//	EnableSubFeature / DisableSubFeature
//	Stop(ctx)             // idempotent — bring it down
//
// See docs/internal/architecture-modules.md §3-§4 for the full design.
package module

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ModuleState is the lifecycle state of a single module instance.
// Reported via Status().State. The Manager updates this field on
// every state transition + writes to persistent state on changes.
const (
	// StateNotInstalled = the module is registered but not installed.
	// Default state for a fresh skygate — operator must explicitly
	// install the module via /admin/modules/{name}/install.
	StateNotInstalled = "not_installed"

	// StateInstalled = the module's binaries/files are on disk but
	// the runtime is not started (e.g. tailscaled is installed but
	// `tailscale up` has not run yet).
	StateInstalled = "installed"

	// StateStarting = Start() is in progress. Transient state during
	// boot or after a manual restart.
	StateStarting = "starting"

	// StateRunning = the module is up and healthy. Manager
	// periodically calls Health() to verify.
	StateRunning = "running"

	// StateStopping = Stop() is in progress. Transient state during
	// graceful shutdown.
	StateStopping = "stopping"

	// StateStopped = the module was once running but is now stopped.
	// Distinct from StateNotInstalled (binaries still on disk).
	StateStopped = "stopped"

	// StateError = the module failed Init/Start/Health. LastError
	// field has details. The operator must clear it (e.g. via
	// /admin/modules/{name}/restart) to recover.
	StateError = "error"
)

// Sentinel errors returned by Module methods. Modules may return
// richer wrapped errors; callers should use errors.Is() to check.
var (
	// ErrNotInstalled = Init/Start called on a module whose state is
	// StateNotInstalled. The operator must install first.
	ErrNotInstalled = errors.New("module: not installed")

	// ErrAlreadyRunning = Start called on a module that is already
	// in StateRunning/StateStarting. Idempotent guard.
	ErrAlreadyRunning = errors.New("module: already running")

	// ErrAlreadyStopped = Stop called on a module that is already
	// in StateStopped/StateNotInstalled. Idempotent guard.
	ErrAlreadyStopped = errors.New("module: already stopped")

	// ErrSubFeatureNotFound = EnableSubFeature/DisableSubFeature
	// called with a name that SubFeatures() did not list.
	ErrSubFeatureNotFound = errors.New("module: sub-feature not found")

	// ErrSubFeatureRequires = EnableSubFeature called but a
	// required sub-feature (per SubFeature.Requires) is not enabled.
	ErrSubFeatureRequires = errors.New("module: sub-feature requires other sub-features to be enabled first")
)

// Module is the plugin API contract every skygate module must
// implement. All methods MUST be safe to call concurrently from
// multiple goroutines (the Manager runs Health() in a loop while
// HTTP handlers may call Status() in parallel).
//
// Implementations should:
//   - Keep Init/Start/Stop idempotent
//   - Make Status() cheap (no I/O if possible; cache if needed)
//   - Make Health() return within 5 seconds (HTTP /healthz timeout)
//   - Return errors that wrap one of the sentinel errors above
//     (errors.Is(err, ErrNotInstalled) etc.)
type Module interface {
	// Name returns a stable identifier (lowercase, no spaces).
	// Used as the directory name for state persistence
	// (/var/lib/skygate/modules/<name>/) and as the URL slug
	// for /admin/modules/{name}.
	Name() string

	// Init validates prerequisites (binaries present, config valid,
	// state file readable) and loads persistent state. Does NOT
	// start the module — that's Start(). On error, returns
	// ErrNotInstalled or a wrapped error describing the missing
	// prerequisite.
	Init(ctx context.Context, cfg ModuleConfig) error

	// Start brings the module up. Idempotent: calling Start on a
	// running module returns nil (no-op). On error, the module
	// transitions to StateError and LastError is set.
	Start(ctx context.Context) error

	// Stop brings the module down gracefully. Idempotent: calling
	// Stop on a stopped module returns nil (no-op).
	Stop(ctx context.Context) error

	// Status returns a snapshot of the module's runtime state.
	// MUST be cheap (no I/O) — called on every /admin/modules
	// page load + by the Manager's periodic health check.
	Status() ModuleStatus

	// Health returns a structured health report. Called every 30s
	// by the Manager; the result is persisted to state.json and
	// shown on /admin/modules/{name}. MUST return within 5 seconds.
	Health() HealthStatus

	// SubFeatures lists the optional sub-features the module can
	// enable. Empty for modules without sub-features. The Manager
	// uses this to render the /admin/modules/{name} sub-feature
	// toggles and to validate EnableSubFeature calls.
	SubFeatures() []SubFeature

	// EnableSubFeature enables a sub-feature. The Manager has
	// already validated the Requires list; the module is
	// responsible for any side effects (e.g. advertising a
	// subnet route for the "exit" sub-feature). The
	// SubFeature.Enabled field in subsequent SubFeatures()
	// calls MUST reflect the new state.
	EnableSubFeature(ctx context.Context, name string) error

	// DisableSubFeature disables a sub-feature. Symmetric to
	// EnableSubFeature. The module is responsible for cleanup
	// (e.g. un-advertising a route).
	DisableSubFeature(ctx context.Context, name string) error
}

// ModuleConfig is passed to Init(). Holds the environment the
// module runs in: data dir, socket dir, env vars, DB accessor,
// audit log callback. Modules MUST NOT mutate this struct.
type ModuleConfig struct {
	// DataDir is the per-module persistent state directory
	// (/var/lib/skygate/modules/<name>/). The module can use
	// this for state.json, auth keys, caches, etc.
	DataDir string

	// SocketDir is the per-module runtime socket directory
	// (/var/run/skygate/modules/<name>/). The module can write
	// unix sockets here (e.g. tailscaled.sock).
	SocketDir string

	// Env is the SKYGATE_* env vars loaded by the config layer.
	// Modules read their specific env vars (e.g. SKYGATE_TS_*)
	// from this map. Empty key = not set.
	Env map[string]string

	// DBC returns a *sql.DB for the skygate database. Modules
	// that need DB access (e.g. to read portal_users, write
	// audit rows) call this on demand. The returned *sql.DB is
	// safe for concurrent use.
	DBC func() *sql.DB

	// AuditLog writes an audit row. Modules call this on every
	// state transition (Init/Start/Stop/Enable/Disable). The
	// action string follows the convention "module.<name>.<verb>"
	// (e.g. "module.tailscale.install", "module.tailscale.start",
	// "module.tailscale.subfeature.enable"). The detail is a
	// free-form JSON or key=value string (never includes secrets).
	AuditLog func(action, detail string)
}

// ModuleStatus is the runtime state of a module. Reported by
// Status() and persisted to state.json.
type ModuleStatus struct {
	// State is one of the State* constants above.
	State string

	// InstalledAt is when the module's binaries were first
	// installed. Zero if StateNotInstalled.
	InstalledAt time.Time

	// StartedAt is when Start() most recently succeeded. Zero
	// if the module has never been started.
	StartedAt time.Time

	// LastError is the error from the most recent failed
	// Init/Start/Health call. Empty on success.
	LastError string

	// Info holds module-specific status fields (e.g. for Tailscale:
	// "tailscale_ip": "100.64.0.22", "login_server": "..."). The
	// keys are stable identifiers; the values are display strings.
	// Never put secrets in Info — it ends up in the admin page HTML.
	Info map[string]string
}

// HealthStatus is a structured health report. Returned by Health()
// and persisted to state.json.
type HealthStatus struct {
	// Healthy is true when all Checks pass. The Manager uses
	// this as the primary signal for state transitions
	// (Running -> Error if Healthy becomes false).
	Healthy bool

	// LastCheck is when this health report was generated.
	LastCheck time.Time

	// Checks is a map of named checks to their pass/fail status.
	// Example keys: "auth_ok", "interface_up", "peers_visible",
	// "license_valid". Modules are free to add their own keys;
	// the admin page renders them as a checklist.
	Checks map[string]bool

	// LastError is the error message from the first failing
	// check (or "" if all pass). Display-only.
	LastError string
}

// SubFeature describes an opt-in sub-feature a module can enable.
// Reported by Module.SubFeatures() and used by the Manager to
// render the /admin/modules/{name} sub-feature toggles.
type SubFeature struct {
	// Name is a stable identifier (lowercase, no spaces).
	// Used as the sub-feature flag in state.json
	// ("sub_features": {<name>: true}).
	Name string

	// Description is shown on the admin page as a tooltip.
	Description string

	// Enabled is the current state (read from state.json on
	// load, updated when EnableSubFeature/DisableSubFeature
	// is called).
	Enabled bool

	// Requires lists other sub-feature names that MUST be
	// enabled before this one. The Manager validates this
	// when EnableSubFeature is called and returns
	// ErrSubFeatureRequires if the prereqs are not met.
	Requires []string

	// Impact describes what enabling this sub-feature costs.
	// Examples: "no extra cost", "OS-level install required",
	// "in-container only", "adds subnet route advertisement".
	// Display-only.
	Impact string
}

// Installer is the optional Module interface for the
// install path. Modules that have an install step
// (e.g. Tailscale — apt install + systemctl + tailscale up)
// implement this so the /admin/modules UI can show an
// "Install" button that runs the install.
//
// Modules that don't have an install step (e.g. modules
// that assume the daemon is already running on the host
// — B209.1 attach-mode Tailscale) simply don't implement
// Installer. The /admin/modules UI hides the Install
// button in that case.
//
// Install is a separate method from the Module interface
// (B-mod-admin, 2026-09-10) because install is *out-of-band*
// from the Manager's lifecycle — the Manager doesn't
// auto-install on first Start; the operator explicitly
// chooses the install mode (os_level / in_container /
// attach) on the /admin/modules/{name}/install form.
type Installer interface {
	// Install runs the install path for the chosen mode
	// (configured via SKYGATE_<NAME>_INSTALL_MODE env var
	// or the install form's mode field). Idempotent —
	// second call skips the heavy steps if the daemon is
	// already present.
	//
	// Returns nil on success. On error, returns a wrapped
	// error describing the failing step (apt install failed,
	// docker run failed, tailscale up non-zero, etc.).
	Install(ctx context.Context) error
}

// compile-time guard: Module + Installer must both be
// satisfied for modules that ship with an install path.
// tailscale.Module satisfies both — see
// internal/module/tailscale/tailscale.go.
var (
	_ Module    = (Module)(nil)
	_ Installer = (Installer)(nil)
)
