// system_tests_runtime.go — running the registry and persisting the result.
//
// Split out of system_tests.go in refactor Phase D (2026-10-01). The registry
// itself stays in system_tests.go because its ORDER is observable (the page
// renders it, PersistRun writes results in it); everything that walks it lives
// here.

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/monitorinbox"
)

// testService is the runtime Service for in-process test
// closures. Set by SetTestService from main.go after
// constructing the admin Service. Guarded by testServiceMu.
var (
	testService   *Service
	testServiceMu sync.Mutex
)

// SetTestService wires the runtime admin Service into the
// test registry closures. Called from cmd/skygate/main.go
// after the admin Service is constructed.
func SetTestService(s *Service) {
	testServiceMu.Lock()
	defer testServiceMu.Unlock()
	testService = s
}

func getTestService() *Service {
	testServiceMu.Lock()
	defer testServiceMu.Unlock()
	return testService
}

// SystemRunSummary is the run-level metadata.
type SystemRunSummary struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Duration   string    `json:"duration"`
	TotalCount int       `json:"total_count"`
	Pass       int       `json:"pass"`
	Fail       int       `json:"fail"`
	Skip       int       `json:"skip"`
}

// RunAllTests runs every test in TestRegistry, returns the
// results + a summary. Each test has a 5s timeout to bound
// the total runtime. Tests are run sequentially.
func (s *Service) RunAllTests(ctx context.Context) ([]SystemTestResult, *SystemRunSummary) {
	if s == nil {
		s = getTestService()
	}
	if s == nil {
		return nil, nil
	}
	results := make([]SystemTestResult, 0, len(TestRegistry))
	summary := &SystemRunSummary{StartedAt: time.Now().UTC()}
	// B306 (v1.5.71): the catalogue is the static registry PLUS one generated test
	// per registered module (status + health), so "Run all" covers the project's
	// modules too instead of leaving them to their own page.
	for _, t := range s.AllTests() {
		testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		start := time.Now()
		status, output := t.Run(testCtx)
		cancel()
		results = append(results, SystemTestResult{
			Name:     t.Name,
			Category: t.Category,
			Status:   status,
			Output:   output,
			Duration: time.Since(start).String(),
		})
		switch status {
		case SystemTestPass:
			summary.Pass++
		case SystemTestFail:
			summary.Fail++
		case SystemTestSkip:
			summary.Skip++
		}
	}
	summary.FinishedAt = time.Now().UTC()
	summary.TotalCount = len(results)
	summary.Duration = summary.FinishedAt.Sub(summary.StartedAt).String()
	return results, summary
}

// PersistRun stores the result + summary in system_tests_runs.
// Called from the page after RunAllTests returns.
//
// 2026-08-05 v0.33.1.11 — replaced the hardcoded "?" placeholders
// (8 of them) with `placeholdersList(8)` so the same code
// works on both SQLite ("?,?,?") and PG ("$1,$2,...$8"). The
// pgx stdlib does NOT auto-convert "?" to "$N" (unlike lib/pq
// which did), so without this fix the prod PG backend rejects
// the INSERT with "syntax error at or near ','" and the
// /admin/system_tests page shows the error flash on every
// "Run all" click. The dispatch uses the same build-tag
// pattern as db.SetGlobalSetting + db.nowUnixSQL.
func (s *Service) PersistRun(ctx context.Context, results []SystemTestResult, summary *SystemRunSummary, userID int64) (int64, error) {
	if s == nil || s.dbc() == nil {
		return 0, errors.New("DB not available")
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return 0, err
	}
	durationMs := summary.FinishedAt.Sub(summary.StartedAt).Milliseconds()
	ph := db.PlaceholdersList(8)
	res, err := s.dbc().ExecContext(ctx, `
		INSERT INTO system_tests_runs
			(started_at, finished_at, duration_ms, results_json,
			 pass_count, fail_count, skip_count, triggered_by_user_id)
		VALUES (`+ph+`)
	`, summary.StartedAt.Unix(), summary.FinishedAt.Unix(), durationMs,
		string(resultsJSON), summary.Pass, summary.Fail, summary.Skip, userID)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	// B305 (v1.5.70): a run's outcome must outlive the page render. Every FAIL
	// becomes an event in the monitoring inbox (deduped by test name, so a test
	// that keeps failing bumps its counter instead of flooding) and every PASS
	// RESOLVES the matching event, which is what makes the inbox honest: it shows
	// what is broken NOW, not what was broken once.
	s.ReportRunToMonitor(results)
	return id, nil
}

// ReportRunToMonitor records the outcome of a system-test run in the monitoring
// inbox (B305). Split out of PersistRun so it can be tested without a run row and
// so a future scheduled runner can reuse it.
func (s *Service) ReportRunToMonitor(results []SystemTestResult) {
	if s == nil {
		return
	}
	for _, res := range results {
		// B306 (v1.5.71): a generated module test reports under the MODULE's own
		// source, so the monitoring inbox can say "the tailscale module is broken"
		// instead of burying it among the in-process checks — and so the event's
		// fingerprint is per module (one row per module fault, resolved by the next
		// healthy run).
		source, fingerprint, subject := "system_test", "system_test:"+res.Name, res.Name
		if modName := ModuleNameFromTest(res.Name); modName != "" {
			source = "module:" + modName
			fingerprint = "module:" + modName + ":health"
			subject = modName
		}
		ev := monitorinbox.Event{
			Source:      source,
			Subject:     subject,
			Fingerprint: fingerprint,
			Link:        "/admin/system_tests",
		}
		if modName := ModuleNameFromTest(res.Name); modName != "" {
			ev.Link = "/admin/modules/" + modName
		}
		switch res.Status {
		case SystemTestFail:
			ev.Severity = monitorinbox.SeverityError
			if ModuleNameFromTest(res.Name) != "" {
				ev.Title = "Модуль неисправен: " + subject
			} else {
				ev.Title = "Системный тест не прошёл: " + res.Name
			}
			ev.Body = strings.TrimSpace(res.Category + " · " + truncateForEvent(res.Output, 400))
			s.MonitorReport(ev)
		case SystemTestPass:
			// Recovery: close the event if this test had one open.
			if in := s.MonitorInbox(); in != nil {
				_ = in.Resolve(ev)
			}
		default:
			// SKIP says nothing about health — a skipped test must neither open
			// nor close anything (a fresh install skips half the catalogue, and a
			// module that was never installed must not look like a fault).
		}
	}
}

// truncateForEvent keeps an event body readable in the list and in a Telegram
// message; the full output stays on /admin/system_tests where it belongs.
func truncateForEvent(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
