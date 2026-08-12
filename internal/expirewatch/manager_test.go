// v1.3.0: openTestDB used db.Open (SQLite file path) which
// has been removed. PG-rewrite is a Phase 2 follow-up. The
// expirewatch manager is exercised by the /admin/system_tests
// network.dns_resolve test on PG.

package expirewatch

import "testing"

func TestExpireWatch_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openTestDB used db.Open (SQLite file path). Rewrite for PG in Phase 2.")
}
