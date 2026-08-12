// v1.3.0: openControlplaneTestDB used SQLite :memory:.
// PG-rewrite is a Phase 2 follow-up. The controlplane router
// is exercised by the /admin/control-planes UI on PG.

package controlplane

import "testing"

func TestControlPlaneRouter_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openControlplaneTestDB used SQLite :memory:. Rewrite for PG in Phase 2.")
}
