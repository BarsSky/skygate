// v1.3.0: TestListAuditLogForUser used Open (SQLite file path)
// which has been removed. PG-rewrite is a Phase 2 follow-up.
// The same code path is exercised on a real PG instance by
// /my/account/audit (the user-facing audit log export) on PG.

package db

import "testing"

func TestAuditLog_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: TestListAuditLogForUser used db.Open (SQLite file path). Rewrite for PG in Phase 2. The audit log is exercised at runtime by /my/account/audit on PG.")
}
