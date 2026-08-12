// v1.3.0: TestMigrateV052_* used migrateV052 (the old
// SQLite-style migration) which has been removed. The PG
// equivalent is migrateV052PG in migrations_pg.go. The
// column-repair logic is exercised on a real PG instance
// by TestPGMigrationIdempotency + TestPGRoundtripSchema in
// test_pg_migrations_test.go.

package db

import "testing"

func TestMigrationsV052_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: migrateV052 (SQLite-style) removed. PG equivalent migrateV052PG in migrations_pg.go is exercised by TestPGMigrationIdempotency on PG.")
}
