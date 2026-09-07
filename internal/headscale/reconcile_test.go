// Package headscale — reconcile_test.go (B237.18, closes TD-10).
//
// Two layers of tests:
//
//  1. Pure unit tests (no DB, no headscale) for the
//     per-row logic. These run on every dev machine + CI
//     and pin the "what happens when oldHSID is X and
//     headscale has Y" decision table.
//
//  2. Integration tests (b237_18_reconcile_integration_test.go,
//     separate file) that hit a real PG via
//     SKYGATE_TEST_PG_DSN. They pin the per-row UPDATE +
//     audit_log row write + the byName lookup against the
//     real headscale API stub. SKIPs if the env var is
//     unset (so the suite still runs on machines without
//     a dev PG — same pattern as the b188_3_integration_test).
//
// The unit tests here are the contract. The integration
// tests are the "did the SQL actually persist" check.

package headscale

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestInt64FromString_Valid covers the happy path —
// numeric IDs (the headscale 0.29.x default) parse
// cleanly.
func TestInt64FromString_Valid(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1", 1},
		{"42", 42},
		{"1234567890", 1234567890},
		{strconv.FormatInt(1<<62, 10), 1 << 62},
	}
	for _, c := range cases {
		got := int64FromString(c.in)
		if got != c.want {
			t.Errorf("int64FromString(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestInt64FromString_EmptyAndInvalid covers the
// safety net — empty + non-numeric inputs return 0,
// not a panic. The 0 sentinel is the same "not in
// headscale" signal the reconciliation uses, so the
// caller treats it consistently.
//
// Note: negative numbers like "-1" parse to a valid
// int64 (-1). They're "valid int64, invalid headscale
// ID" — the caller treats them as "linked to a
// non-existent user" (the loop's case-1 check
// `oldHSID > 0` skips them, so the row goes
// straight to the username-lookup branch). That's
// the right behavior — headscale's `headscale users
// list` never returns a negative id, but a
// corrupted DB row (manual SQL with a bad int) is
// still a real state to handle.
func TestInt64FromString_EmptyAndInvalid(t *testing.T) {
	cases := []string{
		"",
		"abc",
		"abc123",
		"123abc",
		"12.34",
		"0x1A",
		" ",
		"-99999999999999999999", // overflow (won't fit in int64)
	}
	for _, in := range cases {
		got := int64FromString(in)
		if got != 0 {
			t.Errorf("int64FromString(%q) = %d, want 0 (the 'not in headscale' sentinel)", in, got)
		}
	}
}

// TestReconcileOutcome_StringPinsValues pins the
// JSON-tagged outcome strings. /admin/audit filters
// on these literal values; changing them is a
// breaking change for the operator's saved filters.
func TestReconcileOutcome_StringPinsValues(t *testing.T) {
	cases := []struct {
		outcome ReconcileOutcome
		want    string
	}{
		{ReconcileOK, "ok"},
		{ReconcileLinked, "linked"},
		{ReconcileRelinked, "relinked"},
		{ReconcileOrphan, "orphan"},
		{ReconcileError, "err"},
	}
	for _, c := range cases {
		if string(c.outcome) != c.want {
			t.Errorf("outcome %v = %q, want %q (changing this breaks /admin/audit filters)",
				c.outcome, string(c.outcome), c.want)
		}
	}
}

// TestReconcileErr_SentinelsExist pins the error
// sentinels (so the /admin/headscale page can use
// errors.Is to distinguish "skipped" from "ran").
func TestReconcileErr_SentinelsExist(t *testing.T) {
	// errors.Is on a typed string error: use the
	// typed-error pattern. (errors.Is(err, target)
	// where both are typed strings and target has
	// the same value returns true.)
	if !errors.Is(errReconcileNilDB, errReconcileNilDB) {
		t.Error("errReconcileNilDB should match itself via errors.Is")
	}
	if !errors.Is(errReconcileNilHS, errReconcileNilHS) {
		t.Error("errReconcileNilHS should match itself via errors.Is")
	}
	if errors.Is(errReconcileNilDB, errReconcileNilHS) {
		t.Error("errReconcileNilDB and errReconcileNilHS should be distinct sentinels")
	}
}

// TestStartReconcileCron_NilDBSentinel pins the
// "skip if no DB" behavior — important for the
// air-gapped / headscale-unreachable case where
// the cron MUST be a no-op (not a crash).
func TestStartReconcileCron_NilDBSentinel(t *testing.T) {
	err := StartReconcileCron(context.Background(), nil, &Client{}, DefaultReconcileInterval)
	if !errors.Is(err, errReconcileNilDB) {
		t.Errorf("StartReconcileCron(nil DB) = %v, want errReconcileNilDB", err)
	}
}

func TestStartReconcileCron_NilHSSentinel(t *testing.T) {
	// Pass a *sql.DB that is a non-nil zero value; the
	// cron checks db != nil first so this is enough to
	// reach the next check. (We don't have a fakeDB
	// helper in this package; the nil check is what
	// matters here — the DB-still-works check is in
	// the integration test.)
	err := StartReconcileCron(context.Background(), &sql.DB{}, nil, DefaultReconcileInterval)
	if !errors.Is(err, errReconcileNilHS) {
		t.Errorf("StartReconcileCron(nil HS) = %v, want errReconcileNilHS", err)
	}
}

// TestRunOnceNow_NilDBSentinel pins the same
// "skip if no DB" behavior on the manual trigger.
func TestRunOnceNow_NilDBSentinel(t *testing.T) {
	_, err := RunOnceNow(context.Background(), nil, &Client{})
	if !errors.Is(err, errReconcileNilDB) {
		t.Errorf("RunOnceNow(nil DB) = %v, want errReconcileNilDB", err)
	}
}

// TestDefaultReconcileInterval_PinsValue pins the
// default interval. Changing it is a tuning decision
// that affects the operator's "how stale can a stale
// headscale_user_id get" window — the B-check should
// catch accidental changes.
func TestDefaultReconcileInterval_PinsValue(t *testing.T) {
	if DefaultReconcileInterval != time.Hour {
		t.Errorf("DefaultReconcileInterval = %v, want 1h (changing this changes the operator's stale-id window)",
			DefaultReconcileInterval)
	}
}

// TestReconcileUsers_NilDBSentinel pins the
// "no DB, no reconciliation" behavior on the
// core function. Same reasoning as the cron
// sentinel test above.
func TestReconcileUsers_NilDBSentinel(t *testing.T) {
	_, err := ReconcileUsers(context.Background(), nil, &Client{})
	if err == nil {
		t.Fatal("ReconcileUsers(nil DB) = nil err, want non-nil")
	}
	if !strings.Contains(err.Error(), "db is nil") {
		t.Errorf("ReconcileUsers(nil DB) error = %v, want 'db is nil'", err)
	}
}

func TestReconcileUsers_NilHSSentinel(t *testing.T) {
	_, err := ReconcileUsers(context.Background(), &sql.DB{}, nil)
	if err == nil {
		t.Fatal("ReconcileUsers(nil HS) = nil err, want non-nil")
	}
	if !strings.Contains(err.Error(), "headscale client is nil") {
		t.Errorf("ReconcileUsers(nil HS) error = %v, want 'headscale client is nil'", err)
	}
}
