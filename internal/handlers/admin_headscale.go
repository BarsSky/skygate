// 2026-07-20: v0.20.0 — /admin/headscale page.
//
// Renders the headscale-update-monitor's snapshot:
//   - Pinned version (from SKYGATE_HEADSCALE_VERSION_PIN)
//   - Latest release seen (the GitHub API tag)
//   - UpdateAvailable / BreakingAvailable flags
//   - Last 20 releases from the headscale_releases table
//
// Also exposes a "Run check now" POST that forces
// the monitor to re-poll immediately. The handler is
// admin-only.
//
// The page is intentionally simple: a single
// table, no filters. With ~1 release per 6 weeks
// (headscale's cadence), the 20-row cap is ~2.3
// years of history — plenty for any operator.

package handlers

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"skygate/internal/headscale_version"
	"skygate/internal/i18n"
)

// GetAdminHeadscale renders /admin/headscale — the
// headscale-update-monitor status page.
//
// v0.20.0. 2026-07-20.
func (a *App) GetAdminHeadscale(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var (
		latest      headscale_version.Release
		update, brk bool
		checkedAt   time.Time
		history     []headscale_version.HeadscaleReleaseRecord
		pinned      string
		monitorOK   bool
	)
	if a.HeadscaleUpdateMonitor != nil {
		latest, update, brk, checkedAt, history, pinned = a.HeadscaleUpdateMonitor.Snapshot()
		monitorOK = true
	}

	// Localised status string for the "current state"
	// pill at the top of the page.
	lang := a.I18n.LangFromRequest(r)
	var stateKey string
	switch {
	case !monitorOK:
		stateKey = "headscale_admin.state_disabled"
	case pinned == "":
		stateKey = "headscale_admin.state_no_pin"
	case update && brk:
		stateKey = "headscale_admin.state_breaking"
	case update:
		stateKey = "headscale_admin.state_update"
	default:
		stateKey = "headscale_admin.state_current"
	}

	a.renderWithLayout(w, r, "admin/headscale.html", c, map[string]any{
		"Page":         "admin/headscale",
		"Title":        i18n.T(lang, "title.admin_headscale"),
		"StateKey":     stateKey,
		"StateText":    i18n.T(lang, stateKey),
		"MonitorOK":    monitorOK,
		"Pinned":       pinned,
		"Latest":       latest,
		"Update":       update,
		"Breaking":     brk,
		"CheckedAt":    checkedAt,
		"History":      history,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
	})
}

// PostAdminHeadscaleCheckNow forces the monitor to
// re-poll GitHub right now. Same convention as the
// /admin/exit-nodes/health-now button — a synchronous
// tick on a background goroutine, then a redirect
// back to the page so the operator sees the fresh
// state.
func (a *App) PostAdminHeadscaleCheckNow(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if a.HeadscaleUpdateMonitor == nil {
		http.Redirect(w, r, "/admin/headscale?err=monitor_disabled", http.StatusFound)
		return
	}
	// 5-second timeout — a hanging GitHub poll would
	// block the admin's browser request.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := a.HeadscaleUpdateMonitor.CheckNow(ctx); err != nil {
		http.Redirect(w, r, "/admin/headscale?err="+strconv.Quote(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/headscale?ok=checked", http.StatusFound)
}
