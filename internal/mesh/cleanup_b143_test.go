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
//   - DeleteSmokeMeshes: a real round trip against a real SQLite database
//     (the SQL contract used to be verified only by scripts/check_b143.sh
//     live on the PostgreSQL VM, which is how the `ANY($1::bigint[])`
//     form survived: SQLite rejects it outright).
//   - sameCleanupMinute: the same-minute / different-
//     minute / different-day / zero-value branches.
//
// The PostgreSQL side of the SQL contract stays in
// scripts/check_b143.sh (live on the VM).

package mesh

import (
	"reflect"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
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

// TestDeleteSmokeMeshes_SQLite is the regression guard for the defect the
// 2026-10-01 audit found: the DELETE step used
//
//	WHERE id = ANY($1::bigint[])
//
// with a PostgreSQL array literal (`{1,2,3}`) passed as one parameter.
// That is PostgreSQL-only syntax, and `DeleteSmokeMeshes` runs from
// `mesh.StartCleanupScheduler` on **every** install kind, so on SQLite the
// statement failed with
//
//	SQL logic error: near "[]": syntax error (1)
//
// (measured by reverting the fix and re-running this test — the exact
// text is pinned here so the next reader does not have to guess) — the
// B282 dialect-leak class. It was invisible until it mattered, because the
// `Total == 0` early return means the DELETE is only reached when there is
// something to clean: B143's cleanup was silently dead on every SQLite
// install exactly when it had work to do.
//
// The old test suite could not catch it — the header of this file said the
// SQL contract was "covered by scripts/check_b143.sh live on the VM", and
// the VM is PostgreSQL. This test runs the real statement against a real
// SQLite database, so both backends are now exercised in-process.
func TestDeleteSmokeMeshes_SQLite(t *testing.T) {
	_, conn, err := skygatedb.OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer conn.Close()
	if err := skygatedb.ApplyMigrations(conn, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("apply the SQLite chain: %v", err)
	}

	// A portal user for the FK, then three meshes: two smoke-mesh rows
	// without members (the cruft) and one real mesh that must survive.
	if _, err := conn.Exec(
		`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'cleanup_probe', 'x', 0)`,
	); err != nil {
		t.Fatalf("insert portal user: %v", err)
	}
	if _, err := conn.Exec(`
		INSERT INTO meshes (id, code, name, creator_user_id, status) VALUES
		  (10, 'smoke-a', 'smoke-mesh-111', 1, 'active'),
		  (11, 'smoke-b', 'smoke-mesh-222', 1, 'active'),
		  (12, 'real',    'family-mesh',    1, 'active')`); err != nil {
		t.Fatalf("insert meshes: %v", err)
	}
	// The third smoke mesh has a member, so it must be kept (safety rule).
	if _, err := conn.Exec(`
		INSERT INTO meshes (id, code, name, creator_user_id, status) VALUES
		  (13, 'smoke-c', 'smoke-mesh-333', 1, 'active')`); err != nil {
		t.Fatalf("insert the member-bearing smoke mesh: %v", err)
	}
	if _, err := conn.Exec(
		`INSERT INTO mesh_members (mesh_id, user_id, joined_at) VALUES (13, 1, 0)`,
	); err != nil {
		t.Fatalf("insert mesh member: %v", err)
	}

	res, err := DeleteSmokeMeshes(conn)
	if err != nil {
		t.Fatalf("DeleteSmokeMeshes on SQLite failed (this is the ANY($1::bigint[]) bug): %v", err)
	}
	if res.Total != 2 {
		t.Errorf("Total = %d, want 2 (the two member-less smoke meshes)", res.Total)
	}
	if len(res.IDs) != 2 || res.IDs[0] != 10 || res.IDs[1] != 11 {
		t.Errorf("IDs = %v, want [10 11]", res.IDs)
	}

	var remaining int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM meshes`).Scan(&remaining); err != nil {
		t.Fatalf("count meshes: %v", err)
	}
	if remaining != 2 {
		t.Errorf("meshes remaining = %d, want 2 (the real mesh and the one with a member)", remaining)
	}
	for _, id := range []int64{12, 13} {
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM meshes WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("count mesh %d: %v", id, err)
		}
		if n != 1 {
			t.Errorf("mesh %d was deleted — the safety rules must keep a real mesh and a mesh with members", id)
		}
	}

	// A second run must be the clean no-op path (nothing left to delete).
	res2, err := DeleteSmokeMeshes(conn)
	if err != nil {
		t.Fatalf("second DeleteSmokeMeshes: %v", err)
	}
	if res2.Total != 0 {
		t.Errorf("second run Total = %d, want 0", res2.Total)
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
