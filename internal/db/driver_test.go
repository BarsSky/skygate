package db

import (
	"strings"
	"testing"
)

// TestDetectBackend covers the dsn-prefix detection logic. The
// only contract: any string starting with postgres:// or
// postgresql:// (case-insensitive) is a Postgres DSN; anything
// else is treated as a SQLite file path.
func TestDetectBackend(t *testing.T) {
	cases := []struct {
		dsn  string
		want Backend
	}{
		// PostgreSQL (lower)
		{"postgres://user:pass@host:5432/db", BackendPostgres},
		{"postgresql://user:pass@host:5432/db", BackendPostgres},
		// PostgreSQL (upper prefix)
		{"POSTGRES://user:pass@host:5432/db", BackendPostgres},
		{"PostgreSQL://user:pass@host:5432/db", BackendPostgres},
		// With query string
		{"postgres://user:pass@host:5432/db?sslmode=disable", BackendPostgres},
		{"postgresql://skygate:secret@10.0.0.1:5432/skygate?sslmode=disable&pool_max_conns=10", BackendPostgres},
		// SQLite (file paths)
		{"/var/lib/skygate/skygate.db", BackendSQLite},
		{"./skygate.db", BackendSQLite},
		{"/tmp/t.db", BackendSQLite},
		{"skygate.db", BackendSQLite},
		// Edge case: empty string is treated as SQLite (file
		// with empty path; this would fail at Open() but
		// detection itself doesn't error).
		{"", BackendSQLite},
	}
	for _, c := range cases {
		got := DetectBackend(c.dsn)
		if got != c.want {
			t.Errorf("DetectBackend(%q) = %q, want %q", c.dsn, got, c.want)
		}
	}
}

// TestBackendOfNil covers the nil-guard. BackendOf(nil) must
// return the empty string (NOT panic).
func TestBackendOfNil(t *testing.T) {
	if got := BackendOf(nil); got != "" {
		t.Errorf("BackendOf(nil) = %q, want empty string", got)
	}
}

// TestOpenSQLiteBackwardCompat is the critical regression test:
// the existing skygate deploy passes a file path (not a DSN) to
// db.Open(). After v0.27.0's driver abstraction, that path must
// still produce a working SQLite-backed *sql.DB with the schema
// bootstrapped. This test is the proof that we didn't break
// production.
func TestOpenSQLiteBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	if got := BackendOf(d); got != BackendSQLite {
		t.Errorf("BackendOf = %q, want %q", got, BackendSQLite)
	}
	// Verify the schema was bootstrapped (V025 portal_users exists).
	var n int
	if err := d.QueryRow("SELECT count(*) FROM portal_users").Scan(&n); err != nil {
		t.Errorf("SELECT count(*) FROM portal_users: %v", err)
	}
}

// TestRegisterBackendIdempotent verifies the same *sql.DB pointer
// can be re-registered with the same backend (used by Open()
// internally for retry paths). Re-registering with a different
// backend must panic — that would mean the caller is opening the
// same connection under two different engines, which is a bug.
func TestRegisterBackendIdempotent(t *testing.T) {
	// We use a real *sql.DB from Open() for this; that way the
	// pointer isn't shared with other tests.
	dir := t.TempDir()
	d, err := Open(dir + "/idempotent.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	// Re-registering with the same backend is a no-op.
	registerBackend(d, BackendSQLite)
	if BackendOf(d) != BackendSQLite {
		t.Errorf("after re-register, BackendOf = %q, want %q", BackendOf(d), BackendSQLite)
	}
	// Re-registering with a different backend must panic.
	defer func() {
		if r := recover(); r == nil {
			t.Error("registerBackend with different backend should have panicked")
		} else if !strings.Contains(r.(string), "double-open") {
			t.Errorf("panic message = %q, expected to contain 'double-open'", r)
		}
	}()
	registerBackend(d, BackendPostgres)
}
