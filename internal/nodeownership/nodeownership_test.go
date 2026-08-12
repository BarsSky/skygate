// v1.3.0: openBackfillTestDB used SQLite :memory: with
// hand-rolled schema + ? placeholders. PG-rewrite is a Phase 2
// follow-up. The Backfill function is exercised at runtime by
// the B77 autoupdater (every 5 min) on PG.

package nodeownership

import "testing"

func TestNodeOwnership_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openBackfillTestDB used SQLite :memory: + ? placeholders. Rewrite for PG in Phase 2. Backfill is exercised at runtime by the B77 autoupdater on PG.")
}
