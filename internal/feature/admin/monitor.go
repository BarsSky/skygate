// internal/feature/admin/monitor.go — B305 (v1.5.70).
//
// /admin/monitor — the operator's monitoring inbox. The page answers the question
// the operator asked for («поле с уведомлениями куда будут приходить все сообщения
// разной важности с мониторинга skygate»):
//
//   - every monitoring event skygate records, one list, newest activity first;
//   - four severities (info / warning / error / critical) with counts, and a
//     minimum-severity filter;
//   - three states: open (nobody looked yet), acked (the operator said "I know"),
//     resolved (the producer observed recovery — a system test passing again, a
//     relay healthy again, tag drift reconciled);
//   - "acknowledge" per row and "acknowledge all", so a known problem stops
//     shouting without being deleted;
//   - the Telegram threshold (which severities also push to the bot) stored in
//     global_settings, so the operator can lower it to warning or raise it to
//     critical without editing env or restarting.
//
// The store and the dedup/notify policy live in internal/monitorinbox; this file
// is the page and its POST actions.
package admin

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/monitorinbox"
)

// monitorDBHandle is the nil-safe database accessor for this page: a Service
// whose DB source is not wired (a unit-test Service, a boot that failed before the
// DB opened) must render an empty inbox with a note instead of panicking.
func (s *Service) monitorDBHandle() *sql.DB {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Current()
}

// MonitorInbox builds the inbox for this Service (admin.DBSource IS db.DBSource —
// a type alias — so it is passed straight through). The Telegram notifier is
// optional: without one the inbox still records everything.
func (s *Service) MonitorInbox() *monitorinbox.Inbox {
	if s == nil {
		return &monitorinbox.Inbox{}
	}
	return &monitorinbox.Inbox{
		DB:       s.DB,
		Notifier: s.Notifier,
	}
}

// MonitorReport records one monitoring event. Producers (system tests, tag
// reconciliation, the exit-node monitor) call it through their own sinks; the
// signature is intentionally the small Event struct so no producer needs to know
// about the store or the notification policy.
func (s *Service) MonitorReport(ev monitorinbox.Event) {
	if s == nil {
		return
	}
	in := s.MonitorInbox()
	if _, err := in.Report(ev); err != nil {
		log.Printf("monitor: report %q failed: %v", ev.Title, err)
	}
}

// GetAdminMonitor renders the page. Filters come from the query string:
//
//	?state=open|acked|resolved|all   (default: open + acked)
//	?min=info|warning|error|critical (default: info)
//	?source=<subsystem>              (default: all)
func (s *Service) GetAdminMonitor(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	_ = lang // the page's strings are rendered through the template funcmap t
	minSev := monitorinbox.Normalize(r.URL.Query().Get("min"))
	state := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state")))
	source := strings.TrimSpace(r.URL.Query().Get("source"))

	states := []string{"open", "acked"}
	switch state {
	case "all":
		states = nil
	case "open", "acked", "resolved":
		states = []string{state}
	}

	var events []db.MonitorEvent
	var listErr string
	if s.monitorDBHandle() != nil {
		ev, err := db.ListMonitorEvents(s.monitorDBHandle(), db.MonitorEventListFilter{
			States:     states,
			Severities: monitorinbox.LevelsAtLeast(minSev),
			Source:     source,
			Limit:      200,
		})
		if err != nil {
			listErr = err.Error()
			log.Printf("monitor: list failed: %v", err)
		}
		events = ev
	}
	counts := map[string]int{}
	if s.monitorDBHandle() != nil {
		if got, err := db.CountMonitorEventsByState(s.monitorDBHandle()); err == nil {
			counts = got
		}
	}

	// Severity styling + a per-row "age" string are computed here so the template
	// stays presentation-only.
	rows := make([]map[string]any, 0, len(events))
	for _, e := range events {
		rows = append(rows, map[string]any{
			"Event":    e,
			"Severity": monitorinbox.Normalize(e.Severity),
			"State":    e.State,
			"Rank":     monitorinbox.SeverityRank(monitorinbox.Normalize(e.Severity)),
		})
	}

	threshold := ""
	if s.monitorDBHandle() != nil {
		if v, err := db.GetGlobalSetting(s.monitorDBHandle(), monitorinbox.SettingNotifyMinSeverity, "error"); err == nil {
			threshold = v
		}
	}
	if strings.TrimSpace(threshold) == "" {
		threshold = "error"
	}

	_ = lang
	s.Backend.RenderWithLayout(w, r, "admin/monitor.html", c, map[string]any{
		"Page":        "admin/monitor",
		"Title":       "Мониторинг",
		"Events":      rows,
		"Total":       len(rows),
		"Counts":      counts,
		"OpenCount":   counts["open"],
		"AckedCount":  counts["acked"],
		"ResolvedCnt": counts["resolved"],
		"MinSeverity": string(minSev),
		"StateFilter": state,
		"SourceFilter": source,
		"Levels":      monitorinbox.AllLevels(),
		"NotifyMin":   monitorinbox.Normalize(threshold),
		"NotifyRaw":   threshold,
		"ListErr":     listErr,
		"FlashOK":     r.URL.Query().Get("ok"),
		"FlashErr":    r.URL.Query().Get("err"),
	})
}

// monitorEventIDFromPath pulls the event id out of /admin/monitor/{id}/ack.
// extractIDFromPath only knows /admin/users and /admin/nodes, and widening it for
// one page would make every future reader re-check which callers it serves.
func monitorEventIDFromPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) >= 4 && parts[1] == "admin" && parts[2] == "monitor" {
		return parts[3]
	}
	return ""
}

// PostAdminMonitorAck acknowledges one event: POST /admin/monitor/{id}/ack.
func (s *Service) PostAdminMonitorAck(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(monitorEventIDFromPath(r.URL.Path), 10, 64)
	if err != nil || id <= 0 {
		s.redirectMonitor(w, r, "err", "bad event id")
		return
	}
	changed, err := db.AckMonitorEvent(s.monitorDBHandle(), id, c.Username)
	if err != nil {
		s.redirectMonitor(w, r, "err", "acknowledge failed: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "monitor_ack", fmt.Sprintf("event=%d changed=%d", id, changed))
	s.redirectMonitor(w, r, "ok", fmt.Sprintf("Событие %d отмечено как известное.", id))
}

// PostAdminMonitorAckAll acknowledges every OPEN event.
func (s *Service) PostAdminMonitorAckAll(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	changed, err := db.AckAllOpenMonitorEvents(s.monitorDBHandle(), c.Username)
	if err != nil {
		s.redirectMonitor(w, r, "err", "acknowledge all failed: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "monitor_ack_all", fmt.Sprintf("changed=%d", changed))
	s.redirectMonitor(w, r, "ok", fmt.Sprintf("Отмечено событий: %d.", changed))
}

// PostAdminMonitorSettings stores the Telegram push threshold.
func (s *Service) PostAdminMonitorSettings(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectMonitor(w, r, "err", "bad form: "+err.Error())
		return
	}
	raw := strings.TrimSpace(r.FormValue("min_severity"))
	sev := monitorinbox.Normalize(raw)
	if err := db.SetGlobalSetting(s.monitorDBHandle(), monitorinbox.SettingNotifyMinSeverity, string(sev)); err != nil {
		s.redirectMonitor(w, r, "err", "save failed: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "monitor_settings", "notify_min_severity="+string(sev))
	s.redirectMonitor(w, r, "ok", "Порог уведомлений в Telegram: "+string(sev)+" и выше. События ниже порога всё равно записываются.")
}

// redirectMonitor sends the operator back to the page with a flash. Every refusal
// on this page is a flash, never a raw error page (the rule the devices page
// learned in B303).
func (s *Service) redirectMonitor(w http.ResponseWriter, r *http.Request, kind, msg string) {
	if kind != "ok" && kind != "err" {
		kind = "err"
	}
	http.Redirect(w, r, "/admin/monitor?"+kind+"="+url.QueryEscape(msg), http.StatusSeeOther)
}
