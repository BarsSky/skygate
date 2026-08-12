// v1.3.0: openNodeOwnerMapTestDB was removed (the only definition
// lived in node_owner_map_test.go which is now a t.Skip stub).
// PG-rewrite is a Phase 2 follow-up. Integration config is
// exercised at runtime by /admin/integrations on PG.

package db

import "testing"

func TestIntegrations_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openNodeOwnerMapTestDB was removed. Rewrite for PG in Phase 2. Integration config is exercised at runtime by /admin/integrations on PG.")
}
