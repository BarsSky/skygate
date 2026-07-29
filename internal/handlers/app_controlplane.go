// 2026-07-29: refactor-v0.30 Phase D3 — the per-user
// control plane routing moved to internal/controlplane/.
//
// This file is now a thin layer of *App methods that
// delegate to the *controlplane.Router field. The
// indirection exists so the public *App.HSForUser /
// HSGlobal / PlaneURLForUser / InvalidateHSCache /
// InitHSForUserState signatures don't change (the
// handlers_export.go wrappers, the test file
// app_controlplane_test.go, and any other caller that
// used a.HSForUser(uid) keeps working). Phase D4 (the
// final cleanup) can collapse the indirection once all
// callers route through HSForUserFn / HSGlobalFn on the
// Service struct (the feature/* packages already do —
// the only remaining direct a.HSForUser callers are
// here in handlers/ + the control planes page which
// uses the Router directly).

package handlers

import (
	"skygate/internal/headscale"
)

// HSForUser — thin delegate to Router.ForUser. The
// per-user control plane routing is now in
// internal/controlplane/router.go (Phase D3). The
// signature is unchanged so existing callers
// (handlers_export.go's HSForUserFn, the test file,
// and the few in-package callers) keep working.
func (a *App) HSForUser(userID int64) *headscale.Client {
	return a.Router.ForUser(userID)
}

// HSGlobal — thin delegate to Router.Global. See
// HSForUser above for the migration story.
func (a *App) HSGlobal() *headscale.Client {
	return a.Router.Global()
}

// PlaneURLForUser — thin delegate to
// Router.PlaneURLForUser. The bot path uses this to
// scope acl.GenerateACLForPlane to the right identities.
func (a *App) PlaneURLForUser(userID int64) string {
	return a.Router.PlaneURLForUser(userID)
}

// InvalidateHSCache — thin delegate to
// Router.InvalidateCache. /admin/users and
// /admin/control-planes call this when the admin
// rotates a per-user api_key.
func (a *App) InvalidateHSCache(url string) {
	a.Router.InvalidateCache(url)
}

// InitHSForUserState — kept for backward compatibility
// with the App constructor (cmd/skygate/main.go used
// to call this after New). Now a no-op because the
// Router is constructed + Init'd in New(). The method
// stays so older callers don't break.
func (a *App) InitHSForUserState() {
	// The Router is already Init'd in New(). This
	// method is now a no-op kept for backward
	// compatibility with the App init pattern.
	// (Phase D3, 2026-07-29.)
}
