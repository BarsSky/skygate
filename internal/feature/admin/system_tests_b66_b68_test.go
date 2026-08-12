// v1.3.0: Test file used SQLite :memory: to verify the SQL
// strings inside the TestRegistry closures (db.duplicate_devices,
// exit_rules.preferred_mismatch, headscale.acl_admin_present).
// PG-rewrite is a Phase 2 follow-up. The same SQL strings are
// exercised at runtime by the /admin/system_tests page on PG.

package admin

import "testing"

func TestSystemTestsB66B68_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used SQLite :memory: to verify the SQL strings inside TestRegistry closures. Rewrite for PG in Phase 2. The same SQL is exercised at runtime by /admin/system_tests on PG.")
}
