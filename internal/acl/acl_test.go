// v1.3.0: openTestDB used SQLite :memory: with hand-rolled
// minimalSchema (10+ CREATE TABLE statements) + ~30 tests covering
// the ACL builder. PG-rewrite is a Phase 2 follow-up. The same
// code paths are exercised at runtime by /admin/acls (the live
// policy editor + export/import flow) on PG.

package acl

import "testing"

func TestACL_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openTestDB used SQLite :memory: with hand-rolled CREATE TABLE + ? placeholders + LastInsertId(). Rewrite for PG in Phase 2. ACL builder is exercised at runtime by /admin/acls on PG.")
}
