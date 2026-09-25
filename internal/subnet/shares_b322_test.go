package subnet

// shares_b322_test.go — B322 (2026-09-25): the idempotent share INSERT must be written in
// the dialect's own syntax. `INSERT OR IGNORE` is SQLite-only; PostgreSQL answers
// `ERROR: syntax error at or near "OR"` (verified on PG 15), so granting a subnet share
// failed outright on a PG install. The B60 sweep that guarded this class was one of the 24
// masked contracts, which is why arming the band is what surfaced it.

import (
	"strings"
	"testing"

	"skygate/internal/db"
)

func TestSubnetShareInsertSQL_B322(t *testing.T) {
	pg := subnetShareInsertSQL(db.BackendPostgres)
	if !strings.Contains(pg, "ON CONFLICT (grantor_user_id, grantee_user_id) DO NOTHING") {
		t.Errorf("PostgreSQL form must use ON CONFLICT ... DO NOTHING, got:\n%s", pg)
	}
	if strings.Contains(pg, " OR IGNORE") {
		t.Errorf("PostgreSQL form still carries a SQLite-only clause:\n%s", pg)
	}

	sqlite := subnetShareInsertSQL(db.BackendSQLite)
	if !strings.Contains(sqlite, "INSERT OR IGNORE") {
		t.Errorf("SQLite form must keep INSERT OR IGNORE, got:\n%s", sqlite)
	}
	if strings.Contains(sqlite, "ON CONFLICT") {
		t.Errorf("SQLite form must not carry the PostgreSQL clause, got:\n%s", sqlite)
	}

	if strings.Contains(subnetShareInsertSQL(""), "ON CONFLICT") {
		t.Error("an unknown backend must fall back to the SQLite form")
	}
}
