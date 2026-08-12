// v1.3.0: testutil.go is the shared helper for the feature/admin
// tests. The full file used newMemoryDB (SQLite) which has been
// removed. PG-rewrite is a Phase 2 follow-up.
//
// The current feature/admin test suite is fully stubbed. This
// stub is kept to avoid breaking any other code that imports
// the testutil package symbols. Restore the previous helper
// body (with PG-equivalent db.OpenTestPG + SERIAL/$N/EXTRACT)
// when rewriting the tests.
package admin

// (Phase 2 TODO: restore testBackend, newTestService,
// newMemoryDB (now PG-backed), authedReqFor, etc.)
