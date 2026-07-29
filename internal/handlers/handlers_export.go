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
	"net/http"

	"skygate/internal/auth"
	"skygate/internal/config"
	"skygate/internal/headscale"
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

// Config — public accessor for the private Cfg field. Used
// by handlers that need config flags (e.g. the admin/users
// handler reads AutoAllocateSubnetOnUserCreate to decide
// whether to auto-allocate a subnet on user create).
func (a *App) Config() *config.Config {
	return a.Cfg
}

// HSGlobalFn — wrapper exposing the private HSGlobal()
// method as a function value, so feature/* packages
// can hold it as a Service field (matching the
// feature/admin pattern). The "Fn" suffix is what
// avoids the name clash with the existing
// HSGlobal() method — a method named HSGlobalFn
// returning *headscale.Client and called like
// `a.HSGlobalFn()` satisfies a Backend interface
// that declares `HSGlobalFn() *headscale.Client`.
// 2026-07-29: refactor-v0.30 Phase B step 5.
func (a *App) HSGlobalFn() *headscale.Client {
	return a.HSGlobal()
}

// HSForUserFn — same trick for HSForUser. feature/my
// holds the per-user control plane routing via this
// callback so the per-handler self-service code
// doesn't need a method on *App.
func (a *App) HSForUserFn(userID int64) *headscale.Client {
	return a.HSForUser(userID)
}
