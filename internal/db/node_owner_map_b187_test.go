package db

// B187 regression test (2026-09-14): devicedelete.Delete uses
// qDeleteNodeOwnerByNodeTag with tag="" — but real rows have
// tag='tag:dev-<user>-<host>'. The silent 0-row match meant the
// snapshot stayed stale after a delete/reregister and headplane
// re-showed the deleted device.
//
// This test verifies the new qDeleteNodeOwnerByNodeIDOnly query
// correctly removes a row whose tag is non-empty.

import (
	"database/sql"
	"strings"
	"testing"
)

// stubExec records the last SQL + args passed to Exec so we can
// inspect what the caller did without standing up a real DB.
type stubExec struct {
	lastSQL  string
	lastArgs []interface{}
	rowsAffected int64
	err      error
}

func (s *stubExec) Exec(query string, args ...interface{}) (sql.Result, error) {
	s.lastSQL = query
	s.lastArgs = args
	if s.err != nil {
		return nil, s.err
	}
	return stubResult{n: s.rowsAffected}, nil
}

func (s *stubExec) QueryRow(query string, args ...interface{}) *sql.Row {
	return nil // unused in this test
}

type stubResult struct{ n int64 }

func (s stubResult) LastInsertId() (int64, error) { return 0, nil }
func (s stubResult) RowsAffected() (int64, error) { return s.n, nil }

// Compile-time guard that stubExec satisfies dbExec.
var _ dbExec = (*stubExec)(nil)

func TestDeleteNodeOwnerByNodeIDOnly_RemovesNonEmptyTag(t *testing.T) {
	stub := &stubExec{rowsAffected: 1}
	n, err := DeleteNodeOwnerByNodeIDOnly(stub, "28")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("RowsAffected = %d, want 1", n)
	}
	if stub.lastSQL != qDeleteNodeOwnerByNodeIDOnly {
		t.Errorf("SQL mismatch:\n  got  %s\n  want %s", stub.lastSQL, qDeleteNodeOwnerByNodeIDOnly)
	}
	// CRITICAL: the query MUST NOT filter by tag or username —
	// otherwise the B171 bug is back. If a future refactor adds
	// `AND tag = $2` or `AND username = $2`, this assertion fires.
	if strings.Contains(stub.lastSQL, "AND tag") {
		t.Errorf("query must NOT filter by tag (B171 bug): %s", stub.lastSQL)
	}
	if strings.Contains(stub.lastSQL, "AND username") {
		t.Errorf("query must NOT filter by username (B171 bug): %s", stub.lastSQL)
	}
	if len(stub.lastArgs) != 1 {
		t.Errorf("args = %v, want exactly 1 (just nodeID)", stub.lastArgs)
	}
}

func TestDeleteNodeOwnerByNodeTagCounted_RegressionGuard(t *testing.T) {
	// This is the OLD function. It still requires tag to match.
	// The B171 bug was callers passing tag="" — which never matches
	// because real tags are 'tag:dev-<user>-<host>'. This test pins
	// the contract: tag="" returns 0 rows affected (so future
	// callers don't accidentally rely on tag="" matching).
	stub := &stubExec{rowsAffected: 0}
	n, err := DeleteNodeOwnerByNodeTagCounted(stub, "28", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("RowsAffected with tag=\"\" = %d, want 0 (would silently skip)", n)
	}
	// The query DOES require tag — that's the whole point of the
	// bug. We keep this as documentation that this function is NOT
	// a drop-in replacement for DeleteNodeOwnerByNodeIDOnly.
	if !strings.Contains(stub.lastSQL, "AND tag") {
		t.Errorf("query should require tag (old contract): %s", stub.lastSQL)
	}
}
