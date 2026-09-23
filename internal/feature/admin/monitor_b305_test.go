// B305 (v1.5.70) — /admin/monitor page + the producer wiring.
//
// The operator's ask: «добавить поле с уведомлениями куда будут приходить все
// сообщения разной важности с мониторинга skygate» and «получать уведомления по
// неисправности или некорректном поведении». These tests drive the real handlers
// over an in-memory database and pin the parts an operator depends on: the page
// renders what the store holds, ack/ack-all/settings work, and a failing system
// test opens an event that a passing run resolves.
package admin

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/monitorinbox"
)

// captureBackend records what the handler rendered.
type captureBackend struct {
	admin    *auth.Claims
	template string
	data     map[string]any
	audits   []string
}

func (b *captureBackend) Render(http.ResponseWriter, *http.Request, string, any) {}
func (b *captureBackend) RenderWithLayout(_ http.ResponseWriter, _ *http.Request, name string, _ *auth.Claims, data map[string]any) {
	b.template, b.data = name, data
}
func (b *captureBackend) CurrentUser(*http.Request) *auth.Claims { return b.admin }
func (b *captureBackend) Audit(_ int64, _ string, action, detail string) {
	b.audits = append(b.audits, action+" "+detail)
}
func (b *captureBackend) InfraAuditIdentity(uid int64, username string) (int64, string) {
	return uid, username
}

// b305Service builds a Service over a migrated in-memory DB with a capturing
// backend and an admin caller.
func b305Service(t *testing.T) (*Service, *captureBackend) {
	t.Helper()
	src, _, _ := setupClaimTestDB(t)
	be := &captureBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	return &Service{DB: src, Backend: be, I18n: &i18n.Catalog{}}, be
}

func b305Get(t *testing.T, h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

func b305Post(t *testing.T, h http.HandlerFunc, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// TestGetAdminMonitorRendersStoredEvents_B305: the page shows what the store has,
// with the counters the operator uses to triage.
func TestGetAdminMonitorRendersStoredEvents_B305(t *testing.T) {
	svc, be := b305Service(t)
	if _, _, err := db.ReportMonitorEvent(svc.monitorDBHandle(), db.MonitorEvent{
		Fingerprint: "system_test:headscale.ping", Source: "system_test", Subject: "headscale.ping",
		Severity: "error", Title: "headscale does not answer", Body: "timeout", Link: "/admin/system_tests",
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, _, err := db.ReportMonitorEvent(svc.monitorDBHandle(), db.MonitorEvent{
		Fingerprint: "db_health:wal", Source: "db_health", Severity: "warning", Title: "lag",
	}); err != nil {
		t.Fatalf("seed warning: %v", err)
	}

	rec := b305Get(t, svc.GetAdminMonitor, "/admin/monitor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
	}
	if be.template != "admin/monitor.html" {
		t.Errorf("rendered %q, want admin/monitor.html", be.template)
	}
	if got := be.data["Total"].(int); got != 2 {
		t.Errorf("Total = %d, want 2", got)
	}
	if got := be.data["OpenCount"].(int); got != 2 {
		t.Errorf("OpenCount = %d, want 2", got)
	}
	if got := be.data["MinSeverity"].(string); got != "info" {
		t.Errorf("MinSeverity = %q, want info by default", got)
	}

	// The severity filter is applied at the store level.
	rec = b305Get(t, svc.GetAdminMonitor, "/admin/monitor?min=error")
	if got := be.data["Total"].(int); got != 1 {
		t.Errorf("Total with min=error = %d, want 1", got)
	}
	if got := be.data["MinSeverity"].(string); got != "error" {
		t.Errorf("MinSeverity = %q, want error", got)
	}
	_ = rec
}

// TestMonitorAckAndAckAllAndSettings_B305: the three POST actions.
func TestMonitorAckAndAckAllAndSettings_B305(t *testing.T) {
	svc, _ := b305Service(t)
	id, _, err := db.ReportMonitorEvent(svc.monitorDBHandle(), db.MonitorEvent{
		Fingerprint: "system_test:x", Source: "system_test", Severity: "error", Title: "boom",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := db.ReportMonitorEvent(svc.monitorDBHandle(), db.MonitorEvent{
		Fingerprint: "system_test:y", Source: "system_test", Severity: "error", Title: "boom2",
	}); err != nil {
		t.Fatalf("seed2: %v", err)
	}

	rec := b305Post(t, svc.PostAdminMonitorAck, "/admin/monitor/1/ack", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ack status = %d", rec.Code)
	}
	var state, by string
	if err := svc.monitorDBHandle().QueryRow(
		`SELECT state, acked_by FROM monitor_events WHERE id = ?`, id).Scan(&state, &by); err != nil {
		t.Fatalf("read back acked row: %v", err)
	}
	if state != "acked" || by != "admin" {
		t.Errorf("row = (%q, %q), want (acked, admin)", state, by)
	}

	rec = b305Post(t, svc.PostAdminMonitorAckAll, "/admin/monitor/ack-all", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ack-all status = %d", rec.Code)
	}
	counts, err := db.CountMonitorEventsByState(svc.monitorDBHandle())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["open"] != 0 || counts["acked"] != 2 {
		t.Errorf("after ack-all: %+v, want 0 open / 2 acked", counts)
	}

	rec = b305Post(t, svc.PostAdminMonitorSettings, "/admin/monitor/settings", url.Values{"min_severity": {"warning"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("settings status = %d", rec.Code)
	}
	got, err := db.GetGlobalSetting(svc.monitorDBHandle(), monitorinbox.SettingNotifyMinSeverity, "")
	if err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if got != "warning" {
		t.Errorf("stored threshold = %q, want warning", got)
	}

	// An unknown severity normalises instead of being stored verbatim.
	rec = b305Post(t, svc.PostAdminMonitorSettings, "/admin/monitor/settings", url.Values{"min_severity": {"WARN"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("settings status = %d", rec.Code)
	}
	got, _ = db.GetGlobalSetting(svc.monitorDBHandle(), monitorinbox.SettingNotifyMinSeverity, "")
	if got != "warning" {
		t.Errorf("normalised threshold = %q, want warning", got)
	}
}

// TestAckRejectsABadIDAsAFlash_B305: the page never answers a raw error body.
func TestAckRejectsABadIDAsAFlash_B305(t *testing.T) {
	svc, _ := b305Service(t)
	rec := b305Post(t, svc.PostAdminMonitorAck, "/admin/monitor/notanumber/ack", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 with a flash", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want ?err=", loc)
	}
	if strings.Contains(rec.Body.String(), "bad event id") {
		t.Errorf("raw error text leaked into the body: %q", rec.Body.String())
	}
}

// TestSystemTestFailuresOpenAndPassesResolveInboxEvents_B305: the system tests are
// a producer, and a passing run must CLOSE the event — otherwise the inbox would
// be a list of everything that ever went wrong.
func TestSystemTestFailuresOpenAndPassesResolveInboxEvents_B305(t *testing.T) {
	svc, _ := b305Service(t)
	svc.ReportRunToMonitor([]SystemTestResult{
		{Name: "headscale.ping", Category: "headscale", Status: SystemTestFail, Output: "context deadline exceeded"},
		{Name: "db.integrity", Category: "db", Status: SystemTestPass, Output: "ok"},
		{Name: "wal-g.backup", Category: "wal-g", Status: SystemTestSkip, Output: "no wal-g"},
	})

	events, err := db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "system_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want only the failing test (%+v)", len(events), events)
	}
	if events[0].Fingerprint != "system_test:headscale.ping" || events[0].Severity != "error" || events[0].State != "open" {
		t.Errorf("event = %+v, want an open error for headscale.ping", events[0])
	}
	if !strings.Contains(events[0].Body, "headscale") || !strings.Contains(events[0].Body, "deadline") {
		t.Errorf("body = %q, want category + output", events[0].Body)
	}

	// The same test passes now: the event resolves.
	svc.ReportRunToMonitor([]SystemTestResult{{Name: "headscale.ping", Category: "headscale", Status: SystemTestPass}})
	events, _ = db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "system_test"})
	if len(events) != 1 || events[0].State != "resolved" {
		t.Fatalf("after a pass: %+v, want one resolved event", events)
	}

	// And a failure that comes back REOPENS the same row (no duplicate).
	svc.ReportRunToMonitor([]SystemTestResult{{Name: "headscale.ping", Category: "headscale", Status: SystemTestFail, Output: "again"}})
	events, _ = db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "system_test"})
	if len(events) != 1 || events[0].State != "open" || events[0].Repeats != 2 {
		t.Errorf("after a recurrence: %+v, want one open row with repeats=2", events)
	}
}

// TestTrackedTagFailuresAlsoLandInTheInbox_B305: the tag reconciler's alert sink
// records into the inbox as well as Telegram, so a tag that never reached
// headscale is visible on the page even if the bot message was missed.
func TestTrackedTagFailuresAlsoLandInTheInbox_B305(t *testing.T) {
	svc, _ := b305Service(t)
	sink := adminTagAlertSink(svc)
	if sink == nil {
		t.Fatal("adminTagAlertSink returned nil")
	}
	sink.ReportFailure("2", "workpc", "tag:dev-daniil-workpc",
		errors.New("requested tags [tag:dev-daniil-workpc] are invalid or not permitted"))

	events, err := db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "tag_reconcile"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("tag events = %d, want 1", len(events))
	}
	if events[0].Severity != "error" || !strings.Contains(events[0].Body, "not permitted") {
		t.Errorf("event = %+v, want an error carrying the headscale refusal", events[0])
	}
	// A second failure folds into the same row (dedup) instead of flooding.
	sink.ReportFailure("3", "laptop", "tag:dev-x", errors.New("requested tags [tag:dev-x] are invalid or not permitted"))
	events, _ = db.ListMonitorEvents(svc.monitorDBHandle(), db.MonitorEventListFilter{Source: "tag_reconcile"})
	if len(events) != 1 || events[0].Repeats != 2 {
		t.Errorf("after a second failure: %+v, want one row with repeats=2", events)
	}
}

// TestMonitorWithoutDatabaseRendersInsteadOfPanicking_B305: a Service whose DB is
// not wired must still render (an empty inbox with a note).
func TestMonitorWithoutDatabaseRendersInsteadOfPanicking_B305(t *testing.T) {
	be := &captureBackend{admin: &auth.Claims{UserID: 1, Username: "admin", IsAdmin: true}}
	svc := &Service{Backend: be, I18n: &i18n.Catalog{}}
	rec := b305Get(t, svc.GetAdminMonitor, "/admin/monitor")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := be.data["Total"].(int); got != 0 {
		t.Errorf("Total = %d, want 0", got)
	}
	_ = sql.ErrNoRows
}
