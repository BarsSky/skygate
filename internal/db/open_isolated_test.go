// internal/db/open_isolated_test.go — the isolation contract for
// opening a SECOND database inside a running server.
//
// WHY THIS FILE EXISTS
// --------------------
// The /admin/database conversion opens the target database from inside
// the skygate process. The ordinary OpenWithDialect funnels through
// registerBackend, which ALSO writes the process-wide active dialect
// (active_dialect.go) — the value every concurrent request branches on
// through the SQL-fragment shims (nowUnixSQL, the ON CONFLICT builders,
// placeholders). A SQLite → PostgreSQL conversion would therefore flip
// the running server to PostgreSQL SQL while it is still talking to
// SQLite.
//
// TestOpenIsolated_DoesNotTouchTheActiveDialect is the guard: it makes
// the hazard visible (by showing that the ordinary path DOES write the
// value) and then asserts the isolated path does not.
package db

import (
	"database/sql"
	"testing"
)

// TestRegisterBackendWritesTheProcessWideDialect documents the hazard.
// If this ever stops being true the isolation below is unnecessary —
// but the test asserting it keeps the two paths distinguishable.
func TestRegisterBackendWritesTheProcessWideDialect(t *testing.T) {
	prev := ActiveDialect()
	defer SetActiveDialect(prev)

	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()

	SetActiveDialect(DialectSQLite)
	registerBackend(conn, BackendPostgres)
	if got := ActiveDialect(); got != DialectPostgres {
		t.Fatalf("registerBackend did not write the active dialect (got %v) — "+
			"the premise of OpenIsolated changed", got)
	}
}

// TestOpenIsolated_DoesNotTouchTheActiveDialect is the contract: a
// second database must be registered for BackendOf (which the migration
// chain reads) without becoming the process's dialect.
func TestOpenIsolated_DoesNotTouchTheActiveDialect(t *testing.T) {
	prev := ActiveDialect()
	defer SetActiveDialect(prev)

	// A running SQLite process.
	_, live, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open the live connection: %v", err)
	}
	defer live.Close()
	if got := ActiveDialect(); got != DialectSQLite {
		t.Fatalf("ActiveDialect() = %v after opening SQLite, want sqlite", got)
	}

	// A second database, whose dialect deliberately DIFFERS from the
	// live one. We cannot reach a real PostgreSQL here, so the second
	// connection is a plain SQLite handle registered as PostgreSQL —
	// which is exactly the state registerBackend would have created by
	// accident, and exactly what must not happen.
	other, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer other.Close()
	registerToolConnection(other, DialectPostgres)

	if got := BackendOf(other); got != BackendPostgres {
		t.Errorf("BackendOf(isolated connection) = %q, want %q — the migration chain "+
			"reads this value to pick its DDL", got, BackendPostgres)
	}
	if got := ActiveDialect(); got != DialectSQLite {
		t.Errorf("ActiveDialect() = %v, want sqlite — opening a second database changed "+
			"the dialect every concurrent request branches on (the SQLite → PostgreSQL "+
			"conversion would corrupt the live server)", got)
	}
}

// TestOpenIsolated_SQLiteRoundTrip: the isolated opener really opens a
// usable database and leaves the process dialect alone.
func TestOpenIsolated_SQLiteRoundTrip(t *testing.T) {
	prev := ActiveDialect()
	defer SetActiveDialect(prev)

	_, live, err := OpenWithDialect(":memory:")
	if err != nil {
		t.Fatalf("open the live connection: %v", err)
	}
	defer live.Close()

	dir := t.TempDir()
	d, iso, err := OpenIsolated("sqlite:" + dir + "/isolated.db")
	if err != nil {
		t.Fatalf("OpenIsolated: %v", err)
	}
	defer iso.Close()

	if d.Kind != DialectSQLite {
		t.Errorf("dialect kind = %v, want sqlite", d.Kind)
	}
	if got := BackendOf(iso); got != BackendSQLite {
		t.Errorf("BackendOf(isolated) = %q, want %q", got, BackendSQLite)
	}
	if _, err := iso.Exec(`CREATE TABLE probe (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("the isolated connection is not usable: %v", err)
	}
	if got := ActiveDialect(); got != DialectSQLite {
		t.Errorf("ActiveDialect() = %v — OpenIsolated changed it", got)
	}
}

// TestDialectFor_DescribesAKnownConnection: the live connection's
// dialect comes from ActiveDialect, not from re-parsing a DSN, so the
// converter needs a way to build a *Dialect for it.
func TestDialectFor_DescribesAKnownConnection(t *testing.T) {
	d := DialectFor(DialectSQLite, "sqlite:/tmp/x.db")
	if d.Kind != DialectSQLite {
		t.Errorf("Kind = %v, want sqlite", d.Kind)
	}
	if d.DSN() != "sqlite:/tmp/x.db" {
		t.Errorf("DSN() = %q", d.DSN())
	}
	if d.Kind.Placeholders(2) != "?,?" {
		t.Errorf("Placeholders(2) = %q, want ?,?", d.Kind.Placeholders(2))
	}
}
