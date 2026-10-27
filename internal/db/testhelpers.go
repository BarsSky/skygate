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
//
// THE BUSY_TIMEOUT THAT WAS MISSING (B426.1, 2026-10-26)
// -----------------------------------------------------
// openSQLiteWith appends foreign_keys(1) + journal_mode(WAL) + busy_timeout(2000) to a
// file DSN — but it TRUSTS a DSN that already carries `_pragma=`, so the two fast pragmas
// above silently replaced the whole production set. Without busy_timeout, a second writer
// on the same file gets SQLITE_BUSY IMMEDIATELY instead of waiting, and the alert
// transport writes from two goroutines by design (insertAlert on the send path,
// `go pruneAlerts` on every SendAlert). Measured on this workstation: 4 goroutines x 100
// insert+prune rounds on a fast per-test DB —
//
//	without busy_timeout   inserts_ok=163  busy=237   ← 59% of the writes LOST
//	with busy_timeout(2000) inserts_ok=400  busy=0
//
// A lost insert is not a lost message — the send still happens — but insertAlert returns
// no id, so SendAlertEvent returns 0 while the operator WAS notified, which is exactly the
// "0 means not delivered" contract callers (and the B391 disk monitor) act on. That is how
// CI went red on a green tree: TestB391_DedupSuppressesRepeatsInsideTheWindow failed with
// "the first alert of a key must be sent" and passed 5/5 standalone.
//
// STILL OPEN (TD-35): the same replacement drops foreign_keys(1), so a per-test SQLite
// database enforces NO foreign keys. Restoring it is a separate block because it changes
// what every per-test fixture may insert; the gap is recorded rather than silently fixed.
package db

import "database/sql"

// sqliteFastTestPragmas is the connection suffix that makes a throwaway SQLite database
// cheap to migrate. synchronous(0) + journal_mode(memory) are the B419 measurement;
// busy_timeout(2000) is B426.1; foreign_keys(1) is B427 (TD-35) — without it a per-test
// database enforced NO foreign key, so every FK clause in the schema was lint.
const sqliteFastTestPragmas = "?_pragma=synchronous(0)&_pragma=journal_mode(memory)&_pragma=busy_timeout(2000)&_pragma=foreign_keys(1)"

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
