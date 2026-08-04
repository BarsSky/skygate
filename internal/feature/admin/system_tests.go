package admin

// system_tests.go — Admin Test Page (v0.33.0).
//
// The /admin/system_tests page lets the operator run a
// battery of system checks (network, db, headscale, disk,
// wal-g, replication) and see the result inline. Each
// test is a Go function that returns (status, output).
// Results are stored in the system_tests_runs table
// (migration v0.51) for the "history" strip on the page.
//
// Test definition lifecycle:
//
//   1. Add the test func to the TestRegistry below.
//   2. The /admin/system_tests page renders the registry as
//      a grid; "Run" buttons call the runner.
//   3. The runner stores the result in system_tests_runs and
//      returns the live JSON for the page.
//   4. The "History" column on the page reads the last 20
//      rows from system_tests_runs.
//
// All tests are best-effort and timeout-fast (≤ 5s each).
// A test that hangs is a bug — the timeout is a safety net.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"skygate/internal/headscale"
)

// SystemTestStatus is the result of one test.
type SystemTestStatus string

const (
	SystemTestPass SystemTestStatus = "pass"
	SystemTestFail SystemTestStatus = "fail"
	SystemTestSkip SystemTestStatus = "skip"
)

// SystemTestResult is the persisted shape of a single test run.
type SystemTestResult struct {
	Name     string            `json:"name"`
	Category string            `json:"category"`
	Status   SystemTestStatus  `json:"status"`
	Output   string            `json:"output"`
	Duration string            `json:"duration"`
}

// SystemTestDef is a registered test. Run returns
// (status, output). Closures capture *Service so they
// can read DB / headscale state at run time.
type SystemTestDef struct {
	Name        string
	Category    string // "network", "db", "headscale", "disk", "wal-g", "replication"
	Description string
	Run         func(ctx context.Context) (SystemTestStatus, string)
}

// TestRegistry is the full list of in-process tests. The
// /admin/system_tests page renders every entry.
var TestRegistry = []SystemTestDef{
	{
		Name:        "net.tailscale_self",
		Category:    "network",
		Description: "tailscale0 interface is up (Tailscale daemon alive in the container)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			out, err := os.ReadFile("/proc/net/dev")
			if err != nil {
				return SystemTestFail, "cannot read /proc/net/dev: " + err.Error()
			}
			if strings.Contains(string(out), "tailscale0") {
				return SystemTestPass, "tailscale0 interface is up"
			}
			return SystemTestFail, "tailscale0 interface not found (Tailscale may be down)"
		},
	},
	{
		Name:        "net.headscale_reachable",
		Category:    "network",
		Description: "Headscale container /api/v1/policy responds with a non-empty policy",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil {
				return SystemTestFail, "service not initialised"
			}
			hs := s.HSGlobalFn()
			if hs == nil {
				return SystemTestFail, "headscale client not configured"
			}
			raw, err := hs.GetACL()
			if err != nil {
				return SystemTestFail, "getacl: " + err.Error()
			}
			if len(strings.TrimSpace(raw)) == 0 {
				return SystemTestFail, "policy is empty"
			}
			return SystemTestPass, fmt.Sprintf("policy fetched (%d bytes)", len(raw))
		},
	},
	{
		Name:        "db.sqlite_integrity",
		Category:    "db",
		Description: "PRAGMA integrity_check returns 'ok' on the skygate.db file",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			var result string
			if err := s.DB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
				return SystemTestFail, err.Error()
			}
			if result != "ok" {
				return SystemTestFail, "integrity_check returned: " + result
			}
			return SystemTestPass, "integrity_check = ok"
		},
	},
	{
		Name:        "db.wal_mode",
		Category:    "db",
		Description: "SQLite is in WAL journal mode (concurrent reads + crash safety)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.DB == nil {
				return SystemTestFail, "DB not configured"
			}
			var mode string
			if err := s.DB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
				return SystemTestFail, err.Error()
			}
			if mode != "wal" {
				return SystemTestFail, "journal_mode is " + mode + " (expected wal)"
			}
			return SystemTestPass, "journal_mode = wal"
		},
	},
	{
		Name:        "headscale.peers_visible",
		Category:    "headscale",
		Description: "Headscale /api/v1/node returns a non-empty node list",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil {
				return SystemTestFail, "service not initialised"
			}
			hs := s.HSGlobalFn()
			if hs == nil {
				return SystemTestFail, "headscale client not configured"
			}
			nodes, err := hs.ListAllNodes()
			if err != nil {
				return SystemTestFail, "list nodes: " + err.Error()
			}
			if len(nodes) == 0 {
				return SystemTestFail, "no nodes registered"
			}
			online := 0
			for _, n := range nodes {
				if n.Online {
					online++
				}
			}
			return SystemTestPass, fmt.Sprintf("%d nodes (%d online)", len(nodes), online)
		},
	},
	{
		Name:        "headscale.acl_admin_present",
		Category:    "headscale",
		Description: "Headscale policy includes a rule with skyadmin in src (admin can reach all)",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil {
				return SystemTestFail, "service not initialised"
			}
			view, err := s.ListACL(ctx)
			if err != nil {
				return SystemTestFail, "list acl: " + err.Error()
			}
			hasAdmin := false
			for _, r := range view.AllACLs {
				for _, src := range r.Src {
					if strings.Contains(src, "skyadmin") {
						hasAdmin = true
						break
					}
				}
				if hasAdmin {
					break
				}
			}
			if !hasAdmin {
				return SystemTestFail, "no rule with skyadmin in src — admin has no access to any device"
			}
			return SystemTestPass, fmt.Sprintf("admin rule present (total acls=%d)", view.TotalCount)
		},
	},
}

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
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	Duration    string    `json:"duration"`
	TotalCount  int       `json:"total_count"`
	Pass        int       `json:"pass"`
	Fail        int       `json:"fail"`
	Skip        int       `json:"skip"`
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
	for _, t := range TestRegistry {
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
func (s *Service) PersistRun(ctx context.Context, results []SystemTestResult, summary *SystemRunSummary, userID int64) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, errors.New("DB not available")
	}
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return 0, err
	}
	durationMs := summary.FinishedAt.Sub(summary.StartedAt).Milliseconds()
	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO system_tests_runs
			(started_at, finished_at, duration_ms, results_json,
			 pass_count, fail_count, skip_count, triggered_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, summary.StartedAt.Unix(), summary.FinishedAt.Unix(), durationMs,
		string(resultsJSON), summary.Pass, summary.Fail, summary.Skip, userID)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListRecentRuns returns the last N runs (default 20) for
// the history strip on /admin/system_tests.
func (s *Service) ListRecentRuns(ctx context.Context, limit int) ([]SystemRunSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, started_at, finished_at, duration_ms,
		       pass_count, fail_count, skip_count
		FROM system_tests_runs
		ORDER BY id DESC LIMIT ?
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

// ensureListNodes is here to keep the import of headscale in
// the file's symbol table even when the test definitions
// don't reference it. The compiler can dead-code-eliminate
// the headscale import if no symbol from the package is
// referenced. We keep headscale imported for the future
// test additions (e.g. "headscale.exit_node_health").
var _ = (*headscale.Client)(nil)
var _ sql.IsolationLevel = 0
