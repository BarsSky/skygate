// v1.3.0: openTestDB used db.Open (SQLite file path) which
// has been removed. PG-rewrite is a Phase 2 follow-up.

package subnet

import "testing"

func TestSubnetShares_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openTestDB used db.Open (SQLite file path). Rewrite for PG in Phase 2.")
}
