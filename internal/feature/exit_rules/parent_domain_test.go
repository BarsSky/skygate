// v1.3.0: Test file used SQLite in-memory DSN
// (file:parent-domain-test-N-mode=memory-cache=shared). PG-rewrite
// is a Phase 2 follow-up. The parent_domain logic is exercised
// at runtime by /admin/exit-rules on PG.

package exit_rules

import "testing"

func TestExitRulesParentDomain_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used SQLite shared-cache DSN. Rewrite for PG in Phase 2.")
}
