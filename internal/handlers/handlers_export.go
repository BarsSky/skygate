package handlers

// handlers_export.go — capital-letter wrappers for selected *App
// methods that need to be reachable from other packages (e.g.
// the feature/ packages that the refactor-v0.30 plan moves them
// out of). The underlying methods are unexported by design
// (they're internal to the handlers package), but the wrappers
// re-export them through the same public signature so external
// packages can satisfy an interface like feature/auth.Backend.
//
// Wrappers (intentionally minimal — pure delegation):
//
//   Render            (w, r, name, data) — render a template
//                       without the layout chrome
//   RenderWithLayout  (w, r, name, *auth.Claims, data) — render
//                       with the user nav / sidebar layout
//   CurrentUser       (r) *auth.Claims — JWT cookie → claims
//   Audit             (userID, username, action, detail) —
//                       write one row to audit_log
//   Config            () *config.Config — read-only view of
//                       the *App's private Cfg field (used by
//                       handlers that need config flags like
//                       AutoAllocateSubnetOnUserCreate)
//
// Why we don't just rename the lowercase methods to capital:
// 70+ handlers/*.go files call them as a.render, a.audit, etc.
// Renaming would be a 70-file diff for no behavior change. The
// wrappers add a few trivial lines in exchange for a focused
// refactor surface.

import (
	"database/sql"
	"net/http"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/headscale"
	"skygate/internal/nodeownership"
)

// Render — public wrapper around unexported a.render. Lets
// external packages (e.g. internal/feature/auth) define a
// Backend interface and have *App satisfy it.
func (a *App) Render(w http.ResponseWriter, r *http.Request, name string, data any) {
	a.render(w, r, name, data)
}

// RenderWithLayout — public wrapper around a.renderWithLayout.
func (a *App) RenderWithLayout(w http.ResponseWriter, r *http.Request, name string, c *auth.Claims, data map[string]any) {
	a.renderWithLayout(w, r, name, c, data)
}

// CurrentUser — public wrapper around a.currentUser.
func (a *App) CurrentUser(r *http.Request) *auth.Claims {
	return a.currentUser(r)
}

// Audit — public wrapper around a.audit.
func (a *App) Audit(userID int64, username, action, detail string) {
	a.audit(userID, username, action, detail)
}

// InfraAuditIdentity — v0.33.1.41 — Issue 4 infra user.
// Returns (id, username) of the 'infra' portal user so
// the caller can record the action in audit_log on behalf
// of infrastructure (not the admin who clicked). Falls
// back to the supplied (fallbackUID, fallbackUsername)
// if the infra row is missing or hasn't been linked to
// a headscale user yet (headscale_user_id IS NULL or
// = 0 — the latter covers test schemas where the column
// is NOT NULL DEFAULT 0).
//
// Implementation: a single SQL lookup against portal_users
// keyed on username='infra'. Caching is left to the caller
// (the handler should call once at the top of the request
// and re-use the result; the per-request cost is one
// indexed query, not a perf concern).
func (a *App) InfraAuditIdentity(fallbackUID int64, fallbackUsername string) (int64, string) {
	if a.DB == nil {
		return fallbackUID, fallbackUsername
	}
	var id sql.NullInt64
	var hsID sql.NullInt64
	var uname string
	if err := a.DB.QueryRow(
		`SELECT id, username, headscale_user_id FROM portal_users WHERE username = 'infra'`,
	).Scan(&id, &uname, &hsID); err != nil || !id.Valid || !hsID.Valid || hsID.Int64 == 0 {
		return fallbackUID, fallbackUsername
	}
	return id.Int64, uname
}

// Config — public accessor for the private Cfg field. Used
// by handlers that need config flags (e.g. the admin/users
// handler reads AutoAllocateSubnetOnUserCreate to decide
// whether to auto-allocate a subnet on user create).
func (a *App) Config() *config.Config {
	return a.Cfg
}

// HSGlobalFn — routes the per-request "which headscale
// client should I use" lookup directly to a.Router.Global().
// Was a 3-hop chain before Phase D3 (a.HSGlobalFn →
// a.HSGlobal → a.Router.Global); collapsed to 1 hop in
// Phase D4 (2026-07-29). The "Fn" suffix is what lets
// feature/* packages hold this as a Service field — a
// method named HSGlobalFn returning *headscale.Client
// and called like `a.HSGlobalFn()` satisfies a Backend
// interface that declares `HSGlobalFn() *headscale.Client`.
func (a *App) HSGlobalFn() *headscale.Client {
	return a.Router.Global()
}

// HSForUserFn — same routing pattern as HSGlobalFn, but
// for the per-user override. Routes directly to
// a.Router.ForUser(userID). feature/my holds the per-user
// control plane routing via this callback so the per-
// handler self-service code doesn't need a method on *App.
func (a *App) HSForUserFn(userID int64) *headscale.Client {
	return a.Router.ForUser(userID)
}

// BackfillNodeOwnershipFn — public wrapper around
// nodeownership.Backfill so feature/my can call the
// helper as a callback (instead of carrying a copy of
// the 250-line function in feature/my/devices.go).
//
// refactor-v0.30 Phase B step 5b (2026-07-29): the
// helper was previously a method on *App, so feature/my
// could only reach it via this indirection.
//
// refactor-v0.30 Phase D2 (2026-07-29): the function
// moved to its own package (internal/nodeownership/).
// This wrapper is now a 1-line delegate; the indirection
// stays so feature/my's Service struct doesn't need to
// know about nodeownership directly. Future cleanup
// (Phase D4) can collapse this to a direct call.
func (a *App) BackfillNodeOwnershipFn(d *sql.DB, nodes []headscale.NodeView, userID int64, username string) {
	nodeownership.Backfill(d, a.HS, nodes, userID, username)
}
