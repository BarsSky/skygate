// internal/db/schema_parity_pg_test.go — the SQLite ↔ PostgreSQL schema
// parity contract, measured against two REAL databases.
//
// WHY THIS EXISTS (2026-09-28, TD-17)
// -----------------------------------
// The 2026-09-28 audit found that the two migration chains were
// asserted "in lockstep" by AGENTS.md rule 9, but nothing compared
// them:
//
//   - migrations_sqlite_schema_test.go pins the SQLite chain's own head
//     with a hard-coded literal (`maxV != 77`) and spot-checks a handful
//     of columns. Adding V078 to the PostgreSQL chain ALONE leaves it
//     green, under a subtest literally named "chain reaches V077 like
//     PostgreSQL".
//   - Every B-check that guards a migration greps for the function name
//     in both files (`^func migrateV072PG` / `^func migrateV072SQLite`),
//     which proves the author wrote two functions — not that they define
//     the same columns.
//   - The largest chain audit (migrations_audit_b233_test.go) explicitly
//     SKIPS migrations_sqlite.go.
//
// So the class of bug that already shipped twice (the missing V071 and
// four `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` statements that SQLite
// silently swallowed, see the header of migrations_sqlite_schema_test.go)
// had no general guard.
//
// This test runs BOTH chains against real databases and compares the
// resulting schemas table by table, column by column:
//
//   - a table or column present in one chain and missing from the other
//     is a FAILURE — that is exactly the shipped-bug class above;
//   - a column whose type falls into a different CLASS is a FAILURE —
//     the two sides must store the same kind of value;
//   - an ARRAY-ish type on the SQLite side is a FAILURE with its own
//     message: SQLite has no array type, so `TEXT[]` is an arbitrary
//     type name that stores the literal `{}` — the PostgreSQL
//     `cluster_node.roles` and `cluster_database.replica_node_ids`
//     columns were copied into the SQLite chain verbatim, which is how
//     the two backends ended up with different semantics for the same
//     column.
//
// It SKIPs without SKYGATE_TEST_PG_DSN (AGENTS rule 1).
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// typeClassOf maps a declared type in either dialect onto a small class,
// so `BIGSERIAL` and `INTEGER` compare equal while `INTEGER` and `TEXT`
// do not. skygate stores timestamps and booleans as integers on BOTH
// backends, so those map to "int" as well.
func typeClassOf(declared string) string {
	b := typeBase(declared)
	// An array form in either dialect (PostgreSQL reports `ARRAY`,
	// a SQLite column may literally be declared `TEXT[]`).
	if b == "ARRAY" || strings.HasSuffix(b, "[]") {
		return "array"
	}
	switch {
	case isBooleanType(b):
		return "int" // skygate stores booleans as INTEGER 0/1 on both sides
	case isIntegerType(b):
		return "int"
	case isTimeType(b):
		return "int" // skygate stores epoch seconds as INTEGER on both sides
	case isBinaryType(b):
		return "blob"
	case b == "REAL" || b == "DOUBLE PRECISION" || b == "FLOAT" ||
		b == "NUMERIC" || b == "DECIMAL":
		return "real"
	case isTextType(b) || strings.Contains(b, "CHAR") || b == "":
		return "text"
	default:
		return "other:" + b
	}
}

// schemaOf returns table → column → declared type for an open database.
func schemaOf(t *testing.T, ctx context.Context, conn *sql.DB, kind DialectKind) map[string]map[string]string {
	t.Helper()
	tables, err := listTables(ctx, conn, kind)
	if err != nil {
		t.Fatalf("list %s tables: %v", kind, err)
	}
	out := make(map[string]map[string]string, len(tables))
	for _, tbl := range tables {
		cols, err := tableColumns(ctx, conn, kind, tbl)
		if err != nil {
			t.Fatalf("read %s columns of %s: %v", kind, tbl, err)
		}
		m := make(map[string]string, len(cols))
		for _, c := range cols {
			m[strings.ToLower(c.Name)] = c.Type
		}
		out[tbl] = m
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// knownArrayColumnsOnSQLite are the two array-shaped declarations the
// SQLite chain carries on purpose, with the reason each is safe. Any
// OTHER array-shaped column on the SQLite side fails the contract, so a
// new one cannot arrive by copying a PostgreSQL migration again.
//
// Both are read and written through code that knows the SQLite form is
// the PostgreSQL text literal `{a,b}` rather than a real array:
// `rolesContainLiteral` and `FindClusterPrimary` in cluster_sql_b291.go,
// and `parsePGTextArray` on the PostgreSQL side. They are declared this
// way (rather than TEXT) only because the SQLite chain is a mechanical
// port of the PostgreSQL one; changing the declaration now would mean
// rebuilding both tables on every existing SQLite install for no
// functional gain, because SQLite accepts any type name and the value
// is a string either way.
//
// What is NOT safe is adding a third one: `DEFAULT '{}'` stores the
// two-character string "{}" on SQLite and an empty ARRAY on PostgreSQL,
// so a new array column needs an explicit decision about which side's
// semantics its readers implement.
var knownArrayColumnsOnSQLite = map[string]string{
	"cluster_database.replica_node_ids": "compensated by parsePGTextArray (PostgreSQL) / ClusterDatabase.ReplicaNodeIDs (SQLite)",
	"cluster_node.roles":                "compensated by rolesContainLiteral + FindClusterPrimary (cluster_sql_b291.go)",
}

// TestSchemaParity_SQLiteVsPostgres_Real is the TD-17 contract.
func TestSchemaParity_SQLiteVsPostgres_Real(t *testing.T) {
	ctx := context.Background()
	pgTestAdminDSN(t)

	// SQLite side: the real chain on a real database.
	_, lite, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer lite.Close()
	if err := ApplyMigrations(lite, DialectSQLite); err != nil {
		t.Fatalf("apply the SQLite chain: %v", err)
	}

	// PostgreSQL side: the real chain on a real server.
	pgDSN := pgFreshDatabase(t, dbNameFor(t))
	_, pg, err := OpenIsolated(pgDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pg.Close()

	liteSchema := schemaOf(t, ctx, lite, DialectSQLite)
	pgSchema := schemaOf(t, ctx, pg, DialectPostgres)

	// --- 1. the table sets -----------------------------------------------
	for _, tbl := range sortedKeys(pgSchema) {
		if _, ok := liteSchema[tbl]; !ok {
			t.Errorf("table %q exists in the PostgreSQL chain but NOT in the SQLite chain — "+
				"a SQLite install silently lacks it (the V071 class)", tbl)
		}
	}
	for _, tbl := range sortedKeys(liteSchema) {
		if _, ok := pgSchema[tbl]; !ok {
			t.Errorf("table %q exists in the SQLite chain but NOT in the PostgreSQL chain", tbl)
		}
	}

	// --- 2. the column sets, table by table ------------------------------
	var columnDiffs, classDiffs, arrayOnSQLite []string
	for _, tbl := range sortedKeys(liteSchema) {
		pgCols, ok := pgSchema[tbl]
		if !ok {
			continue // already reported above
		}
		liteCols := liteSchema[tbl]

		for _, col := range sortedKeys(liteCols) {
			pgType, ok := pgCols[col]
			if !ok {
				columnDiffs = append(columnDiffs,
					fmt.Sprintf("%s.%s is in the SQLite chain but not in the PostgreSQL chain", tbl, col))
				continue
			}
			liteType := liteCols[col]
			if typeClassOf(liteType) != typeClassOf(pgType) {
				classDiffs = append(classDiffs, fmt.Sprintf(
					"%s.%s: SQLite %q (%s) vs PostgreSQL %q (%s)",
					tbl, col, liteType, typeClassOf(liteType), pgType, typeClassOf(pgType)))
			}
			// SQLite has no array type: an array-shaped declaration is an
			// arbitrary type name whose default of '{}' stores a
			// two-character string, so the column means something
			// different on the two backends.
			liteBase := typeBase(liteType)
			if strings.HasSuffix(liteBase, "[]") || liteBase == "ARRAY" {
				key := tbl + "." + col
				if reason, known := knownArrayColumnsOnSQLite[key]; known {
					t.Logf("known SQLite array-shaped column %s — %s", key, reason)
					continue
				}
				arrayOnSQLite = append(arrayOnSQLite, fmt.Sprintf("%s.%s is declared %q on SQLite",
					tbl, col, liteType))
			}
		}
		for _, col := range sortedKeys(pgCols) {
			if _, ok := liteCols[col]; !ok {
				columnDiffs = append(columnDiffs,
					fmt.Sprintf("%s.%s is in the PostgreSQL chain but not in the SQLite chain", tbl, col))
			}
		}
	}

	if len(columnDiffs) > 0 {
		// This is the shipped-bug class: the SQLite side silently lacks a
		// column the application writes to, so every write on a native
		// install fails or is dropped.
		t.Errorf("column parity broken between the two migration chains (%d):\n  %s",
			len(columnDiffs), strings.Join(columnDiffs, "\n  "))
	}
	if len(arrayOnSQLite) > 0 {
		t.Errorf("array-typed columns in the SQLite chain (%d) — SQLite has no array type, so these "+
			"store the literal '{}' and mean something different from PostgreSQL:\n  %s",
			len(arrayOnSQLite), strings.Join(arrayOnSQLite, "\n  "))
	}
	if len(classDiffs) > 0 {
		// Not always a bug (BIGSERIAL vs INTEGER is fine, both are "int"),
		// but any difference that reaches here crosses a VALUE class and
		// must be a deliberate, documented mapping.
		t.Errorf("column type classes differ between the chains (%d) — every one of these is a "+
			"conversion or read-path hazard:\n  %s",
			len(classDiffs), strings.Join(classDiffs, "\n  "))
	}

	t.Logf("compared %d tables / %d columns across both chains",
		len(liteSchema), countColumns(liteSchema))
}

func countColumns(schema map[string]map[string]string) int {
	n := 0
	for _, cols := range schema {
		n += len(cols)
	}
	return n
}

// TestSchemaParity_HeadVersions_Real: both chains must end at the same
// version. This is worth asserting on a REAL database rather than
// against the hard-coded literal in migrations_sqlite_schema_test.go
// (which cannot see a PostgreSQL-only addition).
func TestSchemaParity_HeadVersions_Real(t *testing.T) {
	ctx := context.Background()
	pgTestAdminDSN(t)

	_, lite, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	defer lite.Close()
	if err := ApplyMigrations(lite, DialectSQLite); err != nil {
		t.Fatalf("apply the SQLite chain: %v", err)
	}

	pgDSN := pgFreshDatabase(t, dbNameFor(t))
	_, pg, err := OpenIsolated(pgDSN)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pg.Close()

	var liteHead int
	if err := lite.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM applied_migrations`).Scan(&liteHead); err != nil {
		t.Fatalf("read the SQLite head: %v", err)
	}
	var pgHead int
	if err := pg.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM applied_migrations`).Scan(&pgHead); err != nil {
		t.Fatalf("read the PostgreSQL head: %v", err)
	}
	if liteHead != pgHead {
		t.Errorf("chain heads differ: SQLite V%d, PostgreSQL V%d — one chain has migrations the "+
			"other does not", liteHead, pgHead)
	}
	if liteHead == 0 {
		t.Error("both chains report head V0 — applied_migrations was not written on either side")
	}
	t.Logf("both chains end at V%d", liteHead)
}
