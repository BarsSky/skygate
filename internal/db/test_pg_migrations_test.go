// test_pg_migrations_test.go — v0.31.0 PG verification tests
// (updated v1.3.0: skygate is PG-only; no build tag).
//
// Four tests pin the contract for the PG migration chain:
//
//  1. roundtrip    — MigratePostgres creates the expected table set
//  2. idempotency  — running MigratePostgres twice on the same DB
//                    produces the same final state (no FK errors,
//                    no duplicate-row errors)
//  3. lock_timeout — concurrent MigratePostgres calls don't deadlock
//                    (lock_timeout + 5s budget per call)
//  4. data_mig     — historical SQLite dump applies cleanly to PG
//                    (roundtrip: SQLite → SQL file → PG)
//
// All four require a live PG instance. They skip when
// SKYGATE_TEST_PG_DSN is unset, so the suite still passes on
// dev machines without PG.
//
// Set SKYGATE_TEST_PG_DSN=postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable
// to enable on a staging VM.
//
// As of v1.3.0, the actual test helper (OpenTestPG + pgTestDSN +
// skipPGMessage) lives in test_helpers_pg.go (no _test.go suffix
// + no build tag) so packages outside internal/db can use it.

package db

import (
	"database/sql"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test 1: roundtrip — PG migration creates the same set of tables
// as the SQLite migration. We check by name (the full column-level
// equivalence is a separate, future test).
func TestPGRoundtripSchema(t *testing.T) {
	conn := OpenTestPG(t)
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
	conn := OpenTestPG(t)
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
// v1.3.0: skygate is PG-only. SQLite → PG data migration is no
// longer a supported flow (fresh deploys start on PG; the legacy
// SQLite DB on the VM is dead). The dump_sqlite.py script is
// retained for one-time historical conversion but is not tested
// in CI. The body of this test is replaced with a stub to keep
// the test file compiling (no Open() / os.Stdin references
// allowed after the SQLite driver removal).
func TestPGDataMigrationFromSQLite(t *testing.T) {
	t.Skip("v1.3.0: skygate is PG-only; SQLite → PG data migration is no longer a supported flow. The dump_sqlite.py script is retained for one-time historical conversion but is not tested in CI. Operators upgrading from a pre-v1.3.0 SQLite deployment should follow docs/deploy.md#postgresql-migration-from-sqlite (added in Phase 5).")
	_ = exec.Command // keep import for tests that DO run above
}
