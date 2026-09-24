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

	t.Run("chain reaches V077 like PostgreSQL", func(t *testing.T) {
		var maxV int
		if err := sqlDB.QueryRow(
			`SELECT COALESCE(MAX(version), 0) FROM applied_migrations`).Scan(&maxV); err != nil {
			t.Fatalf("read max(version): %v", err)
		}
		// PostgreSQL's chain ends at 77 (v0.77 = exit_servers location, B312). If
		// SQLite lags behind, every table/column added by the missing tail is
		// absent.
		if maxV != 77 {
			t.Errorf("SQLite migration chain ends at V%d, want V77 — the PG and SQLite "+
				"chains have diverged again (see driver_sqlite.go sqliteMigrations)", maxV)
		}
	})

	t.Run("exit_servers location columns exist (V077, B312)", func(t *testing.T) {
		// V077 is the B312 location feature: /admin/exit-nodes renders these columns
		// and the assignment engine uses them to prefer a nearby relay when an owner
		// becomes unreachable. Missing columns on SQLite would make a native install
		// silently lose both halves (the page would show nothing and the close-relay
		// preference would never fire).
		for _, col := range []string{
			"location_label", "location_country", "location_lat",
			"location_lon", "location_source", "location_checked_at",
		} {
			var present int
			if err := sqlDB.QueryRow(
				`SELECT COUNT(*) FROM pragma_table_info('exit_servers') WHERE name = ?`, col,
			).Scan(&present); err != nil {
				t.Fatalf("read exit_servers columns: %v", err)
			}
			if present != 1 {
				t.Errorf("exit_servers.%s is missing on SQLite — the location feature "+
					"would silently do nothing on a native install", col)
			}
		}
	})

	t.Run("monitor_events table exists (V076, B305)", func(t *testing.T) {
		// V076 is the B305 monitoring inbox: the page reads here, the producers
		// (system tests, tag reconciliation) write here. A missing table on
		// SQLite would make the inbox silently empty on a native install.
		var present int
		if err := sqlDB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='monitor_events'`,
		).Scan(&present); err != nil {
			t.Fatalf("read monitor_events presence: %v", err)
		}
		if present != 1 {
			t.Errorf("monitor_events is MISSING in the SQLite schema — V076 "+
				"didn't run end-to-end (see migrations_v0_76_monitor_events.go)")
		}
	})

	t.Run("oidc_settings table exists (V075, B-oidc-setup)", func(t *testing.T) {
		// V075 is the B-oidc-setup DB-backed OIDC config
		// table: the admin web UI writes here on save, the boot
		// sequence reads from here.
		var present int
		if err := sqlDB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='oidc_settings'`,
		).Scan(&present); err != nil {
			t.Fatalf("read oidc_settings presence: %v", err)
		}
		if present != 1 {
			t.Errorf("oidc_settings is MISSING in the SQLite schema — V075 "+
				"didn't run end-to-end (see migrations_v0_75_oidc_settings.go)")
		}
	})

	t.Run("device_rules.all_devices exists (V074, B276.1)", func(t *testing.T) {
		// V074 is the B276.1 intent marker: a rule saved for "all my devices" is
		// re-materialised for the user's current devices by the propagation pass.
		// It goes through execSQLiteDDL/addColumnIfMissingSQLite, so a regression in
		// the chokepoint shows up here.
		if !sqliteColumnExists(sqlDB, "device_rules", "all_devices") {
			t.Error("device_rules.all_devices is MISSING in the SQLite schema — V074 " +
				"(B276.1 all-devices propagation) did not run on SQLite")
		}
	})
	t.Run("portal_users.is_primary exists (V072, B264)", func(t *testing.T) {
		// V072 is the B264 immutable-primary-admin marker. It is added
		// through execSQLiteDDL/addColumnIfMissingSQLite, so a regression
		// in the chokepoint shows up here.
		if !sqliteColumnExists(sqlDB, "portal_users", "is_primary") {
			t.Error("portal_users.is_primary is MISSING in the SQLite schema — V072 " +
				"(B264 primary admin) did not run on SQLite")
		}
		// The partial UNIQUE index is the DB-level "at most one primary"
		// guarantee. Assert it exists in sqlite_master, not just that the
		// CREATE statement was present in the source.
		var n int
		if err := sqlDB.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='portal_users_one_primary_uniq'`).Scan(&n); err != nil {
			t.Fatalf("query sqlite_master for the primary index: %v", err)
		}
		if n != 1 {
			t.Error("portal_users_one_primary_uniq is missing — nothing enforces " +
				"'at most one is_primary=1 row' on SQLite")
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
