// internal/db/dialect_type_name_b420_test.go — the measurement behind
// the dialect-leak guard's token list.
//
// The guard decides which PostgreSQL spellings to forbid in shared code.
// "SQLite rejects it" is not a safe assumption for TYPE NAMES: SQLite's
// parser accepts an arbitrary type-name, so `TEXT[]`, `jsonb`,
// `timestamptz` and even `SERIAL PRIMARY KEY` all CREATE successfully.
// That distinction decides severity, and severity decides whether a
// token belongs in the guard:
//
//   - `TEXT[]` — stored with TEXT affinity, a JSON array round-trips
//     unchanged → NOT in the list (it cannot break a deployment);
//   - `SERIAL PRIMARY KEY` — accepted as an unknown type, but SQLite does
//     not auto-assign, so the id stays NULL after an INSERT → IN the
//     list (silently broken rows are worse than an error).
//
// This test exists so those two decisions stay measured rather than
// remembered: if a future SQLite driver starts rejecting the type names
// (or starts honouring SERIAL), the guard's rationale changes with it.
package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openProbeDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open :memory: sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestSQLiteAcceptsPGTypeNames_B420 pins the permissive half: a PG type
// name is not a syntax error on SQLite.
func TestSQLiteAcceptsPGTypeNames_B420(t *testing.T) {
	d := openProbeDB(t)
	for _, c := range []struct{ name, ddl string }{
		{"TEXT[]", `CREATE TABLE t_text_array (a TEXT[] NOT NULL DEFAULT '{}')`},
		{"jsonb", `CREATE TABLE t_jsonb (a jsonb)`},
		{"timestamptz", `CREATE TABLE t_tstz (a timestamptz)`},
		{"SERIAL PRIMARY KEY", `CREATE TABLE t_serial (id SERIAL PRIMARY KEY, v TEXT)`},
		{"BIGSERIAL PRIMARY KEY", `CREATE TABLE t_bigserial (id BIGSERIAL PRIMARY KEY, v TEXT)`},
	} {
		if _, err := d.Exec(c.ddl); err != nil {
			t.Errorf("SQLite rejected the type name %s: %v — the dialect-leak guard's token rationale "+
				"is now wrong; re-measure and update dialect_leak_guard_test.go", c.name, err)
		}
	}
}

// TestSQLiteTextArrayRoundTrips_B420 pins the reason `TEXT[]` is NOT a
// guard token: SQLite stores it with TEXT affinity and gives the value
// back unchanged, so a shared statement may carry it without breaking a
// SQLite deployment.
func TestSQLiteTextArrayRoundTrips_B420(t *testing.T) {
	d := openProbeDB(t)
	if _, err := d.Exec(`CREATE TABLE t (a TEXT[] NOT NULL DEFAULT '{}')`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO t (a) VALUES ('["a","b"]')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var got string
	if err := d.QueryRow(`SELECT a FROM t`).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != `["a","b"]` {
		t.Fatalf("TEXT[] round-trip = %q, want the JSON array unchanged", got)
	}
}

// TestSQLiteSerialPrimaryKeyDoesNotAutoAssign_B420 pins the reason
// `SERIAL PRIMARY KEY` IS a guard token: it is accepted but inert, so a
// shared CREATE/INSERT that relies on it writes NULL ids on SQLite.
func TestSQLiteSerialPrimaryKeyDoesNotAutoAssign_B420(t *testing.T) {
	d := openProbeDB(t)
	if _, err := d.Exec(`CREATE TABLE t (id SERIAL PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO t (v) VALUES ('x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var id sql.NullInt64
	if err := d.QueryRow(`SELECT id FROM t`).Scan(&id); err != nil {
		t.Fatalf("select: %v", err)
	}
	if id.Valid {
		t.Fatalf("SERIAL PRIMARY KEY auto-assigned id=%d on SQLite; the guard lists it as a defect "+
			"because it is inert — re-measure and update the token list", id.Int64)
	}
}
