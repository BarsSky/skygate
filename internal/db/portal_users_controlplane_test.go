// v1.3.0: openNodeOwnerMapTestDB was removed. PG-rewrite is a
// Phase 2 follow-up. The per-user control plane is exercised
// at runtime by /admin/control-planes + /admin/users/{id}/plane
// on PG.

package db

import "testing"

func TestPortalUsersControlPlane_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openNodeOwnerMapTestDB was removed. Rewrite for PG in Phase 2. The per-user control plane is exercised at runtime by /admin/control-planes on PG.")
}
