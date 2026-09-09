// Package tailscale — Module #1 (B-mod-tailscale, 2026-09-09).
//
// stub.go is a temporary placeholder for the Tailscale module,
// used to verify the B-mod-core Manager + interface end-to-end
// before the real B-mod-tailscale implementation lands. The
// stub:
//   - Registers as module "tailscale"
//   - Returns StateNotInstalled from Status (no actual install)
//   - Returns Healthy=true from Health (smoke-test friendly)
//   - Lists the 4 sub-features (cluster/telegram/derp/exit) for
//     the future admin UI
//   - Logs every Init/Start/Stop/EnableSubFeature/DisableSubFeature
//     call so the audit log proves the Manager wiring works
//
// This file will be REPLACED (not extended) by the real
// TailscaleModule in B-mod-tailscale. The replacement adds:
//   - Real install modes (in_container / os_level / none)
//   - Real Start that spawns tailscaled (in-container) or
//     runs `sudo tailscale up` (OS-level)
//   - Real Health that calls `tailscale status --json`
//   - Real EnableSubFeature that calls `tailscale set
//     --advertise-routes=` for the exit-node sub-feature
//
// See docs/internal/architecture-modules.md §9 for the full
// design and the B-mod-tailscale roadmap entry in AGENTS.md.
package tailscale

import (
	"context"
	"log"
	"time"

	"skygate/internal/module"
)

// ModuleName is the stable identifier for the Tailscale module.
// Used as the directory name for state persistence
// (/var/lib/skygate/modules/tailscale/) and the URL slug for
// /admin/modules/tailscale.
const ModuleName = "tailscale"

// StubModule is a no-op implementation of the Module interface.
// It exists to verify that the Manager + interface + state
// pipeline works end-to-end (B-mod-core smoke test on the live
// VM). It does NOT install, start, or stop Tailscale — that's
// the B-mod-tailscale work.
//
// The StubModule is safe for concurrent use (no mutable state).
type StubModule struct {
	// installMode is the detected install mode (set by
	// the deploy-time detection in install-common.sh +
	// passed via cfg.Env or the SKYGATE_TS_INSTALL_MODE
	// env var). Stored on the struct so Status() can
	// surface it on /admin/modules/tailscale.
	installMode string
}

// NewStub returns a fresh StubModule. The module is registered
// under the name "tailscale" (ModuleName). Callers should NOT
// cache the result across registrations — the Manager expects
// one instance per Register() call.
func NewStub() *StubModule {
	return &StubModule{
		installMode: "none", // default; overridden by Init() if env says otherwise
	}
}

// Name implements module.Module.
func (s *StubModule) Name() string { return ModuleName }

// Init implements module.Module. Stub mode: validates the env
// (just reads SKYGATE_TS_INSTALL_MODE for Status reporting),
// creates the per-module state directory, and persists a
// fresh State. Does NOT actually install Tailscale.
func (s *StubModule) Init(_ context.Context, cfg module.ModuleConfig) error {
	if mode, ok := cfg.Env["SKYGATE_TS_INSTALL_MODE"]; ok && mode != "" {
		s.installMode = mode
	}
	log.Printf("tailscale (stub): Init called, install_mode=%s, data_dir=%s",
		s.installMode, cfg.DataDir)
	return nil
}

// Start implements module.Module. Stub mode: no-op. The real
// B-mod-tailscale will spawn tailscaled (in-container) or run
// `sudo tailscale up` (OS-level) here.
func (s *StubModule) Start(_ context.Context) error {
	log.Printf("tailscale (stub): Start called (no-op)")
	return nil
}

// Stop implements module.Module. Stub mode: no-op.
func (s *StubModule) Stop(_ context.Context) error {
	log.Printf("tailscale (stub): Stop called (no-op)")
	return nil
}

// Status implements module.Module. Stub mode: reports
// StateNotInstalled so the admin UI clearly shows "this
// module is not yet installed — click Install to enable it".
func (s *StubModule) Status() module.ModuleStatus {
	return module.ModuleStatus{
		State:     module.StateNotInstalled,
		StartedAt: time.Time{},
		Info: map[string]string{
			"install_mode": s.installMode,
			"stub":         "true",
		},
	}
}

// Health implements module.Module. Stub mode: always Healthy
// (the stub is trivially healthy — it does nothing). The
// real B-mod-tailscale will return Healthy only when
// `tailscale status --json` shows BackendState=Running AND
// the per-sub-feature health checks pass.
func (s *StubModule) Health() module.HealthStatus {
	return module.HealthStatus{
		Healthy:   true,
		LastCheck: time.Now(),
		Checks: map[string]bool{
			"stub_ok": true,
		},
		LastError: "",
	}
}

// SubFeatures implements module.Module. Returns the 4
// sub-features that the real B-mod-tailscale will support.
// All reported as Enabled=false in stub mode (the admin UI
// shows them as "available, click to enable").
func (s *StubModule) SubFeatures() []module.SubFeature {
	return []module.SubFeature{
		{
			Name:        "cluster",
			Description: "HA mesh between skygate primary and standby over Tailscale (used for etcd peer + Patroni replica)",
			Enabled:     false,
			Requires:    nil,
			Impact:      "in-container sidecar on primary, OS-level install required on standby",
		},
		{
			Name:        "telegram",
			Description: "Telegram API relay over Tailscale (works around NAT/firewall blocks of api.telegram.org)",
			Enabled:     false,
			Requires:    nil,
			Impact:      "in-container sidecar, no extra OS access required",
		},
		{
			Name:        "derp",
			Description: "Local DERP relay server (helps Tailscale clients in restricted networks find peers)",
			Enabled:     false,
			Requires:    nil,
			Impact:      "separate derper container, distinct from tailscale client",
		},
		{
			Name:        "exit",
			Description: "Tailscale exit-node: this skygate instance advertises a subnet route for use as a VPN gateway",
			Enabled:     false,
			Requires:    []string{"cluster"},
			Impact:      "OS-level install required (needs IP forwarding on the host)",
		},
	}
}

// EnableSubFeature implements module.Module. Stub mode: no-op
// (records the call so the audit log proves the wiring works).
// The real B-mod-tailscale will run `tailscale set
// --advertise-routes=...` for the "exit" sub-feature and
// `tailscale up --accept-routes` for "cluster".
func (s *StubModule) EnableSubFeature(_ context.Context, name string) error {
	log.Printf("tailscale (stub): EnableSubFeature(%s) called (no-op)", name)
	return nil
}

// DisableSubFeature implements module.Module. Stub mode: no-op.
func (s *StubModule) DisableSubFeature(_ context.Context, name string) error {
	log.Printf("tailscale (stub): DisableSubFeature(%s) called (no-op)", name)
	return nil
}
