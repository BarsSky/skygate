// internal/db/dialect_test.go — RED tests for the B-mod-sqlite-pg-bidi
// dialect interface (Task 1).
//
// The Dialect interface replaces the pre-v1.5.4 hard-coded PG-only
// backend with a small abstraction so the same skygate binary can run
// against either SQLite (file-based, default for self-host) or
// PostgreSQL (replicated, prod-scale).
//
// The 9 tests below pin the contract:
//
//   - DetectDSN: every accepted DSN form maps to the right kind
//     (sqlite: explicit prefix, file:, postgres://, postgresql://,
//     bare path, :memory:, empty → SQLite default for self-host)
//   - Placeholders: $1,$2 for PG; ?,? for SQLite
//   - UnixEpoch: EXTRACT(EPOCH FROM x)::bigint for PG;
//     CAST(strftime('%s', x) AS INTEGER) for SQLite
//   - InsertIgnore: ON CONFLICT (cols) DO NOTHING for PG;
//     INSERT OR IGNORE INTO ... for SQLite (rewrites "INSERT INTO" →
//     "INSERT OR IGNORE INTO")
//   - BooleanType: BOOLEAN for PG; INTEGER for SQLite (no native BOOL)
//   - BooleanLiteral: true/false for PG; 1/0 for SQLite
//   - OpenWithDialect + live SQLite :memory: open + SELECT 1 round-trip
//
// Pre-v1.5.4 (B-mod-db-retry era) skygate was PG-only and these tests
// couldn't even compile — Dialect was a single hard-coded Backend
// enum. The whole point of B-mod-sqlite-pg-bidi is to put a real
// interface in front so the rest of the db package can dispatch on
// `d.Kind() == DialectSQLite` instead of `backend == BackendPostgres`.
package db

import "testing"

// TestDetectDSN covers every DSN form skygate accepts: explicit
// sqlite: prefix, postgresql:// + postgres://, bare file paths
// (default SQLite for self-host), :memory: (test mode), and empty
// (default = SQLite for v1.5.x self-host backward compat).
func TestDetectDSN(t *testing.T) {
	cases := []struct {
		dsn      string
		wantKind DialectKind
	}{
		// SQLite explicit prefix
		{"sqlite:/var/lib/skygate/skygate.db", DialectSQLite},
		{"sqlite::memory:", DialectSQLite},
		{"sqlite:/tmp/foo.db?_pragma=foreign_keys(1)", DialectSQLite},

		// SQLite file:// URI form (used by modernc.org/sqlite docs)
		{"file:/var/lib/skygate/skygate.db", DialectSQLite},
		{"file::memory:?cache=shared", DialectSQLite},

		// Bare path (no scheme) → SQLite (default for self-host)
		{"/var/lib/skygate/skygate.db", DialectSQLite},
		{"./skygate.db", DialectSQLite},
		{`C:\skygate\skygate.db`, DialectSQLite}, // Windows path

		// PostgreSQL
		{"postgres://user:pass@host/db", DialectPostgres},
		{"postgresql://user:pass@host:5432/db?sslmode=disable", DialectPostgres},
		{"postgres://user:pass@<host>:5432/skygate?sslmode=disable", DialectPostgres},

		// In-memory SQLite (test mode)
		{":memory:", DialectSQLite},

		// Empty → default to SQLite (backward compat with v1.5.x self-host)
		{"", DialectSQLite},
	}
	for _, tc := range cases {
		t.Run(tc.dsn, func(t *testing.T) {
			got := DetectDSN(tc.dsn)
			if got.Kind != tc.wantKind {
				t.Errorf("DetectDSN(%q) kind = %v, want %v",
					tc.dsn, got.Kind, tc.wantKind)
			}
		})
	}
}

// TestDialect_PG_Placeholders pins the PG placeholder contract.
// pgx/extended-protocol requires $1,$2,... not "?,?" (pre-v0.33.1.8
// SQLite-style "?"); the dialect must emit the canonical PG form.
//
// Convention note: NO space after the comma. The pre-existing
// placeholders_postgres.go uses the same no-space form (matches the
// 60+ migration files that splice placeholdersList(N) into VALUES
// clauses — adding a space would shift every column position by 1
// byte in the generated DDL).
func TestDialect_PG_Placeholders(t *testing.T) {
	d := DialectPostgres
	if got := d.Placeholders(3); got != "$1,$2,$3" {
		t.Errorf("PG placeholders(3) = %q, want \"$1,$2,$3\"", got)
	}
	if got := d.Placeholders(1); got != "$1" {
		t.Errorf("PG placeholders(1) = %q, want \"$1\"", got)
	}
	if got := d.Placeholders(0); got != "" {
		t.Errorf("PG placeholders(0) = %q, want empty", got)
	}
}

// TestDialect_SQLite_Placeholders pins the SQLite "?,?,?" form.
// SQLite has only ONE positional placeholder — it does not understand
// "$1" (PG's form). Caller uses d.Placeholders(n) to build the
// comma-joined list. NO space after the comma (matches PG convention
// for splice-ability into existing SQL fragments).
func TestDialect_SQLite_Placeholders(t *testing.T) {
	d := DialectSQLite
	if got := d.Placeholders(3); got != "?,?,?" {
		t.Errorf("SQLite placeholders(3) = %q, want \"?,?,?\"", got)
	}
	if got := d.Placeholders(1); got != "?" {
		t.Errorf("SQLite placeholders(1) = %q, want \"?\"", got)
	}
	if got := d.Placeholders(0); got != "" {
		t.Errorf("SQLite placeholders(0) = %q, want empty", got)
	}
}

// TestDialect_UnixEpoch_PG pins the PG "now as unix epoch" SQL fragment.
// Pre-v0.33.1.12 the backup/config.go code path hardcoded
// "strftime('%s','now')" — PG has no strftime() function, so the
// queries crashed with "function strftime() does not exist". The fix
// was to route every "now as unix epoch" through d.UnixEpoch("now()")
// which expands to the dialect-native form.
func TestDialect_UnixEpoch_PG(t *testing.T) {
	d := DialectPostgres
	if got := d.UnixEpoch("now()"); got != "EXTRACT(EPOCH FROM now())::bigint" {
		t.Errorf("PG UnixEpoch(now()) = %q", got)
	}
	if got := d.UnixEpoch("created_at"); got != "EXTRACT(EPOCH FROM created_at)::bigint" {
		t.Errorf("PG UnixEpoch(col) = %q", got)
	}
}

// TestDialect_UnixEpoch_SQLite pins the SQLite equivalent: SQLite uses
// strftime('%s', x) cast to INTEGER. The CAST wrapper is required
// because strftime returns TEXT in some SQLite builds (and we want
// the same BIGINT-vs-INTEGER semantics as PG).
func TestDialect_UnixEpoch_SQLite(t *testing.T) {
	d := DialectSQLite
	if got := d.UnixEpoch("CURRENT_TIMESTAMP"); got != "CAST(strftime('%s', CURRENT_TIMESTAMP) AS INTEGER)" {
		t.Errorf("SQLite UnixEpoch(CURRENT_TIMESTAMP) = %q", got)
	}
	if got := d.UnixEpoch("created_at"); got != "CAST(strftime('%s', created_at) AS INTEGER)" {
		t.Errorf("SQLite UnixEpoch(col) = %q", got)
	}
}

// TestDialect_InsertIgnore_PG pins the PG idempotent-INSERT contract.
// Caller passes the bare INSERT statement (with its own placeholder
// style — here we use the literal "?, ?" form for test simplicity;
// real callers use d.Placeholders()) + the conflict target columns.
// The dialect appends the canonical `ON CONFLICT (<cols>) DO NOTHING`
// suffix that pgx understands.
func TestDialect_InsertIgnore_PG(t *testing.T) {
	d := DialectPostgres
	sql := d.InsertIgnore(
		"INSERT INTO device_rules (user_id, device_id) VALUES (?, ?)",
		[]string{"user_id", "device_id"},
	)
	expected := "INSERT INTO device_rules (user_id, device_id) VALUES (?, ?) " +
		"ON CONFLICT (user_id, device_id) DO NOTHING"
	if sql != expected {
		t.Errorf("PG InsertIgnore = %q\nwant %q", sql, expected)
	}
}

// TestDialect_InsertIgnore_SQLite pins the SQLite idempotent-INSERT
// contract. SQLite's canonical form is `INSERT OR IGNORE INTO ...` —
// the OR IGNORE keyword goes BEFORE INTO. The dialect rewrites the
// leading "INSERT INTO" with "INSERT OR IGNORE INTO" (one occurrence,
// via strings.Replace N=1).
func TestDialect_InsertIgnore_SQLite(t *testing.T) {
	d := DialectSQLite
	sql := d.InsertIgnore(
		"INSERT INTO device_rules (user_id, device_id) VALUES (?, ?)",
		[]string{"user_id", "device_id"},
	)
	expected := "INSERT OR IGNORE INTO device_rules (user_id, device_id) VALUES (?, ?)"
	if sql != expected {
		t.Errorf("SQLite InsertIgnore = %q\nwant %q", sql, expected)
	}
}

// TestDialect_BooleanType pins the column-type contract: PG has a
// native BOOLEAN type, SQLite does NOT — SQLite stores BOOL as
// INTEGER (0=false, 1=true). The migration tool (Task 4) uses this
// to translate BOOLEAN columns when copying schema PG↔SQLite.
func TestDialect_BooleanType(t *testing.T) {
	if got := DialectPostgres.BooleanType(); got != "BOOLEAN" {
		t.Errorf("PG BooleanType = %q, want BOOLEAN", got)
	}
	if got := DialectSQLite.BooleanType(); got != "INTEGER" {
		t.Errorf("SQLite BooleanType = %q, want INTEGER (SQLite has no native BOOL)", got)
	}
}

// TestDialect_BooleanLiteral pins the literal-value contract. PG uses
// the SQL-standard true/false; SQLite accepts 0/1 as the only numeric
// form (it does NOT recognize "true"/"false" as INTEGER literals in
// some edge cases — INSERT INTO ... VALUES (true) coerces "true" to
// the string "true" then fails the INTEGER cast). 1/0 is the portable
// form.
func TestDialect_BooleanLiteral(t *testing.T) {
	if got := DialectPostgres.BooleanLiteral(true); got != "true" {
		t.Errorf("PG BooleanLiteral(true) = %q, want true", got)
	}
	if got := DialectPostgres.BooleanLiteral(false); got != "false" {
		t.Errorf("PG BooleanLiteral(false) = %q, want false", got)
	}
	if got := DialectSQLite.BooleanLiteral(true); got != "1" {
		t.Errorf("SQLite BooleanLiteral(true) = %q, want 1", got)
	}
	if got := DialectSQLite.BooleanLiteral(false); got != "0" {
		t.Errorf("SQLite BooleanLiteral(false) = %q, want 0", got)
	}
}

// TestDialect_OpenDB_OpenCloses pins the live open/close round-trip:
// DetectDSN + OpenWithDialect + Ping + SELECT 1 must all work for the
// in-memory SQLite case. This is the smoke test that the pure-Go
// modernc.org/sqlite driver is registered correctly (the package
// init-time blank import `_ "modernc.org/sqlite"` registers the
// "sqlite" driver name with database/sql; without that import,
// sql.Open("sqlite", ...) returns "sql: unknown driver").
func TestDialect_OpenDB_OpenCloses(t *testing.T) {
	d, db, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("OpenWithDialect(:memory:): %v", err)
	}
	defer db.Close()

	if d.Kind != DialectSQLite {
		t.Errorf("in-memory DSN kind = %v, want %v", d.Kind, DialectSQLite)
	}

	var v int
	if err := db.QueryRow("SELECT 1").Scan(&v); err != nil {
		t.Fatalf("SELECT 1 on :memory: db: %v", err)
	}
	if v != 1 {
		t.Errorf("SELECT 1 returned %d, want 1", v)
	}
}
