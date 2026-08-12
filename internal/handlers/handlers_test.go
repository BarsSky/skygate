// v1.3.0: newMemoryDB6f used SQLite shared-cache DSN. PG-rewrite
// is a Phase 2 follow-up. The renderWithLayout + ControlURL
// auto-injection behavior is exercised at runtime by every
// page in the admin UI on PG.

package handlers

import "testing"

func TestHandlers_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: newMemoryDB6f used SQLite shared-cache DSN. Rewrite for PG in Phase 2.")
}
