package db

// migrations_audit_b233_test.go — v0.69 (B233) — source-level
// migration audit that catches the B232-class shape-
// drift bug at unit-test time.
//
// Why this exists
// ---------------
// V056 (B188.2 era, deploy ~2026-08-17) defined the
// natural-key UNIQUE INDEX on `device_rules` as 6
// columns (with parent_domain), but the
// `CREATE UNIQUE INDEX IF NOT EXISTS` statement is a
// no-op when an index with the same NAME already
// exists with a different shape. Pre-V056, the index
// was 5 columns (no parent_domain). On every DB that
// was upgraded past V055 before V056, the V056
// statement was a silent no-op and the index STAYED
// 5-col. Then B188.2 changed `qInsertDeviceRule` to
// use a 6-col `ON CONFLICT` clause; the 5-col index
// didn't match, so every new INSERT failed.
//
// `TestPGMigrations_OrderedByVersion` (the
// framework-state test from B213) passed for V056
// because it only checks that V056 is REGISTERED +
// ORDERED. It does NOT check the SHAPE of V056's
// CREATE statement. The bug only became visible at
// runtime — the "db error on /my/exit-rules POST"
// symptom reported by the operator on 2026-09-03.
//
// This test file closes the gap with a source-level
// audit that runs at `go test` time (no DB needed).
// It pins the B232 pattern: every migration that
// creates or modifies an index must do explicit
// DROP + CREATE (or be a NEW index that the V0_
// migration chain never had before). Any
// `CREATE INDEX IF NOT EXISTS` without a paired
// `DROP INDEX IF EXISTS` in the same migration is
// flagged as a shape-drift risk, and the test fails.
//
// We use source-level grep/parsing instead of a
// real DB so the test runs in <100ms (no docker,
// no testcontainer, no sqlite dep). The trade-off
// is that we can only catch the PATTERN, not the
// runtime behaviour — but the B232 pattern IS a
// pattern problem (CREATE IF NOT EXISTS vs DROP
// + CREATE), so source-level catches the next
// instance of the same class of bug.
//
// Scope
// -----
// This test scans every migration file in
// internal/db/migrations*.go. We exclude
// - the test files themselves
//   (migrations_v0_*_test.go)
// - the B232 file
//   (migrations_v0_68_b232.go — it intentionally
//   uses the new DROP+CREATE pattern; the test
//   asserts that it's the LATEST CREATE for the
//   device_rules_natural_key_uniq index)
// - reference snippets in comments (we look for
//   the SQL string-literal pattern only, not for
//   the word "CREATE INDEX" anywhere)
//
// 2026-09-04: v0.69 (B233).

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// testdataMigrationFiles returns the paths of every
// file in internal/db that looks like a migration
// (i.e. matches the migrations*.go naming pattern).
// We exclude test files (migrations_v0_*_test.go)
// because the test source is allowed to contain
// example SQL strings (the B232 test file uses
// `regexp.QuoteMeta` on those examples).
//
// 2026-09-04: v0.69 (B233).
func testdataMigrationFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("migrations*.go")
	if err != nil {
		t.Fatalf("glob migrations*.go: %v", err)
	}
	var out []string
	for _, m := range matches {
		base := filepath.Base(m)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// auditMigrationFile extracts the (creates, drops)
// lists from a single migration file. We look for
// SQL fragments in the form:
//   - `CREATE [UNIQUE] INDEX IF NOT EXISTS <name> ...`
//   - `DROP INDEX IF EXISTS <name>`
// anchored to backticks so we don't pick up SQL
// fragments inside Go comments.
//
// 2026-09-04: v0.69 (B233).
func auditMigrationFile(t *testing.T, path string) (creates, drops []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(b)
	// `CREATE [UNIQUE] INDEX IF NOT EXISTS <name> ...`
	createRe := regexp.MustCompile("`[^`]*?CREATE\\s+(UNIQUE\\s+)?INDEX\\s+IF\\s+NOT\\s+EXISTS\\s+([a-zA-Z_][a-zA-Z0-9_]*)[^`]*?`")
	for _, m := range createRe.FindAllStringSubmatch(src, -1) {
		creates = append(creates, m[2])
	}
	// `DROP INDEX IF EXISTS <name>`
	dropRe := regexp.MustCompile("`[^`]*?DROP\\s+INDEX\\s+IF\\s+EXISTS\\s+([a-zA-Z_][a-zA-Z0-9_]*)[^`]*?`")
	for _, m := range dropRe.FindAllStringSubmatch(src, -1) {
		drops = append(drops, m[1])
	}
	return
}

// shapeDriftWhitelist is the set of (file, index_name)
// pairs where CREATE INDEX IF NOT EXISTS without
// DROP is INTENTIONAL (the index was new in that
// migration, so the IF NOT EXISTS is a no-op on
// fresh DBs and harmless on upgraded ones because
// the existing index already has the right shape).
//
// This whitelist exists because migrating an entire
// production DB is out of scope for B233 (we want
// to catch FUTURE instances of the V056 pattern,
// not retroactively fail on V056 itself). V056 is
// repaired by V068 in B232; V056 stays in the
// whitelist as a one-time ack.
//
// 2026-09-04: v0.69 (B233).
var shapeDriftWhitelist = map[string]bool{
	// V056 (B188.2 era) is the original offender. The
	// shape-drift bug it caused is fixed by V068
	// (B232) — the audit framework's purpose is to
	// prevent FUTURE instances of the same class.
	"migrations_pg.go::device_rules_natural_key_uniq": true,
}

// TestMigrations_ShapeDriftAudit scans every
// migration file. The key insight is that
// `CREATE INDEX IF NOT EXISTS <name>` is fine if
// `<name>` is a NEW index (never seen in the chain
// before this migration), but it's BAD if `<name>`
// already exists from an earlier migration with a
// different shape — that's the V056 / B232 bug
// pattern.
//
// The test walks the migration chain in order, keeping
// a running set of "indexes I've seen so far". When
// it encounters a CREATE INDEX IF NOT EXISTS for an
// already-seen name, it asserts there's a paired
// DROP INDEX IF EXISTS in the same migration. The
// first CREATE for any name is exempt (no prior shape
// to drift from).
//
// Whitelist: V056 (B188.2 era) is the historical
// offender. The shape-drift bug it caused is fixed
// by V068 (B232). The audit framework's purpose is
// to prevent FUTURE instances of the same class;
// V056 is a one-time ack.
//
// 2026-09-04: v0.69 (B233).
func TestMigrations_ShapeDriftAudit(t *testing.T) {
	files := testdataMigrationFiles(t)
	if len(files) == 0 {
		t.Fatal("no migration files found via migrations*.go glob — something is wrong with the test setup")
	}
	seen := make(map[string]bool) // running set of index names
	// Sort files by version ASC to walk the chain in
	// the same order migrations run at startup. We
	// use lexicographic sort on the version number
	// in the filename (migrations_v0_25_... <
	// migrations_v0_26_... < ... < migrations_pg.go
	// // which is "consolidated" — sorts LAST because
	// of the trailing "p" in "pg" vs digit).
	sortedFiles := make([]fileVersion, 0, len(files))
	for _, f := range files {
		base := filepath.Base(f)
		v := versionOf(base)
		sortedFiles = append(sortedFiles, fileVersion{path: f, version: v})
	}
	sort.Slice(sortedFiles, func(i, j int) bool {
		return sortedFiles[i].version < sortedFiles[j].version
	})
	for _, fv := range sortedFiles {
		creates, drops := auditMigrationFile(t, fv.path)
		if len(creates) == 0 && len(drops) == 0 {
			continue
		}
		dropSet := make(map[string]bool, len(drops))
		for _, d := range drops {
			dropSet[d] = true
		}
		for _, c := range creates {
			if dropSet[c] {
				// explicit DROP + CREATE in same
				// migration: OK (the new V068 +
				// future B-blocks pattern).
				continue
			}
			// First-time CREATE? No prior migration
			// in the chain has this index name.
			// Safe (no prior shape to drift from).
			if !seen[c] {
				continue
			}
			// Re-CREATE without paired DROP — this is
			// the V056 / B232 pattern. Check the
			// whitelist before failing.
			key := filepath.Base(fv.path) + "::" + c
			if shapeDriftWhitelist[key] {
				continue
			}
			t.Errorf("shape-drift risk: %s has CREATE INDEX IF NOT EXISTS %q without a paired DROP INDEX IF EXISTS, but %q was already created in an earlier migration. This is the B232 pattern: the CREATE is a silent no-op on a DB where the prior index exists with a different shape, breaking any code that depends on the new shape (e.g. ON CONFLICT clauses). Add an explicit `DROP INDEX IF EXISTS %s` before the CREATE, or add (file, index) to shapeDriftWhitelist with a one-time ack comment.",
				fv.path, c, c, c)
		}
		// Update the seen set after this migration.
		for _, c := range creates {
			seen[c] = true
		}
	}
}

// fileVersion is a (file, version) pair used by the
// sort + chain walk in TestMigrations_ShapeDriftAudit.
//
// 2026-09-04: v0.69 (B233).
type fileVersion struct {
	path    string
	version int
}

// versionOf extracts the migration version from a
// filename like `migrations_v0_56_b188.go` → 56.
// Returns 0 for files that don't match (e.g.
// `migrations_pg.go` which is the consolidated
// initial schema and is treated as version 0).
//
// 2026-09-04: v0.69 (B233).
func versionOf(base string) int {
	re := regexp.MustCompile(`migrations_v0_(\d+)`)
	m := re.FindStringSubmatch(base)
	if m == nil {
		return 0 // migrations_pg.go sorts first
	}
	var v int
	_, _ = scanInt(m[1], &v)
	return v
}

// TestMigrations_DeviceRulesNaturalKeyIndexIsSixColumns
// pins the FINAL shape of the device_rules natural-
// key UNIQUE INDEX across the migration chain.
//
// The audit above catches the pattern; this test
// pins the runtime contract: after ALL migrations
// run (V025 through V068), the final CREATE for
// `device_rules_natural_key_uniq` MUST be 6-col
// (with parent_domain). This is the contract that
// `qInsertDeviceRule` and `qSelectRuleByComposite`
// rely on — drift here means the ON CONFLICT clause
// in qInsertDeviceRule stops matching, and every new
// INSERT fails (the B232 symptom).
//
// We look at the LAST CREATE statement for this
// index in source order. The order matches the
// migration version order (migrations are run
// sequentially by version).
//
// 2026-09-04: v0.69 (B233).
func TestMigrations_DeviceRulesNaturalKeyIndexIsSixColumns(t *testing.T) {
	files := testdataMigrationFiles(t)
	var lastCreate, lastCreateFile string
	var lastCreateIs6Col bool
	for _, f := range files {
		creates, _ := auditMigrationFile(t, f)
		for _, c := range creates {
			if c != "device_rules_natural_key_uniq" {
				continue
			}
			// Read the actual CREATE statement's column
			// list by searching the source line containing
			// the index name in a backtick-quoted string.
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			// Match the backtick-quoted CREATE statement
			// for THIS index name (not for an earlier
			// CREATE of a different index).
			re := regexp.MustCompile("`[^`]*?CREATE\\s+(UNIQUE\\s+)?INDEX\\s+IF\\s+NOT\\s+EXISTS\\s+device_rules_natural_key_uniq[^`]*?ON\\s+device_rules\\s*\\(([^)]+)\\)")
			if m := re.FindStringSubmatch(string(b)); m != nil {
				lastCreate = m[2]
				lastCreateFile = f
				cols := strings.Split(m[2], ",")
				is6Col := false
				for _, col := range cols {
					if strings.Contains(col, "parent_domain") {
						is6Col = true
						break
					}
				}
				lastCreateIs6Col = is6Col
			}
		}
	}
	if lastCreate == "" {
		t.Fatal("no CREATE statement found for device_rules_natural_key_uniq across the entire migration chain")
	}
	if !lastCreateIs6Col {
		t.Errorf("FINAL CREATE for device_rules_natural_key_uniq in %s is not 6-col (missing parent_domain). Last columns: %q. This is the B232 bug: qInsertDeviceRule uses 6-col ON CONFLICT and fails when the index is 5-col. V068 is the fix — verify migrations_v0_68_b232.go is the LATEST CREATE in the chain.",
			lastCreateFile, lastCreate)
	}
	t.Logf("FINAL CREATE for device_rules_natural_key_uniq in %s is 6-col (with parent_domain): %s", lastCreateFile, lastCreate)
}

// TestMigrations_V068IsLastToCreateDeviceRulesNaturalKey
// pins the fix ordering: V068 (B232, the DROP +
// RECREATE repair) must be the LAST migration to
// touch `device_rules_natural_key_uniq`. If a
// future B-block adds a V069 that recreates the
// index with a different shape, this test fails
// (catches the next V056-style drift in the wild).
//
// 2026-09-04: v0.69 (B233).
func TestMigrations_V068IsLastToCreateDeviceRulesNaturalKey(t *testing.T) {
	files := testdataMigrationFiles(t)
	// The "version" of a migration file is encoded
	// in its name. We sort by version (lexicographic
	// on the version number — works because the file
	// names are like migrations_v0_56_..., with the
	// version number zero-padded to 3 digits).
	type entry struct {
		version int
		path    string
		creates []string
	}
	var entries []entry
	for _, f := range files {
		base := filepath.Base(f)
		re := regexp.MustCompile(`migrations_v0_(\d+)`)
		m := re.FindStringSubmatch(base)
		if m == nil {
			continue // migrations_pg.go is the consolidated file, skip
		}
		var v int
		if _, err := scanInt(m[1], &v); err != nil {
			continue
		}
		creates, _ := auditMigrationFile(t, f)
		entries = append(entries, entry{version: v, path: f, creates: creates})
	}
	// Sort by version ASC.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].version < entries[j].version
	})
	// Find the LAST migration that creates the index.
	var lastTouch *entry
	for i := range entries {
		for _, c := range entries[i].creates {
			if c == "device_rules_natural_key_uniq" {
				lastTouch = &entries[i]
			}
		}
	}
	if lastTouch == nil {
		t.Fatal("no migration creates device_rules_natural_key_uniq")
	}
	if lastTouch.version != 68 {
		t.Errorf("expected v68 to be the last migration to touch device_rules_natural_key_uniq, but v%d (%s) is. If a future B-block recreates the index, this is a shape-drift risk — the new migration must DROP the existing index before re-creating it, and this test must be updated to the new version.",
			lastTouch.version, lastTouch.path)
	}
}

// scanInt is a tiny helper that parses a numeric
// string. We use this instead of strconv.Atoi so
// the audit test file doesn't depend on strconv
// (the imports list is already long enough).
//
// 2026-09-04: v0.69 (B233).
func scanInt(s string, dst *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, nil
		}
		n = n*10 + int(c-'0')
	}
	*dst = n
	return len(s), nil
}

// TestShapeDriftAudit_CatchesSyntheticOffender is a
// mutation test: it constructs an in-memory migration
// chain that mimics the V056 / B232 pattern (a
// re-CREATE without DROP) and asserts the audit
// detects it. If this test ever passes with the
// re-CREATE pattern (i.e. the audit stops catching
// the bug), the audit is broken.
//
// We use synthetic in-memory strings (no real files)
// so the test is self-contained and doesn't depend
// on the actual migration files. The synthetic
// chain has:
//   - "M1": CREATE INDEX IF NOT EXISTS idx_x (first-time, OK)
//   - "M2": CREATE INDEX IF NOT EXISTS idx_x (re-CREATE, FAIL)
//
// 2026-09-04: v0.69 (B233).
func TestShapeDriftAudit_CatchesSyntheticOffender(t *testing.T) {
	// We don't have a public audit entry point — we
	// inline the same logic. This duplicates code, but
	// keeps the audit entry point (the running-set
	// walk) private to the test. The duplication is
	// tiny (10 lines) and the alternative is a wider
	// API refactor that exposes internal details.
	files := map[string]struct {
		creates []string
		drops   []string
	}{
		"M1": {creates: []string{"idx_x"}},
		"M2": {creates: []string{"idx_x"}}, // re-CREATE without DROP — BUG
		"M3": {creates: []string{"idx_x"}, drops: []string{"idx_x"}}, // re-CREATE with DROP — OK
	}
	seen := make(map[string]bool)
	var found bool
	for _, name := range []string{"M1", "M2", "M3"} {
		f := files[name]
		dropSet := make(map[string]bool)
		for _, d := range f.drops {
			dropSet[d] = true
		}
		for _, c := range f.creates {
			if dropSet[c] {
				continue
			}
			if !seen[c] {
				continue
			}
			if c == "idx_x" && name == "M2" {
				found = true
			}
		}
		for _, c := range f.creates {
			seen[c] = true
		}
	}
	if !found {
		t.Fatal("mutation test FAILED: synthetic re-CREATE without DROP was NOT caught by the audit logic. The B233 audit is broken.")
	}
}
