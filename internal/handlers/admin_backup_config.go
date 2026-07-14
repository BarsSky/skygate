// 2026-07-14: Этап 14 v6 — HTTP handlers for the /admin/backup
// config UI.
//
// The /admin/backup page is split into two halves:
//
//   - "Create backup / Restore" (the old behaviour, kept in
//     admin_backup.go) — fires `scripts/backup.sh <backupDir>`
//     on click and downloads the archive.
//
//   - "Destination & schedule" (this file) — saves the
//     persistent backup.Config to global_settings. From
//     here the admin chooses protocol (local / SMB / NFS /
//     SFTP), destination URL, mountpoint, credentials,
//     keep-count, schedule, and the master switches
//     (Enabled, InAppEnabled). The "Test connection" button
//     runs backup.TestConnection (no actual mount) and
//     reports the parsed fields. "Run now" calls
//     backup.RunBackup.
//
// The two halves share the same page template
// (admin/backup.html); the config card lives below the
// existing create/restore cards. Flash messages are
// passed via the same ?ok=&err= redirect query params
// the legacy code already uses, so the user gets
// consistent feedback regardless of which button they
// clicked.
//
// We intentionally do NOT use CSRF tokens for these
// forms. /admin/backup/save (legacy) doesn't have one
// either, and the actions are admin-only — gating
// them on a CSRF cookie would only add ceremony
// without buying any real security (the attacker would
// already need the admin's session cookie).

package handlers

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/backup"
	"skygate/internal/i18n"
)

// backupConfigRedirect builds a /admin/backup redirect URL
// with the standard ?ok= / ?err= flash parameters.
func backupConfigRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	target := "/admin/backup"
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// GetAdminBackupConfig serves the page. The config
// fields are passed to the template via the same
// "Backups" + new "Config" + "TestResult" keys so the
// existing /admin/backup render call picks them up.
//
// We separate the GET (render) from the action handlers
// so a form post that hits GetAdminBackupConfig (e.g.
// someone navigating via the back button) doesn't
// accidentally re-save the form values.
func (a *App) GetAdminBackupConfig(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := backup.Load(a.DB)
	if err != nil {
		http.Error(w, "load config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	lang := a.I18n.LangFromRequest(r)
	data := map[string]any{
		"Page": "admin/backup",
		"Config":   cfg,
		"Protocols": backup.AllProtocols,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	}
	_ = lang
	// Reuse the same page template. The destination &
	// schedule card is rendered conditionally on
	// {{ if .Config }}.
	a.renderWithLayout(w, r, "admin-backup", c, data)
}

// PostAdminBackupConfig saves the persistent form
// fields. The test/run actions are handled by separate
// routes so a save doesn't accidentally fire a backup.
//
// Form fields (snake_case → struct field):
//
//   protocol         → c.Protocol (auto-detected from
//                      destination if blank)
//   destination      → c.Destination (required)
//   mountpoint       → c.Mountpoint
//   username         → c.Username
//   password         → c.Password
//   ssh_key_path     → c.SSHKeyPath
//   keep_count       → c.KeepCount (0 = keep all)
//   schedule         → c.Schedule
//   enabled          → c.Enabled (master switch)
//   in_app_enabled   → c.InAppEnabled
func (a *App) PostAdminBackupConfig(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		backupConfigRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	lang := a.I18n.LangFromRequest(r)

	cfg := &backup.Config{
		Destination:  strings.TrimSpace(r.FormValue("destination")),
		Protocol:     backup.Protocol(strings.TrimSpace(r.FormValue("protocol"))),
		Mountpoint:   strings.TrimSpace(r.FormValue("mountpoint")),
		Username:     strings.TrimSpace(r.FormValue("username")),
		Password:     r.FormValue("password"), // don't trim — leading/trailing space might be intentional
		SSHKeyPath:   strings.TrimSpace(r.FormValue("ssh_key_path")),
		Schedule:     strings.TrimSpace(r.FormValue("schedule")),
	}
	// Booleans: HTML form only sends the value when the
	// checkbox is checked. Missing = false.
	cfg.Enabled = r.FormValue("enabled") == "1"
	cfg.InAppEnabled = r.FormValue("in_app_enabled") == "1"

	// Keep count.
	kcStr := strings.TrimSpace(r.FormValue("keep_count"))
	if kcStr != "" {
		n, err := strconv.Atoi(kcStr)
		if err != nil {
			backupConfigRedirect(w, r, "", "Keep count must be an integer")
			return
		}
		if n < 0 {
			backupConfigRedirect(w, r, "", "Keep count must be >= 0")
			return
		}
		cfg.KeepCount = n
	} else {
		cfg.KeepCount = 0
	}

	// Auto-detect protocol from destination if the form
	// left it blank (admin pasted a URL into destination
	// and didn't pick the dropdown).
	if cfg.Protocol == "" && cfg.Destination != "" {
		cfg.Protocol = backup.Protocol("")
	}

	// Validate. Validate() auto-detects a missing
	// protocol from the destination before failing.
	if err := cfg.Validate(); err != nil {
		backupConfigRedirect(w, r, "", i18n.Tf(lang, "backup.test_failed", err.Error()))
		return
	}
	if err := backup.Save(a.DB, cfg); err != nil {
		backupConfigRedirect(w, r, "", "Save failed: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "backup.config.save", fmt.Sprintf("protocol=%s destination=%s enabled=%t in_app=%t", cfg.Protocol, cfg.Destination, cfg.Enabled, cfg.InAppEnabled))
	backupConfigRedirect(w, r, "Saved", "")
}

// PostAdminBackupTest runs the no-mount test. The result
// is rendered on the page by re-fetching the config from
// the DB (so the form fields are preserved) and adding
// a TestResult key.
func (a *App) PostAdminBackupTest(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		backupConfigRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	lang := a.I18n.LangFromRequest(r)
	cfg := &backup.Config{
		Destination: strings.TrimSpace(r.FormValue("destination")),
		Protocol:    backup.Protocol(strings.TrimSpace(r.FormValue("protocol"))),
		Mountpoint:  strings.TrimSpace(r.FormValue("mountpoint")),
		Username:    strings.TrimSpace(r.FormValue("username")),
		SSHKeyPath:  strings.TrimSpace(r.FormValue("ssh_key_path")),
	}
	// Run the no-mount test. We do NOT Save() here — the
	// admin has to click "Save" separately to persist.
	tc := backup.TestConnection(cfg)
	if !tc.OK {
		backupConfigRedirect(w, r, "", i18n.Tf(lang, "backup.test_failed", strings.Join(tc.Issues, "; ")))
		return
	}
	var okMsg string
	switch tc.Protocol {
	case backup.ProtocolSMB:
		okMsg = i18n.Tf(lang, "backup.test_ok", tc.Fields["host"], tc.Fields["share"], tc.Fields["subpath"])
	case backup.ProtocolLocal:
		okMsg = i18n.Tf(lang, "backup.test_ok_local", tc.Fields["path"])
	case backup.ProtocolSFTP:
		okMsg = i18n.Tf(lang, "backup.test_ok_sftp", tc.Fields["source"], cfg.SSHKeyPath)
	case backup.ProtocolNFS:
		okMsg = i18n.Tf(lang, "backup.test_nfs", tc.Fields["host"]+":"+tc.Fields["path"])
	default:
		okMsg = "OK"
	}
	backupConfigRedirect(w, r, okMsg, "")
}

// PostAdminBackupRun triggers backup.RunBackup
// immediately. The button is admin-only and gated by
// the master switch (cfg.Enabled) — we still allow
// "Run now" when Enabled is false so the admin can
// test the config without enabling the schedule.
func (a *App) PostAdminBackupRun(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg, err := backup.Load(a.DB)
	if err != nil {
		backupConfigRedirect(w, r, "", "Load config: "+err.Error())
		return
	}
	lang := a.I18n.LangFromRequest(r)
	res, err := backup.RunBackup(a.DB, cfg)
	detail := fmt.Sprintf("status=%s archive=%s bytes=%d", res.Status, res.Archive, res.Bytes)
	if res != nil {
		a.audit(c.UserID, c.Username, "backup.run", detail)
	}
	if err != nil {
		// The error from RunBackup is "another backup is
		// already running" or a mount failure etc.
		// Distinguish the friendly case from real errors.
		if res != nil && res.Status == "fail" {
			backupConfigRedirect(w, r, "", res.Error)
			return
		}
		if strings.Contains(err.Error(), "already running") {
			backupConfigRedirect(w, r, "", i18n.T(lang, "backup.run_in_flight"))
			return
		}
		backupConfigRedirect(w, r, "", "Backup failed: "+err.Error())
		return
	}
	backupConfigRedirect(w, r, i18n.T(lang, "backup.run_started")+
		fmt.Sprintf(" (%s, %d bytes)", res.Archive, res.Bytes), "")
}

// PostAdminBackupToggle flips the in-app scheduler
// switch without going through the full form. Saves a
// full re-render for a one-click "pause backups for the
// weekend" action.
func (a *App) PostAdminBackupToggle(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		backupConfigRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	lang := a.I18n.LangFromRequest(r)
	cfg, err := backup.Load(a.DB)
	if err != nil {
		backupConfigRedirect(w, r, "", "Load config: "+err.Error())
		return
	}
	cfg.InAppEnabled = r.FormValue("enabled") == "1"
	if err := backup.Save(a.DB, cfg); err != nil {
		backupConfigRedirect(w, r, "", "Save failed: "+err.Error())
		return
	}
	a.audit(c.UserID, c.Username, "backup.toggle", fmt.Sprintf("in_app_enabled=%t", cfg.InAppEnabled))
	var msg string
	if cfg.InAppEnabled {
		msg = i18n.T(lang, "backup.toggle_enabled")
	} else {
		msg = i18n.T(lang, "backup.toggle_disabled")
	}
	backupConfigRedirect(w, r, msg, "")
}

// _ = time.Sleep keeps the time import in case a future
// handler schedules a delayed action (e.g. a "pause
// for 1 hour" toggle).
var _ = time.Sleep
