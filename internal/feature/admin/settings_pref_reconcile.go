// 2026-09-03: v1.5.2 (B231) — UI-controlled preferred-exit
// auto-reconciler toggle.
//
// The /admin/system_tests page exposes a checkbox that
// lets the administrator turn the B229/B231
// preferred-exit auto-reconciler on or off without
// editing .env or restarting skygate. The state is
// persisted in global_settings (key =
// 'preferred_reconcile_enabled') and read on every
// tick by the reconciler goroutine (see
// internal/handlers/handlers.go RunPreferredExitReconciler).
//
// Read order (mirrors dns_autoupdate_enabled in
// settings_dns_autoupdate.go):
//   1. SKYGATE_PREFERRED_RECONCILE_ENABLED env var
//      (default at first start, when the
//      global_settings row doesn't exist yet). Read
//      at the start of every tick so the operator can
//      also flip via env without UI.
//   2. global_settings row (after the first UI toggle).
//
// Two safety belts stay env-only (NOT exposed in UI):
//   - SKYGATE_PREFERRED_RECONCILER_LIVE: live-mode vs
//     dry-run. A UI button that toggles "live" would be
//     too dangerous (one click = writes to the
//     DB). Operators flip the env, redeploy, and
//     watch the dry-run log for a few ticks first.
//   - SKYGATE_PREFERRED_RECONCILE_INTERVAL: tick
//     cadence. Low-frequency tuning knob; env is fine.
//
// Audit log: every toggle writes a
// "preferred_reconcile_toggle" row with detail
// "preferred-reconcile enabled" or "preferred-reconcile
// disabled". The 1-shot Telegram alert on disable
// (B229 initial summary + B231 transition handling)
// fires from inside RunPreferredExitReconciler so the
// alert cadence stays in the goroutine (avoids
// per-handler SendAlert calls that would double-fire
// if the operator mashes the toggle button).
package admin

import (
	"net/http"

	"skygate/internal/db"
)

// globalSettingsKeyPrefReconcile is the DB key for the
// preferred-exit reconciler on/off toggle.
//
// 2026-09-03: v1.5.2 (B231).
const globalSettingsKeyPrefReconcile = "preferred_reconcile_enabled"

// PostAdminSystemTestsPrefReconcileToggle is the handler
// for POST /admin/system_tests/preferred-reconcile-toggle.
// The form posts an "enabled" field ("1" or "0"); the
// handler writes the new value to global_settings and
// redirects back to /admin/system_tests.
//
// Admin-only. The same form is exposed for both the DNS
// autoupdater (settings_dns_autoupdate.go) and the
// preferred-reconcile autoupdater; both follow the same
// "DB-backed toggle with env-var default" pattern.
//
// 2026-09-03: v1.5.2 (B231).
func (s *Service) PostAdminSystemTestsPrefReconcileToggle(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	enabled := r.FormValue("enabled") == "1"

	if err := db.SetGlobalSettingBool(s.dbc(), globalSettingsKeyPrefReconcile, enabled); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "preferred_reconcile_toggle", "FAILED: "+err.Error())
		http.Error(w, "could not persist: "+err.Error(), http.StatusInternalServerError)
		return
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.Backend.Audit(c.UserID, c.Username, "preferred_reconcile_toggle", "preferred-reconcile "+state)

	http.Redirect(w, r, "/admin/system_tests?pref_reconcile_toggled="+state, http.StatusSeeOther)
}
