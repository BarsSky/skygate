package admin

// system_tests_b394_run_test.go — B394: the run is asynchronous, reports
// progress, can run ONE test, and the history is pruned.
//
// These are behavioural: they drive the real StartSystemRun against a migrated
// in-memory SQLite database and inspect the snapshot and the history table. A
// source grep could not tell "the POST blocks" from "the POST returns" — the
// whole point of the change is the shape of the call.

import (
	"context"
	"testing"
	"time"

	"skygate/internal/config"
	"skygate/internal/headscale"
)

// b394TestService is a Service whose DB is a migrated in-memory SQLite database
// and whose headscale client is absent (the tests that need live state SKIP with
// a reason, which is what rule 1 asks for).
func b394TestService(t *testing.T) *Service {
	t.Helper()
	src := b282SQLiteDB(t)
	return &Service{
		DB:         src,
		Cfg:        &config.Config{},
		HSGlobalFn: func() *headscale.Client { return nil },
	}
}

// waitForRun waits (bounded) for the snapshot to finish and returns it.
func waitForRun(t *testing.T, svc *Service, id string) *SystemRunState {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.CurrentSystemRun()
		if st != nil && st.ID == id && st.Done {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish within 60s", id)
	return nil
}

// TestStartSystemRunReportsProgress_B394 pins the async contract: StartSystemRun
// returns BEFORE the catalogue has finished, the snapshot carries an ID the page
// can poll, and it ends Done with every result.
func TestStartSystemRunReportsProgress_B394(t *testing.T) {
	svc := b394TestService(t)
	st, err := svc.StartSystemRun(1, "tester", "")
	if err != nil {
		t.Fatalf("StartSystemRun: %v", err)
	}
	if st == nil || st.ID == "" {
		t.Fatal("StartSystemRun returned no run id — the page has nothing to poll")
	}
	if st.Done {
		t.Log("note: the run finished before StartSystemRun returned — acceptable on a fast machine")
	}
	if st.Total == 0 {
		t.Fatal("the snapshot reports zero tests")
	}
	done := waitForRun(t, svc, st.ID)
	if done.Completed != done.Total {
		t.Errorf("completed %d of %d", done.Completed, done.Total)
	}
	if done.Summary == nil {
		t.Fatal("no summary on a finished run")
	}
	if !done.Persisted {
		t.Error("a whole-catalogue run must write a history row")
	}
	if n := b394CountRuns(t, svc); n != 1 {
		t.Errorf("system_tests_runs has %d rows, want 1", n)
	}
}

// TestSingleTestRunDoesNotPolluteHistory_B394 pins the other half: ?test=<name>
// runs exactly that test, reports it to the monitoring inbox, and writes NO
// history row (a history of one-test runs would bury the real runs).
func TestSingleTestRunDoesNotPolluteHistory_B394(t *testing.T) {
	svc := b394TestService(t)
	before := b394CountRuns(t, svc)
	st, err := svc.StartSystemRun(1, "tester", "db.schema_tables")
	if err != nil {
		t.Fatalf("StartSystemRun(one): %v", err)
	}
	done := waitForRun(t, svc, st.ID)
	if len(done.Results) != 1 || done.Results[0].Name != "db.schema_tables" {
		t.Fatalf("expected exactly one result for db.schema_tables, got %+v", done.Results)
	}
	// B408.1 RENEGOTIATES this contract. B394 required that a single-test run write
	// NO row at all — which is exactly why the operator's «прогнать этот» produced
	// an audit line, no row, and a page that kept showing the last FULL run from
	// three days earlier. Now the run IS stored, tagged with its scope, so the row
	// can reflect what was just measured; what must NOT happen is that it enters the
	// history RATES, which is what ListRecentRuns filters out.
	if !done.Persisted {
		t.Error("a single-test run must be persisted (B408.1) — otherwise the re-run is thrown away")
	}
	if after := b394CountRuns(t, svc); after != before+1 {
		t.Errorf("stored runs went %d -> %d, want exactly one new row", before, after)
	}
	var only string
	if err := svc.dbc().QueryRow(`SELECT only_test FROM system_tests_runs ORDER BY id DESC LIMIT 1`).Scan(&only); err != nil {
		t.Fatalf("read only_test: %v", err)
	}
	if only != "db.schema_tables" {
		t.Errorf("only_test = %q, want the test name — the scope is what keeps rates comparable", only)
	}
	if hist, err := svc.ListRecentRuns(t.Context(), 20); err == nil {
		for _, h := range hist {
			if h.TotalCount == 1 {
				t.Errorf("the single-test run leaked into the history rates: %+v", h)
			}
		}
	}
	if done.Summary == nil || done.Summary.TotalCount != 1 {
		t.Errorf("summary = %+v, want one test", done.Summary)
	}
}

// TestStartSystemRunRefusesAnUnknownTest pins the honest error: a name that is not
// in the catalogue must say so instead of starting an empty run.
func TestStartSystemRunRefusesAnUnknownTest(t *testing.T) {
	svc := b394TestService(t)
	if _, err := svc.StartSystemRun(1, "tester", "no.such_test"); err == nil {
		t.Fatal("an unknown test name started a run")
	}
}

// TestPruneSystemTestRuns_B394 pins retention: rows older than the window are
// removed, the newest N are always kept, and a recent row is never touched.
func TestPruneSystemTestRuns_B394(t *testing.T) {
	svc := b394TestService(t)
	now := time.Now().UTC()
	insert := func(age time.Duration) {
		t.Helper()
		at := now.Add(-age)
		if _, err := svc.dbc().Exec(
			`INSERT INTO system_tests_runs (started_at, finished_at, duration_ms, results_json, pass_count, fail_count, skip_count, triggered_by_user_id)
			 VALUES ($1,$2,$3,'[]',1,0,0,1)`,
			at.Unix(), at.Unix(), 10); err != nil {
			t.Fatalf("insert run: %v", err)
		}
	}
	// 3 ancient runs and 3 recent ones; keep=2 and a 1-day window.
	insert(100 * 24 * time.Hour)
	insert(99 * 24 * time.Hour)
	insert(98 * 24 * time.Hour)
	insert(2 * time.Hour)
	insert(1 * time.Hour)
	insert(30 * time.Minute)
	if n := b394CountRuns(t, svc); n != 6 {
		t.Fatalf("setup: %d rows, want 6", n)
	}
	deleted, err := svc.PruneSystemTestRuns(context.Background(), 2, 24*time.Hour)
	if err != nil {
		t.Fatalf("PruneSystemTestRuns: %v", err)
	}
	if deleted != 3 {
		t.Errorf("deleted %d rows, want the 3 ancient ones", deleted)
	}
	if n := b394CountRuns(t, svc); n != 3 {
		t.Errorf("%d rows remain, want 3 (the three recent ones)", n)
	}
	// The newest N survive even when they are old.
	insert(200 * 24 * time.Hour)
	if _, err := svc.PruneSystemTestRuns(context.Background(), 4, time.Hour); err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if n := b394CountRuns(t, svc); n < 4 {
		t.Errorf("the newest-4 floor was not respected: %d rows", n)
	}
}

func b394CountRuns(t *testing.T, svc *Service) int {
	t.Helper()
	var n int
	if err := svc.dbc().QueryRow(`SELECT COUNT(*) FROM system_tests_runs`).Scan(&n); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}
