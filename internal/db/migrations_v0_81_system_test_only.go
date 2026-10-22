// migrations_v0_81_system_test_only.go — v0.81 (B408.1): a re-run of ONE test must
// leave a trace.
//
// WHY (measured on the operator's installation 2026-10-21, after v1.5.151 was live):
//
//	20:41:35  skyadmin  system_tests_run  started test exit_rules.domain_prefix (run f2203374b438)
//	20:42:17  skyadmin  system_tests_run  started test exit_rules.domain_prefix (run 8be1d4df586d)
//
// He pressed «прогнать этот» twice; the audit shows both starts; `system_tests_runs`
// gained nothing, because `runTestsAsync` persisted only whole-catalogue runs:
//
//	persisted := false
//	if only == "" { s.PersistRun(...) }
//
// So after the page reloaded, the row fell back to the last FULL run — three days
// old — and the operator's conclusion was the only reasonable one: "the re-run does
// not work, the test still fails". The button did work; its result was thrown away.
//
// V081 gives a run the memory of its SCOPE:
//
//	only_test — the test name when this row is a single-test re-run, '' for a whole
//	            catalogue run. History RATES stay comparable by ignoring the former,
//	            while the row's last verdict is taken from whichever run is newest.
//
// Additive with a default, so every existing row keeps meaning what it meant.
package db

import (
	"database/sql"
	"fmt"
)

const systemTestOnlyColumnSQL = `ALTER TABLE system_tests_runs ADD COLUMN IF NOT EXISTS only_test TEXT NOT NULL DEFAULT ''`

// migrateV081PG adds system_tests_runs.only_test on PostgreSQL.
func migrateV081PG(d *sql.DB) error {
	if _, err := d.Exec(systemTestOnlyColumnSQL); err != nil {
		return fmt.Errorf("v0.81 system_tests_runs.only_test: %w", err)
	}
	return nil
}

// migrateV081SQLite adds system_tests_runs.only_test through the single DDL
// chokepoint (SQLite has no ADD COLUMN IF NOT EXISTS).
func migrateV081SQLite(d *sql.DB) error {
	if err := execSQLiteDDL(d, []string{systemTestOnlyColumnSQL}); err != nil {
		return fmt.Errorf("v0.81 system_tests_runs.only_test (sqlite): %w", err)
	}
	return nil
}
