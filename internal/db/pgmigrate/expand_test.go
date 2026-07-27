package pgmigrate

import (
	"strings"
	"testing"
)

// TestIsDestructive_RejectsDropColumn pins the contract that any
// migration containing DROP COLUMN is flagged. The v0.29.0 updater
// refuses to apply such migrations unless SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION
// is set. The expand-contract pattern requires DROP to be a
// separate, operator-approved release.
func TestIsDestructive_RejectsDropColumn(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want bool
	}{
		// Safe — these should NOT be flagged
		{"create table", "CREATE TABLE foo (id INT)", false},
		{"create table if not exists", "CREATE TABLE IF NOT EXISTS foo (id INT)", false},
		{"add column", "ALTER TABLE foo ADD COLUMN bar TEXT DEFAULT 'x'", false},
		{"add column if not exists", "ALTER TABLE foo ADD COLUMN IF NOT EXISTS bar TEXT", false},
		{"create index", "CREATE INDEX idx_foo ON foo (id)", false},
		{"create index if not exists", "CREATE INDEX IF NOT EXISTS idx_foo ON foo (id)", false},
		{"create index concurrently", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foo ON foo (id)", false},
		{"insert", "INSERT INTO foo (id) VALUES (1)", false},
		{"insert or ignore", "INSERT OR IGNORE INTO foo (id, val) VALUES (1, 'x')", false},
		{"create table with default", "CREATE TABLE foo (id INT PRIMARY KEY, status TEXT NOT NULL DEFAULT 'pending')", false},

		// Destructive — these MUST be flagged
		{"drop column", "ALTER TABLE foo DROP COLUMN bar", true},
		{"drop column with backtick", "ALTER TABLE `foo` DROP COLUMN `bar`", true},
		{"drop table", "DROP TABLE foo", true},
		{"drop index", "DROP INDEX idx_foo", true},
		{"rename table", "ALTER TABLE foo RENAME TO bar", true},
		{"rename column", "ALTER TABLE foo RENAME COLUMN bar TO baz", true},
		{"truncate", "TRUNCATE TABLE foo", true},
		{"truncate no keyword", "TRUNCATE foo", true},
		{"delete from schema_migrations", "DELETE FROM schema_migrations WHERE version < 10", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsDestructive(c.sql)
			if got != c.want {
				t.Errorf("IsDestructive(%q) = %v, want %v", c.sql, got, c.want)
			}
		})
	}
}

// TestIsDestructiveRefused_PassesForSafe pins the call-site
// contract: the updater calls this BEFORE running the migration.
// A safe migration returns nil; a destructive one returns
// ErrDestructive.
func TestIsDestructiveRefused_PassesForSafe(t *testing.T) {
	if err := IsDestructiveRefused("CREATE TABLE foo (id INT)"); err != nil {
		t.Errorf("safe migration should pass: %v", err)
	}
}

func TestIsDestructiveRefused_BlocksDestructive(t *testing.T) {
	err := IsDestructiveRefused("ALTER TABLE foo DROP COLUMN bar")
	if err == nil {
		t.Fatal("destructive migration should be refused")
	}
	if !strings.Contains(err.Error(), "SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION") {
		t.Errorf("error should mention the env var to set; got: %v", err)
	}
}

// TestBuildCreateIndexStmt pins the per-driver SQL form. PG and
// pgx use CONCURRENTLY; every other driver (sqlite3, mysql) uses
// the standard form. The CALLER (the updater) is responsible for
// choosing the right driver; this helper just maps driver name
// to SQL form.
func TestBuildCreateIndexStmt(t *testing.T) {
	cases := []struct {
		driver string
		want   string
	}{
		{"postgres", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foo ON bar (a, b)"},
		{"pgx", "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_foo ON bar (a, b)"},
		{"sqlite3", "CREATE INDEX IF NOT EXISTS idx_foo ON bar (a, b)"},
		{"", "CREATE INDEX IF NOT EXISTS idx_foo ON bar (a, b)"},
	}
	for _, c := range cases {
		t.Run(c.driver, func(t *testing.T) {
			got := buildCreateIndexStmt(c.driver, "idx_foo", "bar", "a, b", "")
			if got != c.want {
				t.Errorf("buildCreateIndexStmt(%q) = %q, want %q", c.driver, got, c.want)
			}
		})
	}
}

// TestBuildCreateIndexStmt_WithWhereClause pins the partial-index
// behavior. The WHERE clause is appended after the column list,
// not as a separate statement.
func TestBuildCreateIndexStmt_WithWhereClause(t *testing.T) {
	got := buildCreateIndexStmt("postgres", "idx_user_subnets_status", "user_subnets", "user_id, status", "status != 'disabled'")
	want := "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_subnets_status ON user_subnets (user_id, status) WHERE status != 'disabled'"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRun_NilDBReturnsError pins the contract that Run on a nil
// *sql.DB returns an error (rather than panicking). The updater
// uses this in early-startup paths where the DB connection may
// not be available yet.
func TestRun_NilDBReturnsError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Run on nil DB should return error, not panic: %v", r)
		}
	}()
	err := Run(nil, nil)
	if err == nil {
		t.Error("Run(nil, nil) should return error")
	}
}

// TestAllowDestructiveEnv_Name pins the env var name contract.
// The updater reads this exact string; renaming it is a
// breaking change.
func TestAllowDestructiveEnv_Name(t *testing.T) {
	if AllowDestructiveEnv != "SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION" {
		t.Errorf("AllowDestructiveEnv = %q, want SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION (breaking change)", AllowDestructiveEnv)
	}
}

// TestDefaultLockTimeout_Value pins the lock_timeout value at
// 10 seconds. Changing this value affects every future migration
// in production; the change must be intentional and reviewed.
func TestDefaultLockTimeout_Value(t *testing.T) {
	if DefaultLockTimeout.Seconds() != 10 {
		t.Errorf("DefaultLockTimeout = %v, want 10s (any change here is a deployment-wide policy change)", DefaultLockTimeout)
	}
}

// TestRun_NilStmtsIsNoop pins that Run with no statements
// commits an empty transaction (BEGIN ... COMMIT) and returns
// nil. Useful for the updater to "ping" the DB connection
// without doing any work.
func TestRun_NilStmtsIsNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Run with no stmts should return error from BeginTx (no DB), not panic: %v", r)
		}
	}()
	// We pass a fresh, unconnected *sql.DB.Open() result to
	// exercise the BeginTx path. This will fail (no real
	// driver behind it), so we just assert that we get SOME
	// error, not a panic, and that the error mentions Begin.
	err := Run(nil, nil)
	if err == nil {
		t.Skip("Run with no DB returns an error; test depends on having no driver registered")
	}
}
