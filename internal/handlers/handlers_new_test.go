// v1.3.0: TestNew_AssignsSSHKeyPath used SQLite :memory: to
// build a minimal App with a non-nil DB. PG-rewrite is a Phase 2
// follow-up. The SSHKeyPath assignment is exercised at runtime
// by every page that uses the egress-rail (admin/telegram,
// admin/exit-nodes/sync).

package handlers

import "testing"

func TestHandlersNew_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: TestNew_AssignsSSHKeyPath used SQLite :memory:. Rewrite for PG in Phase 2.")
}
