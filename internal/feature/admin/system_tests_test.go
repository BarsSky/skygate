// v1.3.0: openSystemTestsDB used SQLite :memory: with
// hand-rolled system_tests_runs table. PG-rewrite is a Phase 2
// follow-up. The system_tests persistence is exercised at
// runtime by /admin/system_tests on PG.

package admin

import "testing"

func TestSystemTests_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openSystemTestsDB used SQLite :memory:. Rewrite for PG in Phase 2.")
}
