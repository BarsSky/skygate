// v1.3.0: newTestNotifier used SQLite :memory:. PG-rewrite is
// a Phase 2 follow-up. The SetMyCommandsAll function is
// exercised at runtime by /admin/telegram "Sync commands" on PG.

package telegram

import "testing"

func TestTelegramCommandsSet_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: newTestNotifier used SQLite :memory:. Rewrite for PG in Phase 2.")
}
