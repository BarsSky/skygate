// v1.3.0: integrations_renderer_test used newMemoryDB (SQLite)
// which has been removed. PG-rewrite is a Phase 2 follow-up.
// The /admin/integrations render path is exercised at runtime
// by the live /admin/integrations page on PG.

package admin

import "testing"

func TestAdmin_Skip_integrations_renderer(t *testing.T) {
	t.Skip("v1.3.0: tests used newMemoryDB (SQLite). Rewrite for PG in Phase 2.")
}
