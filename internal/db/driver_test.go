package db

import (
	"database/sql"
	"strings"
	"testing"
)

// TestDetectBackend covers the dsn-prefix detection logic. v1.5.4
// (B246-dialect-retry): both PG and SQLite are first-class backends
// again. The dispatch is:
//
//   - postgres:// / postgresql:// (any case) → BackendPostgres
//   - sqlite: / file: / :memory: / bare path → BackendSQLite
//   - anything else with "://" but no recognised scheme → BackendPostgres
//     (legacy: pre-v1.3.0 PG-only fallback so the next Open/Ping
//     fails loudly on the unrecognised URL)
//
// The test pins all four cases. Pre-v1.5.4 (B246) this test expected
// bare paths to return BackendPostgres (the v1.3.0 PG-only contract);
// that behaviour was changed by B-mod-sqlite-pg-bidi + B246 to
// restore SQLite as a first-class backend.
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
		// v1.5.4 (B246): file paths ARE SQLite — the self-host
		// default. The dialect layer's openSQLite creates the file
		// if missing and applies the standard PRAGMAs.
		{"/var/lib/skygate/skygate.db", BackendSQLite},
		{"./skygate.db", BackendSQLite},
		{"/tmp/t.db", BackendSQLite},
		{"skygate.db", BackendSQLite},
		{"", BackendSQLite},
		{":memory:", BackendSQLite},
		{"sqlite:/data/skygate.db", BackendSQLite},
		{"file:/data/skygate.db", BackendSQLite},
		{"FILE:/data/skygate.db", BackendSQLite},
		{"SQLITE::memory:", BackendSQLite},
		// Has "://" but no recognised scheme — fallback to PG so
		// the next Open/Ping fails loudly on the unknown URL.
		{"mysql://user:pass@host:3306/db", BackendPostgres},
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
// v1.5.4 (B246): re-registered with BackendPostgres OR BackendSQLite
// (both are valid values now). Pre-v1.5.4 only BackendPostgres was
// accepted.
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
