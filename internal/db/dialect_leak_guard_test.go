// internal/db/dialect_leak_guard_test.go — static guard against
// re-introducing dialect-specific SQL into shared code.
//
// WHY
// ---
// The 2026-09-18 audit found that shared query files had accumulated
// SQL fragments valid on exactly one backend:
//
//   - `EXTRACT(EPOCH FROM now())::bigint` (PostgreSQL-only) in files used
//     by both backends → "near FROM: syntax error" on SQLite;
//   - `?` placeholders (SQLite-flavoured) → SQLSTATE 42601 on PostgreSQL;
//   - raw `strftime('%s','now')` (SQLite-only) → only tolerated on PG
//     because migrations_pg.go:965 installs a compat function.
//
// Both classes were invisible in production for a long time because the
// callers swallowed the errors. The runtime tests in
// dialect_runtime_test.go cover the helpers; this guard covers the
// REVIEW dimension — it fails the build when someone types one of these
// fragments into a shared file again.
//
// Deliberately conservative: it only inspects lines that look like SQL
// literals (contain a backtick), so prose in comments does not trip it.
package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Files allowed to contain backend-specific SQL: the dialect helpers
// themselves, per-backend implementations, and the migration chains.
func dialectLeakAllowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, ok := range []string{
		"internal/db/migrations_",
		"internal/db/now_unix",
		"internal/db/active_dialect.go",
		"internal/db/dialect.go",
		"internal/db/dialect_runtime_test.go",
		"internal/db/dialect_leak_guard_test.go",
		"internal/db/placeholders",
		"internal/db/on_conflict",
	} {
		if strings.HasPrefix(rel, ok) {
			return true
		}
	}
	return false
}

func TestNoDialectLeaksInSharedCode(t *testing.T) {
	root := filepath.Join("..", "..")

	forbidden := []struct {
		token string
		why   string
	}{
		// The genuinely dangerous direction. PostgreSQL has a
		// strftime(format, ts) compat function (migrations_pg.go:965),
		// so SQLite-isms in a shared file are TOLERATED on PG. The
		// reverse is not true: SQLite has neither EXTRACT() nor the
		// ::bigint cast, so a raw PG timestamp expression in a shared
		// file is a hard runtime error on a SQLite deployment. That
		// asymmetry is why only this class is forbidden here.
		{"EXTRACT(EPOCH FROM now())", "PostgreSQL-only timestamp expression — use db.NowUnixSQL()"},
		{"EXTRACT(epoch FROM now())", "PostgreSQL-only timestamp expression — use db.NowUnixSQL()"},
	}

	var scanned, violations int

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable dirs are not this test's problem
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == ".trash" || base == "tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if dialectLeakAllowed(rel) {
			return nil
		}

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		scanned++

		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			// Skip comments — the repo documents these fragments in
			// prose (often inside backticks), which is not a leak.
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			// Only lines that look like SQL literals.
			if !strings.Contains(line, "`") {
				continue
			}
			for _, f := range forbidden {
				if strings.Contains(line, f.token) {
					violations++
					t.Errorf("%s:%d contains %q in a SQL literal — %s",
						filepath.ToSlash(rel), i+1, f.token, f.why)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if scanned == 0 {
		t.Fatal("scanned 0 files — the walk root is wrong, so this guard is not actually checking anything")
	}
	t.Logf("scanned %d non-test .go files for dialect leaks; violations=%d", scanned, violations)
}

// TestPGStrftimeCompatFunctionStillInstalled pins the other half of the
// asymmetry: shared query files DO rely on strftime() working on
// PostgreSQL (queries.go qTouchAPITokenLastUsed, secrets.go upserts,
// telegram_login_tokens.go). That only holds because migrations_pg.go
// installs a compatibility function. If someone removes it, those
// statements start failing on PG — so pin its presence here.
func TestPGStrftimeCompatFunctionStillInstalled(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "db", "migrations_pg.go"))
	if err != nil {
		t.Fatalf("read migrations_pg.go: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "FUNCTION strftime") {
		t.Fatal("migrations_pg.go no longer installs the strftime() compatibility function, " +
			"but shared query files still call strftime() directly — those statements would " +
			"now fail on PostgreSQL with \"function strftime(...) does not exist\". " +
			"Either restore the function or route those queries through db.NowUnixSQL()")
	}
}

// TestNoSQLitePlaceholdersInSharedSQL pins the `?` class for the two
// statements that were actually broken. Detecting every `?` reliably is
// not feasible statically (string literals legitimately contain '?'), so
// this checks the exact shapes instead of guessing.
func TestNoSQLitePlaceholdersInSharedSQL(t *testing.T) {
	checks := []struct {
		file  string
		token string
	}{
		{"internal/db/node_owner_map.go", "VALUES (?,"},
		{"internal/headscale_version/monitor.go", "LIMIT ?"},
	}
	for _, c := range checks {
		data, err := os.ReadFile(filepath.Join("..", "..", c.file))
		if err != nil {
			t.Fatalf("read %s: %v", c.file, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			// Comments legitimately quote the pre-fix SQL to explain
			// what was wrong (both files do exactly that).
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") {
				continue
			}
			if strings.Contains(line, c.token) {
				t.Errorf("%s:%d still contains the SQLite-era placeholder %q in code — "+
					"pgx rejects `?` outright (SQLSTATE 42601); use db.PlaceholdersList",
					c.file, i+1, c.token)
			}
		}
	}
}
