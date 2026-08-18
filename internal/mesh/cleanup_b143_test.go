// 2026-08-18 (B143, v1.4.3) — unit tests for the smoke-mesh
// cleanup helpers.
//
// The B143 fix is mostly a SQL contract (a transaction
// that SELECTs candidate rows + DELETEs them) plus a
// message-formatting helper. Both are pure (no I/O)
// functions, so the test is a pure-Go test that pins:
//   - FormatCleanupMessage: the "no cruft" / "one
//     row" / "many rows" / "many rows truncated"
//     branches.
//   - FormatHumanSchedule: the "every minute" / "5 AM
//     daily" / "invalid (fall back to raw)" branches.
//   - int64ArrayToPGArray: the empty-slice / single-
//     element / multi-element branches.
//   - sameCleanupMinute: the same-minute / different-
//     minute / different-day / zero-value branches.
//
// The SQL contract (DeleteSmokeMeshes) is covered
// by scripts/check_b143.sh live on the VM — the
// SQL touchpoints are easier to verify in shell
// against the real PG than to mock in Go. The
// pure-Go tests below catch the formatting and
// schedule-parsing bugs that the shell check would
// miss.

package mesh

import (
	"reflect"
	"testing"
	"time"
)

// TestFormatCleanupMessage_NoRows pins the empty-result
// branch. The "no smoke-mesh cruft" wording is the
// default state on the live VM (0 cruft rows at the
// time of the B143 fix), so the audit log will see
// this branch firing daily once the operator enables
// the scheduler.
func TestFormatCleanupMessage_NoRows(t *testing.T) {
	got := FormatCleanupMessage(CleanupResult{})
	want := "cleanup: no smoke-mesh cruft found"
	if got != want {
		t.Errorf("empty result: got %q, want %q", got, want)
	}
}

// TestFormatCleanupMessage_SingleRow pins the
// "one row" branch. A single-row cleanup is the
// most common case on a busy dev VM (smoke.sh
// runs daily, leaves 1 row behind if a manual
// test interrupted it).
func TestFormatCleanupMessage_SingleRow(t *testing.T) {
	got := FormatCleanupMessage(CleanupResult{
		IDs:   []int64{42},
		Names: []string{"smoke-mesh-12345"},
		Total: 1,
	})
	want := `cleanup: removed 1 smoke-mesh row (id=42 name="smoke-mesh-12345")`
	if got != want {
		t.Errorf("single row: got %q, want %q", got, want)
	}
}

// TestFormatCleanupMessage_FewRows pins the "many
// rows" branch under the truncation threshold (5
// names listed). The threshold is defined in the
// formatCleanupMessage body as `if len(preview) > 5`.
func TestFormatCleanupMessage_FewRows(t *testing.T) {
	res := CleanupResult{
		IDs:   []int64{1, 2, 3, 4, 5},
		Names: []string{"smoke-mesh-a", "smoke-mesh-b", "smoke-mesh-c", "smoke-mesh-d", "smoke-mesh-e"},
		Total: 5,
	}
	got := FormatCleanupMessage(res)
	// All 5 names fit; no truncation marker.
	wantSubstr := "removed 5 smoke-mesh rows"
	if !contains(got, wantSubstr) {
		t.Errorf("few rows: missing %q in %q", wantSubstr, got)
	}
	for _, n := range res.Names {
		if !contains(got, n) {
			t.Errorf("few rows: missing name %q in %q", n, got)
		}
	}
	if contains(got, "more)") {
		t.Errorf("few rows: should NOT show 'more' marker; got %q", got)
	}
}

// TestFormatCleanupMessage_TruncatedAtFive pins the
// truncation branch. A 50-row cleanup produces a
// "(45 more)" suffix so the audit log doesn't blow
// past 1KB. This is the bug fix from the original
// "1KB+ audit message" concern.
func TestFormatCleanupMessage_TruncatedAtFive(t *testing.T) {
	names := make([]string, 50)
	ids := make([]int64, 50)
	for i := range names {
		names[i] = "smoke-mesh-" + itoaForTest(i)
		ids[i] = int64(1000 + i)
	}
	got := FormatCleanupMessage(CleanupResult{IDs: ids, Names: names, Total: 50})
	// Verify the suffix is present.
	if !contains(got, "(45 more)") {
		t.Errorf("truncated: missing '(45 more)' suffix in %q", got)
	}
	// Verify only the first 5 names appear.
	for i := 0; i < 5; i++ {
		if !contains(got, names[i]) {
			t.Errorf("truncated: missing first 5 name[%d]=%q in %q", i, names[i], got)
		}
	}
	// Verify names[5..] do NOT appear (that's the
	// whole point of the truncation).
	for i := 5; i < 50; i++ {
		if contains(got, names[i]) {
			t.Errorf("truncated: name[%d]=%q should NOT appear; got %q", i, names[i], got)
		}
	}
}

// TestInt64ArrayToPGArray_Empty pins the empty-slice
// branch. DeleteSmokeMeshes guards the empty case
// before calling this, but the function should still
// return "{}" (which matches no rows) for defensive
// reasons.
func TestInt64ArrayToPGArray_Empty(t *testing.T) {
	if got := int64ArrayToPGArray(nil); got != "{}" {
		t.Errorf("empty: got %q, want %q", got, "{}")
	}
	if got := int64ArrayToPGArray([]int64{}); got != "{}" {
		t.Errorf("empty (len 0): got %q, want %q", got, "{}")
	}
}

// TestInt64ArrayToPGArray_Single pins the single-
// element branch.
func TestInt64ArrayToPGArray_Single(t *testing.T) {
	if got := int64ArrayToPGArray([]int64{42}); got != "{42}" {
		t.Errorf("single: got %q, want %q", got, "{42}")
	}
}

// TestInt64ArrayToPGArray_Many pins the multi-element
// branch. The PG array literal "{1,2,3}" is what the
// DELETE statement parses with $1::bigint[].
func TestInt64ArrayToPGArray_Many(t *testing.T) {
	if got := int64ArrayToPGArray([]int64{1, 2, 3}); got != "{1,2,3}" {
		t.Errorf("many: got %q, want %q", got, "{1,2,3}")
	}
	if got := int64ArrayToPGArray([]int64{100, -5, 0, 9999999999}); got != "{100,-5,0,9999999999}" {
		t.Errorf("many (mixed): got %q, want %q", got, "{100,-5,0,9999999999}")
	}
}

// TestSameCleanupMinute_Same pins the basic
// same-minute branch. The scheduler relies on this
// to prevent a 30s-tick from firing the cleanup
// twice in the same minute.
func TestSameCleanupMinute_Same(t *testing.T) {
	a := time.Date(2026, 8, 18, 5, 0, 15, 0, time.UTC)
	b := time.Date(2026, 8, 18, 5, 0, 45, 0, time.UTC)
	if !sameCleanupMinute(a, b) {
		t.Errorf("same minute: got false, want true (a=%s b=%s)", a, b)
	}
}

// TestSameCleanupMinute_DifferentMinute pins the
// cross-minute negative case.
func TestSameCleanupMinute_DifferentMinute(t *testing.T) {
	a := time.Date(2026, 8, 18, 5, 0, 45, 0, time.UTC)
	b := time.Date(2026, 8, 18, 5, 1, 0, 0, time.UTC)
	if sameCleanupMinute(a, b) {
		t.Errorf("diff minute: got true, want false")
	}
}

// TestSameCleanupMinute_DifferentDay pins the
// cross-day negative case. The cleanup is
// daily, so the last-run from yesterday should
// NOT block today's trigger.
func TestSameCleanupMinute_DifferentDay(t *testing.T) {
	a := time.Date(2026, 8, 18, 5, 0, 15, 0, time.UTC)
	b := time.Date(2026, 8, 19, 5, 0, 15, 0, time.UTC)
	if sameCleanupMinute(a, b) {
		t.Errorf("diff day: got true, want false")
	}
}

// TestSameCleanupMinute_ZeroValue pins the
// zero-value (no last run yet) branch. The
// scheduler treats 0 as "no last run" and
// proceeds; sameCleanupMinute should NOT
// consider a zero value as "same as anything".
func TestSameCleanupMinute_ZeroValue(t *testing.T) {
	if sameCleanupMinute(time.Time{}, time.Now()) {
		t.Errorf("zero value: got true, want false")
	}
}

// TestFormatHumanSchedule_EveryMinute pins the
// "*" branch. The /admin/system_tests page
// (post-TD-8) uses this to render the schedule
// description.
func TestFormatHumanSchedule_EveryMinute(t *testing.T) {
	if got := FormatHumanSchedule("*"); got != "Every minute" {
		t.Errorf("every minute: got %q, want %q", got, "Every minute")
	}
}

// TestFormatHumanSchedule_Daily pins the
// "0 5 * * *" branch (the default).
func TestFormatHumanSchedule_Daily(t *testing.T) {
	if got := FormatHumanSchedule("0 5 * * *"); got != "Daily at 05:00" {
		t.Errorf("daily 5 AM: got %q, want %q", got, "Daily at 05:00")
	}
	if got := FormatHumanSchedule("30 4 * * *"); got != "Daily at 04:30" {
		t.Errorf("daily 4:30 AM: got %q, want %q", got, "Daily at 04:30")
	}
	if got := FormatHumanSchedule("0 23 * * *"); got != "Daily at 23:00" {
		t.Errorf("daily 11 PM: got %q, want %q", got, "Daily at 23:00")
	}
}

// TestFormatHumanSchedule_Empty pins the empty
// branch — falls back to the default schedule
// then formats it.
func TestFormatHumanSchedule_Empty(t *testing.T) {
	if got := FormatHumanSchedule(""); got != "Daily at 05:00" {
		t.Errorf("empty: got %q, want %q", got, "Daily at 05:00")
	}
}

// TestFormatHumanSchedule_Invalid pins the
// invalid-input branch — falls back to the raw
// string. Better to show the operator their
// typo than to silently mis-render.
func TestFormatHumanSchedule_Invalid(t *testing.T) {
	if got := FormatHumanSchedule("not a cron"); got != "not a cron" {
		t.Errorf("invalid: got %q, want %q", got, "not a cron")
	}
}

// TestSmokeMeshNamePrefix pins the exported
// prefix constant. The smoke.sh script uses
// the same prefix; if a refactor accidentally
// renames it, the smoke run + the cleanup
// will silently miss each other.
func TestSmokeMeshNamePrefix(t *testing.T) {
	const want = "smoke-mesh-"
	if SmokeMeshNamePrefix != want {
		t.Errorf("prefix: got %q, want %q", SmokeMeshNamePrefix, want)
	}
}

// TestStorageKeyConstants pins the 3 storage key
// strings. The B-check (check_b143.sh) and the
// /admin/system_tests page (post-TD-8) reference
// these by string; if a refactor renames any
// of them, the scheduler will silently read a
// different key from global_settings.
func TestStorageKeyConstants(t *testing.T) {
	wantKeys := map[string]string{
		"KeyCleanupSmokeMeshEnabled":  "cleanup.smoke_mesh_enabled",
		"KeyCleanupSmokeMeshSchedule": "cleanup.smoke_mesh_schedule",
		"KeyCleanupSmokeMeshLastRun":  "cleanup.smoke_mesh_last_run",
	}
	gotKeys := map[string]string{
		"KeyCleanupSmokeMeshEnabled":  KeyCleanupSmokeMeshEnabled,
		"KeyCleanupSmokeMeshSchedule": KeyCleanupSmokeMeshSchedule,
		"KeyCleanupSmokeMeshLastRun":  KeyCleanupSmokeMeshLastRun,
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("storage keys mismatch:\n  got:  %+v\n  want: %+v", gotKeys, wantKeys)
	}
}

// contains is a tiny helper that checks whether
// s contains substr. We can't use strings.Contains
// in the tests directly because the test file
// already imports a lot of stdlib — wait, we can.
// This is here only because we want the test to
// be self-contained for grep.
func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && indexOf(s, substr) >= 0)
}

// indexOf is a small substring-search helper.
// We use this instead of strings.Index to keep
// the test file imports minimal.
func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// itoaForTest is a tiny int-to-string helper used
// only in TestFormatCleanupMessage_TruncatedAtFive.
// strconv.Itoa would do the same job, but a tiny
// hand-rolled helper keeps the test file imports
// minimal.
func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
