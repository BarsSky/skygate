package admin

// headscale.go — /admin/headscale page (headscale-update-monitor
// status).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/admin_headscale.go.
//
// Handlers: GetAdminHeadscale, PostAdminHeadscaleCheckNow.

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"skygate/internal/headscale_version"
	"skygate/internal/i18n"
)

// GetAdminHeadscale renders /admin/headscale — the
// headscale-update-monitor status page. Admin-only.
func (s *Service) GetAdminHeadscale(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
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
	if s.HeadscaleUpdateMonitor != nil {
		latest, update, brk, checkedAt, history, pinned = s.HeadscaleUpdateMonitor.Snapshot()
		monitorOK = true
	}

	lang := s.I18n.LangFromRequest(r)
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

	s.Backend.RenderWithLayout(w, r, "admin/headscale.html", c, map[string]any{
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

// PostAdminHeadscaleCheckNow forces the monitor to re-poll
// GitHub right now. Admin-only.
func (s *Service) PostAdminHeadscaleCheckNow(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.HeadscaleUpdateMonitor == nil {
		http.Redirect(w, r, "/admin/headscale?err=monitor_disabled", http.StatusFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.HeadscaleUpdateMonitor.CheckNow(ctx); err != nil {
		http.Redirect(w, r, "/admin/headscale?err="+strconv.Quote(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin/headscale?ok=checked", http.StatusFound)
}
