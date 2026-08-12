// 2026-07-29: v0.31.x — per-device OS + device_type
// migration. Adds 2 columns to node_owner_map with
// 'unknown' defaults.
//
// v1.3.0: Test fixture used SQLite-specific CREATE TABLE
// (AUTOINCREMENT, TEXT PRIMARY KEY for node_id, ? placeholders).
// Rewriting for PG (SERIAL, TEXT PRIMARY KEY works, $1, EXTRACT)
// is a Phase 2 follow-up. The v0.48 migration itself is now
// `ADD COLUMN IF NOT EXISTS` which is PG-idiomatic and tested
// in test_pg_migrations_test.go on a real PG instance.

package db

import "testing"

func TestMigrateV048_AddColumnsPGOnly(t *testing.T) {
	t.Skip("v1.3.0: SQLite fixture removed. The v0.48 ALTER is now `ADD COLUMN IF NOT EXISTS` (PG-idiomatic, idempotent). The end-to-end migration is covered by TestPGRoundtripSchema + TestPGMigrationIdempotency in test_pg_migrations_test.go when SKYGATE_TEST_PG_DSN is set.")
}
