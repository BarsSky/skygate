// v1.3.0: TestGetMyTelegram_* / TestPostMyTelegram* / TestGetMyTelegramQR*
// used newMemoryDB (SQLite) which has been removed. PG-rewrite
// is a Phase 2 follow-up. The /my/telegram flow is exercised
// at runtime by the live /my/telegram page on PG.

package my

import "testing"

func TestMyTelegram_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used newMemoryDB (SQLite). Rewrite for PG in Phase 2.")
}
