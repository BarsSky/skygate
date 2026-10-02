// system_tests_runs.go — the run-history READERS (CLI/JSON).
//
// Split out of system_tests.go in refactor Phase D (2026-10-01): the CLI/JSON
// run-history readers and the last-run-with-results view.
//
// NOTE: this file is deliberately NOT called system_tests_history.go. That name
// was already taken by the B144 History tab (ComputeTestHistory,
// ParseHistoryWindow), and the first attempt at this split WROTE OVER it — the
// split script must never write a file that already exists. It was restored from
// git; see docs/LESSONS.md L-53.

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// ListRecentRuns returns the last N runs (default 20) for
// the history strip on /admin/system_tests.
//
// 2026-08-05 v0.33.1.11 — LIMIT ? replaced with
// placeholdersList(1) for the same PG/SQLite dispatch
// reason as PersistRun (see comment there).
func (s *Service) ListRecentRuns(ctx context.Context, limit int) ([]SystemRunSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.dbc().QueryContext(ctx, `
		SELECT id, started_at, finished_at, duration_ms,
		       pass_count, fail_count, skip_count
		FROM system_tests_runs
		ORDER BY id DESC LIMIT `+db.PlaceholdersList(1)+`
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SystemRunSummary, 0, limit)
	for rows.Next() {
		var r SystemRunSummary
		var id, startedAt, finishedAt, durationMs, pass, fail, skip int64
		if err := rows.Scan(&id, &startedAt, &finishedAt, &durationMs,
			&pass, &fail, &skip); err != nil {
			return nil, err
		}
		_ = id
		r.StartedAt = time.Unix(startedAt, 0).UTC()
		r.FinishedAt = time.Unix(finishedAt, 0).UTC()
		r.Duration = (time.Duration(durationMs) * time.Millisecond).String()
		r.TotalCount = int(pass + fail + skip)
		r.Pass = int(pass)
		r.Fail = int(fail)
		r.Skip = int(skip)
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastRunWithResults is what ListLastRunWithResults
// returns: the most recent run's parsed test results +
// summary + when it started. Used by the
// /admin/system_tests page to render per-test PASS / FAIL /
// SKIP icons on initial page load (not just after "Run
// all" was clicked). Zero-value results means no runs yet.
//
// 2026-08-09 v0.33.1.26 — added. The pre-fix
// /admin/system_tests page only showed per-test status
// after a fresh "Run all" click (LiveResults was
// populated by the POST handler, not by GET). On a cold
// page load the operator saw a wall of gray circles
// instead of "this test failed with: ..." for the
// 6 broken tests. B78 wires the last persisted run from
// system_tests_runs into the page so the operator
// always sees the actual status of the last suite
// execution, including failure output, without having
// to click "Run all" first.
type LastRunWithResults struct {
	Results    []SystemTestResult
	Summary    *SystemRunSummary
	StartedAt  time.Time
	FinishedAt time.Time
	RunID      int64
}

// ListLastRunWithResults returns the most recent row from
// system_tests_runs with the results_json unmarshalled
// into SystemTestResult slice. Returns (nil, nil, no err)
// if no runs exist yet (fresh install). Returns the
// already-parsed results even if the JSON is malformed
// (returns a non-nil error and a partial result so the
// page degrades to "JSON parse error" rather than
// silently showing gray circles).
//
// 2026-08-09 v0.33.1.26 — added.
func (s *Service) ListLastRunWithResults(ctx context.Context) (*LastRunWithResults, error) {
	if s == nil || s.dbc() == nil {
		return nil, errors.New("DB not configured")
	}
	row := s.dbc().QueryRowContext(ctx, `
		SELECT id, started_at, finished_at, duration_ms,
		       results_json, pass_count, fail_count, skip_count
		FROM system_tests_runs
		ORDER BY id DESC
		LIMIT 1
	`)
	var (
		id, startedAt, finishedAt, durationMs int64
		resultsJSON                           string
		pass, fail, skip                      int
	)
	if err := row.Scan(&id, &startedAt, &finishedAt, &durationMs,
		&resultsJSON, &pass, &fail, &skip); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Fresh install: no runs yet. The page renders
			// the gray placeholders. Not an error.
			return nil, nil
		}
		return nil, err
	}
	out := &LastRunWithResults{
		StartedAt:  time.Unix(startedAt, 0).UTC(),
		FinishedAt: time.Unix(finishedAt, 0).UTC(),
		RunID:      id,
		Summary: &SystemRunSummary{
			StartedAt:  time.Unix(startedAt, 0).UTC(),
			FinishedAt: time.Unix(finishedAt, 0).UTC(),
			Duration:   (time.Duration(durationMs) * time.Millisecond).String(),
			TotalCount: pass + fail + skip,
			Pass:       pass,
			Fail:       fail,
			Skip:       skip,
		},
	}
	if resultsJSON == "" || resultsJSON == "{}" {
		return out, nil
	}
	var results []SystemTestResult
	if err := json.Unmarshal([]byte(resultsJSON), &results); err != nil {
		// Malformed JSON — return the summary but no
		// per-test details. The page will still show
		// the summary counts. The error is bubbled up
		// so the handler can log it.
		return out, fmt.Errorf("parse results_json (run #%d): %w", id, err)
	}
	out.Results = results
	return out, nil
}

// ensureListNodes is here to keep the import of headscale in
// the file's symbol table even when the test definitions
// don't reference it. The compiler can dead-code-eliminate
// the headscale import if no symbol from the package is
// referenced. We keep headscale imported for the future
// test additions (e.g. "headscale.exit_node_health").
var _ = (*headscale.Client)(nil)
var _ sql.IsolationLevel = 0
