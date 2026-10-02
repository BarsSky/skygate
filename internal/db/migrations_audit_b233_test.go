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
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// migrationFilesForChain returns the files that make up ONE
// migration chain: a file belongs to the chain exactly when it
// defines at least one `migrateV<NNN><suffix>` function.
//
// Deriving the chain from the code instead of from a filename
// convention is what closes TD-25. The pre-B333 audit skipped
// `migrations_sqlite.go` because its CREATE INDEX patterns mirror
// the PG source, and mixing the two chains into one walk made every
// index look like it was created twice. Walking each chain
// separately removes the false positive without leaving the SQLite
// chain unaudited — and a file such as
// `migrations_v0_77_exit_location.go`, which defines BOTH
// `migrateV077PG` and `migrateV077SQLite`, correctly appears in
// both chains.
//
// 2026-10-01: v0.78 (B333).
func migrationFilesForChain(t *testing.T, suffix string) []string {
	t.Helper()
	matches, err := filepath.Glob("migrations*.go")
	if err != nil {
		t.Fatalf("glob migrations*.go: %v", err)
	}
	// A migration is production code, so a _test.go file can never
	// define one. Excluding them also keeps the example SQL strings
	// inside the test sources out of the audit.
	defines := regexp.MustCompile(`func\s+migrateV[0-9]+` + suffix + `\s*\(`)
	var out []string
	for _, m := range matches {
		base := filepath.Base(m)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		if defines.Match(b) {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// auditMigrationFile extracts the (creates, drops)
// lists from a single migration file. We look for
// SQL fragments in the form:
//   - `CREATE [UNIQUE] INDEX IF NOT EXISTS <name> ...`
//   - `DROP INDEX IF EXISTS <name>`
//
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

// shapeDriftWhitelist is the set of (chain, file, index_name)
// triples where CREATE INDEX IF NOT EXISTS without
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
// The key starts with the chain suffix ("PG" / "SQLite")
// because the two chains are audited separately since B333.
//
// 2026-09-04: v0.69 (B233).
// 2026-10-01: v0.78 (B333) — chain-qualified key (TD-25).
var shapeDriftWhitelist = map[string]bool{
	// V056 (B188.2 era) is the original offender. The
	// shape-drift bug it caused is fixed by V068
	// (B232) — the audit framework's purpose is to
	// prevent FUTURE instances of the same class.
	"PG:migrations_pg.go::device_rules_natural_key_uniq": true,
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
// The test walks EACH migration chain on its own, in version
// order, keeping a running set of "indexes I've seen so far". When
// it encounters a CREATE INDEX IF NOT EXISTS for an
// already-seen name, it asserts there's a paired
// DROP INDEX IF EXISTS in the same migration. The
// first CREATE for any name is exempt (no prior shape
// to drift from).
//
// Auditing both chains separately is the TD-25 fix: the SQLite
// chain used to be skipped entirely, so a B232-class shape drift
// introduced on the SQLite side would have been invisible.
//
// Whitelist: V056 (B188.2 era) is the historical
// offender. The shape-drift bug it caused is fixed
// by V068 (B232). The audit framework's purpose is
// to prevent FUTURE instances of the same class;
// V056 is a one-time ack.
//
// 2026-09-04: v0.69 (B233).
// 2026-10-01: v0.78 (B333) — both chains (TD-25).
func TestMigrations_ShapeDriftAudit(t *testing.T) {
	for _, suffix := range []string{"PG", "SQLite"} {
		files := migrationFilesForChain(t, suffix)
		if len(files) == 0 {
			t.Fatalf("%s: no migration files found — something is wrong with the test setup", suffix)
		}
		auditShapeDrift(t, suffix, files)
	}
}

// shapeDriftOffenders returns the index names in ONE migration that
// are re-created without a paired DROP, given the set of names the
// earlier migrations in the same chain already created.
//
// It is a pure function so the mutation test below can prove the
// audit still catches the B232 pattern it exists for — a contract
// that cannot fail is not a contract. The whitelist is applied by
// the caller, so the mutation test exercises the same decision path
// the real chains go through.
//
// 2026-10-01: v0.78 (B333).
func shapeDriftOffenders(seen map[string]bool, creates, drops []string) []string {
	dropSet := make(map[string]bool, len(drops))
	for _, d := range drops {
		dropSet[d] = true
	}
	var out []string
	for _, c := range creates {
		// An explicit DROP + CREATE inside the same migration is the
		// sanctioned repair (the V068 pattern), so it is never drift.
		if dropSet[c] {
			continue
		}
		// A first-time CREATE has no prior shape to drift from.
		if !seen[c] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// auditShapeDrift walks ONE chain's files in version order and
// reports every re-CREATE that has no paired DROP.
//
// 2026-10-01: v0.78 (B333).
func auditShapeDrift(t *testing.T, suffix string, files []string) {
	t.Helper()
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
		for _, c := range shapeDriftOffenders(seen, creates, drops) {
			// Re-CREATE without paired DROP — this is
			// the V056 / B232 pattern. Check the
			// whitelist before failing.
			key := suffix + ":" + filepath.Base(fv.path) + "::" + c
			if shapeDriftWhitelist[key] {
				continue
			}
			t.Errorf("shape-drift risk [%s chain]: %s has CREATE INDEX IF NOT EXISTS %q without a paired DROP INDEX IF EXISTS, but %q was already created in an earlier migration. This is the B232 pattern: the CREATE is a silent no-op on a DB where the prior index exists with a different shape, breaking any code that depends on the new shape (e.g. ON CONFLICT clauses). Add an explicit `DROP INDEX IF EXISTS %s` before the CREATE, or add (chain, file, index) to shapeDriftWhitelist with a one-time ack comment.",
				suffix, fv.path, c, c, c)
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
// Since B333 the check runs against BOTH chains: the SQLite port
// must end on the same 6-column shape, otherwise a SQLite install
// reaches the same B232 failure the PG fix closed.
//
// 2026-09-04: v0.69 (B233).
// 2026-10-01: v0.78 (B333) — both chains (TD-25).
func TestMigrations_DeviceRulesNaturalKeyIndexIsSixColumns(t *testing.T) {
	for _, suffix := range []string{"PG", "SQLite"} {
		files := migrationFilesForChain(t, suffix)
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
			t.Errorf("%s chain: no CREATE statement found for device_rules_natural_key_uniq across the entire migration chain", suffix)
			continue
		}
		if !lastCreateIs6Col {
			t.Errorf("FINAL CREATE for device_rules_natural_key_uniq in the %s chain (%s) is not 6-col (missing parent_domain). Last columns: %q. This is the B232 bug: qInsertDeviceRule uses 6-col ON CONFLICT and fails when the index is 5-col. V068 is the fix — verify the V068 migration is the LATEST CREATE in the chain.",
				suffix, lastCreateFile, lastCreate)
		}
		t.Logf("FINAL CREATE for device_rules_natural_key_uniq in the %s chain (%s) is 6-col (with parent_domain): %s", suffix, lastCreateFile, lastCreate)
	}
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
// 2026-10-01: v0.78 (B333) — both chains (TD-25).
func TestMigrations_V068IsLastToCreateDeviceRulesNaturalKey(t *testing.T) {
	// --- the shared half: V068's own body must DROP before it CREATEs ---
	//
	// This works identically on both chains, and it is the only
	// version pin that CAN work on the SQLite side: the legacy
	// SQLite ports (V025–V070) were generated into the single
	// migrations_sqlite.go, so a filename no longer encodes the
	// version there. The registry does (B333 pins it), so we locate
	// each chain's migrateV068<Dialect> through the registry and
	// inspect that function's body.
	for _, suffix := range []string{"PG", "SQLite"} {
		body := migrationFuncBody(t, suffix, 68)
		if body == "" {
			t.Errorf("%s chain: could not extract the body of migrateV068%s — the B232 repair must exist on both backends", suffix, suffix)
			continue
		}
		createRe := regexp.MustCompile("`[^`]*?CREATE\\s+(UNIQUE\\s+)?INDEX\\s+(IF\\s+NOT\\s+EXISTS\\s+)?device_rules_natural_key_uniq")
		dropRe := regexp.MustCompile("`[^`]*?DROP\\s+INDEX\\s+IF\\s+EXISTS\\s+device_rules_natural_key_uniq")
		createAt := createRe.FindStringIndex(body)
		dropAt := dropRe.FindStringIndex(body)
		if createAt == nil {
			t.Errorf("%s chain: migrateV068%s does not CREATE device_rules_natural_key_uniq — the B232 repair is missing on this backend", suffix, suffix)
			continue
		}
		if dropAt == nil {
			t.Errorf("%s chain: migrateV068%s CREATEs device_rules_natural_key_uniq without a paired DROP INDEX IF EXISTS — on a DB where the pre-V056 5-column index exists, the CREATE is a silent no-op and the ON CONFLICT clause in qInsertDeviceRule stops matching (the B232 outage)", suffix, suffix)
			continue
		}
		if dropAt[0] > createAt[0] {
			t.Errorf("%s chain: migrateV068%s runs its CREATE before its DROP, so the DROP removes the index the CREATE just wrote — the order in the source is the order at runtime", suffix, suffix)
		}
	}

	// --- the PG-only half: V068 must be the LAST version that touches it ---
	//
	// Only the PostgreSQL chain keeps one file per version, so only
	// there can a filename pin the version. On the SQLite side the
	// equivalent guarantee is carried by
	// TestMigrations_ShapeDriftAudit, which flags any later
	// re-CREATE without a DROP on that chain too.
	files := migrationFilesForChain(t, "PG")
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
		t.Fatal("PG chain: no migration creates device_rules_natural_key_uniq")
	}
	if lastTouch.version != 68 {
		t.Errorf("PG chain: expected v68 to be the last migration to touch device_rules_natural_key_uniq, but v%d (%s) is. If a future B-block recreates the index, this is a shape-drift risk — the new migration must DROP the existing index before re-creating it, and this test must be updated to the new version.",
			lastTouch.version, lastTouch.path)
	}
}

// migrationFuncBody returns the source of the `migrateV<version><suffix>`
// function belonging to the given chain, from its `func …` line up to
// the next top-level `func`.
//
// The defining file comes from the registry entry itself (which
// TestMigrations_SourceFileNamesTheDefiningFile pins), so this works
// for the generated SQLite ports too, where several versions share
// one file. Comments above the function — which freely quote the SQL
// — are excluded, so the callers only match real statements.
//
// 2026-10-01: v0.78 (B333).
func migrationFuncBody(t *testing.T, suffix string, version int) string {
	t.Helper()
	reg := PGMigrations()
	if suffix == "SQLite" {
		reg = SQLiteMigrations()
	}
	for _, e := range reg {
		if e.Version != version {
			continue
		}
		fn := runtime.FuncForPC(reflect.ValueOf(e.Run).Pointer())
		if fn == nil {
			return ""
		}
		full := fn.Name()
		short := full
		if i := strings.LastIndex(full, "."); i >= 0 {
			short = full[i+1:]
		}
		file, _ := fn.FileLine(fn.Entry())
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(b)
		start := strings.Index(src, "\nfunc "+short+"(")
		if start < 0 {
			return ""
		}
		rest := src[start+1:]
		if end := strings.Index(rest[1:], "\nfunc "); end >= 0 {
			return rest[:end+1]
		}
		return rest
	}
	return ""
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
// It now drives the SAME pure decision function the real chain walk
// uses (shapeDriftOffenders), instead of re-implementing the logic
// next to it — an inlined copy proves nothing about the code that
// actually runs. The synthetic chain has:
//   - "M1": CREATE INDEX IF NOT EXISTS idx_x (first-time, OK)
//   - "M2": CREATE INDEX IF NOT EXISTS idx_x (re-CREATE, FAIL)
//   - "M3": CREATE INDEX IF NOT EXISTS idx_x + DROP idx_x (repair, OK)
//
// 2026-09-04: v0.69 (B233).
// 2026-10-01: v0.78 (B333) — drives the production decision function.
func TestShapeDriftAudit_CatchesSyntheticOffender(t *testing.T) {
	chain := []struct {
		name    string
		creates []string
		drops   []string
	}{
		{name: "M1", creates: []string{"idx_x"}},
		{name: "M2", creates: []string{"idx_x"}},                           // re-CREATE without DROP — BUG
		{name: "M3", creates: []string{"idx_x"}, drops: []string{"idx_x"}}, // re-CREATE with DROP — OK
	}
	seen := make(map[string]bool)
	var caught []string
	for _, m := range chain {
		for _, c := range shapeDriftOffenders(seen, m.creates, m.drops) {
			caught = append(caught, m.name+":"+c)
		}
		for _, c := range m.creates {
			seen[c] = true
		}
	}
	if len(caught) != 1 || caught[0] != "M2:idx_x" {
		t.Fatalf("mutation test FAILED: expected the audit to flag exactly [M2:idx_x] (the re-CREATE without DROP) and to accept the first-time CREATE and the DROP+CREATE repair, got %v. The B233 audit is broken.", caught)
	}
}
