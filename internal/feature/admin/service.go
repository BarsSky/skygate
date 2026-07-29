// Package admin is the feature module for /admin/* pages (cross-cutting
// admin surface). See doc.go for the full design.
//
// service.go — the Service struct + Backend interface shared by all
// admin handlers in this package.
//
// refactor-v0.30 Phase B step 3 (2026-07-29): the 7 small admin
// handlers files (handlers_admin_users.go, handlers_admin_nodes.go,
// admin_acls_import.go, admin_headscale.go, admin_invites.go,
// admin_meshes.go, admin_subnets.go) moved out of internal/handlers/
// into this package. The 7 larger admin files (admin_telegram,
// admin_integrations*, admin_exit_nodes, admin_control_planes,
// admin_user_subnet, admin_backup) are still in internal/handlers/ —
// they'll be moved in a follow-up step (Phase B step 3b).
package admin

import (
	"database/sql"
	"net/http"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/headscale"
	"skygate/internal/headscale_version"
	"skygate/internal/i18n"
	"skygate/internal/monitoring"
	"skygate/internal/sidecar"
	"skygate/internal/telegram"
)

// Backend is the minimum surface the admin handlers need from the
// host application. *App satisfies it via the capital-letter
// wrappers in internal/handlers/handlers_export.go (Render,
// RenderWithLayout, CurrentUser, Audit). The same interface used
// in internal/feature/auth — duplicated here to keep feature
// packages independent (no cross-feature imports).
type Backend interface {
	Render(w http.ResponseWriter, r *http.Request, name string, data any)
	RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any)
	CurrentUser(r *http.Request) *auth.Claims
	Audit(userID int64, username, action, detail string)
}

// Service is the admin feature module. One Service is created
// at boot by cmd/skygate/main.go and registered as the handler
// for the small admin routes (users, devices, subnets overview,
// invites, meshes, headscale-update-monitor, acls import/export).
//
// All fields are read-only after construction.
//
// Field semantics:
//   - Backend:             satisfies the Backend interface (typically *App).
//   - DB:                  the open *sql.DB.
//   - HSGlobalFn:          returns the global headscale client (for admin
//                          operations on the primary control plane).
//                          Modeled as a function (not a *headscale.Client)
//                          so the Service re-reads on every call — future
//                          v0.12.0+ per-user plane swap doesn't leave
//                          admin operations stuck on a stale client.
//   - HSForUserFn:         returns the headscale client for a specific
//                          user (used by the acls import Apply which
//                          pushes the policy to every distinct plane).
//   - Cfg:                 config flags (AutoAllocateSubnetOnUserCreate
//                          is the main one used here).
//   - Notifier:            for admin actions that need to send a
//                          Telegram alert (password reset, ACL import).
//   - HeadscaleUpdateMonitor: for the /admin/headscale page (snapshot +
//                          CheckNow). Nil-safe (the page handles nil).
//   - Sidecar:             for the /admin/subnets page (LastSync /
//                          LastStats). Nil-safe.
//   - I18n:                 i18n catalog (for translated status pills
//                          on the /admin/headscale page).
//
// Refactor-v0.30 Phase B step 3b.1a (2026-07-29): the
// telegramProbeCache field is state that was previously on
// *App (the Mutex, the at-time, the cached result, the
// token fingerprint that the cache is keyed on). It moved
// to the Service so the cache is owned by the feature
// that uses it. Mutex-guarded (a probe request can run
// concurrently with a save/rotate/disable invalidate).
type Service struct {
	Backend                Backend
	DB                     *sql.DB
	HSGlobalFn             func() *headscale.Client
	HSForUserFn            func(userID int64) *headscale.Client
	Cfg                    *config.Config
	Notifier               telegram.Notifier
	HeadscaleUpdateMonitor *headscale_version.Monitor
	Sidecar                *sidecar.Manager
	I18n                   *i18n.Catalog

	// SecretKeyHex is the SKYGATE_SECRET_KEY hex value used
	// to encrypt per-user control-plane API keys at rest.
	// Phase B step 3b.4 (2026-07-29): added for
	// /admin/users/{id}/plane (post/clear/provision/decommission).
	SecretKeyHex string

	// InvalidateHSCacheFn drops the per-URL cached headscale
	// client (used after the per-user override changes so
	// the next HSForUser call returns a fresh client with
	// the new credentials).
	InvalidateHSCacheFn func(url string)

	// refactor-v0.30 Phase B step 3b.3 (2026-07-29): moved
	// from *App. SSHKeyPath is the default path shown in
	// the /admin/exit-nodes "ssh_key_path" form field
	// (the template renders it as the default). Also used
	// by /admin/backup/config (Phase B step 3b.6) — wired
	// once at boot from app.SSHKeyPath.
	SSHKeyPath string

	// refactor-v0.30 Phase B step 3b.6 (2026-07-29): moved
	// from *App. The /admin/settings page renders the
	// ControlURL field, the JWTSecret (redacted via
	// maskSecret), and the HeadscaleKey (also redacted).
	// Wired once at boot from app.{ControlURL,JWTSecret,
	// HeadscaleKey}.
	ControlURL   string
	JWTSecret    string
	HeadscaleKey string

	// refactor-v0.30 Phase B step 3b.3 (2026-07-29): moved
	// from *App. The "Run health check now" button on
	// /admin/exit-nodes calls ExitNodeMonitor.CheckNow via
	// this field. nil if the monitor is disabled
	// (SKYGATE_EXIT_NODE_CHECK_INTERVAL=off) — handlers
	// guard with `if s.ExitNodeMonitor != nil`.
	ExitNodeMonitor *monitoring.ExitNodeMonitor

	// refactor-v0.30 Phase B step 3b.3 (2026-07-29): moved
	// from *App. The "Sync advertised routes" button on
	// /admin/exit-nodes calls this callback. Modeled as a
	// function (not a method on *App) so the Service
	// stays decoupled from exit_rules_sync.go. The
	// concrete implementation lives on *App
	// (SyncAdvertisedRoutes) and is wired from
	// cmd/skygate/main.go.
	SyncRoutes func() map[string]string

	// refactor-v0.30 Phase B step 6a (2026-07-29): moved
	// from *App. /admin/derp's collectDerpStatus seeds
	// its initial DerpStatus struct with this URL (defaults
	// to http://192.168.13.69:8766 when empty). Wired
	// once at boot from app.DerpBaseURL.
	DerpBaseURL string

	// refactor-v0.30 Phase B step 6b (2026-07-29): moved
	// from *App. /admin/acls links to this URL (when
	// non-empty) instead of the bundled Headplane sidecar.
	// Empty = use the bundled sidecar. Wired once at boot
	// from app.HeadplaneExternalURL.
	HeadplaneExternalURL string

	telegramProbeCache serviceProbeCache
}
