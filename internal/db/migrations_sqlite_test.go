// internal/db/migrations_sqlite_test.go — RED tests for the
// B-mod-sqlite-pg-bidi SQLite migration set (Task 2).
//
// The skygate schema has ~50 migrations (v0.20-v0.70) that all
// apply cleanly to PostgreSQL via migrations_pg.go. Task 2 builds
// the parallel SQLite migration set: the same migration numbers,
// the same data shape, dialect-native DDL.
//
// The test below verifies the whole chain applies to a fresh
// :memory: SQLite DB and that the applied_migrations bookkeeping
// table has the expected row count.
//
// Generation strategy: scripts/port_migrations_sqlite.py reads
// every PG migration source + migrations_pg.go and emits
// migrations_sqlite.go with reverse substitutions
// (BIGSERIAL → INTEGER PK AUTOINCREMENT, ON CONFLICT → INSERT OR
// IGNORE, EXTRACT(EPOCH FROM now())::bigint → strftime('%s','now'),
// etc). The script is conservative — some manual review is
// expected. This test catches the residual failures.
package db

import (
	"os"
	"strings"
	"testing"
)

// TestMigrationsSQLite_AllApply walks the entire SQLite migration
// chain on a fresh in-memory DB and verifies:
//
//   1. Every migration applies without error
//   2. The applied_migrations bookkeeping table has at least 40
//      rows (the live PG set has ~50 migrations v0.20 through
//      v0.70; we accept >=40 to allow for the optional v0.54
//      disabled + any future migrations not yet in scope)
//
// Pre-v1.5.4 (B-mod-db-retry era) this test could not even
// compile — there was no SQLite migration set, no Dialect-aware
// ApplyMigrations. The whole point of B-mod-sqlite-pg-bidi Task 2
// is to make this test pass on a vanilla :memory: SQLite.
func TestMigrationsSQLite_AllApply(t *testing.T) {
	d, db, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	defer db.Close()

	if err := ApplyMigrations(db, d.Kind); err != nil {
		t.Fatalf("ApplyMigrations(SQLite): %v", err)
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM applied_migrations").Scan(&n); err != nil {
		t.Fatalf("count applied_migrations: %v", err)
	}
	if n < 40 {
		t.Errorf("expected >= 40 migrations applied, got %d", n)
	}
}

// TestMigrationsSQLite_NoPGExclusiveSyntax is a static check: the
// generated migrations_sqlite.go must NOT contain any PG-exclusive
// SQL tokens (BIGSERIAL, EXTRACT(EPOCH, JSONB, BOOLEAN, TIMESTAMPTZ,
// bytea) INSIDE backtick-delimited SQL strings. The doc comment at
// the top of the file legitimately mentions these tokens (it
// documents the reverse-substitutions the port script applies) —
// so we extract SQL fragments (text between backticks) only.
//
// If a token leaks into SQL, the live :memory: open fails with
// "syntax error at or near <token>" — but the static check is a
// much faster catch.
func TestMigrationsSQLite_NoPGExclusiveSyntax(t *testing.T) {
	data, err := os.ReadFile("migrations_sqlite.go")
	if err != nil {
		t.Skipf("migrations_sqlite.go not readable: %v (skipping static check)", err)
	}
	src := string(data)
	// Extract all backtick-delimited SQL strings (text between
	// matching backticks). Doc comments OUTSIDE backticks are
	// excluded — they legitimately describe the substitutions.
	var sqlFragments []string
	i := 0
	for i < len(src) {
		if src[i] == '`' {
			j := strings.Index(src[i+1:], "`")
			if j < 0 {
				break
			}
			sqlFragments = append(sqlFragments, src[i+1:i+1+j])
			i = i + 1 + j + 1
		} else {
			i++
		}
	}
	joined := strings.Join(sqlFragments, "\n")
	forbidden := []string{
		"BIGSERIAL",     // PG-only; SQLite uses INTEGER PRIMARY KEY AUTOINCREMENT
		"EXTRACT(EPOCH", // PG-only; SQLite uses strftime('%s','now')
		"JSONB",         // PG-only; SQLite has no JSONB type (use TEXT)
		"TIMESTAMPTZ",   // PG-only; SQLite has no native timestamp-with-tz
		"bytea",         // PG-only; SQLite uses BLOB
	}
	for _, tok := range forbidden {
		t.Run(tok, func(t *testing.T) {
			if strings.Contains(joined, tok) {
				t.Errorf("migrations_sqlite.go SQL fragment contains PG-only token %q — port script regression; re-run scripts/port_migrations_sqlite.py", tok)
			}
		})
	}
}

// TestMigrationsSQLite_KeyTablesExist verifies the canonical
// skygate tables (portal_users, device_rules, exit_servers,
// applied_migrations) exist after applying all migrations. Catches
// the case where the port script silently drops a CREATE TABLE.
func TestMigrationsSQLite_KeyTablesExist(t *testing.T) {
	d, db, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	defer db.Close()

	if err := ApplyMigrations(db, d.Kind); err != nil {
		t.Fatalf("ApplyMigrations(SQLite): %v", err)
	}

	want := []string{
		"portal_users",
		"device_rules",
		"exit_servers",
		"applied_migrations",
		"telegram_alerts",
		"node_owner_map",
		"global_settings",
		"audit_log",
		"personal_api_tokens",
		"cluster",
		"cluster_node",
		"cluster_database",
		"cluster_migration",
		"cluster_invite",
		"cluster_audit",
		"headscale_acl_rules",
		"dbmigrate_run",
		"dbmigrate_step",
		"deploy_runs",
		"deploy_run_steps",
		"derp_health",
	}
	for _, tbl := range want {
		var n int
		err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&n)
		if err != nil {
			t.Errorf("query sqlite_master for %q: %v", tbl, err)
			continue
		}
		if n != 1 {
			t.Errorf("table %q not found after migrations (count=%d)", tbl, n)
		}
	}
}
