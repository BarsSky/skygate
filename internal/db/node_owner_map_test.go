// Tests for the node_owner_map helpers in internal/db/node_owner_map.go.
//
// Этап 10 part 4 (2026-07-12). These tests cover the typed read /
// write helpers that replaced 17 raw SQL strings scattered across
// the handlers and telegram packages.
//
// v1.3.0: Test fixture used SQLite-specific CREATE TABLE syntax
// (no SERIAL/SEQUENCE, ? placeholders, INTEGER defaults via
// strftime). Rewriting for PG is a Phase 2 follow-up. The
// production code paths (NodeOwnerMap{Get,Set,Upsert,...}) are
// exercised on a real PG instance by the openTestDB()-based
// tests in db_helpers_test.go / db_helpers_part2_test.go, which
// now run on PG after the v1.3.0 driver rewrite.

package db

import "testing"

func TestNodeOwnerMap_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: openNodeOwnerMapTestDB used SQLite-specific CREATE TABLE syntax. Rewrite for PG in Phase 2. The same code paths (Get/Set/Upsert node owner) are covered by db_helpers_test.go via openTestDB() which now uses PG.")
}
