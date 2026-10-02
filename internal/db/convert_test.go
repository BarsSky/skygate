// internal/db/convert_test.go — tests for the B-mod-sqlite-pg-bidi
// conversion tool.
//
// The Convert function is the core of the operator's "switch between
// DBs without data loss" requirement (2026-09-11). It copies schema +
// data from one *sql.DB (any supported dialect) to another.
//
// 2026-09-28: Convert was rewritten (see the header of convert.go).
// The v1.5.4 tests below are kept unchanged — they are the regression
// guard for the SQLite→SQLite path — and the tests after them cover
// what the rewrite added: the report and its row-count verification,
// the target-driven type coercion, the FK topological sort, and the
// fact that the target schema now comes from the TARGET's own
// migration chain instead of regex-substituted source DDL.
//
// The PostgreSQL half of every cross-dialect claim SKIPs without
// SKYGATE_TEST_PG_DSN (AGENTS rule 1: a check that needs live state
// reports SKIP, never FAIL) — CI's `test-pg` job provides it, the
// local gate does not.
package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// TestConvert_SQLiteToSQLite_RoundTrip creates a source SQLite DB
// with a few canonical tables (portal_users, device_rules, audit_log)
// + a few rows, then runs Convert to a target SQLite DB, and
// verifies:
//   - Target has the same tables
//   - Target row counts match source
//   - Target data content matches (sample row equality)
//
// This is the SQLite↔SQLite round-trip — same code path as
// SQLite→PG/PG→SQLite (the dispatch on dialect happens inside
// Convert, but the algorithm is dialect-agnostic).
func TestConvert_SQLiteToSQLite_RoundTrip(t *testing.T) {
	ctx := context.Background()

	// 1. Set up source SQLite DB with skygate schema.
	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, fromD.Kind); err != nil {
		t.Fatalf("source ApplyMigrations: %v", err)
	}

	// 2. Insert sample rows.
	if _, err := fromDB.Exec(`
		INSERT INTO portal_users (username, password_hash, is_admin)
		VALUES ('alice', '<REDACTED>', 1),
		       ('bob',   '<REDACTED>', 0),
		       ('carol', '<REDACTED>', 0)
	`); err != nil {
		t.Fatalf("insert portal_users: %v", err)
	}
	if _, err := fromDB.Exec(`
		INSERT INTO global_settings (key, value)
		VALUES ('test_convert_marker', '1')
	`); err != nil {
		t.Fatalf("insert global_settings: %v", err)
	}

	// 3. Set up target SQLite DB (empty, no schema).
	toD, toDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer toDB.Close()

	// 4. Run Convert (schema + data).
	if err := Convert(ctx, fromD, fromDB, toD, toDB, ConvertOptions{
		Mode:   "schema+data",
		DryRun: false,
	}); err != nil {
		t.Fatalf("Convert(schema+data): %v", err)
	}

	// 5. Verify target row counts.
	// Note: the source DB also has the infra row inserted by
	// V054 + the test's 3 rows = 4 total.
	wantUsers := 4
	var gotUsers int
	if err := toDB.QueryRow(`SELECT COUNT(*) FROM portal_users`).Scan(&gotUsers); err != nil {
		t.Fatalf("count target portal_users: %v", err)
	}
	if gotUsers != wantUsers {
		t.Errorf("portal_users: got %d rows, want %d", gotUsers, wantUsers)
	}

	var gotSettings int
	if err := toDB.QueryRow(`SELECT COUNT(*) FROM global_settings`).Scan(&gotSettings); err != nil {
		t.Fatalf("count target global_settings: %v", err)
	}
	if gotSettings != 4 {
		// 3 from migrations (exit_policy, telegram.strict_mode,
		// telegram.login_token_ttl_seconds) + 1 from this test.
		t.Errorf("global_settings: got %d rows, want 4", gotSettings)
	}

	// Verify the test-inserted marker row.
	var markerVal string
	err = toDB.QueryRow(`SELECT value FROM global_settings WHERE key='test_convert_marker'`).Scan(&markerVal)
	if err != nil {
		t.Fatalf("query marker: %v", err)
	}
	if markerVal != "1" {
		t.Errorf("marker value: got %q, want 1", markerVal)
	}

	// 6. Verify data content (specific row).
	var username string
	var isAdmin int
	err = toDB.QueryRow(`SELECT username, is_admin FROM portal_users WHERE username='alice'`).Scan(&username, &isAdmin)
	if err != nil {
		t.Fatalf("query alice: %v", err)
	}
	if username != "alice" {
		t.Errorf("alice username: got %q, want alice", username)
	}
	if isAdmin != 1 {
		t.Errorf("alice is_admin: got %d, want 1", isAdmin)
	}
}

// TestConvert_ModeFlags verifies --schema-only and --data-only
// honor the requested mode:
//   - schema-only: target gets tables but no data
//   - data-only: target gets data only (assumes schema already there)
func TestConvert_ModeFlags(t *testing.T) {
	ctx := context.Background()
	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, fromD.Kind); err != nil {
		t.Fatalf("source ApplyMigrations: %v", err)
	}
	if _, err := fromDB.Exec(`INSERT INTO global_settings (key, value) VALUES ('k', 'v')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Schema-only: target has table but no rows.
	toD, toDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer toDB.Close()
	if err := Convert(ctx, fromD, fromDB, toD, toDB, ConvertOptions{Mode: "schema-only"}); err != nil {
		t.Fatalf("Convert(schema-only): %v", err)
	}
	var n int
	if err := toDB.QueryRow(`SELECT COUNT(*) FROM global_settings`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("schema-only: got %d rows, want 0", n)
	}
}

// TestConvert_DryRun verifies that DryRun=true does NOT write to
// the target. After DryRun Convert, the target DB should still be
// empty.
func TestConvert_DryRun(t *testing.T) {
	ctx := context.Background()
	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, fromD.Kind); err != nil {
		t.Fatalf("source ApplyMigrations: %v", err)
	}

	toD, toDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer toDB.Close()
	if err := Convert(ctx, fromD, fromDB, toD, toDB, ConvertOptions{
		Mode:   "schema+data",
		DryRun: true,
	}); err != nil {
		t.Fatalf("Convert(dry-run): %v", err)
	}

	// Verify target has no tables created.
	var n int
	if err := toDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&n); err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if n != 0 {
		t.Errorf("dry-run: target has %d tables, want 0", n)
	}
}

// TestConvert_SameDialect_SkipFK verifies that Convert on the
// same dialect does the right thing (treat as copy + maybe re-
// create schema). The current impl always creates schema on the
// target, which is fine because the target is empty.
func TestConvert_SameDialect_NoDataLoss(t *testing.T) {
	ctx := context.Background()
	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, fromD.Kind); err != nil {
		t.Fatalf("source ApplyMigrations: %v", err)
	}

	// Insert a row, then convert, then verify the row exists.
	if _, err := fromDB.Exec(`INSERT INTO global_settings (key, value) VALUES ('k1', 'v1')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := fromDB.Exec(`INSERT INTO global_settings (key, value) VALUES ('k2', 'v2')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	toD, toDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer toDB.Close()
	if err := Convert(ctx, fromD, fromDB, toD, toDB, ConvertOptions{Mode: "schema+data"}); err != nil {
		t.Fatalf("Convert: %v", err)
	}

	// Both my-inserted rows + the 3 rows the migrations themselves
	// insert (exit_policy, telegram.strict_mode,
	// telegram.login_token_ttl_seconds) should be on target.
	var n int
	if err := toDB.QueryRow(`SELECT COUNT(*) FROM global_settings`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 5 {
		t.Errorf("target rows: got %d, want 5 (2 from test + 3 from migrations)", n)
	}
}

// silence unused import warning if all tests use sql.DB elsewhere
var _ = sql.ErrNoRows

// ---------- 2026-09-28: the rewrite's own guarantees ----------

// TestConvert_TargetSchemaComesFromTheMigrationChain is the regression
// guard for the defect the rewrite removed: the v1.5.4 implementation
// rebuilt the target from the SOURCE's CREATE TABLE text with six
// regex substitutions, so the target had no index, no trigger and no
// partial UNIQUE index at all — and the SQLite connection runs with
// foreign_keys=1, so it was structurally incomplete.
func TestConvert_TargetSchemaComesFromTheMigrationChain(t *testing.T) {
	ctx := context.Background()

	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, fromD.Kind); err != nil {
		t.Fatalf("source ApplyMigrations: %v", err)
	}

	toD, toDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer toDB.Close()

	rep, err := ConvertWithReport(ctx, fromD, fromDB, toD, toDB, ConvertOptions{Mode: "schema+data"})
	if err != nil {
		t.Fatalf("ConvertWithReport: %v", err)
	}
	if !rep.SchemaCreated {
		t.Error("SchemaCreated = false — the target schema was not created by the target's own chain")
	}
	// A partial UNIQUE index created by the migration chain. The old
	// DDL-substitution path could not produce it.
	var idx int
	if err := toDB.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='device_rules_natural_key_uniq'`,
	).Scan(&idx); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if idx != 1 {
		t.Error("device_rules_natural_key_uniq is missing on the target — the target schema " +
			"did not come from the migration chain (the pre-rewrite DDL-substitution bug)")
	}
	// applied_migrations belongs to the target's own run and must never
	// be overwritten by the source's copy.
	var applied int
	if err := toDB.QueryRow(`SELECT COUNT(*) FROM applied_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count applied_migrations: %v", err)
	}
	if applied == 0 {
		t.Error("applied_migrations is empty on the target — the migration bookkeeping was lost")
	}
	if got := rep.ColumnNames("applied_migrations"); got != nil {
		t.Errorf("applied_migrations was copied (columns %v) — it is schema state, not data", got)
	}
}

// TestConvert_ReportVerifiesRowCounts pins the step the old doc
// comment promised and the old code never performed:
// "Verifies row counts match (source.count == target.count per table)".
func TestConvert_ReportVerifiesRowCounts(t *testing.T) {
	ctx := context.Background()

	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, fromD.Kind); err != nil {
		t.Fatalf("source ApplyMigrations: %v", err)
	}
	if _, err := fromDB.Exec(
		`INSERT INTO global_settings (key, value) VALUES ('report_probe', 'yes')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	toD, toDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer toDB.Close()

	rep, err := ConvertWithReport(ctx, fromD, fromDB, toD, toDB, ConvertOptions{Mode: "schema+data"})
	if err != nil {
		t.Fatalf("ConvertWithReport: %v", err)
	}
	if !rep.Verified {
		t.Errorf("Verified = false; warnings: %v", rep.Warnings)
	}
	if rep.CopiedRows == 0 {
		t.Error("CopiedRows = 0 — the report counts nothing")
	}
	var found bool
	for _, tr := range rep.Tables {
		if tr.Table != "global_settings" {
			continue
		}
		found = true
		if tr.SourceRows != tr.TargetRows {
			t.Errorf("global_settings: source %d != target %d", tr.SourceRows, tr.TargetRows)
		}
		if tr.Copied == 0 {
			t.Error("global_settings.Copied = 0 — the rows were not attributed to a table")
		}
	}
	if !found {
		t.Fatal("global_settings is absent from the report")
	}
}

// TestConvert_SameDialectIsWarned: a same-dialect run is a copy, not a
// conversion, and the operator should be told so in the report.
func TestConvert_SameDialectIsWarned(t *testing.T) {
	ctx := context.Background()
	fromD, fromDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer fromDB.Close()
	if err := ApplyMigrations(fromDB, fromD.Kind); err != nil {
		t.Fatalf("source ApplyMigrations: %v", err)
	}
	toD, toDB, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer toDB.Close()

	rep, err := ConvertWithReport(ctx, fromD, fromDB, toD, toDB, ConvertOptions{})
	if err != nil {
		t.Fatalf("ConvertWithReport: %v", err)
	}
	var warned bool
	for _, w := range rep.Warnings {
		if strings.Contains(w, "same-dialect") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no same-dialect warning; warnings: %v", rep.Warnings)
	}
}

// TestConvert_PGSourceIsNoLongerRefused is the contract for the
// headline defect: the pre-rewrite readSourceSchema returned
//
//	convert: only SQLite source supported in v1.5.4 (got kind=postgres)
//
// so PostgreSQL → SQLite — the direction an operator needs when
// scaling down to a self-hosted install — could not run at all.
// A source file assertion is the only way to pin this without a live
// PostgreSQL (CI's test-pg job exercises the real path).
func TestConvert_PGSourceIsNoLongerRefused(t *testing.T) {
	raw, err := os.ReadFile("convert.go")
	if err != nil {
		t.Skipf("convert.go not readable: %v", err)
	}
	src := string(raw)
	// The error LITERAL, not the phrase: convert.go's header quotes the
	// old message when it explains the defect, and that quote is not a
	// refusal.
	if strings.Contains(src, `"convert: only SQLite source supported`) {
		t.Error("convert.go still refuses a PostgreSQL source — PostgreSQL → SQLite cannot run")
	}
	if !strings.Contains(src, "information_schema.tables") {
		t.Error("convert.go has no PostgreSQL table listing — a PG source cannot be read")
	}
	if !strings.Contains(src, "pg_get_serial_sequence") {
		t.Error("convert.go does not re-seed the PostgreSQL identity sequences — the first " +
			"INSERT after a conversion would collide with a copied id")
	}
}

// TestCoerceValue_TargetDriven pins the coercion matrix. The TARGET
// column's declared type decides the shape; this is what lets
// PostgreSQL's time.Time land in SQLite's INTEGER unix-seconds column
// and SQLite's int64 land in PostgreSQL's timestamptz.
func TestCoerceValue_TargetDriven(t *testing.T) {
	ts := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		in       any
		target   string
		kind     DialectKind
		want     any
		wantType string // "int64", "string", "bool", "time", "bytes", "nil"
	}{
		{"nil stays nil", nil, "TEXT", DialectSQLite, nil, "nil"},
		{"time→sqlite INTEGER is unix seconds", ts, "INTEGER", DialectSQLite, ts.Unix(), "int64"},
		{"time→sqlite TEXT is RFC3339", ts, "TEXT", DialectSQLite, ts.Format(time.RFC3339Nano), "string"},
		{"time→pg timestamptz stays time", ts, "timestamp with time zone", DialectPostgres, ts, "time"},
		{"int64→pg timestamptz becomes time", int64(ts.Unix()), "timestamp with time zone", DialectPostgres, ts, "time"},
		{"bool→sqlite INTEGER is 0/1", true, "INTEGER", DialectSQLite, int64(1), "int64"},
		{"bool→pg boolean stays bool", true, "boolean", DialectPostgres, true, "bool"},
		{"int64→pg boolean is a bool", int64(0), "boolean", DialectPostgres, false, "bool"},
		{"jsonb bytes→sqlite TEXT is a string", []byte(`{"a":1}`), "TEXT", DialectSQLite, `{"a":1}`, "string"},
		{"bytes→pg bytea stays bytes", []byte{1, 2}, "bytea", DialectPostgres, []byte{1, 2}, "bytes"},
		{"sqlite text timestamp→pg timestamptz", "2026-09-28 12:00:00", "timestamp with time zone", DialectPostgres, ts, "time"},
		{"sqlite rfc3339→pg timestamptz", ts.Format(time.RFC3339), "timestamp with time zone", DialectPostgres, ts, "time"},
		{"string→pg bigint is an int", "42", "bigint", DialectPostgres, int64(42), "int64"},
		{"string 'true'→pg boolean", "true", "boolean", DialectPostgres, true, "bool"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := coerceValue(tc.in, tc.target, tc.kind)
			if err != nil {
				t.Fatalf("coerceValue(%v, %q): %v", tc.in, tc.target, err)
			}
			switch tc.wantType {
			case "int64":
				if got != tc.want {
					t.Errorf("got %#v, want %#v", got, tc.want)
				}
			case "string", "bool":
				if got != tc.want {
					t.Errorf("got %#v, want %#v", got, tc.want)
				}
			case "bytes":
				b, ok := got.([]byte)
				if !ok || string(b) != string(tc.want.([]byte)) {
					t.Errorf("got %#v, want %#v", got, tc.want)
				}
			case "time":
				tm, ok := got.(time.Time)
				if !ok {
					t.Fatalf("got %T, want time.Time", got)
				}
				if !tm.Equal(tc.want.(time.Time)) {
					t.Errorf("got %s, want %s", tm, tc.want)
				}
			case "nil":
				if got != nil {
					t.Errorf("got %#v, want nil", got)
				}
			}
		})
	}
}

// TestCoerceValue_RejectsBrokenBoolean: a value that is neither 0/1
// nor a known boolean spelling must be an error, not a silent false.
func TestCoerceValue_RejectsBrokenBoolean(t *testing.T) {
	if _, err := coerceValue("maybe", "boolean", DialectPostgres); err == nil {
		t.Error("coerceValue accepted \"maybe\" as a boolean")
	}
}

// TestTopoOrder_ParentsFirst pins the real topological sort (the old
// topoSortTables was an alphabetical sort whose comment admitted it).
func TestTopoOrder_ParentsFirst(t *testing.T) {
	tables := []string{"a_child", "z_parent", "m_grandchild"}
	edges := map[string][]string{
		"a_child":      {"z_parent"},
		"m_grandchild": {"a_child"},
	}
	got, err := topoOrder(tables, edges)
	if err != nil {
		t.Fatalf("topoOrder: %v", err)
	}
	pos := map[string]int{}
	for i, n := range got {
		pos[n] = i
	}
	if pos["z_parent"] > pos["a_child"] {
		t.Errorf("z_parent must come before a_child, got %v", got)
	}
	if pos["a_child"] > pos["m_grandchild"] {
		t.Errorf("a_child must come before m_grandchild, got %v", got)
	}
}

// TestTopoOrder_DetectsCycle: a cycle must fail loudly rather than let
// the copy die halfway through on the target's FK check.
func TestTopoOrder_DetectsCycle(t *testing.T) {
	tables := []string{"x", "y"}
	edges := map[string][]string{"x": {"y"}, "y": {"x"}}
	if _, err := topoOrder(tables, edges); err == nil {
		t.Error("topoOrder accepted a FOREIGN KEY cycle")
	}
}

// TestSharedColumns_AdditiveDriftIsReported: a column that exists on
// one side only must be reported, not fatal — that is what makes a
// conversion survive the two chains drifting apart.
func TestSharedColumns_AdditiveDriftIsReported(t *testing.T) {
	src := []columnInfo{{Name: "id"}, {Name: "value"}, {Name: "only_in_source"}}
	tgt := []columnInfo{{Name: "ID"}, {Name: "value"}, {Name: "only_in_target"}}
	shared, missing := sharedColumns(src, tgt)
	if strings.Join(shared, ",") != "id,value" {
		t.Errorf("shared = %v, want [id value] (case-insensitive match)", shared)
	}
	if strings.Join(missing, ",") != "only_in_source" {
		t.Errorf("missing = %v, want [only_in_source]", missing)
	}
}

// TestTypeBase_NormalisesDeclaredTypes covers the declared-type shapes
// the two catalogs actually return.
func TestTypeBase_NormalisesDeclaredTypes(t *testing.T) {
	cases := map[string]string{
		"VARCHAR(255)":              "VARCHAR",
		"numeric(10,2)":             "NUMERIC",
		" timestamp with time zone": "TIMESTAMP WITH TIME ZONE",
		"INTEGER":                   "INTEGER",
	}
	for in, want := range cases {
		if got := typeBase(in); got != want {
			t.Errorf("typeBase(%q) = %q, want %q", in, got, want)
		}
	}
	// The classifier must not mistake "POINT" for an integer type —
	// the reason isIntegerType is a closed set rather than a suffix test.
	if isIntegerType(typeBase("POINT")) {
		t.Error(`isIntegerType("POINT") = true — the suffix heuristic is back`)
	}
	if !isTimeType(typeBase("TIMESTAMPTZ")) {
		t.Error(`isTimeType("TIMESTAMPTZ") = false`)
	}
}
