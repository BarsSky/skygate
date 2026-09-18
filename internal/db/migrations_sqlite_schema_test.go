// migrations_sqlite_schema_test.go — 2026-09-18 regression guard for the
// SQLite migration chain.
//
// WHAT WENT WRONG
// ---------------
// Two independent defects left the SQLite schema incomplete, and neither
// was visible from reading the code:
//
//  1. CODE DRIFT — migrations_sqlite.go is a script-generated reverse
//     port of the PG chain, and four statements were copied verbatim from
//     PostgreSQL:
//
//     ALTER TABLE exit_servers ADD COLUMN IF NOT EXISTS ssh_target ...
//
//     SQLite has no `IF NOT EXISTS` for ADD COLUMN. The port then wrapped
//     each statement in an error-swallowing loop ("ignore errors (column
//     may exist)"), so the syntax error was read as "already there" and
//     the ALTER never ran. Result: exit_servers.{ssh_target, ssh_key_path,
//     accept_routes} and device_rules.device_ip were absent from every
//     SQLite database — which is exactly what broke exit-node SSH sync and
//     per-device IP rules on SQLite.
//
//  2. MISSING VERSION — V071 (derp_cert_sync) was registered only in
//     pgMigrations, so the SQLite chain stopped at V070 and the DERP
//     certificate auto-renewal table never existed.
//
// This test asserts on a REAL :memory: database that has been through the
// real chain, so both defects fail loudly here instead of in production.
package db

import (
	"testing"
)

// requiredSQLiteColumns pins the columns that the old ADD COLUMN IF NOT
// EXISTS pattern silently dropped. Each entry is the reason it matters.
var requiredSQLiteColumns = map[string][]string{
	"exit_servers": {
		"ssh_target",    // where skygate SSHes to reconfigure the exit node
		"ssh_key_path",  // which key it uses
		"accept_routes", // per-node --accept-routes tri-state
		"ssh_port",      // non-default port (karolina-style)
	},
	"device_rules": {
		"device_ip", // per-device IP rule target
		"parent_domain",
		"user_name",
		"device_hostname",
	},
}

func TestSQLiteSchemaComplete(t *testing.T) {
	_, sqlDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	defer sqlDB.Close()
	if err := ApplyMigrations(sqlDB, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations(SQLite): %v", err)
	}

	t.Run("chain reaches V071 like PostgreSQL", func(t *testing.T) {
		var maxV int
		if err := sqlDB.QueryRow(
			`SELECT COALESCE(MAX(version), 0) FROM applied_migrations`).Scan(&maxV); err != nil {
			t.Fatalf("read max(version): %v", err)
		}
		// PostgreSQL's chain ends at 71. If SQLite lags behind, every
		// table added by the missing tail is absent.
		if maxV != 71 {
			t.Errorf("SQLite migration chain ends at V%d, want V71 — the PG and SQLite "+
				"chains have diverged again (see driver_sqlite.go sqliteMigrations)", maxV)
		}
	})

	t.Run("derp_cert_sync exists (the missing V071)", func(t *testing.T) {
		var n int
		if err := sqlDB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='derp_cert_sync'`).Scan(&n); err != nil {
			t.Fatalf("query sqlite_master: %v", err)
		}
		if n != 1 {
			t.Fatal("derp_cert_sync is missing after the SQLite chain — the DERP cert " +
				"auto-renewal state has nowhere to write (V071 was PG-only)")
		}
	})

	t.Run("columns dropped by ADD COLUMN IF NOT EXISTS are present", func(t *testing.T) {
		for table, cols := range requiredSQLiteColumns {
			for _, col := range cols {
				if !sqliteColumnExists(sqlDB, table, col) {
					t.Errorf("%s.%s is MISSING in the SQLite schema — this is the "+
						"`ALTER TABLE ... ADD COLUMN IF NOT EXISTS` bug: SQLite rejects that "+
						"syntax, and the migration chain swallowed the error", table, col)
				}
			}
		}
	})

	t.Run("re-running the chain is idempotent", func(t *testing.T) {
		// The whole point of addColumnIfMissingSQLite is that a second run
		// neither errors ("duplicate column name") nor silently skips a
		// genuinely missing column. Both properties are asserted here.
		if err := MigrateSQLite(sqlDB); err != nil {
			t.Fatalf("second MigrateSQLite: %v", err)
		}
		for table, cols := range requiredSQLiteColumns {
			for _, col := range cols {
				if !sqliteColumnExists(sqlDB, table, col) {
					t.Errorf("%s.%s disappeared after a second migration run", table, col)
				}
			}
		}
		// A column must appear exactly once (PRAGMA can report duplicates
		// only if the ALTER ran twice, which the guard prevents).
		rows, err := sqlDB.Query(`PRAGMA table_info(exit_servers)`)
		if err != nil {
			t.Fatalf("PRAGMA table_info: %v", err)
		}
		defer rows.Close()
		seen := map[string]int{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, ctype string
			var dflt any
			if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
				t.Fatalf("scan: %v", err)
			}
			seen[name]++
		}
		for name, n := range seen {
			if n != 1 {
				t.Errorf("exit_servers.%s appears %d times — the column was added twice", name, n)
			}
		}
	})
}

// TestAddColumnIfMissingSQLite pins the helper's contract directly.
func TestAddColumnIfMissingSQLite(t *testing.T) {
	_, sqlDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sqlDB.Close()

	if _, err := sqlDB.Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	if err := addColumnIfMissingSQLite(sqlDB, "probe", "note", "note TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if !sqliteColumnExists(sqlDB, "probe", "note") {
		t.Fatal("column not created on the first call")
	}
	// Second call must be a no-op, not a "duplicate column name" error.
	if err := addColumnIfMissingSQLite(sqlDB, "probe", "note", "note TEXT NOT NULL DEFAULT ''"); err != nil {
		t.Fatalf("second add must be a no-op, got: %v", err)
	}
	// The inserted column must be usable (i.e. the ALTER really ran).
	if _, err := sqlDB.Exec(`INSERT INTO probe (note) VALUES ('x')`); err != nil {
		t.Fatalf("insert into the added column: %v", err)
	}
	// A genuinely malformed DDL must now PROPAGATE instead of being
	// swallowed. Note: an unknown TYPE name is NOT an error in SQLite (it
	// is dynamically typed and accepts anything), so the probe has to be a
	// real syntax error — a trailing DEFAULT with no value, which is
	// exactly the shape that bit this chain before (see the V022 comment
	// in migrations_sqlite.go).
	if err := addColumnIfMissingSQLite(sqlDB, "probe", "broken", "broken TEXT NOT NULL DEFAULT"); err == nil {
		t.Error("a malformed column definition returned nil — errors are being swallowed again")
	}
}
