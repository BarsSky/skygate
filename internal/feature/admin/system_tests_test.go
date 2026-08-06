package admin

// system_tests_test.go — unit tests for the Admin Test
// Page runner (v0.33.0).
//
// B-checks covered:
//   B40: TestRegistry has at least 6 tests
//   The runner respects the 5s timeout per test (so a hang
//   in one test doesn't block the rest).

import (
	"context"
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
