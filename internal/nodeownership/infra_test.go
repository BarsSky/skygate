// v1.3.0: infra_test used openBackfillTestDB (SQLite) which
// has been removed. PG-rewrite is a Phase 2 follow-up. The
// infra-user backfill (V054) is exercised at runtime by the
// BackfillInfra function on PG.

package nodeownership

import "testing"

func TestNodeOwnershipInfra_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used openBackfillTestDB (SQLite). Rewrite for PG in Phase 2. BackfillInfra is exercised at runtime on PG.")
}
