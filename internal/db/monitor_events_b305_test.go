// B305 (v1.5.70) — the monitoring inbox store.
//
// These tests run against a REAL in-memory SQLite database with the full
// migration chain applied, which is also what proves the v0.76 migration landed
// on the SQLite side (AGENTS rule 9: schema changes land in BOTH chains).
package db

import (
	"database/sql"
	"testing"
)

func b305TestDB(t *testing.T) *sql.DB {
	t.Helper()
	_, sqlDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := ApplyMigrations(sqlDB, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	var n int
	if err := sqlDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='monitor_events'`).Scan(&n); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	if n != 1 {
		t.Fatalf("monitor_events table missing after migrations (v0.76 did not land on SQLite)")
	}
	return sqlDB
}

func TestMonitorEventsUpsertDedupAndReopen_B305(t *testing.T) {
	d := b305TestDB(t)

	id1, created1, err := ReportMonitorEvent(d, MonitorEvent{
		Fingerprint: "system_test:headscale.ping",
		Source:      "system_test",
		Subject:     "headscale.ping",
		Severity:    "error",
		Title:       "headscale does not answer",
		Body:        "context deadline exceeded",
		Link:        "/admin/system_tests",
	})
	if err != nil {
		t.Fatalf("first report: %v", err)
	}
	if !created1 || id1 <= 0 {
		t.Fatalf("first report: id=%d created=%v, want a new row", id1, created1)
	}

	// A repeat must UPDATE the same row, not add one.
	id2, created2, err := ReportMonitorEvent(d, MonitorEvent{
		Fingerprint: "system_test:headscale.ping",
		Source:      "system_test", Subject: "headscale.ping", Severity: "error",
		Title: "headscale does not answer", Body: "again",
	})
	if err != nil {
		t.Fatalf("second report: %v", err)
	}
	if created2 {
		t.Error("a repeat reported itself as new — the notifier would page twice")
	}
	if id2 != id1 {
		t.Errorf("repeat created a second row (id %d vs %d)", id2, id1)
	}
	rows, err := ListMonitorEvents(d, MonitorEventListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (dedup by fingerprint)", len(rows))
	}
	if rows[0].Repeats != 2 {
		t.Errorf("repeats = %d, want 2", rows[0].Repeats)
	}
	if rows[0].Body != "again" {
		t.Errorf("body = %q, want the newest text", rows[0].Body)
	}

	// Resolve, then a new occurrence must REOPEN (and be reported as new again).
	if _, err := ResolveMonitorEvent(d, "system_test:headscale.ping"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rows, _ = ListMonitorEvents(d, MonitorEventListFilter{States: []string{"resolved"}})
	if len(rows) != 1 || rows[0].State != "resolved" {
		t.Fatalf("resolve did not mark the row: %+v", rows)
	}
	if _, created3, err := ReportMonitorEvent(d, MonitorEvent{
		Fingerprint: "system_test:headscale.ping", Source: "system_test",
		Severity: "error", Title: "back", Body: "back",
	}); err != nil {
		t.Fatalf("reopen report: %v", err)
	} else if !created3 {
		t.Error("a resolved condition that came back was not reported as reopening")
	}
	rows, _ = ListMonitorEvents(d, MonitorEventListFilter{States: []string{"open"}})
	if len(rows) != 1 || rows[0].ResolvedAt != 0 || rows[0].Repeats != 3 {
		t.Errorf("reopened row = %+v, want state=open, resolved_at=0, repeats=3", rows)
	}
}

func TestMonitorEventsFiltersAndCounters_B305(t *testing.T) {
	d := b305TestDB(t)

	for _, ev := range []MonitorEvent{
		{Fingerprint: "a", Source: "system_test", Severity: "error", Title: "t1"},
		{Fingerprint: "b", Source: "module:tailscale", Severity: "warning", Title: "t2"},
		{Fingerprint: "c", Source: "db_health", Severity: "info", Title: "t3"},
	} {
		if _, _, err := ReportMonitorEvent(d, ev); err != nil {
			t.Fatalf("report %s: %v", ev.Fingerprint, err)
		}
	}

	bySource, err := ListMonitorEvents(d, MonitorEventListFilter{Source: "module:tailscale"})
	if err != nil {
		t.Fatalf("list by source: %v", err)
	}
	if len(bySource) != 1 || bySource[0].Fingerprint != "b" {
		t.Errorf("source filter returned %+v", bySource)
	}

	bySeverity, err := ListMonitorEvents(d, MonitorEventListFilter{Severities: []string{"error", "warning"}})
	if err != nil {
		t.Fatalf("list by severity: %v", err)
	}
	if len(bySeverity) != 2 {
		t.Errorf("severity filter returned %d rows, want 2", len(bySeverity))
	}

	counts, err := CountMonitorEventsByState(d)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts["open"] != 3 {
		t.Errorf("open count = %d, want 3", counts["open"])
	}

	ranks := map[string]int{"info": 0, "warning": 1, "error": 2, "critical": 3}
	n, err := CountOpenMonitorEventsNotLowerThan(d, 1, ranks)
	if err != nil {
		t.Fatalf("ranked count: %v", err)
	}
	if n != 2 {
		t.Errorf("open events at warning+ = %d, want 2", n)
	}

	// Ack one: it leaves the open badge count but stays in the list.
	rows, _ := ListMonitorEvents(d, MonitorEventListFilter{Source: "db_health"})
	if len(rows) != 1 {
		t.Fatalf("expected the db_health row, got %+v", rows)
	}
	if changed, err := AckMonitorEvent(d, rows[0].ID, "admin"); err != nil || changed != 1 {
		t.Fatalf("ack: changed=%d err=%v", changed, err)
	}
	acked, _ := ListMonitorEvents(d, MonitorEventListFilter{Source: "db_health"})
	if acked[0].State != "acked" || acked[0].AckedBy != "admin" || acked[0].AckedAt == 0 {
		t.Errorf("acked row = %+v", acked[0])
	}
	if n, _ := CountOpenMonitorEventsNotLowerThan(d, 0, ranks); n != 2 {
		t.Errorf("open count after ack = %d, want 2", n)
	}

	// Ack-all only touches OPEN rows.
	if changed, err := AckAllOpenMonitorEvents(d, "admin"); err != nil || changed != 2 {
		t.Errorf("ack all: changed=%d err=%v, want 2", changed, err)
	}
	counts, _ = CountMonitorEventsByState(d)
	if counts["open"] != 0 || counts["acked"] != 3 {
		t.Errorf("after ack-all: %+v, want 0 open / 3 acked", counts)
	}
}

func TestReportMonitorEventRejectsAnEmptyFingerprint_B305(t *testing.T) {
	d := b305TestDB(t)
	if _, _, err := ReportMonitorEvent(d, MonitorEvent{Source: "x"}); err == nil {
		t.Fatal("an event without a fingerprint was accepted — dedup would collapse every such event into one row")
	}
}
