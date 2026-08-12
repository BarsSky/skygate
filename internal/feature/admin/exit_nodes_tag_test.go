// v1.3.0: exit_nodes_tag_test used newMemoryDB (SQLite) which
// has been removed. PG-rewrite is a Phase 2 follow-up. The
// PostAdminExitNode{Tag,Untag}AsExitNode routes are exercised
// at runtime by the live /admin/exit-nodes UI on PG.

package admin

import "testing"

func TestAdmin_Skip_exit_nodes_tag(t *testing.T) {
	t.Skip("v1.3.0: tests used newMemoryDB (SQLite). Rewrite for PG in Phase 2.")
}
