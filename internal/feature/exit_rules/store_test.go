// v1.3.0: Test file had a placeholder openGenTestDB that used
// SQLite :memory: (currently unused — the dispatch tests below
// it exercise env-var reads without a DB). PG-rewrite is a
// Phase 2 follow-up.

package exit_rules

import "testing"

func TestExitRulesStore_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openGenTestDB used SQLite :memory: (currently unused). Rewrite for PG in Phase 2.")
}
