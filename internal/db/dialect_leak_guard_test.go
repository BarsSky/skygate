// internal/db/dialect_leak_guard_test.go — static guard against
// re-introducing dialect-specific SQL into shared code.
//
// WHY
// ---
// skygate carries two SQL backends behind ONE set of Go queries. A
// fragment that only one backend understands is therefore a latent
// runtime failure, not a style issue — and because most callers swallow
// the error (or log it and carry on), the symptom is usually "the list
// is empty" or "the gauge reads 0", never a crash:
//
//   - `EXTRACT(EPOCH FROM now())::bigint` (PostgreSQL-only) spliced into
//     shared write paths → "near FROM: syntax error" on SQLite, and
//     every timestamped write failed (fixed in v1.5.5 by making
//     nowUnixSQL a real branch);
//   - `?` placeholders (SQLite flavour) → SQLSTATE 42601 on PostgreSQL;
//   - `INSERT … VALUES (…, now())` against the INTEGER epoch columns this
//     schema uses → "column created_at is of type integer but expression
//     is of type timestamp with time zone" on PostgreSQL AND "no such
//     function: now" on SQLite, i.e. broken on BOTH backends (B420);
//   - `EXTRACT(EPOCH FROM created_at)::bigint` on a column that is
//     ALREADY an integer epoch → a type error on PostgreSQL and a syntax
//     error on SQLite, so /admin/certificates and /admin/deploy rendered
//     an empty event list on every backend (B420).
//
// The original (2026-09-18) version of this guard only inspected lines
// that literally contained a backtick, and only two hardcoded
// expressions. Both limits mattered: a multi-line backtick literal —
// which is how this repo formats every SQL statement — carries a
// backtick only on its FIRST and LAST line, so any dialect token in
// between was invisible, and the guard never looked at double-quoted
// SQL at all (that is how `EXTRACT(epoch FROM now())::bigint`, a token
// the old guard already listed, survived inside writeGlobalSetting).
// B420 widened the scan to whole string literals (with a small Go lexer
// that also strips comments), widened the token set to both directions,
// and required every file that interpolates one-dialect SQL to also
// branch on the dialect.
//
// WHAT SQLITE ACTUALLY REJECTS (measured, see
// dialect_type_name_b420_test.go)
//
//	SQLite accepts ARBITRARY type names: `TEXT[]`, `SERIAL PRIMARY KEY`,
//	`jsonb` and `timestamptz` all CREATE fine. So a PG type name is not
//	automatically a deployment failure — it is a DEFECT, and the
//	measured severity decides whether it belongs in this list:
//
//	- `TEXT[]` is NOT listed: SQLite stores it with TEXT affinity and a
//	  JSON array round-trips unchanged, so it cannot break a deployment.
//	- `SERIAL PRIMARY KEY` IS listed: SQLite parses it as an unknown
//	  type, does not auto-assign, and an INSERT then leaves id = NULL —
//	  silently broken rows instead of an error.
//
// DELIBERATE EXEMPTION — strftime()
//
//	Shared query files DO call strftime() directly (queries.go,
//	secrets.go, telegram_login_tokens.go). That is not a leak in this
//	direction: PostgreSQL tolerates it because migrations_pg.go installs
//	a strftime(format text, ts text) compatibility function (pinned by
//	TestPGStrftimeCompatFunctionStillInstalled below). The asymmetry is
//	real and documented in active_dialect.go: SQLite has no EXTRACT()
//	and no ::bigint, so the reverse direction is a hard error — which is
//	why the PG-only set is enforced strictly while the SQLite-only set
//	lists only tokens PostgreSQL has NO compat shim for (PRAGMA, INSERT
//	OR IGNORE, IFNULL, …). Removing the last direct strftime() call site
//	is a separate, tracked refactor; it must not be smuggled into a
//	block that has no behavioural test for it.
package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// dialectToken is one forbidden SQL fragment plus the fix to reach for.
type dialectToken struct {
	token string
	why   string
}

// pgOnlySQL is SQL that PostgreSQL accepts and SQLite cannot run.
var pgOnlySQL = []dialectToken{
	{"EXTRACT(", "PostgreSQL-only function — SQLite has no EXTRACT(); use db.NowUnixSQL() (or select the epoch column directly)"},
	{"ILIKE", "PostgreSQL-only operator — SQLite needs LOWER(col) LIKE LOWER(?)"},
	{"pg_is_in_recovery", "PostgreSQL-only function — SQLite has no replication role; branch on the dialect (see feature/healthz/db_health.go)"},
	{"pg_current_wal_lsn", "PostgreSQL-only function — SQLite has no WAL LSN; branch on the dialect"},
	{"pg_stat_", "PostgreSQL-only catalog — SQLite has no pg_stat_* views"},
	{"pg_database_size", "PostgreSQL-only function — SQLite sizes a DB via PRAGMA page_count × page_size"},
	{"current_database()", "PostgreSQL-only function — SQLite has no concept of a current database name"},
	{"jsonb", "PostgreSQL-only type and cast — SQLite accepts the type NAME but has no jsonb functions (measured, see dialect_type_name_b420_test.go)"},
	{"DISTINCT ON", "PostgreSQL-only syntax — SQLite needs a window function or a GROUP BY"},
	{"array_agg(", "PostgreSQL-only aggregate — SQLite has group_concat()"},
	{"unnest(", "PostgreSQL-only function — SQLite has no table-valued unnest()"},
	{"to_char(", "PostgreSQL-only function — SQLite formats with strftime()"},
	{"to_timestamp(", "PostgreSQL-only function — SQLite has no to_timestamp()"},
	{"now()", "PostgreSQL-only function returning a timestamp — against the INTEGER epoch columns in this schema it fails on BOTH backends; use db.NowUnixSQL()"},
	{"SERIAL PRIMARY KEY", "PostgreSQL-only column type (covers BIGSERIAL) — SQLite parses it as an unknown type and leaves the id NULL instead of auto-assigning"},
	{"ANY($", "pgx array binding — SQLite cannot bind an array; expand the list inline or use a join"},
}

// pgCast is the `expr::type` cast form, forbidden wherever it appears in
// shared SQL (SQLite has no `::` cast operator at all).
var pgCast = regexp.MustCompile(`[A-Za-z0-9_)\]]::(bigint|int|integer|smallint|text|varchar|numeric|boolean|bool|timestamptz|timestamp|date|jsonb|uuid)\b`)

// sqliteOnlySQL is SQL that SQLite accepts and PostgreSQL REJECTS. Only
// tokens with no compat shim belong here — strftime() is installed on PG
// on purpose (see the package comment).
var sqliteOnlySQL = []dialectToken{
	{"INSERT OR IGNORE", "SQLite-only upsert — PostgreSQL needs INSERT … ON CONFLICT DO NOTHING"},
	{"INSERT OR REPLACE", "SQLite-only upsert — PostgreSQL needs INSERT … ON CONFLICT DO UPDATE"},
	{"REPLACE INTO", "SQLite-only upsert — PostgreSQL needs INSERT … ON CONFLICT DO UPDATE"},
	{"PRAGMA ", "SQLite-only statement — PostgreSQL needs a catalog query"},
	{"AUTOINCREMENT", "SQLite-only column suffix — PostgreSQL uses SERIAL/BIGSERIAL or a sequence"},
	{"sqlite_master", "SQLite-only catalog — PostgreSQL uses information_schema/pg_catalog"},
	{"sqlite_sequence", "SQLite-only catalog"},
	{"sqlite_version(", "SQLite-only function — PostgreSQL reports version()"},
	{"IFNULL(", "SQLite-only function — PostgreSQL spells it COALESCE()"},
	{"julianday(", "SQLite-only function — PostgreSQL has no julianday()"},
	{"datetime(", "SQLite-only function — PostgreSQL has no datetime()"},
	{"last_insert_rowid()", "SQLite-only function — PostgreSQL needs INSERT … RETURNING"},
	{"WITHOUT ROWID", "SQLite-only table option"},
}

// dialectAwareMarkers: a file that interpolates one-dialect SQL is only
// allowed to do so when it ALSO asks which dialect the process is
// talking to. The markers are the sanctioned helpers (db.ActiveDialect,
// db.DetectDSN, db.BackendOf, config.Dialect, …) — the guard does not
// care which one, only that the file branches instead of assuming.
var dialectAwareMarkers = []string{
	"ActiveDialect(",
	"DialectSQLite",
	"DialectPostgres",
	"BackendOf(",
	"IsSQLite(",
	"IsPostgres(",
	"DetectDSN(",
	"dialectKind",
	"currentDialect(",
	".Dialect",
}

// dialectPostgresOnlyMarker must appear in the header of every file
// pinned in postgresOnlyFiles. It is the file's own statement that none
// of its SQL can ever run against SQLite.
const dialectPostgresOnlyMarker = "// dialect:postgres-only"

// postgresOnlyFiles pins the files whose ENTIRE subject is PostgreSQL:
// the SQLite→PG migration tool (pg_dump/pg_restore/pg_terminate_backend)
// and the B215 live-verify helper. They may carry PG-only SQL because
// they can never run against SQLite. The set is PINNED here, with the
// reason, so adding an exemption is a deliberate reviewed edit rather
// than a blanket directory skip — and a marker without a pin, or a pin
// whose file lost its marker, fails TestPostgresOnlyExemptionsArePinned.
var postgresOnlyFiles = map[string]string{
	"internal/dbmigrate/steps/cleanup.go": "drops/terminates the SOURCE database with pg_terminate_backend + DROP DATABASE; the dbmigrate tool only ever targets PostgreSQL",
	"internal/dbmigrate/steps/restore.go": "runs pg_restore and terminates target sessions via pg_stat_activity; PostgreSQL only by construction",
	"scripts/b215_liveverify.go":          "//go:build ignore live-verify helper for the B215 audit emissions; it takes a SKYGATE_TEST_PG_DSN and only speaks PostgreSQL",
}

// dialectLeakAllowed lists the files that ARE the dialect branch — the
// helpers whose whole job is to emit one backend's spelling — plus this
// guard (it documents the tokens it forbids). Migration chains are not
// listed here: they have their own, stricter per-function rule in
// TestMigrationChainsArePerDialectClean below.
func dialectLeakAllowed(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, ok := range []string{
		"internal/db/now_unix.go",
		"internal/db/now_unix_postgres.go",
		"internal/db/active_dialect.go",
		"internal/db/dialect.go",
		"internal/db/dialect_runtime_test.go",
		"internal/db/dialect_leak_guard_test.go",
		"internal/db/sqlite_",
		"internal/db/placeholders",
		"internal/db/on_conflict",
	} {
		if strings.HasPrefix(rel, ok) {
			return true
		}
	}
	return false
}

// sqlSegment is one Go string literal (backtick or double-quoted) with
// the 1-based line its opening quote sits on.
type sqlSegment struct {
	line int
	text string
}

// extractStringLiterals walks Go source and returns the CONTENT of every
// string literal, ignoring comments and rune literals. Whole literals —
// not lines — because a multi-line backtick literal only has quotes on
// its first and last line, and the pre-B420 guard therefore missed every
// token in between (that is how the 2026-09-18 leaks survived a guard
// aimed at exactly them).
func extractStringLiterals(src string) []sqlSegment {
	var out []sqlSegment
	line := 1
	segStart := 0
	var b strings.Builder
	state := byte(0) // 0 normal, 'b' backtick, 'd' double-quoted
	for i := 0; i < len(src); i++ {
		c := src[i]
		if c == '\n' {
			line++
		}
		switch state {
		case 'b':
			if c == '`' {
				out = append(out, sqlSegment{line: segStart, text: b.String()})
				b.Reset()
				state = 0
				continue
			}
			b.WriteByte(c)
		case 'd':
			if c == '\\' && i+1 < len(src) {
				b.WriteByte(c)
				i++
				if src[i] == '\n' {
					line++
				}
				b.WriteByte(src[i])
				continue
			}
			if c == '"' {
				out = append(out, sqlSegment{line: segStart, text: b.String()})
				b.Reset()
				state = 0
				continue
			}
			b.WriteByte(c)
		default:
			switch {
			case c == '`':
				state = 'b'
				segStart = line
			case c == '"':
				state = 'd'
				segStart = line
			case c == '\'':
				// rune literal: skip to the closing quote
				i++
				for i < len(src) && src[i] != '\'' {
					if src[i] == '\\' {
						i++
					}
					if i < len(src) && src[i] == '\n' {
						line++
					}
					i++
				}
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				for i < len(src) && src[i] != '\n' {
					i++
				}
				if i < len(src) {
					line++
				}
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				i += 2
				for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
					if src[i] == '\n' {
						line++
					}
					i++
				}
				i++
			}
		}
	}
	return out
}

// sqlStarters / sqlStatements decide whether a Go literal is SQL at all.
// Without this gate the scan also reports PROSE that happens to mention a
// function name — metrics/collector.go's Prometheus help string "Current
// pg_database_size in bytes." is the measured example. The gate is a
// deliberate heuristic: a dialect token inside a non-SQL literal cannot
// reach a database, so it is not a leak.
var (
	sqlStarters   = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER", "DROP", "PRAGMA", "WITH", "REPLACE", "VALUES", "BEGIN", "COMMIT", "GRANT", "EXPLAIN", "VACUUM", "TRUNCATE", "SAVEPOINT", "ANALYZE", "CLUSTER", "SET"}
	sqlStatements = []string{" WHERE ", " VALUES (", " FROM ", " SET ", " INTO ", " JOIN "}
)

// stripSQLComments removes SQL comments from a literal's text: prose
// inside `-- …` or /* … */ is documentation, not a statement, and a
// migration chain documents the SQLite spelling it replaced exactly like
// that.
func stripSQLComments(text string) string {
	if !strings.Contains(text, "--") && !strings.Contains(text, "/*") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '-' && i+1 < len(text) && text[i+1] == '-' {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
			continue
		}
		if text[i] == '/' && i+1 < len(text) && text[i+1] == '*' {
			i += 2
			for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
				i++
			}
			i++
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

func looksLikeSQL(text string) bool {
	up := strings.ToUpper(strings.TrimSpace(stripSQLComments(text)))
	if up == "" {
		return false
	}
	for _, s := range sqlStarters {
		if strings.HasPrefix(up, s) {
			return true
		}
	}
	for _, m := range sqlStatements {
		if strings.Contains(up, m) {
			return true
		}
	}
	return false
}

// leakViolation is one finding, in the shape the test prints.
type leakViolation struct {
	file  string
	line  int
	token string
	why   string
}

func (v leakViolation) String() string {
	return filepath.ToSlash(v.file) + ":" + strconv.Itoa(v.line) + " contains " + v.token + " — " + v.why
}

// scanDialectLeaks is the detector itself, factored out so
// TestDialectLeakGuardCatchesAPlantedLeak can prove it fires on a
// planted violation and stays quiet for a dialect-aware file. A guard
// that cannot be shown to fail is not a guard.
func scanDialectLeaks(root string) (violations []leakViolation, scanned, skippedMigrations int, err error) {
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return nil // unreadable entries are not this test's problem
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", ".trash", "tmp", "vendor":
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
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/db/migrations_") {
			// Covered (more strictly) by
			// TestMigrationChainsArePerDialectClean: every literal
			// there is classified by the chain function that owns it.
			skippedMigrations++
			return nil
		}
		if dialectLeakAllowed(rel) {
			return nil
		}
		if _, ok := postgresOnlyFiles[rel]; ok {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		src := string(data)
		scanned++
		aware := isDialectAware(src)
		for _, seg := range extractStringLiterals(src) {
			if aware || !looksLikeSQL(seg.text) {
				continue
			}
			sqlText := stripSQLComments(seg.text)
			for _, tok := range pgOnlySQL {
				if strings.Contains(sqlText, tok.token) {
					violations = append(violations, leakViolation{rel, seg.line, tok.token, tok.why +
						" (file never asks which dialect is live — branch on db.ActiveDialect())"})
					break
				}
			}
			if m := pgCast.FindString(sqlText); m != "" {
				violations = append(violations, leakViolation{rel, seg.line, m,
					"PostgreSQL-only `::` cast — SQLite has no cast operator; use a dialect helper"})
			}
			for _, tok := range sqliteOnlySQL {
				if strings.Contains(sqlText, tok.token) {
					violations = append(violations, leakViolation{rel, seg.line, tok.token, tok.why +
						" (file never asks which dialect is live — branch on db.ActiveDialect())"})
					break
				}
			}
		}
		return nil
	})
	return violations, scanned, skippedMigrations, walkErr
}

func isDialectAware(src string) bool {
	for _, m := range dialectAwareMarkers {
		if strings.Contains(src, m) {
			return true
		}
	}
	return false
}

// TestNoDialectLeaksInSharedCode is the build-breaking contract: no
// shared (non-migration) Go file may carry a one-backend SQL fragment
// unless it also branches on the dialect.
func TestNoDialectLeaksInSharedCode(t *testing.T) {
	root := filepath.Join("..", "..")
	violations, scanned, skipped, err := scanDialectLeaks(root)
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if scanned == 0 {
		t.Fatal("scanned 0 files — the walk root is wrong, so this guard is not actually checking anything")
	}
	if skipped == 0 {
		t.Fatal("skipped 0 migration files — the migration exemption no longer matches the files it claims to exempt; " +
			"either the naming changed or this guard is silently checking nothing")
	}
	for _, v := range violations {
		t.Errorf("DIALECT LEAK: %s", v.String())
	}
	t.Logf("scanned %d shared .go files (%d migration files are covered by TestMigrationChainsArePerDialectClean); violations=%d",
		scanned, skipped, len(violations))
}

// TestPostgresOnlyExemptionsArePinned keeps the one safe escape hatch
// honest: every pinned file must exist AND carry the marker comment in
// its header, so a reader of the file learns immediately that its SQL
// never meets SQLite, and a stale pin (file deleted or renamed) fails
// instead of silently exempting a name that no longer exists.
func TestPostgresOnlyExemptionsArePinned(t *testing.T) {
	root := filepath.Join("..", "..")
	for rel, reason := range postgresOnlyFiles {
		if reason == "" {
			t.Errorf("%s is pinned without a reason", rel)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("pinned PostgreSQL-only file %s is unreadable: %v (stale exemption)", rel, err)
			continue
		}
		lines := strings.SplitN(string(data), "\n", 60)
		found := false
		for _, ln := range lines {
			if strings.Contains(ln, dialectPostgresOnlyMarker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is exempted from the dialect-leak guard but does not carry %q in its header — "+
				"add the marker (and the reason) or remove the pin", rel, dialectPostgresOnlyMarker)
		}
	}
}

// TestDialectLeakGuardCatchesAPlantedLeak proves the detector fires on a
// planted violation AND stays quiet for the same token in a file that
// branches on the dialect. Without the first half, a future refactor of
// extractStringLiterals or looksLikeSQL could turn the whole guard into a
// no-op and the suite would stay green (that failure mode is AGENTS.md
// trap #16: a filter that matches nothing reporting a pass).
func TestDialectLeakGuardCatchesAPlantedLeak(t *testing.T) {
	dir := t.TempDir()
	plant := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Multi-line literal on purpose: the now() token sits on the third
	// line, where a line-based scanner (the pre-B420 shape) never looked.
	plant("shared.go", "package x\n\nvar q = `INSERT INTO audit_log\n\t(user_id, created_at)\nVALUES (0, now())`\n\nvar c = \"SELECT created_at::bigint FROM audit_log\"\n")
	plant("aware.go", "package x\n\nvar q = `INSERT INTO audit_log (user_id, created_at)\nVALUES (0, now())`\n\nfunc d() bool { return db.ActiveDialect() == db.DialectSQLite }\n")
	// Prose that mentions a PG function is not SQL and must stay quiet —
	// this is the measured false positive from metrics/collector.go.
	plant("prose.go", "package x\n\nvar help = \"Current pg_database_size in bytes.\"\n")

	violations, scanned, _, err := scanDialectLeaks(dir)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scanned != 3 {
		t.Fatalf("scanned %d files, want 3", scanned)
	}
	var shared, aware, prose int
	for _, v := range violations {
		switch filepath.Base(v.file) {
		case "shared.go":
			shared++
		case "aware.go":
			aware++
		case "prose.go":
			prose++
		}
	}
	if shared < 2 {
		t.Fatalf("planted leaks: got %d violations in shared.go, want >= 2 "+
			"(now() sits on a continuation line of a multi-line literal; ::bigint sits in a double-quoted literal)", shared)
	}
	if aware != 0 {
		t.Fatalf("dialect-aware plant reported %d violations, want 0", aware)
	}
	if prose != 0 {
		t.Fatalf("prose plant reported %d violations, want 0 (a Prometheus help string is not SQL)", prose)
	}
}

// TestMigrationChainsArePerDialectClean enforces the migration rule per
// FUNCTION instead of exempting the whole chain: a literal that lives in
// migrateV<NNN>PG must carry no SQLite-only token, and one in
// migrateV<NNN>SQLite no PostgreSQL-only token. The old blanket skip
// (dialectLeakAllowed started with "internal/db/migrations_") meant a
// wrong-dialect statement inside a per-version file was only caught by
// running that chain against a real backend — and the per-version files
// were never covered by TestMigrationsSQLite_NoPGExclusiveSyntax either,
// which only reads migrations_sqlite.go.
func TestMigrationChainsArePerDialectClean(t *testing.T) {
	files, err := filepath.Glob("migrations_*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	funcRe := regexp.MustCompile(`^func\s+([A-Za-z0-9_]+)\s*\(`)
	var checkedFiles, checkedLiterals, violations int
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		src := string(data)

		// The file-level default covers package-scope literals (the
		// base schema blocks) in the two full chains.
		fileDefault := ""
		switch {
		case strings.Contains(filepath.Base(f), "_pg"):
			fileDefault = "pg"
		case strings.Contains(filepath.Base(f), "_sqlite"):
			fileDefault = "sqlite"
		}

		// One pass: which function owns each line?
		lines := strings.Split(src, "\n")
		ownerAt := make([]string, len(lines)+2)
		owner := ""
		for i, ln := range lines {
			if m := funcRe.FindStringSubmatch(ln); m != nil {
				owner = m[1]
			}
			ownerAt[i+1] = owner
		}

		checkedFiles++
		for _, seg := range extractStringLiterals(src) {
			checkedLiterals++
			d := fileDefault
			ownerName := ""
			if seg.line >= 1 && seg.line < len(ownerAt) {
				ownerName = ownerAt[seg.line]
			}
			switch {
			case strings.HasSuffix(ownerName, "SQLite"):
				d = "sqlite"
			case strings.HasSuffix(ownerName, "PG"):
				d = "pg"
			case ownerName != "":
				d = "" // helper func in a mixed file: no chain claim to make
			}
			if d == "" {
				continue
			}
			sqlText := stripSQLComments(seg.text)
			if d == "sqlite" {
				for _, tok := range pgOnlySQL {
					if strings.Contains(sqlText, tok.token) {
						violations++
						t.Errorf("MIGRATION LEAK: %s:%d (func %s, SQLite chain) contains PostgreSQL-only %q",
							f, seg.line, ownerName, tok.token)
						break
					}
				}
				if m := pgCast.FindString(sqlText); m != "" {
					violations++
					t.Errorf("MIGRATION LEAK: %s:%d (func %s, SQLite chain) contains PostgreSQL-only cast %q",
						f, seg.line, ownerName, m)
				}
			}
			if d == "pg" {
				for _, tok := range sqliteOnlySQL {
					if strings.Contains(sqlText, tok.token) {
						violations++
						t.Errorf("MIGRATION LEAK: %s:%d (func %s, PostgreSQL chain) contains SQLite-only %q",
							f, seg.line, ownerName, tok.token)
						break
					}
				}
				// strftime is compat-installed on PG, but a migration is
				// not a place to lean on the shim: the chain must already
				// be native in both directions. The statements that
				// DEFINE or GRANT the shim are the exception — they are
				// the installation itself, not a use of it.
				if strings.Contains(sqlText, "strftime(") && !strings.Contains(sqlText, "FUNCTION strftime") {
					violations++
					t.Errorf("MIGRATION LEAK: %s:%d (func %s, PostgreSQL chain) calls strftime() — "+
						"migrations must not depend on the compatibility shim", f, seg.line, ownerName)
				}
			}
		}
	}
	if checkedFiles == 0 {
		t.Fatal("checked 0 migration files — glob root is wrong")
	}
	if checkedLiterals == 0 {
		t.Fatal("classified 0 SQL literals in the migration chains — the lexer or the function-owner detection is broken")
	}
	t.Logf("checked %d migration files, classified %d SQL literals by owning chain function; violations=%d",
		checkedFiles, checkedLiterals, violations)
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
