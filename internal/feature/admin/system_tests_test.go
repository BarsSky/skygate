// system_tests_test.go — B78: the /admin/system_tests persistence layer.
//
// B78 (v0.33.1.26) made the page show the LAST persisted run on a cold load:
// GetAdminSystemTests calls ListLastRunWithResults and passes LastResults /
// LastSummary / LastRunID to the template, so the operator sees the real
// PASS/FAIL/SKIP icon (and the failure output) without clicking "Run all"
// first. The render half of that is pinned by
// internal/handlers/system_tests_render_test.go; THIS file pins the data half.
//
// History: the original TestListLastRunWithResults_* tests used a hand-rolled
// SQLite `:memory:` schema and were deleted with the v1.3.0 SQLite→PG purge;
// the file became a t.Skip stub while the package still compiled. These tests
// run against a REAL migrated database through the production open path
// (db.OpenWithDialect + db.ApplyMigrations) and seed rows through the
// production writer (Service.PersistRun) as well as directly, so both halves of
// the round trip are covered. No network, no docker, no sleeps.

package admin

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"skygate/internal/db"
)

// newB78DB opens a migrated in-memory SQLite database through the production
// open path. The DSN is a UNIQUELY named shared-cache database so it cannot
// share state with any other test in this package that also opens
// "file::memory:?cache=shared", and so a pooled second connection still sees
// the schema.
//
// The only skip is for an unavailable SQLite dialect (the pure-Go
// modernc.org/sqlite driver imported by internal/db) — never for a missing
// database, because the test creates its own.
func newB78DB(t *testing.T) *dbDBSource {
	t.Helper()
	_, sqlDB, err := db.OpenWithDialect("file:b78_system_tests_lastrun?mode=memory&cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable (modernc.org/sqlite): %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.ApplyMigrations(sqlDB, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return &dbDBSource{db: sqlDB}
}

// insertB78Run writes one system_tests_runs row directly (bypassing
// Service.PersistRun) and returns its id. Used where the test needs to control
// the raw results_json (empty / malformed) or the insert order.
func insertB78Run(t *testing.T, src *dbDBSource, startedAt, finishedAt, durationMs int64,
	resultsJSON string, pass, fail, skip int) int64 {
	t.Helper()
	res, err := src.Current().Exec(`
		INSERT INTO system_tests_runs
			(started_at, finished_at, duration_ms, results_json,
			 pass_count, fail_count, skip_count, triggered_by_user_id)
		VALUES (`+db.PlaceholdersList(8)+`)
	`, startedAt, finishedAt, durationMs, resultsJSON, pass, fail, skip, int64(1))
	if err != nil {
		t.Fatalf("insert system_tests_runs: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// TestListLastRunWithResults_RoundTripsPerTestStatus is the B78 contract: a run
// written through the production writer comes back with every per-test status,
// the failure OUTPUT included, plus the summary counts — which is exactly what
// the page needs to draw status icons and "this test failed with: …".
func TestListLastRunWithResults_RoundTripsPerTestStatus(t *testing.T) {
	src := newB78DB(t)
	svc := &Service{DB: src}
	ctx := context.Background()

	started := time.Unix(1_760_000_000, 0).UTC()
	finished := time.Unix(1_760_000_013, 0).UTC()
	results := []SystemTestResult{
		{Name: "net.tailscale_self", Category: "network", Status: SystemTestPass, Output: "tailscale0 interface is up", Duration: "1.2ms"},
		{Name: "db.integrity_check", Category: "db", Status: SystemTestFail, Output: "boom: no such table: meshes", Duration: "12ms"},
		{Name: "wal-g.backup", Category: "wal-g", Status: SystemTestSkip, Output: "wal-g not installed", Duration: "0s"},
	}
	summary := &SystemRunSummary{
		StartedAt:  started,
		FinishedAt: finished,
		// Deliberately NOT what comes back: the reader recomputes the duration
		// from duration_ms (FinishedAt - StartedAt) and never returns the
		// writer's string. 13 s here, so the assertion below is unambiguous.
		Duration:   "999ms",
		TotalCount: len(results),
		Pass:       1,
		Fail:       1,
		Skip:       1,
	}

	runID, err := svc.PersistRun(ctx, results, summary, 7)
	if err != nil {
		t.Fatalf("PersistRun: %v", err)
	}

	got, err := svc.ListLastRunWithResults(ctx)
	if err != nil {
		t.Fatalf("ListLastRunWithResults: %v", err)
	}
	if got == nil {
		t.Fatal("ListLastRunWithResults returned nil after a persisted run — the page would render gray circles")
	}
	if got.RunID != runID {
		t.Errorf("RunID = %d, want the persisted run id %d", got.RunID, runID)
	}
	if !got.StartedAt.Equal(started) || !got.FinishedAt.Equal(finished) {
		t.Errorf("started/finished = %s / %s, want %s / %s", got.StartedAt, got.FinishedAt, started, finished)
	}
	if got.Summary == nil {
		t.Fatal("Summary is nil — the page would have no pass/fail/skip counters")
	}
	if got.Summary.Pass != 1 || got.Summary.Fail != 1 || got.Summary.Skip != 1 || got.Summary.TotalCount != 3 {
		t.Errorf("Summary = %+v, want pass=1 fail=1 skip=1 total=3", *got.Summary)
	}
	if got.Summary.Duration != "13s" {
		t.Errorf("Summary.Duration = %q, want %q — the reader recomputes it from duration_ms, not from the writer's string", got.Summary.Duration, "13s")
	}
	if len(got.Results) != len(results) {
		t.Fatalf("Results = %d entries, want %d (%+v)", len(got.Results), len(results), got.Results)
	}
	// Per-test status must be populated for EVERY test, in the persisted order.
	for i, want := range results {
		if got.Results[i].Name != want.Name || got.Results[i].Status != want.Status ||
			got.Results[i].Category != want.Category || got.Results[i].Duration != want.Duration {
			t.Errorf("Results[%d] = %+v, want %+v", i, got.Results[i], want)
		}
	}
	// The failure output is the reason the operator opens the page: it must
	// survive the JSON round trip verbatim.
	if out := got.Results[1].Output; !strings.Contains(out, "no such table: meshes") {
		t.Errorf("failed test output = %q, want the SQL error preserved", out)
	}
	if got.Results[0].Status != SystemTestPass || got.Results[2].Status != SystemTestSkip {
		t.Errorf("per-test statuses = %q/%q, want pass/skip", got.Results[0].Status, got.Results[2].Status)
	}
}

// TestListLastRunWithResults_ReturnsTheLatestRow: the page shows ONE run. The
// query is ORDER BY id DESC LIMIT 1, so "latest" is insert order (the
// autoincrement id), not the largest started_at — pinned here with a
// deliberately out-of-order older-then-newer pair.
func TestListLastRunWithResults_ReturnsTheLatestRow(t *testing.T) {
	src := newB78DB(t)
	svc := &Service{DB: src}

	older := insertB78Run(t, src, 1_760_000_000, 1_760_000_001, 1000,
		`[{"name":"net.old","category":"network","status":"pass","output":"ok","duration":"1ms"}]`, 1, 0, 0)
	// Inserted SECOND (higher id) but STARTED EARLIER: the id must win.
	newer := insertB78Run(t, src, 1_700_000_000, 1_700_000_001, 2000,
		`[{"name":"net.new","category":"network","status":"fail","output":"nope","duration":"2ms"}]`, 0, 1, 0)

	got, err := svc.ListLastRunWithResults(context.Background())
	if err != nil {
		t.Fatalf("ListLastRunWithResults: %v", err)
	}
	if got == nil {
		t.Fatal("ListLastRunWithResults returned nil with two rows present")
	}
	if got.RunID != newer {
		t.Errorf("RunID = %d, want %d (the most recently INSERTed run is the one the page must show)", got.RunID, newer)
	}
	if got.RunID == older {
		t.Error("the page picked the older run — ORDER BY id DESC regressed to started_at")
	}
	if len(got.Results) != 1 || got.Results[0].Name != "net.new" {
		t.Errorf("Results = %+v, want only the latest run's result (net.new)", got.Results)
	}
	if got.Summary == nil || got.Summary.Fail != 1 || got.Summary.TotalCount != 1 {
		t.Errorf("Summary = %+v, want the latest row's counts (fail=1, total=1)", got.Summary)
	}
}

// TestListLastRunWithResults_NoRunsIsNotAnError pins the fresh-install
// contract: no rows at all → (nil, nil), because the page treats "no run yet"
// as "render the gray placeholders", not as a failure.
func TestListLastRunWithResults_NoRunsIsNotAnError(t *testing.T) {
	src := newB78DB(t)
	svc := &Service{DB: src}

	got, err := svc.ListLastRunWithResults(context.Background())
	if err != nil {
		t.Fatalf("ListLastRunWithResults on an empty table = %v, want no error (fresh install)", err)
	}
	if got != nil {
		t.Fatalf("ListLastRunWithResults on an empty table = %+v, want nil (no run to show)", got)
	}
}

// TestListLastRunWithResults_EmptyResultsJSONKeepsTheSummary: a run row whose
// results_json is "{}" (the column default) or "" still yields the summary, so
// the page can print "N pass / N fail" even when the per-test detail is
// missing. No error either way.
func TestListLastRunWithResults_EmptyResultsJSONKeepsTheSummary(t *testing.T) {
	for _, tc := range []struct {
		name        string
		resultsJSON string
	}{
		{"empty_object", "{}"},
		{"empty_string", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := newB78DB(t)
			svc := &Service{DB: src}
			id := insertB78Run(t, src, 1_760_000_000, 1_760_000_005, 5000, tc.resultsJSON, 2, 3, 4)

			got, err := svc.ListLastRunWithResults(context.Background())
			if err != nil {
				t.Fatalf("ListLastRunWithResults(%q) = %v, want no error", tc.resultsJSON, err)
			}
			if got == nil {
				t.Fatalf("ListLastRunWithResults(%q) returned nil — the summary row exists", tc.resultsJSON)
			}
			if got.RunID != id {
				t.Errorf("RunID = %d, want %d", got.RunID, id)
			}
			if len(got.Results) != 0 {
				t.Errorf("Results = %+v, want none for %q", got.Results, tc.resultsJSON)
			}
			if got.Summary == nil || got.Summary.Pass != 2 || got.Summary.Fail != 3 || got.Summary.Skip != 4 {
				t.Fatalf("Summary = %+v, want pass=2 fail=3 skip=4", got.Summary)
			}
			if got.Summary.TotalCount != 9 {
				t.Errorf("Summary.TotalCount = %d, want 9 (pass+fail+skip)", got.Summary.TotalCount)
			}
		})
	}
}

// TestListLastRunWithResults_MalformedJSONKeepsTheSummaryAndErrors: corrupt
// results_json must NOT hide the run. The summary is returned WITH a non-nil
// error naming the run, which is what GetAdminSystemTests audits as
// `system_tests_last_parse_error` while still rendering the counts.
func TestListLastRunWithResults_MalformedJSONKeepsTheSummaryAndErrors(t *testing.T) {
	src := newB78DB(t)
	svc := &Service{DB: src}
	id := insertB78Run(t, src, 1_760_000_000, 1_760_000_002, 2000, `{"not":"an array"`, 1, 1, 0)

	got, err := svc.ListLastRunWithResults(context.Background())
	if err == nil {
		t.Fatal("ListLastRunWithResults on corrupt results_json must return an error so the handler can audit it")
	}
	if !strings.Contains(err.Error(), "parse results_json") {
		t.Errorf("error = %v, want it to name the parse step", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("#%d", id)) {
		t.Errorf("error = %v, want it to name the run id #%d (the operator has to find the row)", err, id)
	}
	if got == nil {
		t.Fatal("a parse failure must still return the summary (the page renders the counts)")
	}
	if got.RunID != id {
		t.Errorf("RunID = %d, want %d", got.RunID, id)
	}
	if len(got.Results) != 0 {
		t.Errorf("Results = %+v, want none when the JSON cannot be parsed", got.Results)
	}
	if got.Summary == nil || got.Summary.Pass != 1 || got.Summary.Fail != 1 {
		t.Fatalf("Summary = %+v, want the stored pass=1 fail=1 counts", got.Summary)
	}
}

// TestListLastRunWithResults_UnconfiguredDBErrors: the defensive guards — a
// Service with no DB source, a nil *Service and a DBSource whose handle is nil
// — must return "DB not configured" instead of panicking (half-initialised
// boot, or a misconfigured CLI path).
func TestListLastRunWithResults_UnconfiguredDBErrors(t *testing.T) {
	ctx := context.Background()

	got, err := (&Service{}).ListLastRunWithResults(ctx)
	if err == nil || got != nil {
		t.Errorf("unconfigured Service = (%+v, %v), want (nil, an error)", got, err)
	}

	var nilSvc *Service
	if got, err := nilSvc.ListLastRunWithResults(ctx); err == nil || got != nil {
		t.Errorf("nil Service = (%+v, %v), want (nil, an error) instead of a panic", got, err)
	}

	// A Service whose DBSource reports a nil handle takes the same guard.
	if got, err := (&Service{DB: &dbDBSource{db: (*sql.DB)(nil)}}).ListLastRunWithResults(ctx); err == nil || got != nil {
		t.Errorf("Service with a nil *sql.DB = (%+v, %v), want (nil, an error)", got, err)
	}
}
