// 2026-08-03: v0.32.20 — UI-controlled auto-update toggle.
//
// 2026-08-18 (B129): the original "auto-update" toggle is now
// a SCHEDULED auto-update with a time-of-day picker. The
// pre-B129 flag was misleading — the page called it "auto-update"
// but the operator still had to click "Apply" to actually run
// the orchestrator. Post-B129 the schedule lets the operator
// say "auto-update at 03:00 every night" and the background
// scheduler (B130) actually runs the orchestrator at that time.
//
// Two DB keys now drive the schedule (both with env fallbacks
// in Cfg.UpdateScheduleEnabled + Cfg.UpdateScheduleTime):
//   - globalSettingsKeyUpdateScheduleEnabled (bool)
//   - globalSettingsKeyUpdateScheduleTime    (string "HH:MM")
//
// The legacy globalSettingsKeyAutoUpdate is KEPT for back-compat
// (some pre-B129 deployments may have a row); it has no effect
// on the post-B129 flow but is read nowhere.
package admin

import (
	"fmt"
	"net/http"
	"regexp"
	"time"

	"skygate/internal/db"
)

// globalSettingsKeyUpdateScheduleEnabled is the B129+ key for
// the schedule toggle. Read on every render of /admin/update
// AND by the background scheduler in B130.
const globalSettingsKeyUpdateScheduleEnabled = "update_schedule_enabled"

// globalSettingsKeyUpdateScheduleTime is the B129+ key for
// the schedule time-of-day (HH:MM 24-hour). Read by the
// background scheduler to know when to run.
const globalSettingsKeyUpdateScheduleTime = "update_schedule_time"

// hhmmPattern validates "HH:MM" 24-hour time. Anchored — the
// whole input must match. Permits "00:00" through "23:59".
var hhmmPattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// PostAdminUpdateAutoToggle is the handler for
// POST /admin/update/auto-toggle. The form posts an
// "enabled" field ("1" or "0"); the handler writes the new
// value to global_settings and redirects back to /admin/update.
//
// Admin-only. 2026-08-18 (B129): the toggle now writes the
// B129+ schedule-enabled key (not the legacy auto_update_enabled
// key). The form is still called "auto-toggle" for URL
// stability — only the underlying key changed.
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

	if err := db.SetGlobalSettingBool(s.DB, globalSettingsKeyUpdateScheduleEnabled, enabled); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "auto_update_toggle", "FAILED: "+err.Error())
		http.Error(w, "could not persist: "+err.Error(), http.StatusInternalServerError)
		return
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.Backend.Audit(c.UserID, c.Username, "auto_update_toggle", "schedule "+state)

	http.Redirect(w, r, "/admin/update?auto_toggled="+state, http.StatusSeeOther)
}

// PostAdminUpdateSchedule is the B129 handler for the new
// "Schedule" section on /admin/update. The form posts:
//   - enabled  ("1" or "0") — whether scheduled auto-update is on
//   - time     ("HH:MM" 24-hour) — when to run the orchestrator
//
// Both fields are validated. Invalid time → falls back to
// the Cfg.UpdateScheduleTime default and a flash is shown
// (via redirect query). The handler writes to global_settings
// and redirects back to /admin/update.
//
// Admin-only. Wire-up is in main.go (POST /admin/update/schedule).
func (s *Service) PostAdminUpdateSchedule(w http.ResponseWriter, r *http.Request) {
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
	timeStr := r.FormValue("time")

	// Validate HH:MM. Empty / invalid → fall back to the
	// env-default from Cfg.UpdateScheduleTime. The flash
	// query param tells the page to show "saved with
	// fallback" so the operator knows the input was
	// rejected.
	saved := "saved"
	if !hhmmPattern.MatchString(timeStr) {
		timeStr = s.Cfg.UpdateScheduleTime
		if timeStr == "" {
			timeStr = "03:00"
		}
		saved = "fallback"
	}

	// Persist both fields. Two writes (could be a
	// transaction, but SetGlobalSetting is idempotent and
	// the page is admin-only — partial-write risk is OK
	// here, the next toggle will repair it).
	if err := db.SetGlobalSettingBool(s.DB, globalSettingsKeyUpdateScheduleEnabled, enabled); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "update_schedule", "FAILED enabled: "+err.Error())
		http.Error(w, "could not persist enabled: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := db.SetGlobalSetting(s.DB, globalSettingsKeyUpdateScheduleTime, timeStr); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "update_schedule", "FAILED time: "+err.Error())
		http.Error(w, "could not persist time: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.Backend.Audit(c.UserID, c.Username, "update_schedule",
		fmt.Sprintf("enabled=%v time=%s", enabled, timeStr))

	_ = time.Now() // suppress unused import in some build configs
	http.Redirect(w, r, "/admin/update?schedule_saved="+saved, http.StatusSeeOther)
}
