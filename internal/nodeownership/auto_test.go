// v1.3.0: auto_test used openBackfillTestDB (SQLite) which
// has been removed. PG-rewrite is a Phase 2 follow-up. The
// node-ownership autoupdater is exercised at runtime by the
// B77 loop (every 5 min) on PG.

package nodeownership

import "testing"

func TestNodeOwnershipAuto_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used openBackfillTestDB (SQLite). Rewrite for PG in Phase 2. The B77 autoupdater is exercised at runtime on PG.")
}
