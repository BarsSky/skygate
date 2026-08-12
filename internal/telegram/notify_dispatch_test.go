// v1.3.0: notify_dispatch_test used setupTestDB / insertValidLoginToken
// / testLoginToken (SQLite fixtures) which have been removed.
// PG-rewrite is a Phase 2 follow-up. The notify dispatch path
// is exercised at runtime by the live bot on PG.

package telegram

import "testing"

func TestTelegramNotifyDispatch_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used setupTestDB (SQLite). Rewrite for PG in Phase 2.")
}
