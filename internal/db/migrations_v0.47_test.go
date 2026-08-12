// v1.3.0: TestColumnExists_PGOnly used the columnExists
// helper that was removed along with the old SQLite-style
// migrations. The PG migration chain (MigratePostgres →
// migrateV047PG) uses information_schema directly; the
// columnExists test was a pre-v1.3.0 convenience. Skipping
// pending a Phase 2 rewrite that exercises the v0.47
// migration via TestPGMigrationIdempotency on a real PG
// instance.

package db

import "testing"

func TestMigrationsV047_SkipPendingPGRewrite(t *testing.T) {
	t.Skip("v1.3.0: columnExists helper removed (migrations_v0.47.go no longer exists). The v0.47 ALTER is now `migrateV047PG` which uses information_schema directly. Covered by TestPGMigrationIdempotency in test_pg_migrations_test.go on PG.")
}
