package db

// open_sqlite_b261_test.go — regression guard for the installer DSN form.
//
// Live finding (2026-09-18, B261 canary on the reference VM): a native
// install could not start at all. /etc/skygate/skygate.env (written by
// deploy/install-*.sh via resolve_db_type) sets
//
//	SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db
//
// config.resolveDBDSN() returns that verbatim, DetectDSN classifies it
// as DialectSQLite, and openSQLite used to hand "sqlite:/var/..." to
// modernc.org/sqlite unchanged. The driver does not know the "sqlite:"
// scheme, so SQLite tried to create a RELATIVE file literally named
// "sqlite:/var/..." (impossible: the "sqlite:" directory does not
// exist), failed with SQLITE_CANTOPEN, and the driver surfaced it as
//
//	sqlite ping "sqlite:/var/lib/skygate/skygate.db": unable to open database file: out of memory (14)
//
// which reads like an OOM bug rather than a path bug. The 5-attempt
// retry loop turned it into a ~40s hang, then log.Fatalf.
//
// Why the test suite missed it: every SQLite test used ":memory:" or
// OpenWithDialect(":memory:"), and the two tests that mention the
// "sqlite:/..." form (dialect_test.go, driver_test.go) only assert the
// DIALECT/BACKEND classification — never a real open. This file closes
// that gap with real file opens for each accepted DSN shape.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenSQLiteInstallerDSNForms opens a real database file through
// every DSN shape skygate accepts, including the exact form the
// installers write.
func TestOpenSQLiteInstallerDSNForms(t *testing.T) {
	cases := []struct {
		name string
		dsn  func(path string) string
	}{
		{"sqlite: prefix (installer form)", func(p string) string { return "sqlite:" + p }},
		{"bare path", func(p string) string { return p }},
		{"file: URI", func(p string) string { return "file:" + p }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "skygate.db")
			dsn := c.dsn(path)

			_, sqlDB, err := OpenWithDialect(dsn)
			if err != nil {
				t.Fatalf("OpenWithDialect(%q): %v", dsn, err)
			}
			defer sqlDB.Close()

			if got := BackendOf(sqlDB); got != BackendSQLite {
				t.Errorf("BackendOf = %q, want %q", got, BackendSQLite)
			}
			var one int
			if err := sqlDB.QueryRow("SELECT 1").Scan(&one); err != nil {
				t.Fatalf("SELECT 1: %v", err)
			}
			if one != 1 {
				t.Errorf("SELECT 1 = %d", one)
			}
			// The file must exist on disk — the pre-fix bug created
			// nothing (SQLITE_CANTOPEN) and looked like a hang.
			if _, err := os.Stat(path); err != nil {
				t.Errorf("database file was not created at %s: %v", path, err)
			}
		})
	}
}

// TestOpenSQLiteSchemeStripping pins the normalisation itself so the
// failure mode is a fast unit-test error, not a 40s retry loop.
func TestOpenSQLiteSchemeStripping(t *testing.T) {
	// ":memory:" variants must stay in-memory (no file side effects).
	for _, dsn := range []string{":memory:", "sqlite::memory:", "  :memory:  "} {
		_, sqlDB, err := OpenWithDialect(dsn)
		if err != nil {
			t.Fatalf("OpenWithDialect(%q): %v", dsn, err)
		}
		sqlDB.Close()
	}

	// "sqlite:<path>" must produce a real file, not a "sqlite:" dir.
	dir := t.TempDir()
	path := filepath.Join(dir, "prefixed.db")
	_, sqlDB, err := OpenWithDialect("sqlite:" + path)
	if err != nil {
		t.Fatalf("prefixed open: %v", err)
	}
	sqlDB.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("prefixed DSN did not create %s: %v", path, err)
	}
	// No stray file/dir whose name starts with the scheme.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "sqlite:") {
			t.Errorf("a file named %q was created — the scheme was not stripped", e.Name())
		}
	}
}

// TestConfigDSNFormsOpen is the end-to-end shape of the live bug: the
// exact string resolve_db_type writes must open. Kept as a plain
// constant so the contract does not depend on the install scripts being
// present in the test environment.
func TestConfigDSNFormsOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skygate.db")
	// verbatim from deploy/install-common.sh resolve_db_type: sqlite)
	dsn := "sqlite:" + path
	if !strings.HasPrefix(dsn, "sqlite:") {
		t.Fatal("fixture drift: the installer form must carry the sqlite: prefix")
	}
	_, sqlDB, err := OpenWithDialect(dsn)
	if err != nil {
		t.Fatalf("installer DSN %q does not open: %v", dsn, err)
	}
	defer sqlDB.Close()
	var n int
	if err := sqlDB.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n); err != nil {
		t.Fatalf("sqlite_master query: %v", err)
	}
}
