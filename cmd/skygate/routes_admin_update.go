// routes_admin_update.go — B528 step 2 (2026-11-27): the /admin/update route group.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. The group moves here 11 routes VERBATIM, and the parameter list is exactly
// what the moved lines use — the compiler derived it (mux, authMW, adminSvc), so the seam
// costs nothing at the call site.
package main

import (
	"net/http"

	adminsvc "skygate/internal/feature/admin"
)

// registerAdminUpdateRoutes registers the /admin/update endpoints.
func registerAdminUpdateRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, adminSvc *adminsvc.Service) {
	mux.Handle("GET /admin/update", authMW(http.HandlerFunc(adminSvc.GetAdminUpdate)))
	mux.Handle("POST /admin/update/check-now", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateCheck)))
	// 2026-07-27: v0.29.0 — auto-update apply/rollback/dismiss.
	// The "Apply" button kicks off a background updater
	// goroutine that runs git pull + docker compose build +
	// container recreate + healthz poll, with automatic
	// rollback on any failure. "Rollback now" is the
	// operator-initiated escape hatch. "Dismiss" clears
	// the persisted status file when the operator has
	// read the success/failure banner.
	mux.Handle("POST /admin/update/apply", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateApply)))
	mux.Handle("POST /admin/update/rollback", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateRollback)))
	mux.Handle("POST /admin/update/dismiss", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateDismiss)))
	// 2026-07-30: v0.32.3 — manual "Push update" trigger
	// that works regardless of SKYGATE_AUTO_UPDATE_ENABLED.
	// For when the operator wants to force a rebuild +
	// restart right now, without waiting for a newer
	// release to be detected.
	mux.Handle("POST /admin/update/push", authMW(http.HandlerFunc(adminSvc.PostAdminUpdatePush)))
	// B249 (v1.5.4): the image-pull update path — fast (~5-30s)
	// alternative to the git+build path above. Same UI page,
	// separate button. Requires docker-compose.ghcr.yml.
	mux.Handle("POST /admin/update/pull-image", authMW(http.HandlerFunc(adminSvc.PostAdminUpdatePullImage)))
	// 2026-08-03: v0.32.20 — UI toggle for auto-update. The
	// operator flips the auto-update mode on /admin/update
	// without editing .env or restarting skygate. Persisted in
	// global_settings (key='auto_update_enabled'). The
	// orchestrator below reads this same DB value at every
	// tick (5s), so the change takes effect without a restart.
	mux.Handle("POST /admin/update/auto-toggle", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateAutoToggle)))
	// 2026-08-18 (B129): the new "Schedule" section on
	// /admin/update. Replaces the pre-B129 auto-toggle form
	// for the schedule-enabled + schedule-time fields. The
	// /admin/update/auto-toggle route is kept for back-compat
	// (the form's hidden field) but writes to the B129+
	// key (see PostAdminUpdateAutoToggle).
	mux.Handle("POST /admin/update/schedule", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateSchedule)))
	// B491 (TD-39): the release-check endpoint is the operator's choice (proxy / mirror / static JSON).
	mux.Handle("POST /admin/update/check-url", authMW(http.HandlerFunc(adminSvc.PostAdminUpdateCheckEndpoint)))
	// B346 (2026-10-04): pin the release every instance orients on.
	// Writes global_settings["update.pinned_release"] — read by this
	// page's target, by the B130 scheduled updater and by the B342
	// cluster-onboarding install command. Empty input clears the pin.
	mux.Handle("POST /admin/update/pin", authMW(http.HandlerFunc(adminSvc.PostAdminUpdatePin)))
}
