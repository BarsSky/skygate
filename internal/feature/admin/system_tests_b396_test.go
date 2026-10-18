package admin

// system_tests_b396_test.go — B396: the diagnostic catalogue must not answer one
// question from two sources, and it must name the cause it has already measured.
//
// All four defects below were found by running the B394 catalogue on the operator's
// live installation (2026-10-18) and reading the failures:
//
//	backup.recent            FAIL "newest backup ... is 819h39m0s old (>7d)"
//	backup.verify            PASS "last backup 20h38m0s ago: s3://skygate-backups/..."
//	derp.reachable           FAIL "dial tcp 127.0.0.1:443: connect: connection refused"
//	                         — the name resolves to the container's OWN loopback
//	prefix_owner.consistency FAIL "2 prefix(es) are not served as recorded"
//	                         — and nothing distinguished "not advertised" from
//	                           "advertised but not approved"
//	exit_rules.domain_prefix FAIL "7 of 12 ... stale"
//	                         — and nothing said whether the auto-updater would fix it
//
// Two backup tests, one question, two sources: the local directory and the run
// records. The one that reads the wrong source is the lie, so the run records win
// whenever they exist.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

// b396RunTest finds a registered test by name and runs it, failing the test when the
// name is not registered (a silently renamed test would make this file vacuous).
func b396RunTest(t *testing.T, name string) (SystemTestStatus, string) {
	t.Helper()
	for _, def := range TestRegistry {
		if def.Name == name {
			return def.Run(context.Background())
		}
	}
	t.Fatalf("no system test named %q in TestRegistry", name)
	return SystemTestSkip, ""
}

// b396Service wires a migrated in-memory SQLite database into the package-level test
// service the system tests read, and returns its handle.
func b396Service(t *testing.T) *dbDBSource {
	t.Helper()
	src := b282SQLiteDB(t)
	SetTestService(&Service{DB: src})
	t.Cleanup(func() { SetTestService(nil) })
	return src
}

func b396SetRun(t *testing.T, src *dbDBSource, age time.Duration, archive string) {
	t.Helper()
	d := src.db
	if err := skygatedb.SetGlobalSetting(d, "backup.last_run", strconv.FormatInt(time.Now().Add(-age).Unix(), 10)); err != nil {
		t.Fatalf("set backup.last_run: %v", err)
	}
	if err := skygatedb.SetGlobalSetting(d, "backup.last_archive", archive); err != nil {
		t.Fatalf("set backup.last_archive: %v", err)
	}
	if err := skygatedb.SetGlobalSetting(d, "backup.last_status", "ok"); err != nil {
		t.Fatalf("set backup.last_status: %v", err)
	}
}

func TestB396_AllLoopback(t *testing.T) {
	cases := []struct {
		addrs []string
		want  bool
	}{
		{[]string{"127.0.0.1"}, true},
		{[]string{"127.0.0.1", "::1"}, true},
		{[]string{"127.0.0.1", "192.0.2.10"}, false}, // one real address is enough to dial
		{[]string{"192.0.2.10"}, false},
		{[]string{"not-an-ip"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := b396AllLoopback(c.addrs); got != c.want {
			t.Errorf("b396AllLoopback(%v) = %v, want %v", c.addrs, got, c.want)
		}
	}
}

// TestB396_BackupRecentReadsTheRunRecords is the regression for the live disagreement:
// with a RECENT run record and an EMPTY local directory, backup.recent must PASS — the
// run records describe the backup whatever the destination is.
func TestB396_BackupRecentReadsTheRunRecords(t *testing.T) {
	src := b396Service(t)
	b396SetRun(t, src, 2*time.Hour, "s3://skygate-backups/portal/skygate-full-20261018_040000.tar.gz")
	st, msg := b396RunTest(t, "backup.recent")
	if st != SystemTestPass {
		t.Fatalf("backup.recent = %v (%s), want PASS: a 2 h old run record must satisfy it even with no local archive", st, msg)
	}
	if !strings.Contains(msg, "run records") {
		t.Errorf("the message does not say where it read the fact from: %q", msg)
	}
}

// TestB396_BackupPairAgreeOnTheSource pins the property the live installation broke:
// the two backup tests answer the SAME question, so a state that satisfies one must
// not contradict the other.
func TestB396_BackupPairAgreeOnTheSource(t *testing.T) {
	src := b396Service(t)
	b396SetRun(t, src, 3*time.Hour, "s3://bucket/fresh.tar.gz")
	for _, name := range []string{"backup.recent", "backup.verify"} {
		if st, msg := b396RunTest(t, name); st != SystemTestPass {
			t.Fatalf("%s = %v (%s), want PASS on a 3 h old successful run", name, st, msg)
		}
	}
	b396SetRun(t, src, 819*time.Hour, "s3://bucket/ancient.tar.gz")
	for _, name := range []string{"backup.recent", "backup.verify"} {
		if st, msg := b396RunTest(t, name); st != SystemTestFail {
			t.Fatalf("%s = %v (%s), want FAIL on an 819 h old run record", name, st, msg)
		}
	}
}
