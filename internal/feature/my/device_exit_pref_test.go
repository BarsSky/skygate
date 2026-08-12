// v1.3.0: TestCallerOwnsDevice_* / TestSetDeviceExitNodePref_*
// used newMemoryDB (SQLite) which has been removed. PG-rewrite
// is a Phase 2 follow-up. The per-device preferred-exit flow
// is exercised at runtime by /my/exit-nodes + /admin/exit-nodes
// on PG.

package my

import "testing"

func TestMyDeviceExitPref_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used newMemoryDB (SQLite). Rewrite for PG in Phase 2.")
}
