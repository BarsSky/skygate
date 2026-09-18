package db

// sqlite_ddl_exec_test.go — 2026-09-18 (§12.13 follow-up).
//
// execSQLiteDDL is the single chokepoint every SQLite migration now runs its
// statements through (previously ~18 ad-hoc loops, nine of which swallowed
// every error). These tests pin the two behaviours that the swallowing hid:
//
//  1. an `ADD COLUMN` statement is idempotent even when the migration spells
//     it with PostgreSQL's `IF NOT EXISTS` (a SYNTAX ERROR in SQLite, which
//     the old loops mistook for "the column already exists" — that is how
//     exit_servers.{ssh_target,ssh_key_path,accept_routes} and
//     device_rules.device_ip silently never appeared on a fresh database);
//  2. everything that is NOT an ADD COLUMN surfaces its error instead of
//     being skipped, so a broken migration stops rather than continuing on a
//     half-built schema.

import (
	"strings"
	"testing"
)

func TestExecSQLiteDDL_AddColumnIsIdempotent(t *testing.T) {
	d, err := openSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}

	stmts := []string{
		`ALTER TABLE t ADD COLUMN parent_domain TEXT NOT NULL DEFAULT ''`,
		`CREATE INDEX IF NOT EXISTS t_parent_idx ON t(parent_domain)`,
	}
	// Run the batch twice: the second run is what used to break
	// ("duplicate column name" — and, before §12.13, the error was swallowed).
	for i := 1; i <= 2; i++ {
		if err := execSQLiteDDL(d, stmts); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if !sqliteColumnExists(d, "t", "parent_domain") {
		t.Fatal("parent_domain was not created")
	}
}

func TestExecSQLiteDDL_AcceptsPostgresIfNotExistsSpelling(t *testing.T) {
	d, err := openSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	// The exact pre-fix defect: this is invalid SQLite, and the old loops
	// treated the syntax error as "already exists".
	stmts := []string{`ALTER TABLE t ADD COLUMN IF NOT EXISTS device_ip TEXT NOT NULL DEFAULT ''`}
	for i := 1; i <= 2; i++ {
		if err := execSQLiteDDL(d, stmts); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if !sqliteColumnExists(d, "t", "device_ip") {
		t.Fatal("device_ip was not created from an IF NOT EXISTS spelling")
	}
}

func TestExecSQLiteDDL_NonAddColumnErrorsSurface(t *testing.T) {
	d, err := openSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	err = execSQLiteDDL(d, []string{"CREATE TABLE (broken"})
	if err == nil {
		t.Fatal("a broken CREATE TABLE must return an error, not be skipped")
	}
	if !strings.Contains(err.Error(), "ddl") {
		t.Errorf("error should name the failing statement, got %q", err)
	}
}

func TestExecSQLiteDDL_SkipsEmptyStatements(t *testing.T) {
	d, err := openSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// The generated migrations contain bare `` entries.
	if err := execSQLiteDDL(d, []string{"", "   ", "\n"}); err != nil {
		t.Fatalf("empty statements must be skipped, got %v", err)
	}
}
