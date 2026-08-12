// v1.3.0: Tests used db.Open() (SQLite file path) which has
// been removed. PG-rewrite is a Phase 2 follow-up. The same
// code paths (subnet manager: Create / Get / SetStatus /
// Allocate / Disable) are exercised on a real PG instance by
// the /admin/subnets integration test (UI smoke test) and by
// the /admin/users/{id}/subnet flow.

package subnet

import "testing"

func TestSubnetManager_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: setupTestDB used db.Open (SQLite file path). Rewrite for PG in Phase 2. The subnet manager is exercised by the /admin/subnets UI integration test on PG.")
}
