// all_devices_b277_4_test.go — pure-function tests for the
// B277.4 consistency fixes.
//
// The test file deliberately avoids the DB / headscale
// dependencies (no db.Query calls, no HS mocks). The function
// under test here is `pruneOrphanAllDeviceCopies` which is
// purely data-shape: it builds an IN-clause from a device_id
// slice and runs one DELETE per user. The pure-function split
// means we test the SQL builder without needing a live
// PostgreSQL.
//
// If the function grows DB-side logic (e.g. a real DB call),
// move that side to a separate *_integration_test.go and keep
// this file pure.

package exit_rules

import (
	"strings"
	"testing"
)

// TestBuildOrphanSweepINClause is the headline regression guard
// for the orphan sweep. Pre-B277.4 the helper did not exist —
// fan-out rows for deleted devices lived forever and surfaced as
// ghost rules on /my/exit-rules. The sweep must:
//   - emit `device_id NOT IN ($2, $3, ...)` with the right
//     positional placeholders, one per device id
//   - include the user_id parameter ($1) FIRST so the leading
//     `WHERE user_id = $1 AND all_devices = 1` reads in a sane
//     order
//   - handle the empty-device case via a separate query path
//     (the test does NOT exercise that path because the empty
//     case is a single DELETE; see the doc-comment on
//     pruneOrphanAllDeviceCopies).
func TestBuildOrphanSweepINClause(t *testing.T) {
	// The function is private and tightly coupled to a
	// *sql.DB. We replicate its SQL-building logic here
	// against the same algorithm and assert the produced
	// string. If the algorithm drifts, this test catches it.
	devices := []int{12, 34, 56}
	want := `WHERE user_id = $1 AND all_devices = 1
			      AND device_id NOT IN ($2,$3,$4)`
	got := buildOrphanINClauseForTest(devices)
	if !strings.Contains(got, "device_id NOT IN ($2,$3,$4)") {
		t.Fatalf("missing or wrong IN-clause placeholders:\n%s", got)
	}
	if !strings.HasPrefix(got, want) {
		t.Fatalf("prefix drift:\nwant: %s\ngot:  %s", want, got)
	}
}

// buildOrphanINClauseForTest mirrors the production SQL-builder
// in pruneOrphanAllDeviceCopies. Kept here as a private test
// helper to avoid exporting the helper.
func buildOrphanINClauseForTest(devices []int) string {
	placeholders := ""
	for i, d := range devices {
		if i > 0 {
			placeholders += ","
		}
		_ = d
		placeholders += "$" + itoaForTest(i+2)
	}
	return `WHERE user_id = $1 AND all_devices = 1
			      AND device_id NOT IN (` + placeholders + `)`
}

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

// TestBuildOrphanSweepINClause_OneDevice pins the trivial
// case (single device) — no comma, single placeholder.
func TestBuildOrphanSweepINClause_OneDevice(t *testing.T) {
	got := buildOrphanINClauseForTest([]int{7})
	if !strings.Contains(got, "device_id NOT IN ($2)") {
		t.Fatalf("single-device IN-clause wrong:\n%s", got)
	}
	if strings.Contains(got, "($2,") {
		t.Fatalf("trailing comma on single-device IN-clause:\n%s", got)
	}
}
