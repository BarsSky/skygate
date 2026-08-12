package db

import (
	"database/sql"
	"strings"
	"testing"
)

// TestDetectBackend covers the dsn-prefix detection logic. v1.3.0:
// skygate is PG-only; the only valid prefix is postgres:// /
// postgresql://. Any other string is treated as a malformed PG
// DSN (returns BackendPostgres anyway, because the next Open/Ping
// will fail loudly). This is intentional: a pre-v1.3.0 file path
// passed to OpenDSN now fails at Ping with "connection refused"
// instead of silently opening a SQLite file.
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
		// v1.3.0: file paths are no longer SQLite — they are
		// treated as malformed PG DSNs. The next Open/Ping
		// fails with a loud error.
		{"/var/lib/skygate/skygate.db", BackendPostgres},
		{"./skygate.db", BackendPostgres},
		{"/tmp/t.db", BackendPostgres},
		{"skygate.db", BackendPostgres},
		{"", BackendPostgres},
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

// TestRegisterBackendIdempotent verifies the same *sql.DB pointer
// can be re-registered with the same backend (used by OpenDSN
// internally for retry paths). Re-registering with a different
// backend must panic — that would mean the caller is opening the
// same connection under two different engines, which is a bug.
//
// v1.3.0: re-registered with BackendPostgres (the only valid
// value). Pre-v1.3.0 used BackendSQLite which no longer exists.
func TestRegisterBackendIdempotent(t *testing.T) {
	// We need a real *sql.DB pointer that lives long enough
	// to test the register/BackendOf round-trip. Use the
	// OpenTestPG helper which skips if no PG is available.
	d := OpenTestPG(t)
	if d == nil {
		return // skipped (t.Skip already called)
	}
	// Re-registering with the same backend is a no-op.
	registerBackend(d, BackendPostgres)
	if BackendOf(d) != BackendPostgres {
		t.Errorf("after re-register, BackendOf = %q, want %q", BackendOf(d), BackendPostgres)
	}
	// Re-registering with a different backend must panic.
	defer func() {
		if r := recover(); r == nil {
			t.Error("registerBackend with different backend should have panicked")
		} else if s, ok := r.(string); ok && !strings.Contains(s, "double-open") {
			t.Errorf("panic message = %q, expected to contain 'double-open'", s)
		}
	}()
	registerBackend(d, Backend("other-impossible-backend"))
}

// Compile-time check that the test file imports the *sql.DB
// type so the import is retained even if all uses of it are
// in skipped tests.
var _ *sql.DB
