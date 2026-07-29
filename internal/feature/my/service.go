// Package my is the feature module for /my/* user-facing
// pages. The /my/telegram, /my/account, and /my/tokens
// routes moved to feature/auth in step 2; this package
// owns the remaining /my/* pages (devices, keys, preauth,
// exit-nodes, audit, account audit export) + the
// per-device preferred exit-node self-service endpoint.
//
// refactor-v0.30 Phase B step 5 (2026-07-29): the
// package is the new home for the 7 files that lived in
// internal/handlers/handlers_my_*.go. Decomposition:
//   - service.go        — Service struct + Backend interface
//   - preauth.go        — POST /my/preauth
//   - exit_nodes.go     — GET /my/exit-nodes + preferred-set
//   - keys.go           — GET /my/keys + expire
//   - audit.go          — GET /my/account/audit (CSV/JSON
//                          export, uses SanitizeFilename)
//   - device_exit_pref.go — POST /my/devices/preferred-exit
//                            + admin override (admin half
//                            moves to feature/admin in a
//                            later step; left here in the
//                            interim to keep the file
//                            self-contained)
//   - devices.go        — GET /my/devices + backfill (step 5b)
//   - meshes.go         — /my/meshes CRUD (step 5c)
//
// The Service holds the shared headscale client + DB +
// cfg as plain fields; the Backend interface gives it
// access to the shared App-only methods (Render /
// RenderWithLayout / CurrentUser / Audit / HSForUser /
// HSGlobal). HSGlobal is needed by PostMyDevicePreferredExit
// when the per-plane resolve falls back to the global
// client; HSForUser is needed by every handler that
// operates on the caller's own headscale (per the
// v0.12.0+ per-user control plane model).
package my

import (
	"database/sql"
	"net/http"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// Backend is the minimum surface the /my/* handlers need
// from the host application. *App satisfies it via the
// capital-letter wrappers in internal/handlers/handlers_export.go
// (Render, RenderWithLayout, CurrentUser, Audit). Same
// pattern as feature/auth + feature/admin + feature/
// exit_rules — the interface keeps the feature package
// decoupled from internal/handlers.
//
// HSGlobalFn / HSForUserFn are modeled as functions
// (not methods) for the same reason feature/admin uses
// that pattern: the wrappers live in handlers_export.go
// and would conflict with the existing
// (a *App).HSGlobal() / HSForUser() method names if
// they tried to expose the same methods.
type Backend interface {
	Render(w http.ResponseWriter, r *http.Request, name string, data any)
	RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any)
	CurrentUser(r *http.Request) *auth.Claims
	Audit(userID int64, username, action, detail string)
	HSGlobalFn() *headscale.Client
	HSForUserFn(userID int64) *headscale.Client
}

// Service is the /my/* feature module. One Service is
// created at boot by cmd/skygate/main.go and registered
// as the handler for the user-facing /my/* routes.
//
// All fields are read-only after construction.
//
// Field semantics:
//   - Backend: satisfies the Backend interface (typically *App).
//   - DB: the open *sql.DB.
//   - HS: the global headscale client (used as a fallback
//     for handlers that don't have a per-user plane).
//     The per-user-plane routing goes through
//     Backend.HSForUser(c.UserID) which the per-plane
//     ACL pipeline + the /my/preauth + /my/exit-nodes
//     handlers use.
//   - Cfg: config flags (ACLWithViaEnabled is the main
//     one — strict-pinning opt-in toggle for via-constraint
//     ACL grants).
//   - I18n: i18n catalog (the templates and form pages
//     render translated strings via this).
//   - Notifier: for the operator-channel alerts on
//     admin / user actions (the /my/* surface emits
//     few of these — mostly the device-pref handlers).
//   - BackfillNodeOwnership: callback to the legacy
//     App.backfillNodeOwnership helper. The full
//     implementation lives in
//     internal/handlers/handlers_node_ownership.go
//     (~250 lines; would be a 1:1 copy if inlined in
//     feature/my/devices.go). Modeled as a callback
//     instead of duplicated. Set by main.go at boot.
type Service struct {
	Backend                Backend
	DB                     *sql.DB
	HS                     *headscale.Client
	Cfg                    *config.Config
	I18n                   *i18n.Catalog
	Notifier                telegram.Notifier
	BackfillNodeOwnership  func(d *sql.DB, nodes []headscale.NodeView, userID int64, username string)
}
