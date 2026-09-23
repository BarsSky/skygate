// 2026-08-06 v0.33.1.18 — UI-controlled DNS autoupdater toggle.
//
// The /admin/system_tests page exposes a checkbox that lets the
// administrator turn the domain→/32 DNS-refresh autoupdater on
// or off without editing .env or restarting skygate. The state
// is persisted in global_settings (key='dns_autoupdate_enabled')
// and read on every tick by the autoupdater goroutine (see
// internal/handlers/handlers.go RunDomainAutoUpdater). The env
// var SKYGATE_DNS_AUTOUPDATE_ENABLED is the DEFAULT at first
// start (when the global_settings row doesn't exist yet); the
// UI takes over after the first toggle.
//
// Why this exists separately from the v0.32.20 auto_update
// toggle (PostAdminUpdateAutoToggle):
//   - AutoUpdateEnabled controls the skygate SELF-UPDATE banner
//     on /admin/update (one-click "Apply" button vs always-on
//     "Push update" button).
//   - DNSAutoUpdateEnabled controls the BACKGROUND GOROUTINE
//     that re-resolves domain rules to /32 entries every
//     SKYGATE_DNS_AUTO_CHECK interval (default 5m).
//   - These were conflated in v0.32.13 (the main.go gate used
//     AutoUpdateEnabled for BOTH). v0.33.1.18 separates them;
//     an operator who set SKYGATE_AUTO_UPDATE_ENABLED=false in
//     .env (a sane default for production) was silently disabling
//     their DNS autoupdater → domain rules rotted as Cloudflare
//     rotated IPs. The two flags now have separate env vars,
//     separate DB keys, separate UI checkboxes, and separate
//     audit log entries.
//
// Audit log: every toggle writes a "dns_autoupdate_toggle" row
// with detail "dns-autoupdate enabled" or "dns-autoupdate disabled".
package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

		"skygate/internal/db"
	"skygate/internal/feature/exit_rules"
)

// globalSettingsKeyDNSAutoUpdate is the DB key for the toggle.
const globalSettingsKeyDNSAutoUpdate = "dns_autoupdate_enabled"

// PostAdminSystemTestsDNSAutoToggle is the handler for
// POST /admin/system_tests/dns-autoupdate-toggle. The form
// posts an "enabled" field ("1" or "0"); the handler writes
// the new value to global_settings and redirects back to
// /admin/system_tests.
//
// Admin-only.
func (s *Service) PostAdminSystemTestsDNSAutoToggle(w http.ResponseWriter, r *http.Request) {
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

	if err := db.SetGlobalSettingBool(s.dbc(), globalSettingsKeyDNSAutoUpdate, enabled); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "dns_autoupdate_toggle", "FAILED: "+err.Error())
		http.Error(w, "could not persist: "+err.Error(), http.StatusInternalServerError)
		return
	}

	state := "disabled"
	if enabled {
		state = "enabled"
	}
	s.Backend.Audit(c.UserID, c.Username, "dns_autoupdate_toggle", "dns-autoupdate "+state)

	http.Redirect(w, r, "/admin/system_tests?dns_auto_toggled="+state, http.StatusSeeOther)
}

// PostAdminSystemTestsDNSInterval (B308, v1.5.73) stores how often a domain rule
// may be re-resolved: POST /admin/system_tests/dns-interval with `interval_sec`.
//
// Why it is a setting and not a constant: every domain used to be re-resolved on
// every five-minute tick, so a domain whose A records rotate rewrote ±20 derived
// rows per tick, the generated ACL never stopped changing and /admin/exit-nodes
// showed the red «политика УСТАРЕЛА» banner almost permanently (live: 39
// drift/defer lines in two hours). Six hours is the default; an operator chasing a
// moving target can lower it, and 0 means "every tick" (the pre-B308 behaviour).
//
// A refused/parsed value is clamped, never rejected: the value that lands in the
// database is exactly what the updater will use, and the flash says which one.
func (s *Service) PostAdminSystemTestsDNSInterval(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	raw := r.FormValue("interval_sec")
	d := parseAdminDNSResolveInterval(raw)
	if err := db.SetGlobalSetting(s.dbc(), exit_rules.SettingDomainResolveIntervalSec, adminDNSIntervalValue(d)); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "dns_resolve_interval", "FAILED: "+err.Error())
		http.Error(w, "could not persist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "dns_resolve_interval",
		"interval="+exit_rules.DomainResolveIntervalLabel(d)+" (raw="+raw+")")
	http.Redirect(w, r, "/admin/system_tests?dns_interval="+url.QueryEscape(exit_rules.DomainResolveIntervalLabel(d)), http.StatusSeeOther)
}

// parseAdminDNSResolveInterval accepts the operator's seconds (or an empty value,
// which means "back to the default") and clamps it through the same policy the
// updater uses, so the page can never show a number the runtime ignores.
func parseAdminDNSResolveInterval(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return exit_rules.DefaultDomainResolveInterval
	}
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return exit_rules.DefaultDomainResolveInterval
	}
	return exit_rules.ClampDomainResolveInterval(time.Duration(secs) * time.Second)
}

// adminDNSIntervalValue renders the duration as the seconds the updater reads.
func adminDNSIntervalValue(d time.Duration) string {
	if d <= 0 {
		return "0"
	}
	return strconv.FormatInt(int64(d.Seconds()), 10)
}
