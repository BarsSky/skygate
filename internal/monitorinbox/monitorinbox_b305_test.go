// B305 (v1.5.70) — the monitoring inbox policy.
//
// The behaviour that matters to the operator: a condition is recorded ONCE with a
// repeat counter, a repeat does not page again, recovery closes it, and only
// events at or above the chosen severity reach Telegram.
package monitorinbox

import (
	"database/sql"
	"strings"
	"sync"
	"testing"

	"skygate/internal/db"
)

// staticDB satisfies db.DBSource for tests.
type staticDB struct{ d *sql.DB }

func (s staticDB) Current() *sql.DB { return s.d }

// recordingNotifier captures the alerts the inbox pushes.
type recordingNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (r *recordingNotifier) SendAlert(text string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, text)
	return int64(len(r.sent))
}

func (r *recordingNotifier) alerts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sent...)
}

func b305Inbox(t *testing.T) (*Inbox, *recordingNotifier) {
	t.Helper()
	_, d, err := db.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.ApplyMigrations(d, db.DialectSQLite); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	n := &recordingNotifier{}
	return &Inbox{DB: staticDB{d}, Notifier: n, Logf: func(string, ...any) {}}, n
}

func TestSeverityNormalisationAndRanking_B305(t *testing.T) {
	cases := map[string]Severity{
		"":         SeverityInfo,
		"info":     SeverityInfo,
		"Warning":  SeverityWarning,
		"warn":     SeverityWarning,
		"ERROR":    SeverityError,
		"fatal":    SeverityCritical,
		"panic":    SeverityCritical,
		"nonsense": SeverityInfo,
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	if SeverityRank(SeverityCritical) <= SeverityRank(SeverityError) {
		t.Error("critical must outrank error")
	}
	levels := LevelsAtLeast(SeverityWarning)
	if len(levels) != 3 || levels[0] != "critical" {
		t.Errorf("LevelsAtLeast(warning) = %v, want [critical error warning]", levels)
	}
}

func TestReportDedupAndNotifyPolicy_B305(t *testing.T) {
	in, notifier := b305Inbox(t)

	// A new error event: recorded and pushed (default threshold = error).
	rep, err := in.Report(Event{
		Source: "system_test", Subject: "headscale.ping", Severity: SeverityError,
		Title: "headscale does not answer", Body: "timeout", Link: "/admin/system_tests",
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if !rep.Created || !rep.Notified {
		t.Fatalf("new error event: created=%v notified=%v, want both true", rep.Created, rep.Notified)
	}
	if alerts := notifier.alerts(); len(alerts) != 1 || !strings.Contains(alerts[0], "headscale does not answer") {
		t.Fatalf("alerts = %v, want one message naming the event", alerts)
	}

	// A repeat: recorded (counter), NOT pushed again.
	rep2, err := in.Report(Event{
		Source: "system_test", Subject: "headscale.ping", Severity: SeverityError,
		Title: "headscale does not answer", Body: "timeout again",
	})
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if rep2.Created || rep2.Notified {
		t.Errorf("repeat: created=%v notified=%v, want false/false (no page for a known problem)", rep2.Created, rep2.Notified)
	}
	if got := len(notifier.alerts()); got != 1 {
		t.Errorf("alerts after a repeat = %d, want 1", got)
	}

	// A warning below the default threshold: recorded, not pushed.
	rep3, err := in.Report(Event{Source: "db_health", Subject: "lag", Severity: SeverityWarning, Title: "replication lag"})
	if err != nil {
		t.Fatalf("warning: %v", err)
	}
	if !rep3.Created || rep3.Notified {
		t.Errorf("warning below the error threshold: created=%v notified=%v, want true/false", rep3.Created, rep3.Notified)
	}

	// Lowering the threshold makes warnings page — without a restart.
	in.MinNotify = SeverityWarning
	if rep, err := in.Report(Event{Source: "db_health", Subject: "lag2", Severity: SeverityWarning, Title: "lag again"}); err != nil {
		t.Fatalf("warning with a lower threshold: %v", err)
	} else if !rep.Notified {
		t.Error("a warning was not pushed after lowering the threshold")
	}

	// Recovery closes the event; a NEW occurrence pages again.
	if err := in.Resolve(Event{Source: "system_test", Subject: "headscale.ping"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	events, err := db.ListMonitorEvents(in.DB.Current(), db.MonitorEventListFilter{States: []string{"resolved"}})
	if err != nil || len(events) != 1 {
		t.Fatalf("resolved events = %d (err %v), want 1", len(events), err)
	}
	rep4, err := in.Report(Event{
		Source: "system_test", Subject: "headscale.ping", Severity: SeverityError,
		Title: "headscale does not answer",
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !rep4.Created || !rep4.Notified {
		t.Errorf("a recovered condition that came back: created=%v notified=%v, want true/true", rep4.Created, rep4.Notified)
	}
}

func TestOpenCountCountsOnlyUnhandledAtOrAbove_B305(t *testing.T) {
	in, _ := b305Inbox(t)
	for _, ev := range []Event{
		{Source: "a", Subject: "1", Severity: SeverityInfo, Title: "info"},
		{Source: "a", Subject: "2", Severity: SeverityWarning, Title: "warn"},
		{Source: "a", Subject: "3", Severity: SeverityError, Title: "err"},
	} {
		if _, err := in.Report(ev); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	if n := in.OpenCount(SeverityWarning); n != 2 {
		t.Errorf("OpenCount(warning) = %d, want 2", n)
	}
	if n := in.OpenCount(SeverityError); n != 1 {
		t.Errorf("OpenCount(error) = %d, want 1", n)
	}
	// Acknowledging removes it from the badge.
	events, _ := db.ListMonitorEvents(in.DB.Current(), db.MonitorEventListFilter{Source: "a", Severities: []string{"error"}})
	if len(events) != 1 {
		t.Fatalf("expected the error row, got %+v", events)
	}
	if _, err := db.AckMonitorEvent(in.DB.Current(), events[0].ID, "admin"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if n := in.OpenCount(SeverityError); n != 0 {
		t.Errorf("OpenCount(error) after ack = %d, want 0", n)
	}
}

func TestFormatAlertNamesSeveritySourceAndLink_B305(t *testing.T) {
	text := FormatAlert(Event{
		Source: "module:tailscale", Subject: "tailscaled",
		Title: "модуль не отвечает", Body: "connection refused", Link: "/admin/modules/tailscale",
	}, SeverityCritical)
	for _, want := range []string{"skygate critical", "module:tailscale", "модуль не отвечает", "connection refused", "/admin/modules/tailscale"} {
		if !strings.Contains(text, want) {
			t.Errorf("alert text is missing %q:\n%s", want, text)
		}
	}
}

func TestInboxWithoutADatabaseSaysSoInsteadOfPanicking_B305(t *testing.T) {
	in := &Inbox{Logf: func(string, ...any) {}}
	if _, err := in.Report(Event{Source: "x", Subject: "y", Title: "t"}); err == nil {
		t.Fatal("report without a database must return an error, not silently succeed")
	}
	var nilInbox *Inbox
	if _, err := nilInbox.Report(Event{Title: "t"}); err == nil {
		t.Fatal("nil inbox must be reported, not panicked on")
	}
	if err := nilInbox.Resolve(Event{Title: "t"}); err != nil {
		t.Errorf("nil inbox Resolve = %v, want nil", err)
	}
}
