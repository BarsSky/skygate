// 2026-08-03: v0.32.20 — UI-controlled auto-update toggle.
//
// The /admin/update page gets a checkbox that lets the
// administrator turn auto-update on or off without editing
// .env or restarting skygate. The state is persisted in
// global_settings (key='auto_update_enabled') and read on
// every render of /admin/update. The env var
// SKYGATE_AUTO_UPDATE_ENABLED is the DEFAULT at first start
// (when the global_settings row doesn't exist yet); the UI
// takes over after the first toggle.
//
// The autoupdate orchestrator (cmd/skygate/main.go) reads
// this same DB value at every tick, so the toggle takes
// effect on the next tick (5s typical) without a restart.
package admin

import (
	"net/http"

	"skygate/internal/db"
)

// globalSettingsKeyAutoUpdate is the key in global_settings.
const globalSettingsKeyAutoUpdate = "auto_update_enabled"

// PostAdminUpdateAutoToggle is the handler for
// POST /admin/update/auto-toggle. The form posts an
// "enabled" field ("1" or "0"); the handler writes the new
// value to global_settings and redirects back to /admin/update.
//
// Admin-only.
func (s *Service) PostAdminUpdateAutoToggle(w http.ResponseWriter, r *http.Request) {
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

	if err := db.SetGlobalSettingBool(s.DB, globalSettingsKeyAutoUpdate, enabled); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "auto_update_toggle", "FAILED: "+err.Error())
		http.Error(w, "could not persist: "+err.Error(), http.StatusInternalServerError)
		return
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.Backend.Audit(c.UserID, c.Username, "auto_update_toggle", "auto-update "+state)

	http.Redirect(w, r, "/admin/update?auto_toggled="+state, http.StatusSeeOther)
}
