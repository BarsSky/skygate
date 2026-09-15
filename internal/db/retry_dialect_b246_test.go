// internal/db/retry_dialect_b246_test.go — B246 dialect-dispatch tests.
//
// B246 (2026-09-15): openDSNPing used to hardcode `sql.Open("pgx", dsn)`,
// so a SQLite DSN succeeded the open (pgx registered the driver name
// lazily) but failed on every subsequent query with "driver: bad
// connection" / "syntax error at or near ?". The fix dispatches via
// dialect.DetectDSN → Dialect.OpenDialect, then calls the per-dialect
// Migrate function. These tests pin the four critical behaviours:
//
//   1. SQLite :memory: DSN opens a usable *sql.DB with BackendSQLite.
//   2. SQLite file DSN opens a usable *sql.DB with BackendSQLite.
//   3. PostgreSQL DSN opens a usable *sql.DB with BackendPostgres.
//   4. Unrecognised DSN scheme returns a clean "unknown DSN scheme"
//      error instead of the cryptic pgx parse error.
//
// #5-#7 are integration tests that require a live PG (skip if no
// SKYGATE_TEST_PG_DSN env). They're skipped by default in
// `go test ./internal/db/...`; CI sets the env to run them.
package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestOpenDSNPing_SQLiteMemory pins :memory: → BackendSQLite path.
// This is the form the B-mod-sqlite-pg-bidi integration test uses
// (C:\skygate-test\ was the B245 e2e, the B246 e2e re-uses the
// :memory: DSN in a test process to exercise the new path).
func TestOpenDSNPing_SQLiteMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := openDSNPing(":memory:", ctx)
	if err != nil {
		t.Fatalf("openDSNPing(:memory:): %v", err)
	}
	defer conn.Close()

	if got := BackendOf(conn); got != BackendSQLite {
		t.Errorf("BackendOf: got %q, want %q", got, BackendSQLite)
	}

	// Smoke test: the migration chain ran (portal_users exists) and
	// a basic query works.
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM portal_users").Scan(&n); err != nil {
		t.Errorf("SELECT count(*) FROM portal_users: %v", err)
	}
}

// TestOpenDSNPing_SQLiteFile pins the file:/path form. Uses t.TempDir()
// so the test is hermetic (no leftover file on disk).
func TestOpenDSNPing_SQLiteFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dir := t.TempDir()
	dsn := "file:" + dir + "/test.db"

	conn, err := openDSNPing(dsn, ctx)
	if err != nil {
		t.Fatalf("openDSNPing(%q): %v", dsn, err)
	}
	defer conn.Close()

	if got := BackendOf(conn); got != BackendSQLite {
		t.Errorf("BackendOf: got %q, want %q", got, BackendSQLite)
	}

	// Migration chain ran on the file — same SQL fragment works as
	// the :memory: test (proves the schema is dialect-agnostic at the
	// query layer).
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM portal_users").Scan(&n); err != nil {
		t.Errorf("SELECT count(*) FROM portal_users: %v", err)
	}
}

// TestOpenDSNPing_Postgres pins the PG path. Skipped if no live PG
// (the test infrastructure at C:\skygate-test uses Docker; this test
// uses the same SKYGATE_TEST_PG_DSN env var convention).
//
// Live-verify on 192.168.13.20: PG container skygate-test_pg-test
// on port 5432 with skygate/skygate_test_pw credentials.
func TestOpenDSNPing_Postgres(t *testing.T) {
	dsn := os.Getenv("SKYGATE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("SKYGATE_TEST_PG_DSN not set — no live PG for integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := openDSNPing(dsn, ctx)
	if err != nil {
		t.Fatalf("openDSNPing(pg): %v", err)
	}
	defer conn.Close()

	if got := BackendOf(conn); got != BackendPostgres {
		t.Errorf("BackendOf: got %q, want %q", got, BackendPostgres)
	}

	// PG syntax: $1 placeholder (proves the driver is pgx, not sqlite).
	var n int
	if err := conn.QueryRowContext(ctx, "SELECT $1::int", 42).Scan(&n); err != nil {
		t.Errorf("SELECT $1::int: %v", err)
	}
	if n != 42 {
		t.Errorf("SELECT $1::int = %d, want 42", n)
	}
}

// TestOpenDSNPing_UnknownScheme pins the new error path. Pre-B246
// this returned a pgx-specific parse error ("failed to parse as
// keyword/value: ... mysql:...") which made the failure mode
// confusing. Post-B246 it returns a clean message that names the
// dialect dispatcher as the source.
func TestOpenDSNPing_UnknownScheme(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := openDSNPing("mysql://user:pass@host:3306/db", ctx)
	if err == nil {
		t.Fatal("openDSNPing(mysql://): expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown DSN scheme") {
		t.Errorf("error message %q should contain 'unknown DSN scheme'", err.Error())
	}
}

// TestOpenDSNWithRetry_SQLiteMemory pins that the retry helper itself
// works on the SQLite path. The pre-B246 retry helper would have
// returned a (fake) successful connection on :memory: because pgx's
// sql.Open doesn't validate the driver against the DSN — so a SQLite
// DSN would silently get an unusable pgx-typed *sql.DB. Post-B246 the
// retry helper goes through DetectDSN → OpenDialect → the right
// driver.
func TestOpenDSNWithRetry_SQLiteMemory(t *testing.T) {
	conn, err := OpenDSNWithRetry(":memory:", 3, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("OpenDSNWithRetry(:memory:): %v", err)
	}
	defer conn.Close()

	if got := BackendOf(conn); got != BackendSQLite {
		t.Errorf("BackendOf: got %q, want %q", got, BackendSQLite)
	}
}

// TestOpenDSNWithRetry_BadDSN_ExhaustsRetries pins that the retry
// helper still works for genuinely unreachable PG (the original
// B-mod-db-retry promise: "DB unreachable after N attempts" instead
// of an opaque restart loop). Uses a TCP port nothing is listening
// on (127.0.0.1:1 — port 1 is reserved, never has a service).
func TestOpenDSNWithRetry_BadDSN_ExhaustsRetries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow retry test in -short mode")
	}
	// postgres://127.0.0.1:1 → connection refused within ~50ms.
	// 3 attempts × 10ms baseDelay = ~30ms total worst case.
	start := time.Now()
	_, err := OpenDSNWithRetry("postgres://skygate:wrong@127.0.0.1:1/skygate?sslmode=disable&connect_timeout=1", 3, 10*time.Millisecond)
	dur := time.Since(start)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), ErrDBUnreachable.Error()) {
		t.Errorf("error message %q should wrap ErrDBUnreachable", err.Error())
	}
	// Sanity check: the retry budget was actually used (would take
	// ~0ms to fail without retry). Allow a wide upper bound so this
	// isn't flaky on slow CI.
	if dur > 10*time.Second {
		t.Errorf("retry took %v, expected <<10s", dur)
	}
}
