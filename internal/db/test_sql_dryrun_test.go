//go:build postgres
// +build postgres

package db

import (
	"fmt"
	"strings"
	"testing"
)

// TestPlaceholdersRange_PGFormat pins the v0.33.1.27 fix
// at the helper level. The pre-fix bug was that
// placeholdersList(1) returns "$1" — so concatenating
// placeholdersList(3) + placeholdersList(1) produced
// "$1, $2, $3, $1" with TWO references to $1.
// PlaceholdersRange(from, to) produces a contiguous range
// so the placeholders don't collide. On the -tags postgres
// build the placeholders are "$1, $2, $3, $4" (the
// SQLite build uses "?, ?, ?, ?" — see
// placeholders_range_sqlite_test.go).
func TestPlaceholdersRange_PGFormat(t *testing.T) {
	t.Logf("nowUnixSQL() = %q (PG build)", nowUnixSQL())
	t.Logf("PlaceholdersRange(1, 3) = %q", PlaceholdersRange(1, 3))
	t.Logf("PlaceholdersRange(4, 4) = %q", PlaceholdersRange(4, 4))
	t.Logf("PlaceholdersRange(1, 4) = %q", PlaceholdersRange(1, 4))
	t.Logf("PlaceholdersRange(5, 5) = %q", PlaceholdersRange(5, 5))

	// The pre-fix bug: placeholdersList(N) + placeholdersList(1)
	// → TWO refs to $1. The fix: PlaceholdersRange produces
	// a contiguous range.
	// Verify the range itself is well-formed first:
	if got := PlaceholdersRange(1, 3); got != "$1,$2,$3" {
		t.Errorf("PlaceholdersRange(1, 3) = %q, want $1,$2,$3", got)
	}
	if got := PlaceholdersRange(4, 4); got != "$4" {
		t.Errorf("PlaceholdersRange(4, 4) = %q, want $4", got)
	}
	if got := PlaceholdersRange(1, 4); got != "$1,$2,$3,$4" {
		t.Errorf("PlaceholdersRange(1, 4) = %q, want $1,$2,$3,$4", got)
	}
	if got := PlaceholdersRange(5, 5); got != "$5" {
		t.Errorf("PlaceholdersRange(5, 5) = %q, want $5", got)
	}
	// Sanity: the concatenation produced by the fix has
	// unique $N placeholders. (The pre-fix bug was two
	// $1 references; verify that's not back.)
	s := PlaceholdersRange(1, 3) + ", " + PlaceholdersRange(4, 4)
	if strings.Count(s, "$1") != 1 {
		t.Errorf("v0.33.1.19 bug returned — $1 appears %d times in %q (want 1)", strings.Count(s, "$1"), s)
	}

	// Simulate the actual SetUserExitNodePref SQL: 4
	// placeholders + 1 SQL function + 4 Go args.
	sqlUser := fmt.Sprintf(`
		INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		VALUES (%s, %s, %s)
		ON CONFLICT(user_id) DO UPDATE SET ...`,
		PlaceholdersRange(1, 3), nowUnixSQL(), PlaceholdersRange(4, 4))
	if want := "VALUES ($1,$2,$3, EXTRACT(EPOCH FROM now())::bigint, $4)"; !strings.Contains(sqlUser, want) {
		t.Errorf("SetUserExitNodePref SQL doesn't contain %q, got:\n%s", want, sqlUser)
	}

	// Simulate SetDeviceExitNodePref SQL: 5 placeholders
	// + 1 SQL function + 5 Go args.
	sqlDev := fmt.Sprintf(`
		INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
		VALUES (%s, %s, %s)
		ON CONFLICT(user_id, device_hostname) DO UPDATE SET ...`,
		PlaceholdersRange(1, 4), nowUnixSQL(), PlaceholdersRange(5, 5))
	if want := "VALUES ($1,$2,$3,$4, EXTRACT(EPOCH FROM now())::bigint, $5)"; !strings.Contains(sqlDev, want) {
		t.Errorf("SetDeviceExitNodePref SQL doesn't contain %q, got:\n%s", want, sqlDev)
	}

	// Edge cases.
	if got := PlaceholdersRange(5, 3); got != "" {
		t.Errorf("PlaceholdersRange(5, 3) = %q, want empty (from > to)", got)
	}
	if got := PlaceholdersRange(0, 3); got != "" {
		t.Errorf("PlaceholdersRange(0, 3) = %q, want empty (from < 1)", got)
	}
	if got := PlaceholdersRange(7, 7); got != "$7" {
		t.Errorf("PlaceholdersRange(7, 7) = %q, want $7", got)
	}
}
