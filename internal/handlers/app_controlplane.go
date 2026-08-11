// 2026-07-29: refactor-v0.30 Phase D3 — the per-user
// control plane routing moved to internal/controlplane/.
//
// Phase D4 (2026-07-29): the HSForUser + HSGlobal
// thin-delegate methods on *App were deleted (their
// only callers were the HSForUserFn / HSGlobalFn
// wrappers in handlers_export.go, which now route
// directly to a.Router). The PlaneURLForUser +
// InvalidateHSCache methods stay because they are
// passed as bound method values from cmd/skygate/main.go
// (to the telegram notifier + admin Service
// respectively); collapsing them would require changing
// how main.go wires the per-user control plane state,
// which is out of scope for D4.

package handlers

// PlaneURLForUser — thin delegate to
// Router.PlaneURLForUser. The bot path uses this to
// scope acl.GenerateACLForPlane to the right identities.
// cmd/skygate/main.go passes this as a bound method
// value to telegram.NewRealNotifier.SetPlaneURLForUser.
func (a *App) PlaneURLForUser(userID int64) string {
	return a.Router.PlaneURLForUser(userID)
}

// InvalidateHSCache — thin delegate to
// Router.InvalidateCache. /admin/users and
// /admin/control-planes call this when the admin
// rotates a per-user api_key. cmd/skygate/main.go
// passes this as a bound method value to the admin
// Service's InvalidateHSCacheFn field.
func (a *App) InvalidateHSCache(url string) {
	a.Router.InvalidateCache(url)
}
