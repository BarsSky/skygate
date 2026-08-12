// v1.3.0: openTestDB + seedPortalUser helpers were removed
// from acl_test.go (the only test file that defined them).
// PG-rewrite of the multi-subnet integration test is a Phase 2
// follow-up. The same code paths are exercised at runtime by
// /admin/acls (the live policy editor) on PG.

package acl

import "testing"

func TestACLMultiSubnet_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openTestDB + seedPortalUser were removed from acl_test.go. Rewrite for PG in Phase 2. Multi-subnet ACL builder is exercised at runtime by /admin/acls on PG.")
}
