package mesh

// mesh_b322_test.go — B322 (2026-09-25): the idempotent membership INSERT must be written
// in the dialect's own syntax.
//
// Live effect of the defect this pins: `INSERT OR IGNORE` is SQLite-only, PostgreSQL
// answers `ERROR: syntax error at or near "OR"` (verified on PG 15), and joining a mesh
// failed outright on a PG install. The B60 sweep that guarded the class was one of the 24
// masked contracts (its chain ended with `rm -f`, so it always reported PASS), which is why
// the audit's arming of the band is what surfaced this in the first place.

import (
	"strings"
	"testing"

	"skygate/internal/db"
)

func TestMeshMemberInsertSQL_B322(t *testing.T) {
	pg := meshMemberInsertSQL(db.BackendPostgres)
	if !strings.Contains(pg, "ON CONFLICT (mesh_id, user_id) DO NOTHING") {
		t.Errorf("PostgreSQL form must use ON CONFLICT ... DO NOTHING, got:\n%s", pg)
	}
	if strings.Contains(pg, "INSERT OR IGNORE") {
		t.Errorf("PostgreSQL form must NOT contain SQLite-only INSERT OR IGNORE, got:\n%s", pg)
	}
	// The PostgreSQL parser rejects OR IGNORE outright, so the keyword pair must not appear
	// anywhere in the statement it receives.
	if strings.Contains(pg, " OR IGNORE") {
		t.Errorf("PostgreSQL form still carries a SQLite-only clause:\n%s", pg)
	}

	sqlite := meshMemberInsertSQL(db.BackendSQLite)
	if !strings.Contains(sqlite, "INSERT OR IGNORE") {
		t.Errorf("SQLite form must keep INSERT OR IGNORE, got:\n%s", sqlite)
	}
	if strings.Contains(sqlite, "ON CONFLICT") {
		t.Errorf("SQLite form must not carry the PostgreSQL clause, got:\n%s", sqlite)
	}

	// An unknown/empty backend must not produce the PostgreSQL clause (the SQLite driver is
	// the zero value's default in this package's callers).
	if strings.Contains(meshMemberInsertSQL(""), "ON CONFLICT") {
		t.Error("an unknown backend must fall back to the SQLite form")
	}
}
