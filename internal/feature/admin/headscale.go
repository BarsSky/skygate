package admin

// headscale.go — /admin/headscale page.
//
// Two subjects share this page on purpose: WHAT VERSION headscale should be (the
// headscale-update-monitor, v0.20.0) and WHAT THE SERVICE IS DOING right now
// (B533, 2026-11-27). The operator who lands here because "headscale is broken"
// needs both — and B533 exists because on 2026-11-27 neither answer was given:
// the service was healthy, the host had been moved to a new IPv4 and the zone
// still named the old one.
//
// Handlers: GetAdminHeadscale, PostAdminHeadscaleCheckNow,
// PostAdminHeadscaleServiceRestart.
//
// refactor-v0.30 Phase B step 3a: moved from internal/handlers/admin_headscale.go.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"skygate/internal/headscale"
	"skygate/internal/headscale_version"
	"skygate/internal/i18n"
)

// headscaleServiceView is the B533 card, pre-formatted for the template.
type headscaleServiceView struct {
	Facts        headscale.ServiceFacts
	StateKey     string // i18n key for the verdict
	BadgeClass   string // "green" / "warning" / "red" / "gray"
	HelperArmed  bool
	LastResult   headscale.ServiceApplyResult
	RestartBlock string // i18n key explaining why the button is disabled ("" = enabled)
	// B535: is a restart still waiting for the privileged applier, and does the last
	// verdict actually describe the service this page just measured.
	Pending     headscale.ServicePending
	PostRestart headscale.PostRestart
}

// buildHeadscaleServiceView measures the service. Bounded: the whole collection
// (manager commands, DNS, a 2.5 s API probe, the B272 policy audit) runs under one
// deadline, because this page is exactly the one an operator opens WHILE
// headscale is down.
func (s *Service) buildHeadscaleServiceView() headscaleServiceView {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	var client *headscale.Client
	if s.HSGlobalFn != nil {
		client = s.HSGlobalFn()
	}
	facts := client.CollectServiceFacts(ctx, headscale.ServiceProbe{})

	v := headscaleServiceView{
		Facts:       facts,
		StateKey:    "headscale_service.state_" + string(facts.State),
		HelperArmed: headscale.ServiceHelperArmed(),
	}
	switch {
	case facts.State.Healthy():
		v.BadgeClass = "green"
	case facts.State == headscale.ServiceRunningAPIDown, facts.State == headscale.ServiceStopped:
		v.BadgeClass = "warning"
	case facts.State == headscale.ServiceAbsent, facts.State == headscale.ServiceUnknown:
		v.BadgeClass = "gray"
	default:
		v.BadgeClass = "red"
	}
	if res, err := headscale.ReadServiceApplyResult(); err == nil {
		v.LastResult = res
	}
	// B535: «нажал и ничего» is a state of its own — the request file exists until
	// the applier consumes it, and the verdict only describes the service when it is
	// OLDER than this measurement.
	v.Pending = headscale.ReadServicePending()
	v.PostRestart = headscale.PostRestartVerdict(facts, v.LastResult)
	switch {
	case facts.Manager == "" || facts.Manager == "none":
		v.RestartBlock = "headscale_service.restart_blocked_absent"
	case !v.HelperArmed:
		v.RestartBlock = "headscale_service.restart_blocked_helper"
	}
	return v
}

// GetAdminHeadscale renders /admin/headscale. Admin-only.
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
		// B297: what the RUNNING headscale answered, next to what the operator
		// declared. Without both, a stale pin is invisible on the one page that
		// exists to compare versions.
		version headscale_version.ServerVersionStatus
	)
	if s.HeadscaleUpdateMonitor != nil {
		latest, update, brk, checkedAt, history, pinned = s.HeadscaleUpdateMonitor.Snapshot()
		version = s.HeadscaleUpdateMonitor.VersionStatus()
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

	// B533: what the SERVICE is doing, what its name points at, and what to do
	// about it. Nil-safe on HSGlobalFn (the card then reports the local service
	// only and says so in CollectNotes).
	svc := s.buildHeadscaleServiceView()
	// B535: the ordered verdict is resolved to text HERE, so the template never has
	// to compare a typed string constant (a comparison Go's template engine would
	// have to type-switch on).
	postRestartText := ""
	if svc.PostRestart != headscale.PostRestartNone {
		postRestartText = i18n.T(lang, "headscale_service.post_restart_"+string(svc.PostRestart))
	}

	// Flash values: the B533 codes are translated; everything else is passed
	// through exactly as before (existing links use raw codes like "checked").
	flashOK := r.URL.Query().Get("ok")
	if flashOK == "service_restart_queued" {
		flashOK = i18n.T(lang, "headscale_service.flash_queued")
	}
	flashErr := r.URL.Query().Get("err")
	if flashErr == "service_helper_missing" {
		flashErr = i18n.T(lang, "headscale_service.flash_helper_missing")
	}

	s.Backend.RenderWithLayout(w, r, "admin/headscale.html", c, map[string]any{
		"Page":            "admin/headscale",
		"Title":           i18n.T(lang, "title.admin_headscale"),
		"StateKey":        stateKey,
		"StateText":       i18n.T(lang, stateKey),
		"MonitorOK":       monitorOK,
		"Pinned":          pinned,
		"Version":         version,
		"Latest":          latest,
		"Update":          update,
		"Breaking":        brk,
		"CheckedAt":       checkedAt,
		"History":         history,
		"Service":         svc,
		"PostRestartText": postRestartText,
		"FlashSuccess":    flashOK,
		"FlashError":      flashErr,
	})
}

// PostAdminHeadscaleCheckNow forces the monitor to re-poll GitHub right now.
// Admin-only.
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

// PostAdminHeadscaleServiceRestart is the B533 action: hand "restart the
// headscale service" to the root-owned applier.
//
// It never restarts anything itself. Two measured reasons: the native skygate
// unit has no right to call `systemctl restart` (unprivileged user,
// ProtectSystem=strict), and an in-process restart would die with the cgroup it
// belongs to (AGENTS trap #7). The request is DATA (an action plus an
// allow-listed service name); the applier validates it again and writes its
// verdict back as a file this page renders on the next load.
func (s *Service) PostAdminHeadscaleServiceRestart(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	err := headscale.RequestServiceAction("restart", "headscale")
	switch {
	case errors.Is(err, headscale.ErrServiceHelperUnavailable):
		s.Backend.Audit(c.UserID, c.Username, "headscale.restart_refused",
			"privileged service applier is not installed (deploy/install-service-helper.sh)")
		http.Redirect(w, r, "/admin/headscale?err=service_helper_missing", http.StatusFound)
		return
	case err != nil:
		s.Backend.Audit(c.UserID, c.Username, "headscale.restart_refused", err.Error())
		http.Redirect(w, r, "/admin/headscale?err="+strconv.Quote(err.Error()), http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "headscale.restart_queued",
		"request handed to skygate-apply-service.sh (SERVICE=headscale)")
	http.Redirect(w, r, "/admin/headscale?ok=service_restart_queued", http.StatusFound)
}

// PostAdminHeadscaleServiceCheck measures the service RIGHT NOW and puts the verdict
// in the flash (B535).
//
// The page already measures on every render, so why a button? Because the operator's
// question after a restart is «и что теперь» — and a button is the only way to say
// «это замер, сделанный по моему запросу», with the audit row that goes with it. The
// flash carries the same numbers the card shows, so a green flash cannot disagree
// with a red card.
func (s *Service) PostAdminHeadscaleServiceCheck(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	v := s.buildHeadscaleServiceView()

	api := i18n.T(lang, "headscale_service.api_down")
	if v.Facts.APIOK {
		api = i18n.T(lang, "headscale_service.api_ok")
	}
	msg := fmt.Sprintf(i18n.T(lang, "headscale_service.flash_checked"),
		i18n.T(lang, v.StateKey), api, v.Facts.Journal.Errors, v.Facts.Journal.Warnings)
	s.Backend.Audit(c.UserID, c.Username, "headscale.service_checked", msg)
	http.Redirect(w, r, "/admin/headscale?ok="+url.QueryEscape(msg)+"#service", http.StatusFound)
}
