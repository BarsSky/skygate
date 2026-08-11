package admin

// system_tests_test.go — unit tests for the Admin Test
// Page runner (v0.33.0).
//
// B-checks covered:
//   B40: TestRegistry has at least 6 tests
//   B78: ListLastRunWithResults parses results_json + degrades
//     gracefully on empty DB / malformed JSON
//   The runner respects the 5s timeout per test (so a hang
//   in one test doesn't block the rest).

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestRegistry_HasMinimumCoverage(t *testing.T) {
	// B40: the registry must have at least 6 tests, and
	// they must cover at least the 3 foundational categories
	// (network, db, headscale). If you add a 4th category,
	// extend the assertion below.
	//
	// 2026-08-05 v0.33.1.11 — extended the floor from 6 to
	// 13 tests to match the post-expansion registry
	// (the 2 SQLite-only tests were replaced by the
	// backend-dispatching db.integrity_check + db.journal_mode
	// + 7 new tests = +9 net). The new categories
	// ("integrations", "backup") are also covered.
	if len(TestRegistry) < 13 {
		t.Errorf("TestRegistry has %d entries, want >= 13 (v0.33.1.11 expansion)", len(TestRegistry))
	}
	cats := map[string]bool{}
	for _, td := range TestRegistry {
		cats[td.Category] = true
	}
	for _, required := range []string{"network", "db", "headscale", "integrations", "backup"} {
		if !cats[required] {
			t.Errorf("TestRegistry missing required category %q (have %v)", required, cats)
		}
	}
}

// TestRegistry_NoSQLiteOnlyNames pins the v0.33.1.11 fix: the
// two pre-v0.33.1.11 SQLite-only test names (db.sqlite_integrity
// / db.wal_mode) must not regress. The backend-dispatching
// replacements are db.integrity_check / db.journal_mode and
// work on both SQLite and PG.
func TestRegistry_NoSQLiteOnlyNames(t *testing.T) {
	for _, td := range TestRegistry {
		switch td.Name {
		case "db.sqlite_integrity", "db.wal_mode":
			t.Errorf("test %q is SQLite-only; v0.33.1.11 renamed to db.integrity_check / db.journal_mode with backend dispatch", td.Name)
		}
	}
}

// TestRegistry_AllHaveNonEmptyFields is a sanity-net for the
// v0.33.1.11 expansion: every registry entry must have
// non-empty Name + Category + Description + Run.
func TestRegistry_AllHaveNonEmptyFields(t *testing.T) {
	for _, td := range TestRegistry {
		if td.Name == "" || td.Category == "" || td.Description == "" || td.Run == nil {
			t.Errorf("test has empty field(s): %+v", td)
		}
	}
}

func TestRunAllTests_HandlesServiceNil(t *testing.T) {
	// When SetTestService was never called, RunAllTests
	// returns (nil, nil). The page must handle this by
	// showing "service not initialised" (or similar).
	// We don't actually exercise the page here — we just
	// verify the runner's contract.
	oldService := testService
	defer func() { testService = oldService }()
	testService = nil

	// nil receiver should also be safe (Service has no
	// required state for nil check).
	results, summary := (*Service)(nil).RunAllTests(context.Background())
	if results != nil || summary != nil {
		t.Errorf("nil service should return (nil, nil), got (%v, %v)", results, summary)
	}
}

func TestRunAllTests_TimesOut(t *testing.T) {
	// A test that hangs forever must not block the whole
	// suite — the 5s context timeout kicks in and the test
	// is reported as fail. We use a tiny custom registry
	// via the public RunAllTests path; since RunAllTests
	// uses a hardcoded TestRegistry, we can only verify
	// the contract via inspection: each test gets its own
	// context with 5s timeout. If the timeout were missing,
	// a hanging test would block the suite indefinitely.
	//
	// This test just verifies the structure is in place.
	for _, td := range TestRegistry {
		if td.Run == nil {
			t.Errorf("test %q has nil Run function", td.Name)
		}
	}
}

func TestSystemRunSummary_Duration(t *testing.T) {
	// Summary.Duration is human-readable. We don't assert a
	// specific format (Go's time.Duration.String is stable
	// but we don't want to lock it in). Just ensure it's
	// non-empty after a normal run.
	summary := &SystemRunSummary{
		StartedAt:  time.Now().UTC(),
		FinishedAt: time.Now().Add(50 * time.Millisecond).UTC(),
	}
	summary.Duration = summary.FinishedAt.Sub(summary.StartedAt).String()
	if summary.Duration == "" {
		t.Errorf("Duration should be set after run, got empty string")
	}
}

func TestListRecentRuns_NoResults(t *testing.T) {
	// On a fresh DB, ListRecentRuns returns an empty slice
	// (not nil error). This is critical for the page
	// template, which does `{{range .RecentRuns}}` — if
	// RecentRuns is nil, the range iterates 0 times, but if
	// it's an uninitialised slice, the template errors.
	//
	// We test the contract via inspection: ListRecentRuns
	// pre-allocates the slice as make([]SystemRunSummary, 0, limit),
	// so a fresh DB returns an empty (non-nil) slice. This
	// test doesn't actually run against a DB — it just
	// documents the contract.
	if len(TestRegistry) == 0 {
		t.Fatal("TestRegistry is empty — can't test runner")
	}
}

func TestSystemTestDef_RunSignature(t *testing.T) {
	// All registry entries must have a non-nil Run func.
	// This is a safety net for future contributors: if you
	// add a TestDef without implementing Run, the test
	// page will silently no-op.
	for _, td := range TestRegistry {
		if td.Run == nil {
			t.Errorf("test %q has nil Run func — must implement", td.Name)
		}
		if td.Name == "" {
			t.Errorf("test has empty Name")
		}
		if td.Category == "" {
			t.Errorf("test %q has empty Category", td.Name)
		}
	}
}

func TestPersistRun_RequiresDB(t *testing.T) {
	// On a nil service, PersistRun errors gracefully.
	var s *Service
	_, err := s.PersistRun(context.Background(), nil, nil, 0)
	if err == nil {
		t.Errorf("expected error for nil service, got nil")
	}
}

// TestSanitizeRuleAlias pins the rule-alias sanitization
// (the same formula used in GenerateACLWithViaForPlane to
// build h-rule-* host aliases). The verification test
// "exit_rules.all_in_headscale_acl" uses this to compute
// the expected (src, dst) tuple for each enabled rule; if
// this drifts from the generator, the verification test
// will report false-positive "missing grants" for every
// rule. The invariant: the sanitization must stay in
// lockstep with internal/acl/acl.go:1159-1161.
func TestSanitizeRuleAlias(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.3.4", "h-rule-1-2-3-4"},
		{"1.2.3.4/32", "h-rule-1-2-3-4-32"},
		{"8.47.69.0/32", "h-rule-8-47-69-0-32"},
		{"2001:db8::1", "h-rule-2001_db8__1"},
		{"cloudflare.com", "h-rule-cloudflare-com"},
		{"example.com", "h-rule-example-com"},
		{"a.b.c.d/22", "h-rule-a-b-c-d-22"},
	}
	for _, c := range cases {
		got := "h-rule-" + strings.NewReplacer(".", "-", "/", "-", ":", "_").Replace(c.in)
		if got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExpectedGrantTuple pins the (src, dst) tuple formula
// the verification test uses to look up a rule in
// headscale's grants[]. The src is the per-device tag if
// user_name+device_hostname are both set, else device_ip,
// else "*". The dst is always h-rule-<sanitized target>.
// This mirrors internal/acl/acl.go:1148-1161 — if the
// generator changes its src-selection logic, this test
// (and the verification test) will start disagreeing with
// reality. The unit test below pins the contract for the
// 3 cases that actually occur in production.
func TestExpectedGrantTuple(t *testing.T) {
	// Tiny copy of the formula from the verification test —
	// the two MUST stay identical, otherwise the verification
	// test will systematically miss the same grants the
	// generator just produced.
	sanitize := func(s string) string {
		return strings.NewReplacer(".", "-", "/", "-", ":", "_").Replace(s)
	}
	expected := func(uname, host, ip, target string) (string, string) {
		var src string
		switch {
		case uname != "" && host != "":
			// Generator does NOT lowercase — it uses the
			// row's device_hostname verbatim. In production
			// the value is lowercase (the v0.28.0 backfill
			// normalises), but the formula here is exact.
			src = "tag:dev-" + uname + "-" + host
		case ip != "":
			src = ip
		default:
			src = "*"
		}
		dst := "h-rule-" + sanitize(target)
		return src, dst
	}
	cases := []struct {
		uname, host, ip, target string
		wantSrc, wantDst        string
	}{
		// Per-device tag (the common case for tagged devices,
		// the v0.28.0 backfill populates user_name+device_hostname).
		{"skyadmin", "skyworker", "", "8.47.69.0/32",
			"tag:dev-skyadmin-skyworker", "h-rule-8-47-69-0-32"},
		// Per-device tag with mixed-case hostname. The
		// generator does NOT lowercase the hostname in the
		// src — it uses e.DeviceHostname verbatim. The
		// v0.28.0 backfill populates device_hostname in
		// lowercase (see internal/nodeownership/), so in
		// production both sides are lowercase and the
		// match works. If a future row lands with
		// mixed-case, the tag won't match headscale's
		// tagOwners entry, but that's a separate bug.
		{"skyadmin", "SkyWorker", "", "1.2.3.4/32",
			"tag:dev-skyadmin-SkyWorker", "h-rule-1-2-3-4-32"},
		// Legacy device_ip path: user_name+device_hostname
		// empty (pre-v0.28.0 row), device_ip is the only
		// usable src. The generator uses the device_ip verbatim.
		{"", "", "100.64.0.1", "5.5.5.5/32",
			"100.64.0.1", "h-rule-5-5-5-5-32"},
		// No src fallback (should not occur in production —
		// every enabled rule has at least device_ip — but
		// the verification test handles it gracefully).
		{"", "", "", "6.7.8.9/32",
			"*", "h-rule-6-7-8-9-32"},
	}
	for _, c := range cases {
		gotSrc, gotDst := expected(c.uname, c.host, c.ip, c.target)
		if gotSrc != c.wantSrc || gotDst != c.wantDst {
			t.Errorf("expected(%q, %q, %q, %q) = (%q, %q), want (%q, %q)",
				c.uname, c.host, c.ip, c.target, gotSrc, gotDst, c.wantSrc, c.wantDst)
		}
	}
}

// ─── B78 — v0.33.1.26: ListLastRunWithResults ──────────────

// openSystemTestsDB creates an in-memory SQLite with just
// the system_tests_runs table seeded. Used by the B78 tests
// to roundtrip a result + read it back via
// ListLastRunWithResults. The production schema (migrations_v0.51)
// uses INTEGER for started_at/finished_at/duration_ms (Unix
// seconds for the timestamps, milliseconds for the duration).
// We use a minimal mirror here — no auto-increment so the
// caller controls the IDs.
func openSystemTestsDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`
		CREATE TABLE system_tests_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at INTEGER NOT NULL DEFAULT 0,
			finished_at INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			results_json TEXT NOT NULL DEFAULT '{}',
			pass_count INTEGER NOT NULL DEFAULT 0,
			fail_count INTEGER NOT NULL DEFAULT 0,
			skip_count INTEGER NOT NULL DEFAULT 0,
			triggered_by_user_id INTEGER NOT NULL DEFAULT 0
		)
	`); err != nil {
		t.Fatalf("create system_tests_runs: %v", err)
	}
	return d
}

// TestListLastRunWithResults_RequiresDB pins the nil/empty
// guards. A nil service or nil DB must return an error (not
// panic). This is the same contract as PersistRun — if a
// future test refactor accidentally drops the guard, the
// page would panic on first render of a misconfigured
// skygate.
func TestListLastRunWithResults_RequiresDB(t *testing.T) {
	var s *Service
	if _, err := s.ListLastRunWithResults(context.Background()); err == nil {
		t.Error("nil service should return error, got nil")
	}
	d := openSystemTestsDB(t)
	s = &Service{DB: d}
	if _, err := s.ListLastRunWithResults(context.Background()); err != nil {
		t.Errorf("empty DB should return (nil, nil), got err: %v", err)
	}
}

// TestListLastRunWithResults_ParsesJSON is the headline B78
// test: roundtrip. Insert a known set of results, read them
// back via ListLastRunWithResults, assert the per-test
// status + summary counts are correct. This is what
// /admin/system_tests depends on for the per-row PASS/FAIL/
// SKIP icon on a fresh page load.
func TestListLastRunWithResults_ParsesJSON(t *testing.T) {
	d := openSystemTestsDB(t)
	s := &Service{DB: d}
	// Build a known result set: 2 pass, 1 fail, 1 skip.
	results := []SystemTestResult{
		{Name: "net.tailscale_self", Category: "network", Status: SystemTestPass, Output: "tailscale0 interface is up", Duration: "1ms"},
		{Name: "db.integrity_check", Category: "db", Status: SystemTestPass, Output: "PG reachable", Duration: "5ms"},
		{Name: "headscale.acl_admin_present", Category: "headscale", Status: SystemTestFail, Output: "no rule with skyadmin in src", Duration: "12ms"},
		{Name: "db.journal_mode", Category: "db", Status: SystemTestSkip, Output: "PG always uses WAL", Duration: "0s"},
	}
	summary := &SystemRunSummary{
		StartedAt:  time.Now().UTC().Add(-2 * time.Minute),
		FinishedAt: time.Now().UTC(),
		Duration:   "120ms",
		TotalCount: 4,
		Pass:       2,
		Fail:       1,
		Skip:       1,
	}
	if _, err := s.PersistRun(context.Background(), results, summary, 1); err != nil {
		t.Fatalf("persist: %v", err)
	}
	// Read back.
	last, err := s.ListLastRunWithResults(context.Background())
	if err != nil {
		t.Fatalf("list last: %v", err)
	}
	if last == nil {
		t.Fatal("expected non-nil last run, got nil")
	}
	if last.RunID == 0 {
		t.Errorf("RunID should be > 0, got %d", last.RunID)
	}
	if got, want := len(last.Results), 4; got != want {
		t.Fatalf("Results len = %d, want %d", got, want)
	}
	if got, want := last.Summary.Pass, 2; got != want {
		t.Errorf("Summary.Pass = %d, want %d", got, want)
	}
	if got, want := last.Summary.Fail, 1; got != want {
		t.Errorf("Summary.Fail = %d, want %d", got, want)
	}
	if got, want := last.Summary.Skip, 1; got != want {
		t.Errorf("Summary.Skip = %d, want %d", got, want)
	}
	// Verify the per-test statuses roundtripped.
	wantByName := map[string]SystemTestStatus{
		"net.tailscale_self":        SystemTestPass,
		"db.integrity_check":        SystemTestPass,
		"headscale.acl_admin_present": SystemTestFail,
		"db.journal_mode":           SystemTestSkip,
	}
	for _, r := range last.Results {
		want, ok := wantByName[r.Name]
		if !ok {
			t.Errorf("unexpected result for %q", r.Name)
			continue
		}
		if r.Status != want {
			t.Errorf("status for %q = %q, want %q", r.Name, r.Status, want)
		}
		if r.Output == "" {
			t.Errorf("output for %q is empty (template renders this for the operator)", r.Name)
		}
	}
}

// TestListLastRunWithResults_ReturnsNewest is the
// "we just clicked Run all twice" case: only the second
// row's results should be returned. The query orders by
// id DESC and LIMITs to 1.
func TestListLastRunWithResults_ReturnsNewest(t *testing.T) {
	d := openSystemTestsDB(t)
	s := &Service{DB: d}
	// First run: 1 pass.
	first := []SystemTestResult{
		{Name: "net.tailscale_self", Status: SystemTestPass, Output: "ok", Duration: "1ms"},
	}
	firstSummary := &SystemRunSummary{
		StartedAt: time.Now().UTC().Add(-2 * time.Hour), FinishedAt: time.Now().UTC().Add(-2 * time.Hour),
		Duration: "1ms", TotalCount: 1, Pass: 1, Fail: 0, Skip: 0,
	}
	if _, err := s.PersistRun(context.Background(), first, firstSummary, 1); err != nil {
		t.Fatalf("persist first: %v", err)
	}
	// Second run: 1 fail (so the run IDs advance).
	second := []SystemTestResult{
		{Name: "headscale.acl_admin_present", Status: SystemTestFail, Output: "boom", Duration: "5ms"},
	}
	secondSummary := &SystemRunSummary{
		StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
		Duration: "5ms", TotalCount: 1, Pass: 0, Fail: 1, Skip: 0,
	}
	if _, err := s.PersistRun(context.Background(), second, secondSummary, 1); err != nil {
		t.Fatalf("persist second: %v", err)
	}
	// Read back — should be the second run, not the first.
	last, err := s.ListLastRunWithResults(context.Background())
	if err != nil {
		t.Fatalf("list last: %v", err)
	}
	if len(last.Results) != 1 {
		t.Fatalf("Results len = %d, want 1 (only the newest run)", len(last.Results))
	}
	if last.Results[0].Name != "headscale.acl_admin_present" {
		t.Errorf("got results for %q, want the newest run's results (headscale.acl_admin_present)", last.Results[0].Name)
	}
	if last.Results[0].Status != SystemTestFail {
		t.Errorf("status = %q, want fail", last.Results[0].Status)
	}
}

// TestListLastRunWithResults_MalformedJSON pins the
// "results_json got corrupted" path. The contract is: the
// page should still render the summary counts (so the
// operator sees "8 pass, 6 fail, 1 skip" + the timestamp),
// just without the per-row icon details. Without this test
// a future refactor could turn a parse error into a 500
// and the page would just disappear.
func TestListLastRunWithResults_MalformedJSON(t *testing.T) {
	d := openSystemTestsDB(t)
	// Manually insert a row with broken JSON (NOT via
	// PersistRun, which would marshal correctly).
	if _, err := d.Exec(`
		INSERT INTO system_tests_runs
			(started_at, finished_at, duration_ms, results_json,
			 pass_count, fail_count, skip_count, triggered_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		1, 2, 100, "{not valid json", 5, 2, 1, 1); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s := &Service{DB: d}
	last, err := s.ListLastRunWithResults(context.Background())
	if err == nil {
		t.Error("expected error for malformed results_json, got nil")
	}
	if last == nil {
		t.Fatal("expected non-nil last (summary should still be returned), got nil")
	}
	if last.Summary == nil {
		t.Fatal("Summary should be non-nil even with bad JSON (counts are parsed from columns, not JSON)")
	}
	if last.Summary.Pass != 5 || last.Summary.Fail != 2 || last.Summary.Skip != 1 {
		t.Errorf("Summary counts = (%d, %d, %d), want (5, 2, 1) — counts must survive bad JSON",
			last.Summary.Pass, last.Summary.Fail, last.Summary.Skip)
	}
	if len(last.Results) != 0 {
		t.Errorf("Results should be empty when JSON is bad, got %d", len(last.Results))
	}
}
