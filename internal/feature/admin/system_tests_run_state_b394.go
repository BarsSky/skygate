package admin

// system_tests_run_state_b394.go — B394: a run must report PROGRESS.
//
// WHAT WAS WRONG
// --------------
// PostAdminSystemTestsRun ran the whole catalogue inline in the request: the
// browser sat on a blank page for as long as the slowest test took (5 s each, so
// tens of seconds), with no sign of which test was stuck, no partial results, and
// no way to run ONE test while looking at its previous failure. An operator
// diagnosing a flaky subsystem could not tell "slow" from "hung".
//
// WHAT THIS IS
// ------------
// One in-memory snapshot of the current (or last) run — the page is admin-only
// and single-operator, exactly like the update job's state. It is deliberately
// NOT persisted: the durable record is system_tests_runs, written once when a
// whole-catalogue run finishes. A single-test run updates the snapshot and the
// monitor inbox but writes no history row, because a history of one-test runs
// would bury the real runs.
//
// Restarting the process loses the snapshot; the page then shows the last
// persisted run, which is the truth.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SystemRunState is what the progress endpoint returns and what the template's
// poller consumes.
type SystemRunState struct {
	ID        string             `json:"id"`
	Started   time.Time          `json:"started_at"`
	Finished  time.Time          `json:"finished_at,omitempty"`
	Done      bool               `json:"done"`
	Current   string             `json:"current,omitempty"`
	Total     int                `json:"total"`
	Completed int                `json:"completed"`
	Results   []SystemTestResult `json:"results"`
	Summary   *SystemRunSummary  `json:"summary,omitempty"`
	Error     string             `json:"error,omitempty"`
	// Only is the single test this run was restricted to ("" = the whole
	// catalogue). The page uses it to say "running one test" honestly.
	Only string `json:"only,omitempty"`
	// Persisted is true when the run wrote a system_tests_runs row.
	Persisted bool `json:"persisted"`
}

// ErrSystemRunInFlight is returned when a run is already going. Two concurrent
// runs would share the snapshot and interleave their results.
var ErrSystemRunInFlight = errors.New("a system test run is already in progress")

var (
	systemRunMu sync.Mutex
	systemRun   *SystemRunState
)

// CurrentSystemRun returns a COPY of the snapshot (nil when nothing has run since
// the process started).
func (s *Service) CurrentSystemRun() *SystemRunState {
	systemRunMu.Lock()
	defer systemRunMu.Unlock()
	return copySystemRunLocked(systemRun)
}

// copySystemRunLocked copies the snapshot. THE CALLER MUST HOLD systemRunMu.
//
// B400.1 (2026-10-20) — the name says so because CI's `-race` run proved the rule the
// hard way: StartSystemRun spawned `runTestsAsync` and then copied the state it had
// just handed to that goroutine, so the copy raced the first write.
//
//	WARNING: DATA RACE
//	Read at 0x00c00052c0d8 by goroutine 759   (copySystemRun)
//	Previous write at 0x00c00052c0d8 by goroutine 761  (runTestsAsync)
//
// The read was harmless in practice (an empty Results slice) and invisible to every
// non-race run — which is exactly why the copy now happens BEFORE the goroutine is
// started, under the same lock that publishes the run.
func copySystemRunLocked(in *SystemRunState) *SystemRunState {
	if in == nil {
		return nil
	}
	out := *in
	if in.Results != nil {
		out.Results = append([]SystemTestResult(nil), in.Results...)
	}
	if in.Summary != nil {
		sum := *in.Summary
		out.Summary = &sum
	}
	return &out
}

// StartSystemRun launches the catalogue (or ONE test when only != "") in the
// background and returns immediately with the fresh snapshot.
//
// The caller is the HTTP handler: it answers 303 to ?run=<id> and the page polls
// GetAdminSystemTestsRunJSON until Done.
func (s *Service) StartSystemRun(userID int64, username, only string) (*SystemRunState, error) {
	if s == nil {
		return nil, errors.New("service not initialised")
	}
	systemRunMu.Lock()
	if systemRun != nil && !systemRun.Done {
		running := systemRun.Current
		systemRunMu.Unlock()
		return nil, fmt.Errorf("%w (currently running %q)", ErrSystemRunInFlight, running)
	}
	tests := s.testsForRun(only)
	if len(tests) == 0 {
		systemRunMu.Unlock()
		return nil, fmt.Errorf("no test matches %q", only)
	}
	state := &SystemRunState{
		ID:      generateRunID(),
		Started: time.Now().UTC(),
		Total:   len(tests),
		Only:    only,
		Results: make([]SystemTestResult, 0, len(tests)),
	}
	systemRun = state
	// B400.1: copy BEFORE the goroutine exists. The old order (spawn, then copy) was
	// the data race `-race` caught: goroutine 761 writes the run it was handed while
	// this goroutine reads it.
	snap := copySystemRunLocked(state)
	systemRunMu.Unlock()

	go s.runTestsAsync(context.Background(), state.ID, tests, userID, username, only)
	return snap, nil
}

// testsForRun is the subset StartSystemRun will execute, in registry order.
func (s *Service) testsForRun(only string) []SystemTestDef {
	all := s.AllTests()
	if only == "" {
		return all
	}
	for _, t := range all {
		if t.Name == only {
			return []SystemTestDef{t}
		}
	}
	return nil
}

// runTestsAsync executes the tests and updates the snapshot after EVERY one, so
// the poller sees progress rather than a finished blob.
func (s *Service) runTestsAsync(ctx context.Context, id string, tests []SystemTestDef, userID int64, username, only string) {
	summary := &SystemRunSummary{StartedAt: time.Now().UTC()}
	results := make([]SystemTestResult, 0, len(tests))
	for _, t := range tests {
		systemRunMu.Lock()
		if systemRun != nil && systemRun.ID == id {
			systemRun.Current = t.Name
		}
		systemRunMu.Unlock()

		budget := systemTestBudget(t)
		testCtx, cancel := context.WithTimeout(ctx, budget)
		start := time.Now()
		status, output := t.Run(testCtx)
		exhausted := budgetExhausted(testCtx)
		cancel()
		elapsed := time.Since(start)
		if exhausted {
			// B408: the harness stopped this test — say it, and do not let the
			// partial output masquerade as a measured fleet.
			output += budgetNote(budget, elapsed)
			if status == SystemTestPass {
				status = SystemTestFail
			}
		}
		res := SystemTestResult{
			Name:     t.Name,
			Category: t.Category,
			Status:   status,
			Output:   output,
			Duration: time.Since(start).String(),
		}
		results = append(results, res)
		switch status {
		case SystemTestPass:
			summary.Pass++
		case SystemTestFail:
			summary.Fail++
		case SystemTestSkip:
			summary.Skip++
		}

		systemRunMu.Lock()
		if systemRun != nil && systemRun.ID == id {
			systemRun.Results = append([]SystemTestResult(nil), results...)
			systemRun.Completed = len(results)
		}
		systemRunMu.Unlock()
	}
	summary.FinishedAt = time.Now().UTC()
	summary.Duration = summary.FinishedAt.Sub(summary.StartedAt).String()
	summary.TotalCount = len(results)

	persisted := false
	// B408.1: a single-test re-run is persisted TOO. Before this the result of
	// «прогнать этот» was thrown away, so after a page reload the row fell back to
	// the last FULL run — three days old on the operator's installation — and the
	// honest conclusion was «the re-run does not work». The scope (only_test) is
	// what keeps the history tab's rates comparable.
	if _, err := s.PersistRunScoped(ctx, results, summary, userID, only); err != nil {
		systemRunMu.Lock()
		if systemRun != nil && systemRun.ID == id {
			systemRun.Error = "persist: " + err.Error()
		}
		systemRunMu.Unlock()
	} else {
		persisted = true
		// B394 retention: keep the newest N and drop anything older than the
		// window, so the history tab cannot grow forever.
		if deleted, err := s.PruneSystemTestRuns(ctx, SystemTestRunsKeep, SystemTestRunsMaxAge); err == nil && deleted > 0 {
			if s.Backend != nil {
				s.Backend.Audit(userID, username,
					"system_tests_prune", fmt.Sprintf("deleted %d run(s) older than %s", deleted, SystemTestRunsMaxAge))
			}
		}
	}
	if only != "" {
		// Still report the single test to the monitoring inbox: an operator who
		// ran one failing test must see that failure land where every other
		// monitoring signal lives (and a PASS must resolve it).
		s.ReportRunToMonitor(results)
	}

	systemRunMu.Lock()
	if systemRun != nil && systemRun.ID == id {
		systemRun.Results = append([]SystemTestResult(nil), results...)
		systemRun.Summary = summary
		systemRun.Completed = len(results)
		systemRun.Current = ""
		systemRun.Done = true
		systemRun.Finished = summary.FinishedAt
		systemRun.Persisted = persisted
	}
	systemRunMu.Unlock()
}

// generateRunID is a short random identifier for the run snapshot. crypto/rand
// rather than math/rand so two runs started in the same millisecond cannot share
// an id (the poller keys on it).
func generateRunID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
