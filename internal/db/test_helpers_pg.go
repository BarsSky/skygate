// test_helpers_pg.go — exported PG test helpers (v1.3.0+).
//
// As of v1.3.0, skygate is PG-only; SQLite is no longer
// supported. Tests that previously used sql.Open("sqlite3",
// ":memory:") or db.Open(<tempfile>) now use db.OpenTestPG(t),
// which opens a real PG connection (via SKYGATE_TEST_PG_DSN)
// and uses a unique schema name per test for isolation.
//
// The helper SKIPS (does not fail) when SKYGATE_TEST_PG_DSN is
// unset, so the test suite is runnable on a dev machine without
// a live PG. Set SKYGATE_TEST_PG_DSN=postgres://skygate:... in
// CI / staging VM to actually exercise the migration chain.
//
// Why a separate file (no _test.go suffix, no build tag)?
//
//   - It must be reachable from tests OUTSIDE internal/db (e.g.
//     internal/invite, internal/monitoring), so the file is
//     not _test.go.
//   - It is only called from test code, so leaving it in the
//     main package is wasteful (a few KB) but harmless — the
//     production binary never calls these helpers.

package db

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

const skipPGMessage = "SKYGATE_TEST_PG_DSN not set; skipping live PG test (set SKYGATE_TEST_PG_DSN=postgres://... to enable)"

// pgTestDSN returns the test DSN, or empty if not configured.
func pgTestDSN() string {
	return os.Getenv("SKYGATE_TEST_PG_DSN")
}

// OpenTestPG opens a fresh test DB (uses a unique schema name so
// concurrent tests don't collide on table names). Exported so
// packages outside internal/db can use the same helper.
//
// The returned *sql.DB is registered in the BackendOf() registry
// as BackendPostgres (matches the production OpenDSN behavior so
// query helpers that dispatch on backend work in tests too).
//
// The unique schema is created on Open and dropped on t.Cleanup.
//
// 2026-09-15 (B253 fix): the original code used `SET search_path TO <schema>`
// after sql.Open, which DOES NOT propagate across database/sql's
// connection pool. MigratePostgres ran on connection A and
// created tables in `<schema>`, but SaveACLSnapshot / seed
// helpers might run on connection B which still had
// search_path=public — and so the test failed with
// "relation acl_snapshots does not exist" even though the
// table existed in the test schema. Fix: append
// `options=-csearch_path=<schema>` to the DSN so every
// NEW connection in the pool inherits the search_path.
// pgx/stdlib honours the `-c` option on connect.
func OpenTestPG(t testing.TB) *sql.DB {
	t.Helper()
	dsn := pgTestDSN()
	if dsn == "" {
		t.Skip(skipPGMessage)
		return nil // unreachable
	}
	// Use a unique schema per test for isolation. PG lets us
	// CREATE SCHEMA IF NOT EXISTS, so this is idempotent.
	schema := "skygate_pgtest_" + strings.ReplaceAll(t.Name(), "/", "_")
	schema = strings.ToLower(schema)
	// Inject `options=-csearch_path=<schema>` into the DSN so
	// every new pooled connection boots with search_path set.
	// The pgx driver passes the `options` parameter through
	// `PQoptions` on connect, which is equivalent to running
	// `SET search_path <schema>` BEFORE any client query.
	dsn = injectSearchPath(dsn, schema)
	// Open via the same path as production so pool settings +
	// MigratePostgres run. We do NOT use OpenDSN directly because
	// the DSN comes from the env var (not config.Load), but the
	// underlying call is identical.
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open pgx: %v", err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		t.Fatalf("ping: %v (check SKYGATE_TEST_PG_DSN)", err)
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	// Create the schema BEFORE running migrations so the
	// search_path (set via the -c options) resolves to it.
	if _, err := conn.Exec(`CREATE SCHEMA IF NOT EXISTS ` + schema); err != nil {
		conn.Close()
		t.Fatalf("CREATE SCHEMA %q: %v", schema, err)
	}
	if err := MigratePostgres(conn); err != nil {
		conn.Close()
		t.Fatalf("MigratePostgres: %v", err)
	}
	registerBackend(conn, BackendPostgres)
	t.Cleanup(func() {
		conn.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		conn.Close()
	})
	return conn
}

// injectSearchPath adds `options=-csearch_path=<schema>` to the
// DSN so every new pgx connection inherits the search_path at
// connect time (without needing per-connection SET search_path).
// Existing `options=` in the DSN are preserved.
func injectSearchPath(dsn, schema string) string {
	opt := "options=-csearch_path=" + schema
	// Match `?...` (existing options) — replace or append.
	if i := strings.Index(dsn, "?"); i >= 0 {
		// Look for an existing "options=" inside the query string
		// and merge ours in so we don't lose SSL mode / pool_max_conns
		// / etc. that the caller already passed.
		q := dsn[i+1:]
		opts := strings.Split(q, "&")
		replaced := false
		for k, o := range opts {
			if strings.HasPrefix(o, "options=") {
				opts[k] = o + " " + opt
				replaced = true
				break
			}
		}
		if replaced {
			return dsn[:i+1] + strings.Join(opts, "&")
		}
		return dsn + "&" + opt
	}
	return dsn + "?" + opt
}
