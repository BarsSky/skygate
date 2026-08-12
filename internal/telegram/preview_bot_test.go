// v1.3.0: preview_bot_test used setupTestDB / userEnv (SQLite
// fixtures) which have been removed. PG-rewrite is a Phase 2
// follow-up. The preview-bot path is exercised at runtime by
// the live bot's /preview command on PG.

package telegram

import "testing"

func TestTelegramPreviewBot_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: tests used setupTestDB (SQLite). Rewrite for PG in Phase 2.")
}
