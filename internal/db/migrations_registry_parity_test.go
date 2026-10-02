package db

// migrations_registry_parity_test.go — v0.78 (B333) — the two
// migration registries must agree, and each entry must name the
// file that really defines it.
//
// Why this exists
// ---------------
// Skygate ships TWO migration chains (`pgMigrations` in
// driver_postgres.go, `sqliteMigrations` in driver_sqlite.go) and
// the whole premise of the dual-dialect data layer is that they are
// interchangeable: same version numbers, same `Name` labels, same
// `SourceFile` authorship — only the DDL inside `migrateVxxxPG` /
// `migrateVxxxSQLite` differs (see the header of driver_sqlite.go).
//
// Nothing enforced that premise. TD-17 measured the *result* (both
// chains reach V77 and produce identical tables/columns), but the
// *metadata* was only checked by hand, and two real drifts had
// accumulated undetected:
//
//   - V070's label read "v0.70 (B238)" in the PG registry and
//     "v0.70 (B236)" in the SQLite one. B236 is a different block
//     entirely (Tailscale ACL), so `skygate migrate status` and the
//     /admin/database audit page answered a different question
//     depending on which backend the host runs — the exact
//     "is this the same project on both DBs?" question the operator
//     asks before a cutover.
//   - V060, V061 and V062 pointed `SourceFile` at
//     `migrations_v0_60_b183_test.go`, `migrations_v0_61_b188_test.go`
//     and `migrations_v0_62_b194.go` — the first two in BOTH chains,
//     the third in the PG chain — while all three functions are
//     actually defined in `migrations_pg.go`. A `_test.go` source is
//     compiled only for tests, so the recorded authorship was wrong
//     in `applied_migrations.source_file` (the B213/B198 audit trail
//     the operator reads after a failed migration).
//
// The check is deliberately hermetic and needs no database: it uses
// `runtime.FuncForPC(...).FileLine(...)` on each registry entry's
// `Run` function, which reports the file the function was actually
// compiled from. That makes the contract impossible to satisfy by
// editing a label alone — the registry has to name the truth.
//
// 2026-10-01: v0.78 (B333).

import (
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// chainUnderAudit pairs a registry with the dialect suffix its
// migration functions carry (`migrateV025PG` vs `migrateV025SQLite`).
type chainUnderAudit struct {
	dialect string
	suffix  string
	entries []MigrationEntry
}

func chainsUnderAudit() []chainUnderAudit {
	return []chainUnderAudit{
		{dialect: "PostgreSQL", suffix: "PG", entries: PGMigrations()},
		{dialect: "SQLite", suffix: "SQLite", entries: SQLiteMigrations()},
	}
}

// chainDrift returns one human-readable line per difference between
// the two registries. It is a pure function so the mutation test
// below can prove it still detects the drift class it exists for.
//
// 2026-10-01: v0.78 (B333).
func chainDrift(pg, lite []MigrationEntry) []string {
	var out []string
	if len(pg) != len(lite) {
		out = append(out, fmt.Sprintf("the two migration chains have different lengths: pg=%d sqlite=%d — every migration must be registered in BOTH chains (AGENTS rule 9)", len(pg), len(lite)))
	}
	n := len(pg)
	if len(lite) < n {
		n = len(lite)
	}
	for i := 0; i < n; i++ {
		p, s := pg[i], lite[i]
		if p.Version != s.Version {
			out = append(out, fmt.Sprintf("entry %d: version %d (pg) vs %d (sqlite) — the chains must run in the same order; a version registered in only one chain means that schema change never reaches the other backend",
				i, p.Version, s.Version))
			continue
		}
		if p.Name != s.Name {
			out = append(out, fmt.Sprintf("v%d: the registry label drifted between chains\n  pg:     %q\n  sqlite: %q\nUpdate BOTH registries — `skygate migrate status` and /admin/database must describe the same migration regardless of the backend the host runs",
				p.Version, p.Name, s.Name))
		}
		if strings.TrimSpace(p.Name) == "" {
			out = append(out, fmt.Sprintf("v%d: the registry label is empty — the label is what the operator sees in `skygate migrate status`", p.Version))
		}
		if p.SourceFile != s.SourceFile {
			out = append(out, fmt.Sprintf("v%d: SourceFile drifted between chains\n  pg:     %q\n  sqlite: %q\nBoth chains must record the same migration AUTHORSHIP (see driver_sqlite.go's header): the label in applied_migrations.source_file describes who defined the schema change, and it must read the same on a SQLite host and a PostgreSQL host",
				p.Version, p.SourceFile, s.SourceFile))
		}
		if strings.HasSuffix(s.SourceFile, "_test.go") {
			out = append(out, fmt.Sprintf("v%d: SourceFile names the TEST file %q — a migration is production code compiled into the binary, and applied_migrations would record an authorship that is not even in a release build",
				p.Version, s.SourceFile))
		}
	}
	return out
}

// TestMigrations_ChainsRunInLockStep pins the promise the data layer
// makes: the two registries list the same versions, in the same
// order, with the same human-readable label and the same authorship.
//
// A version present in one chain and missing from the other is how
// the SQLite chain silently stopped at V070 (the V071 entry was
// simply absent, so `derp_cert_sync` never existed on a SQLite
// host). A label mismatch is the V070/B236-vs-B238 drift.
//
// 2026-10-01: v0.78 (B333).
func TestMigrations_ChainsRunInLockStep(t *testing.T) {
	for _, line := range chainDrift(PGMigrations(), SQLiteMigrations()) {
		t.Error(line)
	}
}

// TestMigrations_ChainDriftDetectorCatchesSyntheticDrift is the
// mutation test for the detector above: a contract that cannot fail
// is not a contract. Each synthetic pair below reproduces one real
// drift class, and the detector must report every one of them.
//
// 2026-10-01: v0.78 (B333).
func TestMigrations_ChainDriftDetectorCatchesSyntheticDrift(t *testing.T) {
	entry := func(v int, name, src string) MigrationEntry {
		return MigrationEntry{Version: v, Name: name, SourceFile: src}
	}
	cases := []struct {
		what string
		pg   []MigrationEntry
		lite []MigrationEntry
	}{
		{
			what: "a version missing from the SQLite chain (the V071 gap)",
			pg:   []MigrationEntry{entry(70, "v0.70 (B238): trigger", "migrations_v0_70_b238.go"), entry(71, "v0.71 (B252): derp_cert_sync", "migrations_v0_71_derp_cert_sync.go")},
			lite: []MigrationEntry{entry(70, "v0.70 (B238): trigger", "migrations_v0_70_b238.go")},
		},
		{
			what: "the V070 B236/B238 label drift",
			pg:   []MigrationEntry{entry(70, "v0.70 (B238): trigger", "migrations_v0_70_b238.go")},
			lite: []MigrationEntry{entry(70, "v0.70 (B236): trigger", "migrations_v0_70_b238.go")},
		},
		{
			what: "the V060/V061 SourceFile drift (a _test.go authorship)",
			pg:   []MigrationEntry{entry(60, "v0.60 (B183): Telegram + audit_log", "migrations_v0_60_b183_test.go")},
			lite: []MigrationEntry{entry(60, "v0.60 (B183): Telegram + audit_log", "migrations_v0_60_b183_test.go")},
		},
		{
			what: "a SourceFile that differs between the chains",
			pg:   []MigrationEntry{entry(62, "v0.62 (B194):", "migrations_pg.go")},
			lite: []MigrationEntry{entry(62, "v0.62 (B194):", "migrations_v0_62_b194.go")},
		},
		{
			what: "two versions swapped in the run order",
			pg:   []MigrationEntry{entry(69, "v0.69 (B235.3): name column", "migrations_v0_69_b235_3.go"), entry(70, "v0.70 (B238): trigger", "migrations_v0_70_b238.go")},
			lite: []MigrationEntry{entry(70, "v0.70 (B238): trigger", "migrations_v0_70_b238.go"), entry(69, "v0.69 (B235.3): name column", "migrations_v0_69_b235_3.go")},
		},
	}
	for _, tc := range cases {
		if lines := chainDrift(tc.pg, tc.lite); len(lines) == 0 {
			t.Errorf("mutation test FAILED: the detector did not report %s — the B333 lock-step contract is blind to it", tc.what)
		}
	}
	// The detector must stay quiet on a pair that really is in lock-step,
	// otherwise it is just a t.Fatal with extra steps.
	pair := []MigrationEntry{
		entry(60, "v0.60 (B183): Telegram + audit_log", "migrations_pg.go"),
		entry(77, "v0.77 (B312): exit_servers location", "migrations_v0_77_exit_location.go"),
	}
	if lines := chainDrift(pair, append([]MigrationEntry(nil), pair...)); len(lines) != 0 {
		t.Errorf("the detector reported drift on an identical pair: %v", lines)
	}
}

// TestMigrations_VersionsAreUnique catches a duplicated version
// number, which would make `applied_migrations` (keyed on version)
// record one of the two entries and silently skip the other.
//
// 2026-10-01: v0.78 (B333).
func TestMigrations_VersionsAreUnique(t *testing.T) {
	for _, c := range chainsUnderAudit() {
		seen := make(map[int]string, len(c.entries))
		for _, e := range c.entries {
			if prev, dup := seen[e.Version]; dup {
				t.Errorf("%s: version %d is registered twice (%q and %q) — applied_migrations is keyed on the version, so one of them would never run",
					c.dialect, e.Version, prev, e.Name)
				continue
			}
			seen[e.Version] = e.Name
		}
	}
}

// TestMigrations_SourceFileNamesTheDefiningFile is the strong half:
// every entry's `Run` function must be the `migrateV<NNN><Dialect>`
// function for its own version, and the recorded `SourceFile` must
// agree with where that function is really compiled from.
//
// The two chains answer that question differently, and both answers
// are the documented design — so the test encodes both:
//
//   - PostgreSQL (driver_postgres.go) is the authoritative chain:
//     `SourceFile` must BE the file that defines `migrateV<NNN>PG`.
//     This is what catches the V060–V062 drift, where the registry
//     named `migrations_v0_60_b183_test.go`,
//     `migrations_v0_61_b188_test.go` and
//     `migrations_v0_62_b194.go` while all three functions actually
//     live in `migrations_pg.go`.
//   - SQLite (driver_sqlite.go) records the same authorship as PG
//     on purpose, because its legacy functions were generated into
//     the consolidated `migrations_sqlite.go` by
//     scripts/port_migrations_sqlite.py. So the defining file must
//     be either that generated file or the authorship file itself
//     (which is what the hand-written V071+ migrations do) — never a
//     third file, which would mean the port landed somewhere the
//     registry does not mention.
//
// The check uses `runtime.FuncForPC(...).FileLine(...)`, so it
// cannot be satisfied by editing a label alone: the registry has to
// name the truth.
//
// 2026-10-01: v0.78 (B333).
func TestMigrations_SourceFileNamesTheDefiningFile(t *testing.T) {
	// generatedSQLitePort is the single consolidated file that
	// scripts/port_migrations_sqlite.py writes the legacy SQLite
	// ports into.
	const generatedSQLitePort = "migrations_sqlite.go"

	for _, c := range chainsUnderAudit() {
		for _, e := range c.entries {
			want := fmt.Sprintf("migrateV%03d%s", e.Version, c.suffix)

			fn := runtime.FuncForPC(reflect.ValueOf(e.Run).Pointer())
			if fn == nil {
				t.Errorf("%s v%d: cannot resolve the Run function — the registry entry is not a real function", c.dialect, e.Version)
				continue
			}
			full := fn.Name() // e.g. skygate/internal/db.migrateV025PG
			short := full
			if i := strings.LastIndex(full, "."); i >= 0 {
				short = full[i+1:]
			}
			if short != want {
				t.Errorf("%s v%d: the Run function is %s but this entry must run %s — version and function numbering drifted apart",
					c.dialect, e.Version, short, want)
			}

			if e.SourceFile == "" {
				t.Errorf("%s v%d (%s): SourceFile is empty — applied_migrations.source_file would record nothing", c.dialect, e.Version, short)
				continue
			}
			file, _ := fn.FileLine(fn.Entry())
			defining := filepath.Base(file)

			switch c.suffix {
			case "PG":
				if defining != e.SourceFile {
					t.Errorf("PostgreSQL v%d (%s): SourceFile says %q but the function is defined in %q — the PG chain is the authoritative one, so its SourceFile must name the defining file; that string is what an operator reads in applied_migrations after a migration fails",
						e.Version, short, e.SourceFile, defining)
				}
			case "SQLite":
				if defining != generatedSQLitePort && defining != e.SourceFile {
					t.Errorf("SQLite v%d (%s): SourceFile says %q but the function is defined in %q — the SQLite port must live either in the generated %s or in the authorship file the registry names, never in a third file",
						e.Version, short, e.SourceFile, defining, generatedSQLitePort)
				}
			}
		}
	}
}
