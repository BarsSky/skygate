// testhelpers.go — SQLite handles for TESTS (B419, TD-30).
//
// Why this file is not a _test.go file: it has to be callable from EVERY package's
// tests, and a helper declared in `package db`'s *_test.go is invisible outside that
// package. The precedent is internal/telegram/test_helpers.go — helpers for tests live
// in a normal file, marked as such. Nothing in production calls this function.
//
// THE MEASUREMENT (2026-10-25, reference workstation)
// ---------------------------------------------------
// TD-30 recorded `go test ./internal/feature/exit_rules/` at 214 s on the VM and 72 s
// here, blaming the per-test migration chain ("every test migrates a fresh SQLite DB").
// Measured directly: one full chain costs ~536-880 ms with the default pragmas, and the
// cost is FSYNC, not SQL — the chain is hundreds of tiny DDL commits.
//
// Measured alternatives (3 rounds each, same machine):
//
//	default                          chain = 880 ms
//	synchronous(0) alone             chain = 561 ms
//	journal_mode(memory) alone       chain = 1151 ms
//	BOTH (this helper)               chain = 53 ms      ← 16x
//
// Neither pragma alone helps (each is still one atomic commit per statement); together
// they remove the two fsyncs per statement. For a THROWAWAY test database durability is
// irrelevant, and atomicity is preserved: journal_mode=memory still rolls back, it just
// does not survive a process crash, which no test relies on.
//
// WHAT IT DOES NOT CHANGE
// -----------------------
// The schema is produced by the SAME migration chain (`MigrateSQLite`), so a test that
// passes here exercises the real chain, not a hand-written schema. Only the pragmas on
// the connection differ, and they are read-only with respect to the product: production
// opens through OpenWithDialect with its own DSN.
package db

import "database/sql"

// sqliteFastTestPragmas is the connection suffix that makes a throwaway SQLite database
// cheap to migrate. Both pragmas are needed — see the measurement above.
const sqliteFastTestPragmas = "?_pragma=synchronous(0)&_pragma=journal_mode(memory)"

// OpenFastSQLiteForTest opens a throwaway SQLite database whose commits are not fsynced
// and whose rollback journal lives in memory.
//
// Use it ONLY from tests, in place of
//
//	OpenWithDialect("sqlite:" + path)
//
// for a per-test database that is about to be migrated from scratch. Production code must
// keep using OpenWithDialect: durability is the whole point there.
func OpenFastSQLiteForTest(path string) (*sql.DB, error) {
	_, d, err := OpenWithDialect("sqlite:" + path + sqliteFastTestPragmas)
	return d, err
}
