// Package exit_rules is the feature module for the /my/exit-rules
// page (per-user rules), /admin/exit-rules (cross-user view),
// /admin/exit-rules/{rollback,reapply,cleanup,sync,nodes} admin
// actions, the public REST API (/my/exit-rules/api), the
// advertised-routes sync, the DNS autoupdater, and the CDN
// detection logic used by the autoupdater.
//
// refactor-v0.30 Phase B step 4 (2026-07-29): the package
// is the new home for the 14 files that used to live in
// internal/handlers/exit_rules*.go. The 14 source files
// were decomposed out of a 1100-line god-object and live
// here in feature-sized slices: store.go (DB helpers),
// service.go (Service struct + Backend interface), cdn.go
// (pure CDN detection), sync.go (advertised-routes sync +
// autoupdater), routescript*.go (per-OS script generators),
// cleanup.go (admin cleanup / merge duplicates), api.go
// (REST endpoints), form_*.go (HTML form handlers).
//
// The package replaces the legacy App methods that lived in
// exit_rules.go. Most App methods now live on *Service;
// SyncAdvertisedRoutes / RunDomainAutoUpdater stay on
// *App (called from main.go as goroutines + as admin
// HTTP handlers) and are passed to the Service as the
// SyncRoutes callback.
package exit_rules

import (
	"database/sql"
	"net/http"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// Backend is the minimum surface the exit_rules handlers need
// from the host application. *App satisfies it via the
// capital-letter wrappers in internal/handlers/handlers_export.go
// (Render, RenderWithLayout, CurrentUser, Audit). Same
// pattern as feature/auth + feature/admin — the interface
// keeps the feature package decoupled from internal/handlers
// while still letting handlers call into it.
type Backend interface {
	Render(w http.ResponseWriter, r *http.Request, name string, data any)
	RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any)
	CurrentUser(r *http.Request) *auth.Claims
	Audit(userID int64, username, action, detail string)
}

// Service is the exit_rules feature module. One Service is
// created at boot by cmd/skygate/main.go and registered as
// the handler for the user-facing exit rules routes
// (/my/exit-rules, /my/exit-rules/api, /my/exit-rules/help)
// + the admin exit-rules routes (/admin/exit-rules and
// its sub-actions).
//
// All fields are read-only after construction.
//
// Field semantics:
//   - Backend: satisfies the Backend interface (typically *App).
//   - DB: the open *sql.DB.
//   - HS: the global headscale client (per the v0.12.0 model,
//     per-user plane override is in *App and not used here —
//     the global plane is the source of truth for exit rules).
//   - Cfg: config flags (MaxRulesPerDevice, MaxTotalRules,
//     StaggerSync, StaggerInterval, ACLWithViaEnabled, etc.).
//   - I18n: i18n catalog (the templates and form pages
//     render translated strings via this).
//   - Notifier: for the operator-channel alerts on
//     rule add / delete / apply failure / rollback /
//     reapply (matches the legacy *App behaviour).
//   - SyncRoutes: callback to *App.SyncAdvertisedRoutes.
//     The Service calls this from PostMyExitRule /
//     PostDeleteExitRule / DomainAutoUpdater to push
//     the freshly-changed advertised-routes to the
//     exit nodes. Modeled as a function (not a *App
//     method) so the Service stays decoupled from
//     exit_rules_sync.go (which still owns the
//     implementation on *App — needed for the
//     /admin/exit-nodes/sync button).
type Service struct {
	Backend    Backend
	DB         *sql.DB
	HS         *headscale.Client
	Cfg        *config.Config
	I18n       *i18n.Catalog
	Notifier   telegram.Notifier
	SyncRoutes func() map[string]string
}
