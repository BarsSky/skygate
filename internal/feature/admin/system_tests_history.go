// 2026-08-18 (TD-8, v1.4.4) — per-test history aggregation for
// /admin/system_tests "History" tab.
//
// Pre-TD-8 the page rendered the test registry as a grid + a
// "Recent runs (last 20)" strip with aggregate pass/fail/skip
// counts. The strip tells the operator "what was the overall
// result of each run" but not "which tests are flaky" or
// "which tests have been failing for a week". The new
// History tab answers those questions by aggregating per-test
// pass/fail/skip across ALL runs in a configurable window.
//
// Why per-test aggregation matters:
//   - A test that fails 1-in-20 times is a real flake (catches
//     a class of bugs that single-shot tests miss).
//   - A test that fails 20-in-20 times is broken AND has been
//     broken long enough to be visible in the audit log — but
//     the operator may not check the audit log daily.
//   - The aggregate table sorts by fail_count DESC so the
//     worst offenders bubble to the top.
//
// The aggregation is a pure-Go computation over the in-memory
// TestRegistry + the rows read from system_tests_runs. No
// SQL aggregation is needed (SQLite/PG JSON parsing inside
// the query would be slower + harder to unit-test).

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"skygate/internal/db"
)

// TestHistoryRow is one row of the per-test history
// aggregation. One row per SystemTestDef in TestRegistry
// (the operator sees ALL tests, even ones that have never
// run — they show 0/0/0 + LastStatus="" + "never run").
type TestHistoryRow struct {
	Name       string           `json:"name"`
	Category   string           `json:"category"`
	PassCount  int              `json:"pass_count"`
	FailCount  int              `json:"fail_count"`
	SkipCount  int              `json:"skip_count"`
	// LastStatus is the status of the most recent run that
	// included this test. Empty string means "never run in
	// the requested window" (the row still shows 0/0/0).
	LastStatus SystemTestStatus `json:"last_status"`
	// LastRunAt is the started_at of the run where
	// LastStatus was observed. Zero value if never run.
	LastRunAt time.Time `json:"last_run_at"`
	// LastError is the Output field of the most recent
	// FAILED run for this test. Truncated to 200 chars
	// (a long stack trace is unreadable in a table cell
	// anyway; the operator can click into a specific run
	// for the full output). Empty for pass/skip.
	LastError string `json:"last_error"`
}

// PassRate returns a 0-100 integer percentage of runs that
// passed for this test. Returns 0 when the test has never
// run (TotalRuns == 0). Used by the template to colour
// the cell (green ≥ 95%, yellow 50-95%, red < 50%).
func (r TestHistoryRow) PassRate() int {
	total := r.PassCount + r.FailCount + r.SkipCount
	if total == 0 {
		return 0
	}
	return (r.PassCount * 100) / total
}

// TotalRuns returns the total number of runs that included
// this test in the requested window. May be less than the
// global TotalRuns (some tests SKIP on a given run due to
// environment / feature flag).
func (r TestHistoryRow) TotalRuns() int {
	return r.PassCount + r.FailCount + r.SkipCount
}

// TestHistory is the response of ComputeTestHistory.
// Aggregated across all runs in [since, ∞) (or [since,
// until] when until is non-zero).
type TestHistory struct {
	// Rows is the per-test aggregate table, sorted by
	// FailCount DESC, then Name ASC (so the worst
	// offenders are at the top and ties break
	// alphabetically).
	Rows []TestHistoryRow
	// TotalRuns is the number of system_tests_runs rows
	// in the window. Used by the template to render the
	// "Total runs: N" stat.
	TotalRuns int
	// OldestRun / NewestRun are the started_at of the
	// earliest / latest run in the window. Zero if the
	// window is empty.
	OldestRun time.Time
	NewestRun time.Time
	// TotalDuration is the sum of duration_ms across all
	// runs in the window. The template renders this as a
	// human-readable "Xh Ym Zs" (the sum of 20 2-second
	// runs is 40s; on a busy deployment this can grow
	// into minutes).
	TotalDuration time.Duration
	// Since is the lower bound of the window (inclusive).
	// The template echoes this back in the window label.
	Since time.Time
	// Until is the upper bound of the window. Zero means
	// "open-ended" (i.e. from since to now).
	Until time.Time
	// WindowLabel is the human-readable window
	// description ("Last 7 days", "Last 30 days",
	// "All time", or a custom date range). Set by the
	// caller (the handler picks the label from the
	// ?window= query param).
	WindowLabel string
}

// ComputeTestHistory returns the per-test aggregate table
// for runs in the [since, until) window. If until is the
// zero value, the window is [since, ∞) (i.e. "from since
// to now"). The aggregation walks every run in the window
// + parses its results_json, so the cost is O(N) where N
// is the number of runs (typically <100 even on busy
// deployments).
//
// Errors:
//   - sql.ErrNoRows: no runs in the window (not an error
//     for the caller — the page renders an empty state).
//   - JSON parse error on any run: the run is SKIPPED
//     and a warning is logged via the audit log. The
//     aggregate continues over the remaining runs (one
//     corrupt run shouldn't blank the whole history).
func (s *Service) ComputeTestHistory(ctx context.Context, since, until time.Time) (TestHistory, error) {
	if s == nil || s.dbc() == nil {
		return TestHistory{}, errors.New("DB not configured")
	}
	var out TestHistory
	out.Since = since
	out.Until = until

	// Build the query. When until is non-zero, add an
	// upper bound; otherwise unbounded.
	var rows *sql.Rows
	var err error
	if until.IsZero() {
		rows, err = s.dbc().QueryContext(ctx, `
			SELECT id, started_at, finished_at, duration_ms, results_json,
			       pass_count, fail_count, skip_count
			FROM system_tests_runs
			WHERE started_at >= `+db.PlaceholdersList(1)+`
			ORDER BY id ASC
		`, since.Unix())
	} else {
		rows, err = s.dbc().QueryContext(ctx, `
			SELECT id, started_at, finished_at, duration_ms, results_json,
			       pass_count, fail_count, skip_count
			FROM system_tests_runs
			WHERE started_at >= `+db.PlaceholdersList(2)+`
			  AND started_at <  `+db.PlaceholdersList(2)+`
			ORDER BY id ASC
		`, since.Unix(), until.Unix())
	}
	if err != nil {
		return out, err
	}
	defer rows.Close()

	// accumulator[testName] = {PassCount, FailCount, SkipCount,
	//                           LastStatus, LastRunAt, LastError}.
	// We use a fixed-size map (Go's map) keyed on the test
	// name. The values are struct literals — we never need
	// to know the per-test row shape until we serialize
	// to TestHistoryRow.
	type acc struct {
		pass, fail, skip                  int
		lastStatus                        SystemTestStatus
		lastRunAt                         time.Time
		lastError                         string
	}
	accumulator := make(map[string]*acc, len(TestRegistry))

	// Initialize accumulator with EVERY registered test so
	// the operator sees a row for tests that have never
	// been run in the window (0/0/0 + LastStatus="").
	for _, def := range TestRegistry {
		accumulator[def.Name] = &acc{}
	}

	var (
		totalDurationMs int64
		oldestAt        int64
		newestAt        int64
	)
	for rows.Next() {
		var (
			id, startedAt, finishedAt, durationMs int64
			resultsJSON                            string
			pass, fail, skip                       int
		)
		if err := rows.Scan(&id, &startedAt, &finishedAt, &durationMs,
			&resultsJSON, &pass, &fail, &skip); err != nil {
			return out, err
		}
		out.TotalRuns++
		totalDurationMs += durationMs
		if oldestAt == 0 || startedAt < oldestAt {
			oldestAt = startedAt
		}
		if startedAt > newestAt {
			newestAt = startedAt
		}

		// Parse the JSON. A corrupt JSON doesn't
		// kill the whole aggregation — we log the
		// skip and continue with the next run.
		var results []SystemTestResult
		if err := json.Unmarshal([]byte(resultsJSON), &results); err != nil {
			if s.Backend != nil {
				s.Backend.Audit(0, "system", "system_tests_history_parse_error",
					fmt.Sprintf("run #%d: %v", id, err))
			}
			continue
		}

		// Walk every test in this run + update the
		// accumulator. The accumulator entry
		// already exists (from the TestRegistry
		// seed above) so a test that disappears
		// from the registry since v1.3.x doesn't
		// show up at all (avoids orphan rows in
		// the UI).
		startedAtTime := time.Unix(startedAt, 0).UTC()
		for _, res := range results {
			a, ok := accumulator[res.Name]
			if !ok {
				// The run included a test
				// that's no longer in the
				// registry (e.g. an older
				// v0.x test that was removed
				// post-v1.3.0). Create a
				// synthetic entry so the
				// operator can still see the
				// "this test was removed"
				// signal in the history.
				a = &acc{}
				accumulator[res.Name] = a
			}
			// The accumulator tracks the MOST
			// RECENT run for each test (rows
			// are sorted ASC by id so the
			// newer runs overwrite the older
			// ones). For LastStatus, only
			// overwrite if this run is newer.
			if !a.lastRunAt.IsZero() && startedAtTime.Before(a.lastRunAt) {
				// Older than the existing
				// entry — just bump the
				// counter.
				continue
			}
			switch res.Status {
			case SystemTestPass:
				a.pass++
				a.lastStatus = SystemTestPass
				a.lastRunAt = startedAtTime
				a.lastError = ""
			case SystemTestFail:
				a.fail++
				a.lastStatus = SystemTestFail
				a.lastRunAt = startedAtTime
				a.lastError = truncateForHistory(res.Output, 200)
			case SystemTestSkip:
				a.skip++
				a.lastStatus = SystemTestSkip
				a.lastRunAt = startedAtTime
				a.lastError = ""
			default:
				// Unknown status — count as
				// skip for the histogram and
				// don't update lastStatus
				// (we don't know what the
				// value means).
				a.skip++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}

	// Convert accumulator → TestHistoryRow slice. We
	// iterate TestRegistry (so the output is in a
	// stable order with known tests first) + then any
	// "removed" tests (sorted alphabetically) so the
	// operator sees the removed-test signal at the
	// bottom.
	rows2 := make([]TestHistoryRow, 0, len(accumulator))
	for _, def := range TestRegistry {
		a := accumulator[def.Name]
		rows2 = append(rows2, TestHistoryRow{
			Name:       def.Name,
			Category:   def.Category,
			PassCount:  a.pass,
			FailCount:  a.fail,
			SkipCount:  a.skip,
			LastStatus: a.lastStatus,
			LastRunAt:  a.lastRunAt,
			LastError:  a.lastError,
		})
		delete(accumulator, def.Name)
	}
	// Remaining entries are tests that ran in this
	// window but no longer exist in TestRegistry. Sort
	// alphabetically and append with Category="(removed)".
	removed := make([]string, 0, len(accumulator))
	for name := range accumulator {
		removed = append(removed, name)
	}
	sort.Strings(removed)
	for _, name := range removed {
		a := accumulator[name]
		rows2 = append(rows2, TestHistoryRow{
			Name:       name,
			Category:   "(removed)",
			PassCount:  a.pass,
			FailCount:  a.fail,
			SkipCount:  a.skip,
			LastStatus: a.lastStatus,
			LastRunAt:  a.lastRunAt,
			LastError:  a.lastError,
		})
	}

	// Sort: FailCount DESC, then PassRate ASC (a
	// "0% pass rate" test is more concerning than a
	// "20% pass rate" one with the same FailCount),
	// then Name ASC for stable ordering.
	sort.SliceStable(rows2, func(i, j int) bool {
		if rows2[i].FailCount != rows2[j].FailCount {
			return rows2[i].FailCount > rows2[j].FailCount
		}
		if rows2[i].PassRate() != rows2[j].PassRate() {
			return rows2[i].PassRate() < rows2[j].PassRate()
		}
		return rows2[i].Name < rows2[j].Name
	})

	out.Rows = rows2
	out.TotalDuration = time.Duration(totalDurationMs) * time.Millisecond
	if oldestAt > 0 {
		out.OldestRun = time.Unix(oldestAt, 0).UTC()
	}
	if newestAt > 0 {
		out.NewestRun = time.Unix(newestAt, 0).UTC()
	}
	return out, nil
}

// truncateForHistory clamps a test output string to
// max bytes. Used for the LastError column so a 10KB
// stack trace doesn't blow up the history table cell
// width. The full output is in the system_tests_runs
// results_json if the operator wants the whole thing.
func truncateForHistory(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// HistoryWindow is a (since, until) pair parsed from
// the ?window=7d|30d|all URL query parameter. When
// "all" is requested, Until is the zero value (the
// ComputeTestHistory reader treats zero as "no upper
// bound").
type HistoryWindow struct {
	Since time.Time
	Until time.Time
	Label string
}

// ParseHistoryWindow returns the (since, until) pair
// for a window string. The default (unknown / empty)
// is "last 7 days" — the operator's typical
// "yesterday's tests" view.
func ParseHistoryWindow(window string, now time.Time) HistoryWindow {
	switch window {
	case "30d":
		return HistoryWindow{
			Since: now.AddDate(0, 0, -30),
			Until: time.Time{},
			Label: "Last 30 days",
		}
	case "all":
		return HistoryWindow{
			Since: time.Unix(0, 0),
			Until: time.Time{},
			Label: "All time",
		}
	case "7d", "":
		return HistoryWindow{
			Since: now.AddDate(0, 0, -7),
			Until: time.Time{},
			Label: "Last 7 days",
		}
	default:
		// Unknown window → default to 7d. Better
		// than panicking on a typo'd URL.
		return HistoryWindow{
			Since: now.AddDate(0, 0, -7),
			Until: time.Time{},
			Label: "Last 7 days",
		}
	}
}
