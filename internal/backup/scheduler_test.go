// v1.3.0: newTestDB used SQLite shared-cache DSN
// (file:skygate-test-...mode=memory&cache=shared). PG-rewrite
// is a Phase 2 follow-up. The scheduler is exercised by
// scripts/verify_backup.sh + the /admin/backup UI on PG.

package backup

import "testing"

func TestBackupScheduler_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: newTestDB used SQLite shared-cache DSN. Rewrite for PG in Phase 2. Backup scheduler is exercised by scripts/verify_backup.sh on PG.")
}
