//go:build postgres

// test_pg_migrations_test.go — v0.31.0 PG verification tests
//
// Four tests pin the contract for the v0.31.0 PG foundation:
//
//  1. roundtrip    — SQLite migrations have a PG port (schema
//                    equivalence check via table list)
//  2. idempotency  — running MigratePostgres twice on the same DB
//                    produces the same final state (no FK errors,
//                    no duplicate-row errors)
//  3. lock_timeout — concurrent MigratePostgres calls don't deadlock
//                    (lock_timeout + 5s budget per call)
//  4. data_mig     — the dump_sqlite.py output applies cleanly to PG
//                    (roundtrip: SQLite → SQL file → PG)
//
// All four require a live PG instance. The build tag is
// `postgres` (matches driver_postgres.go). Tests skip unless
// SKYGATE_TEST_PG_DSN is set, so they don't break default CI.
//
// Set SKYGATE_TEST_PG_DSN=postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable
// to enable on a staging VM.

package db

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const skipPGMessage = "SKYGATE_TEST_PG_DSN not set; skipping live PG test (build with -tags postgres and set the env var to enable)"

// pgTestDSN returns the test DSN, or empty if not configured.
func pgTestDSN() string {
	return os.Getenv("SKYGATE_TEST_PG_DSN")
}

// openTestPG opens a fresh test DB (uses a unique schema name so
// concurrent tests don't collide on table names).
func openTestPG(t *testing.T) *sql.DB {
	t.Helper()
	dsn := pgTestDSN()
	if dsn == "" {
		t.Skip(skipPGMessage)
	}
	// The DSN is a real connection. We use the SAME database for all
	// tests; the unique-schema trick (below) gives each test a clean
	// slate. Without that, table-name conflicts would fail the
	// second test that runs.
	conn, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	// Use a unique schema per test for isolation. PG lets us
	// CREATE SCHEMA IF NOT EXISTS, so this is idempotent.
	schema := "skygate_pgtest_" + strings.ReplaceAll(t.Name(), "/", "_")
	schema = strings.ToLower(schema)
	if _, err := conn.Exec(`CREATE SCHEMA IF NOT EXISTS ` + schema); err != nil {
		t.Fatalf("CREATE SCHEMA %q: %v", schema, err)
	}
	if _, err := conn.Exec(`SET search_path TO ` + schema); err != nil {
		t.Fatalf("SET search_path %q: %v", schema, err)
	}
	t.Cleanup(func() {
		conn.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		conn.Close()
	})
	return conn
}

// Test 1: roundtrip — PG migration creates the same set of tables
// as the SQLite migration. We check by name (the full column-level
// equivalence is a separate, future test).
func TestPGRoundtripSchema(t *testing.T) {
	conn := openTestPG(t)
	if err := MigratePostgres(conn); err != nil {
		t.Fatalf("MigratePostgres: %v", err)
	}
	rows, err := conn.Query(`
		SELECT tablename FROM pg_tables
		WHERE schemaname = current_schema()
		  AND tablename NOT LIKE 'pg_%'`)
	if err != nil {
		t.Fatalf("Query pg_tables: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Errorf("Scan: %v", err)
			continue
		}
		got = append(got, name)
	}
	sort.Strings(got)
	// Required tables: every table the SQLite migrations_v0.*.go
	// files create on main. (Cross-checked against the live VM DB
	// schema on 2026-07-25.)
	required := []string{
		"acl_snapshots", "audit_log", "device_rules", "devices",
		"exit_node_health", "exit_node_state_changes",
		"exit_rule_logs", "exit_servers", "global_settings",
		"headscale_releases", "invite_codes", "mesh_members",
		"meshes", "node_owner_map", "personal_api_tokens",
		"portal_users", "preauth_keys", "telegram_alerts",
		"telegram_bindings", "telegram_login_tokens",
		"telegram_rate_limit", "user_subnet_shares",
		"user_subnets",
	}
	gotSet := make(map[string]bool, len(got))
	for _, n := range got {
		gotSet[n] = true
	}
	for _, r := range required {
		if !gotSet[r] {
			t.Errorf("missing required table: %s", r)
		}
	}
	t.Logf("PG schema OK: %d tables created, %d required", len(got), len(required))
}

// Test 2: idempotency — running MigratePostgres twice should be a
// no-op the second time. CREATE TABLE IF NOT EXISTS + ALTER with
// duplicate-column guards make this safe. We verify by counting
// tables before and after.
func TestPGMigrationIdempotency(t *testing.T) {
	conn := openTestPG(t)
	if err := MigratePostgres(conn); err != nil {
		t.Fatalf("MigratePostgres (1st): %v", err)
	}
	firstCount := countTables(t, conn)
	if err := MigratePostgres(conn); err != nil {
		t.Fatalf("MigratePostgres (2nd) must be idempotent: %v", err)
	}
	secondCount := countTables(t, conn)
	if firstCount != secondCount {
		t.Errorf("table count changed: 1st=%d, 2nd=%d (regression — migrations not idempotent)", firstCount, secondCount)
	}
	t.Logf("PG migration idempotency OK: %d tables after 2 runs", secondCount)
}

func countTables(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(`
		SELECT count(*) FROM pg_tables
		WHERE schemaname = current_schema()
		  AND tablename NOT LIKE 'pg_%'`).Scan(&n); err != nil {
		t.Fatalf("count pg_tables: %v", err)
	}
	return n
}

// Test 3: lock_timeout — concurrent MigratePostgres calls must not
// deadlock. With SET lock_timeout = '5s', one call wins and the
// other fails fast (or both run sequentially on a single connection).
// We use TWO separate connections, both calling MigratePostgres
// concurrently, and assert neither deadlocks.
func TestPGLockTimeout(t *testing.T) {
	dsn := pgTestDSN()
	if dsn == "" {
		t.Skip(skipPGMessage)
	}
	// Each goroutine gets its own schema to migrate, so they don't
	// actually conflict on tables. The point is to exercise
	// SET lock_timeout + pg_advisory_lock paths in the migration
	// code under concurrency. The migrations should both succeed
	// (each in its own schema), and finish in under 30s.
	schema := "skygate_pgtest_lock_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	if _, err := sql.Open("pgx", dsn); err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Pre-create both schemas so the goroutines don't race on CREATE SCHEMA.
	conn, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE SCHEMA IF NOT EXISTS ` + schema + `_a`); err != nil {
		t.Fatalf("create schema a: %v", err)
	}
	if _, err := conn.Exec(`CREATE SCHEMA IF NOT EXISTS ` + schema + `_b`); err != nil {
		t.Fatalf("create schema b: %v", err)
	}
	t.Cleanup(func() {
		conn.Exec(`DROP SCHEMA IF EXISTS ` + schema + `_a CASCADE`)
		conn.Exec(`DROP SCHEMA IF EXISTS ` + schema + `_b CASCADE`)
	})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, sfx := range []string{"_a", "_b"} {
		i, sfx := i, sfx
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 30s budget per call. With lock_timeout=5s, this is
			// 6x the lock budget — plenty of headroom.
			done := make(chan error, 1)
			go func() {
				c, err := OpenPostgres(dsn)
				if err != nil {
					done <- err
					return
				}
				defer c.Close()
				if _, err := c.Exec(`SET search_path TO ` + schema + sfx); err != nil {
					done <- err
					return
				}
				done <- MigratePostgres(c)
			}()
			select {
			case errs[i] = <-done:
			case <-time.After(30 * time.Second):
				errs[i] = sql.ErrTxDone // any sentinel; the test will fail with a clear message
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent migration %d failed (or deadlocked): %v", i, err)
		}
	}
	t.Logf("PG lock_timeout OK: 2 concurrent migrations both succeeded")
}

// Test 4: data_mig — roundtrip: build a small SQLite DB, run
// dump_sqlite.py on it, apply the output to a fresh PG, and
// verify the row counts match.
//
// This test only runs if the dump_sqlite.py script exists in the
// expected path (the standard project layout) AND Python is on PATH.
// On the build host without the helper, t.Skip the data-mig step.
func TestPGDataMigrationFromSQLite(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("python"); err != nil {
			t.Skip("no python on PATH; skipping dump_sqlite.py roundtrip test")
		}
	}
	python := "python3"
	if _, err := exec.LookPath("python3"); err != nil {
		python = "python"
	}
	// 1. Build a small SQLite DB with a few rows.
	dir := t.TempDir()
	sqlitePath := filepath.Join(dir, "source.db")
	src, err := Open(sqlitePath)
	if err != nil {
		t.Fatalf("Open sqlite: %v", err)
	}
	defer src.Close()
	// Insert one portal_users row + one audit_log row so the
	// dump has something to emit.
	if _, err := src.Exec(`INSERT INTO portal_users (username, password_hash) VALUES (?, ?)`, "dumpme", "x"); err != nil {
		t.Fatalf("insert portal_users: %v", err)
	}
	if _, err := src.Exec(`INSERT INTO audit_log (user_id, username, action, detail) VALUES (1, 'dumpme', 'pg_dump_test', 'created')`); err != nil {
		t.Fatalf("insert audit_log: %v", err)
	}
	src.Close()

	// 2. Run dump_sqlite.py to produce SQL.
	dumpScript := filepath.Join("scripts", "dump_sqlite.py")
	if _, err := os.Stat(dumpScript); err != nil {
		t.Skipf("dump_sqlite.py not found at %s: %v", dumpScript, err)
	}
	outPath := filepath.Join(dir, "dump.sql")
	cmd := exec.Command(python, dumpScript, "--input", sqlitePath, "--output", outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dump_sqlite.py: %v\noutput: %s", err, out)
	}

	// 3. Apply the dump to a fresh PG schema.
	conn := openTestPG(t)
	if err := MigratePostgres(conn); err != nil {
		t.Fatalf("MigratePostgres: %v", err)
	}
	// Read the dump file.
	sqlBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile dump: %v", err)
	}
	if _, err := conn.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("apply dump.sql to PG: %v", err)
	}

	// 4. Verify the rows are there.
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM portal_users WHERE username = $1`, "dumpme").Scan(&n); err != nil {
		t.Fatalf("SELECT portal_users: %v", err)
	}
	if n != 1 {
		t.Errorf("portal_users count = %d, want 1", n)
	}
	if err := conn.QueryRow(`SELECT count(*) FROM audit_log WHERE action = $1`, "pg_dump_test").Scan(&n); err != nil {
		t.Fatalf("SELECT audit_log: %v", err)
	}
	if n != 1 {
		t.Errorf("audit_log count = %d, want 1", n)
	}
	t.Logf("PG data migration OK: dump_sqlite.py output applied cleanly, 2 rows verified")
}
