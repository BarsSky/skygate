// internal/db/convert_cross_pg_test.go — the PostgreSQL legs of the
// SQLite ↔ PostgreSQL conversion, against a REAL server.
//
// WHY THESE TESTS EXIST (2026-09-28)
// ----------------------------------
// The B329 audit found that every conversion test in the repo opened
// `:memory:` SQLite on BOTH sides of `db.Convert`, so the only path
// exercised was the one where the target schema and the source schema
// are identical. The rewritten converter's PostgreSQL half — listing
// tables through information_schema, reading the target's FOREIGN KEY
// metadata, coercing a Go time.Time into SQLite's INTEGER unix-seconds
// column and an int64 back into timestamptz, and advancing the
// identity sequences with pg_get_serial_sequence/setval — had never
// been executed at all. Worse, the direction the operator actually
// needs (PostgreSQL → SQLite) used to be refused outright.
//
// These tests run against a live PostgreSQL and SKIP when it is not
// available (AGENTS rule 1: a check that needs live state reports
// SKIP, never FAIL). CI's `test-pg` job provides the DSN for
// `./internal/db/...`; locally:
//
//	docker run -d --name skygate-pg-convtest \
//	  -e POSTGRES_PASSWORD=convt3st -e POSTGRES_USER=skygate -e POSTGRES_DB=skygate \
//	  -p 127.0.0.1:55432:5432 postgres:15-alpine
//	SKYGATE_TEST_PG_DSN='postgres://skygate:convt3st@127.0.0.1:55432/skygate?sslmode=disable' \
//	  go test ./internal/db/ -run 'TestConvert_.*_Real' -count=1 -v
//
// Each test creates its own throwaway database and drops it again, so
// the tests cannot interfere with each other or with the operator's
// data.
package db

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
)

// pgTestAdminDSN returns the DSN of the throwaway PostgreSQL server, or
// skips the test.
func pgTestAdminDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("SKYGATE_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("SKYGATE_TEST_PG_DSN is not set — the PostgreSQL conversion legs need a live server " +
			"(see the header of convert_cross_pg_test.go; CI's test-pg job sets it)")
	}
	return dsn
}

// pgWithDatabase rewrites the database name in a postgres:// DSN.
func pgWithDatabase(t *testing.T, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse SKYGATE_TEST_PG_DSN: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// pgExecAdmin runs one statement against the admin database of the
// test server.
func pgExecAdmin(t *testing.T, dsn, stmt string) {
	t.Helper()
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(stmt); err != nil {
		t.Fatalf("admin exec %q: %v", stmt, err)
	}
}

// pgFreshDatabase creates an empty database for one test and registers
// its removal. DROP ... WITH (FORCE) terminates any connection a
// *sql.DB pool left behind, so cleanup cannot fail on a lingering
// connection.
func pgFreshDatabase(t *testing.T, name string) string {
	t.Helper()
	admin := pgTestAdminDSN(t)
	// The name is built from t.Name() by the caller and never carries
	// user input; quote it anyway so a future rename cannot inject.
	quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	pgExecAdmin(t, admin, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
	pgExecAdmin(t, admin, "CREATE DATABASE "+quoted)
	t.Cleanup(func() {
		pgExecAdmin(t, admin, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
	})
	return pgWithDatabase(t, admin, name)
}

// dbNameFor turns a test name into a safe, unique database name.
func dbNameFor(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, r := range strings.ToLower(t.Name()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := "conv_" + b.String()
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

// sqliteSourceWithData builds a migrated SQLite database holding the
// fixture rows the assertions below rely on, and returns it open.
func sqliteSourceWithData(t *testing.T, ctx context.Context) (*Dialect, *sql.DB) {
	t.Helper()
	_, conn, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open SQLite source: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := ApplyMigrations(conn, DialectSQLite); err != nil {
		t.Fatalf("apply the SQLite chain: %v", err)
	}
	// Two probe users; created_at is left to the column default so the
	// timestamp path is exercised on the way OUT of SQLite.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO portal_users (username, password_hash, is_admin)
		VALUES ('pg_probe_a', 'x', 1), ('pg_probe_b', 'x', 0)`); err != nil {
		t.Fatalf("insert portal_users: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO global_settings (key, value) VALUES ('pg_convert_probe', 'round-trip')`); err != nil {
		t.Fatalf("insert global_settings: %v", err)
	}
	return DialectFor(DialectSQLite, ":memory:"), conn
}

// TestConvert_SQLiteToPostgres_Real is the direction that used to be
// the only one the old tool could even attempt.
func TestConvert_SQLiteToPostgres_Real(t *testing.T) {
	ctx := context.Background()
	pgTestAdminDSN(t)

	fromD, fromDB := sqliteSourceWithData(t, ctx)

	targetDSN := pgFreshDatabase(t, dbNameFor(t))
	toD, toDB, err := OpenIsolated(targetDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL target: %v", err)
	}
	defer toDB.Close()
	if toD.Kind != DialectPostgres {
		t.Fatalf("target dialect = %v, want postgres", toD.Kind)
	}

	rep, err := ConvertWithReport(ctx, fromD, fromDB, toD, toDB, ConvertOptions{Mode: "schema+data"})
	if err != nil {
		t.Fatalf("SQLite → PostgreSQL conversion failed: %v (warnings: %v)", err, warningsOf(rep))
	}
	if !rep.Verified {
		t.Errorf("report.Verified = false; warnings: %v", rep.Warnings)
	}
	if rep.CopiedRows == 0 {
		t.Error("CopiedRows = 0 — the report counted nothing")
	}

	// The schema on the target must be PostgreSQL's own, not a
	// translated copy of SQLite's: a partial UNIQUE index created by
	// the PG migration chain proves the chain ran.
	var idx int
	if err := toDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pg_indexes WHERE indexname = 'device_rules_natural_key_uniq'`).Scan(&idx); err != nil {
		t.Fatalf("query pg_indexes: %v", err)
	}
	if idx != 1 {
		t.Error("device_rules_natural_key_uniq is missing on the PostgreSQL target — the target schema " +
			"did not come from the PostgreSQL migration chain")
	}

	// applied_migrations belongs to the target's own run.
	var n int
	if err := toDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM applied_migrations`).Scan(&n); err != nil {
		t.Fatalf("count applied_migrations: %v", err)
	}
	if n == 0 {
		t.Error("applied_migrations is empty — the target's migration bookkeeping was lost")
	}
	if cols := rep.ColumnNames("applied_migrations"); cols != nil {
		t.Errorf("applied_migrations was copied (columns %v) — it is schema state, not data", cols)
	}

	// The data itself. NOTE the type contract: skygate stores timestamps
	// and booleans as INTEGER unix seconds / 0-1 on BOTH backends
	// (migrations_pg.go V025: `is_admin INTEGER NOT NULL DEFAULT 0`,
	// `created_at INTEGER NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::bigint)`),
	// so the conversion must preserve them as integers rather than
	// turning them into PostgreSQL's native boolean/timestamptz — the
	// application reads them with the same helpers on both sides.
	var isAdmin int64
	if err := toDB.QueryRowContext(ctx,
		`SELECT is_admin FROM portal_users WHERE username = 'pg_probe_a'`).Scan(&isAdmin); err != nil {
		t.Fatalf("read the probe user: %v", err)
	}
	if isAdmin != 1 {
		t.Errorf("is_admin = %d, want 1 (SQLite 1 → PostgreSQL INTEGER 1)", isAdmin)
	}
	var value string
	if err := toDB.QueryRowContext(ctx,
		`SELECT value FROM global_settings WHERE key = 'pg_convert_probe'`).Scan(&value); err != nil {
		t.Fatalf("read the probe setting: %v", err)
	}
	if value != "round-trip" {
		t.Errorf("global_settings value = %q, want round-trip", value)
	}

	// The timestamp must survive as the same unix second value.
	var createdAt int64
	if err := toDB.QueryRowContext(ctx,
		`SELECT created_at FROM portal_users WHERE username = 'pg_probe_a'`).Scan(&createdAt); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if createdAt <= 0 {
		t.Errorf("created_at = %d — the timestamp was not carried across", createdAt)
	}
	var sourceCreatedAt int64
	if err := fromDB.QueryRowContext(ctx,
		`SELECT created_at FROM portal_users WHERE username = 'pg_probe_a'`).Scan(&sourceCreatedAt); err != nil {
		t.Fatalf("read the source created_at: %v", err)
	}
	if createdAt != sourceCreatedAt {
		t.Errorf("created_at = %d on the target, %d on the source — the copy changed the value",
			createdAt, sourceCreatedAt)
	}

	// The sequence: a fresh INSERT must not collide with a copied id.
	// Pre-fix (no setval) this is where the conversion would break the
	// application.
	if _, err := toDB.ExecContext(ctx,
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ('pg_seq_probe', 'x', 0)`); err != nil {
		t.Fatalf("INSERT after the conversion collided: %v — the identity sequence was not advanced "+
			"past the copied ids", err)
	}
}

// TestConvert_PostgresToSQLite_Real is the direction the operator asked
// for and the pre-rewrite converter refused outright.
func TestConvert_PostgresToSQLite_Real(t *testing.T) {
	ctx := context.Background()
	pgTestAdminDSN(t)

	// Source: a real PostgreSQL database with data.
	sourceDSN := pgFreshDatabase(t, dbNameFor(t))
	fromD, fromDB, err := OpenIsolated(sourceDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL source: %v", err)
	}
	defer fromDB.Close()
	if fromD.Kind != DialectPostgres {
		t.Fatalf("source dialect = %v, want postgres", fromD.Kind)
	}
	if _, err := fromDB.ExecContext(ctx, `
		INSERT INTO portal_users (username, password_hash, is_admin)
		VALUES ('sqlite_probe_a', 'x', 1), ('sqlite_probe_b', 'x', 0)`); err != nil {
		t.Fatalf("insert into the PostgreSQL source: %v", err)
	}
	if _, err := fromDB.ExecContext(ctx, `
		INSERT INTO global_settings (key, value) VALUES ('pg_to_sqlite_probe', 'yes')`); err != nil {
		t.Fatalf("insert global_settings: %v", err)
	}
	// Capture the source's own timestamp so the copy can be compared,
	// not merely checked for non-zero. `created_at` is INTEGER unix
	// seconds on PostgreSQL too (see the note in the SQLite → PG test).
	var sourceUnix int64
	if err := fromDB.QueryRowContext(ctx,
		`SELECT created_at FROM portal_users WHERE username = 'sqlite_probe_a'`).
		Scan(&sourceUnix); err != nil {
		t.Fatalf("read the source timestamp: %v", err)
	}

	// Target: an empty SQLite file (NOT :memory:, so a failure leaves
	// evidence on disk under t.TempDir()).
	targetPath := t.TempDir() + "/converted.db"
	toD, toDB, err := OpenIsolated("sqlite:" + targetPath)
	if err != nil {
		t.Fatalf("open SQLite target: %v", err)
	}
	defer toDB.Close()

	rep, err := ConvertWithReport(ctx, fromD, fromDB, toD, toDB, ConvertOptions{Mode: "schema+data"})
	if err != nil {
		t.Fatalf("PostgreSQL → SQLite conversion failed: %v (warnings: %v)", err, warningsOf(rep))
	}
	if !rep.Verified {
		t.Errorf("report.Verified = false; warnings: %v", rep.Warnings)
	}

	var count int
	if err := toDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM portal_users WHERE username LIKE 'sqlite_probe_%'`).Scan(&count); err != nil {
		t.Fatalf("count the copied users: %v", err)
	}
	if count != 2 {
		t.Errorf("portal_users copied = %d, want 2", count)
	}

	// Boolean: PostgreSQL true must land as SQLite's 1.
	var isAdmin int64
	if err := toDB.QueryRowContext(ctx,
		`SELECT is_admin FROM portal_users WHERE username = 'sqlite_probe_a'`).Scan(&isAdmin); err != nil {
		t.Fatalf("read is_admin from SQLite: %v", err)
	}
	if isAdmin != 1 {
		t.Errorf("is_admin = %d, want 1 (PostgreSQL true → SQLite 1)", isAdmin)
	}

	// Timestamp: INTEGER unix seconds on both sides, so the value must
	// be byte-identical after the copy.
	var targetUnix int64
	if err := toDB.QueryRowContext(ctx,
		`SELECT created_at FROM portal_users WHERE username = 'sqlite_probe_a'`).Scan(&targetUnix); err != nil {
		t.Fatalf("read created_at from SQLite as int64 (a value stored as a string is NOT scannable "+
			"here and would break every SQLite-side reader): %v", err)
	}
	if targetUnix != sourceUnix {
		t.Errorf("created_at = %d, want %d (the source's own unix seconds)", targetUnix, sourceUnix)
	}

	// The SQLite side must be usable as an application database: the
	// schema is the SQLite chain's, so the partial UNIQUE index exists.
	var idx int
	if err := toDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='device_rules_natural_key_uniq'`).
		Scan(&idx); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if idx != 1 {
		t.Error("device_rules_natural_key_uniq is missing on the SQLite target — the target schema did " +
			"not come from the SQLite migration chain")
	}

	// And an INSERT must not collide with a copied id on this side
	// either (SQLite's AUTOINCREMENT sequence must have moved).
	if _, err := toDB.ExecContext(ctx,
		`INSERT INTO portal_users (username, password_hash, is_admin) VALUES ('sqlite_seq_probe', 'x', 0)`); err != nil {
		t.Fatalf("INSERT after the conversion collided on SQLite: %v", err)
	}

	// The converted file must be reopenable as a normal skygate SQLite
	// database (migrations already applied, no pending work).
	_, reopened, err := OpenWithDialect("sqlite:" + targetPath)
	if err != nil {
		t.Fatalf("reopen the converted SQLite file: %v", err)
	}
	defer reopened.Close()
	if err := ApplyMigrations(reopened, DialectSQLite); err != nil {
		t.Fatalf("the converted SQLite database is not a valid skygate database "+
			"(re-applying the chain failed): %v", err)
	}
}

// TestConvert_CrossDialectReportsDrift_Real: a column that exists on
// one side only must be REPORTED, not fatal. Additive drift between the
// two chains is routine (every migration that lands in one chain first),
// so a conversion must survive it.
func TestConvert_CrossDialectReportsDrift_Real(t *testing.T) {
	ctx := context.Background()
	pgTestAdminDSN(t)

	targetDSN := pgFreshDatabase(t, dbNameFor(t))
	toD, toDB, err := OpenIsolated(targetDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL target: %v", err)
	}
	defer toDB.Close()

	// A SQLite source with one extra column on a table that exists on
	// both sides.
	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open SQLite source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, DialectSQLite); err != nil {
		t.Fatalf("apply the SQLite chain: %v", err)
	}
	if _, err := fromDB.ExecContext(ctx,
		`ALTER TABLE global_settings ADD COLUMN only_in_sqlite TEXT`); err != nil {
		t.Fatalf("add the drifting column: %v", err)
	}
	if _, err := fromDB.ExecContext(ctx,
		`INSERT INTO global_settings (key, value, only_in_sqlite) VALUES ('drift_probe', 'v', 'ignored')`); err != nil {
		t.Fatalf("insert the drifting row: %v", err)
	}

	rep, err := ConvertWithReport(ctx, fromD, fromDB, toD, toDB, ConvertOptions{Mode: "schema+data"})
	if err != nil {
		t.Fatalf("the conversion must survive an additive column: %v", err)
	}
	var sawDrift bool
	for _, w := range rep.Warnings {
		if strings.Contains(w, "only_in_sqlite") {
			sawDrift = true
		}
	}
	if !sawDrift {
		t.Errorf("the dropped column was not reported; warnings: %v", rep.Warnings)
	}
	var value string
	if err := toDB.QueryRowContext(ctx,
		`SELECT value FROM global_settings WHERE key = 'drift_probe'`).Scan(&value); err != nil {
		t.Fatalf("the row with the drifting column was not copied: %v", err)
	}
	if value != "v" {
		t.Errorf("value = %q, want v", value)
	}
}

// TestConvert_DryRunTouchesThePostgresTarget_Real: --dry-run must not
// create anything on the target, which for PostgreSQL means the
// migration chain must NOT have run.
func TestConvert_DryRunTouchesThePostgresTarget_Real(t *testing.T) {
	ctx := context.Background()
	pgTestAdminDSN(t)

	fromD, fromDB := sqliteSourceWithData(t, ctx)

	targetDSN := pgFreshDatabase(t, dbNameFor(t))
	// A plain connection (no migration path) so the target really is
	// empty when the dry run starts.
	probe, err := sql.Open("pgx", targetDSN)
	if err != nil {
		t.Fatalf("open the target for counting: %v", err)
	}
	defer probe.Close()

	rep, err := ConvertWithReport(ctx, fromD, fromDB,
		DialectFor(DialectPostgres, targetDSN), nil, ConvertOptions{DryRun: true})
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if rep.SchemaCreated {
		t.Error("SchemaCreated = true on a dry run")
	}
	if rep.CopiedRows != 0 {
		t.Errorf("CopiedRows = %d on a dry run", rep.CopiedRows)
	}
	var tables int
	if err := probe.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`).Scan(&tables); err != nil {
		t.Fatalf("count target tables: %v", err)
	}
	if tables != 0 {
		t.Errorf("the dry run created %d table(s) on the PostgreSQL target", tables)
	}
}

// warningsOf is a nil-safe helper for the failure messages above.
func warningsOf(rep *ConvertReport) []string {
	if rep == nil {
		return nil
	}
	return rep.Warnings
}
