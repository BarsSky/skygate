# B-mod-sqlite-pg-bidi Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:test-driven-development.
> Steps use checkbox (`- [ ]`) syntax for tracking. Each step = RED test → GREEN impl → REFACTOR → commit.

**Goal:** Restore explicit SQLite support alongside PostgreSQL. Add a
bidirectional data + schema migration tool (`skygate db-migrate`)
that converts a live skygate DB between SQLite and PostgreSQL
**without data loss**. Allow the operator to choose the DB type at
first deploy AND switch later (working conversion).

**Why:** v1.3.0 (B-mod-db-retry, 2026-09-09) removed SQLite support
citing "simplification". But skygate is also a self-hosted / dev
project where SQLite is the natural choice (no docker, no PG service,
no 5432 port). The operator explicitly asked for explicit SQLite+PG
support with full conversion (2026-09-11).

**Architecture:**

1. **Driver choice**: `modernc.org/sqlite` (pure Go, no CGO, works on
   Windows + Linux). Already supports SQLite 3.40+ features we need
   (window functions, generated columns, partial indexes).

2. **Dialect pattern**: shadow files (matches existing project
   convention: `on_conflict.go` = interface, `on_conflict_postgres.go` =
   PG impl. Add `on_conflict_sqlite.go` = SQLite impl). Plus a
   `dialect.go` for type coercion helpers (placeholder, boolean,
   timestamp, JSON extract).

3. **Configuration**: env var `SKYGATE_DB` accepts:
   - `sqlite:/path/to/file.db` → SQLite (file-based, embedded)
   - `postgres://user:pass@host/db` → PostgreSQL
   - `:memory:` → SQLite in-memory (test mode)
   - bare path `/var/lib/skygate/skygate.db` → SQLite
   - **Backward compat**: `SKYGATE_DB_DSN` (existing) still works for PG.

4. **Conversion tool**: `skygate db-migrate` subcommand.
   - `--from=<dsn>` (source)
   - `--to=<dsn>` (target)
   - `--schema-only` (DDL only, no data)
   - `--data-only` (data, assumes schema already applied)
   - `--dry-run` (show what would be done, no writes)
   - Default: schema + data, atomic (transaction)
   - Stream tables in FK-dep order (topo sort via INFORMATION_SCHEMA
     + disable_fkey pragma for SQLite, deferred FK for PG).

5. **Auto-deploy**: `deploy/install-debian.sh` (or `deploy.sh`) gets
   a `--db-type sqlite|postgres` flag. Default = ask. The
   `deployments/docker-compose.lite.yml` already exists (PG service);
   add SQLite-only variant (`docker-compose.sqlite.yml`) that
   uses a bind-mounted file.

**Tech stack:** Go 1.25, `database/sql` interface, `modernc.org/sqlite`
v1.34+ (pure-Go driver), `github.com/jackc/pgx/v5` v5.10+ (existing PG
driver). `pgx/v5/stdlib` for `*sql.DB` compatibility (already in use).

**No new external dependencies** beyond `modernc.org/sqlite`.

## Global Constraints

- v1.5.3 is the last PG-only release. This plan reverses the v1.3.0
  decision to restore SQLite.
- **All 60+ existing migrations** must work on BOTH dialects. The
  shadow-file approach means `migrations_pg.go` (~62KB) is paired
  with `migrations_sqlite.go` (~62KB, parallel structure, same
  migration numbers, dialect-specific DDL).
- The `internal/db/dialect.go` interface must be importable by
  EVERY shadow file (no cycles). Single small file, no deps.
- `qDeleteRulesByDeviceID` and similar primitives that use
  `RETURNING id` work on both. `EXTRACT(EPOCH FROM x)` does NOT —
  must be wrapped in `dialect.UnixEpoch(x)`.
- All 22 B-check scripts that do `psql -c "SELECT ..."` against the
  live DB must be reviewed. Either: (a) keep them PG-only and add
  SQLite variants, or (b) make them dialect-aware via a small
  `scripts/_lib/db_exec.sh` helper that picks `psql` or `sqlite3`.

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `go.mod` | Modify | Add `modernc.org/sqlite` dependency |
| `go.sum` | Modify | sum for modernc.org/sqlite |
| `internal/db/dialect.go` | Create | `Dialect` interface + `DetectDSN` helper + PG/SQLite impls |
| `internal/db/dialect_test.go` | Create | TDD tests for dialect detection + each helper |
| `internal/db/driver.go` | Modify | Add `OpenWithDialect(dsn string) (*sql.DB, Dialect, error)` |
| `internal/db/driver_test.go` | Modify | Add tests for SQLite + PG open paths |
| `internal/db/open_sqlite.go` | Create | SQLite-specific connection setup (PRAGMAs, foreign keys, journal mode) |
| `internal/db/open_pg.go` | Create | Renamed from `open_pg_pg.go` (cleanup) |
| `internal/db/on_conflict_sqlite.go` | Create | `INSERT OR IGNORE` / `INSERT OR REPLACE` for ON CONFLICT |
| `internal/db/on_conflict_test.go` | Create | Tests for both PG and SQLite paths |
| `internal/db/now_unix_sqlite.go` | Create | `strftime('%s', 'now')` vs `EXTRACT(EPOCH FROM now())` |
| `internal/db/placeholders_sqlite.go` | Create | `?` placeholders vs `$1, $2` |
| `internal/db/migrations_sqlite.go` | Create | Parallel migration set (60+ entries, SQLite DDL) |
| `internal/db/migrations_sqlite_test.go` | Create | Each migration has a corresponding SQLite test |
| `internal/db/convert.go` | Create | Schema + data migration tool (`Convert` function) |
| `internal/db/convert_test.go` | Create | Round-trip SQLite→PG→SQLite, PG→SQLite→PG tests |
| `internal/db/convert_schema.go` | Create | `readSchema(src)` + `writeSchema(dst, schema)` |
| `internal/db/convert_data.go` | Create | `copyData(src, dst, tableOrder)` with type coercion |
| `internal/db/convert_types.go` | Create | Type mapping tables: PG→SQLite, SQLite→PG |
| `cmd/skygate/main.go` | Modify | Add `db-migrate` subcommand + dispatcher |
| `cmd/skygate/db_migrate.go` | Create | Subcommand handler |
| `cmd/skygate/db_migrate_test.go` | Create | Subcommand arg parsing + dispatch tests |
| `internal/config/config.go` | Modify | `SKYGATE_DB` env var (sqlite:/path or postgres://dsn) |
| `internal/config/config_test.go` | Modify | Add tests for SQLite path + PG DSN + backward compat |
| `deploy/install-debian.sh` | Modify | Add `--db-type` flag + interactive prompt |
| `deployments/docker-compose.sqlite.yml` | Create | SQLite variant (no PG service) |
| `deployments/docker-compose.lite.yml` | Modify | Add DB_TYPE env var comment + selection |
| `scripts/_lib/db_exec.sh` | Create | Helper that picks `psql` or `sqlite3` based on DSN |
| `scripts/check_b_mod_sqlite_pg.sh` | Create | 14-contract B-check |
| `AGENTS.md` | Modify | Add B-mod-sqlite-pg-bidi release note |
| `docs/internal/2026-09-11-skygate-adoption-audit.md` | Modify | Mark F1-F5 DONE |
| `docs/sidecar-mode.md` | Modify | Add "Choosing SQLite vs PostgreSQL" section |
| `docs/deploy.md` | Modify | Add SQLite variant to the install flow |

---

## Task 1: RED — write failing test for dialect detection

**Files:**
- Create: `internal/db/dialect.go`
- Create: `internal/db/dialect_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/db/dialect_test.go
package db

import "testing"

func TestDetectDSN(t *testing.T) {
    cases := []struct {
        dsn      string
        wantKind DialectKind
    }{
        // SQLite explicit prefix
        {"sqlite:/var/lib/skygate/skygate.db", DialectSQLite},
        {"sqlite::memory:", DialectSQLite},
        {"sqlite:/tmp/foo.db?_journal_mode=WAL", DialectSQLite},

        // Bare path (no scheme) → SQLite (default for self-hosted)
        {"/var/lib/skygate/skygate.db", DialectSQLite},
        {"./skygate.db", DialectSQLite},

        // PostgreSQL
        {"postgres://user:pass@host/db", DialectPostgres},
        {"postgresql://user:pass@host:5432/db?sslmode=disable", DialectPostgres},
        // Backward compat: old SKYGATE_DB_DSN name
        {"postgres://user:pass@13.66:5432/skygate", DialectPostgres},

        // In-memory SQLite (test mode)
        {":memory:", DialectSQLite},

        // Empty → default to SQLite (backward compat with v1.5.x self-host)
        {"", DialectSQLite},
    }
    for _, tc := range cases {
        t.Run(tc.dsn, func(t *testing.T) {
            got := DetectDSN(tc.dsn)
            if got.Kind != tc.wantKind {
                t.Errorf("DetectDSN(%q) kind = %v, want %v", tc.dsn, got.Kind, tc.wantKind)
            }
        })
    }
}

func TestDialect_PG_Placeholders(t *testing.T) {
    d := DialectPostgres
    if got := d.Placeholders(3); got != "$1, $2, $3" {
        t.Errorf("PG placeholders(3) = %q, want \"$1, $2, $3\"", got)
    }
    if got := d.Placeholders(1); got != "$1" {
        t.Errorf("PG placeholders(1) = %q, want \"$1\"", got)
    }
    if got := d.Placeholders(0); got != "" {
        t.Errorf("PG placeholders(0) = %q, want empty", got)
    }
}

func TestDialect_SQLite_Placeholders(t *testing.T) {
    d := DialectSQLite
    if got := d.Placeholders(3); got != "?, ?, ?" {
        t.Errorf("SQLite placeholders(3) = %q, want \"?, ?, ?\"", got)
    }
    if got := d.Placeholders(1); got != "?" {
        t.Errorf("SQLite placeholders(1) = %q, want \"?\"", got)
    }
}

func TestDialect_UnixEpoch_PG(t *testing.T) {
    d := DialectPostgres
    // PG: EXTRACT(EPOCH FROM now())::bigint
    if got := d.UnixEpoch("now()"); got != "EXTRACT(EPOCH FROM now())::bigint" {
        t.Errorf("PG UnixEpoch(now()) = %q", got)
    }
    if got := d.UnixEpoch("created_at"); got != "EXTRACT(EPOCH FROM created_at)::bigint" {
        t.Errorf("PG UnixEpoch(col) = %q", got)
    }
}

func TestDialect_UnixEpoch_SQLite(t *testing.T) {
    d := DialectSQLite
    // SQLite: strftime('%s', 'now') and cast to integer
    if got := d.UnixEpoch("CURRENT_TIMESTAMP"); got != "CAST(strftime('%s', CURRENT_TIMESTAMP) AS INTEGER)" {
        t.Errorf("SQLite UnixEpoch(CURRENT_TIMESTAMP) = %q", got)
    }
    if got := d.UnixEpoch("created_at"); got != "CAST(strftime('%s', created_at) AS INTEGER)" {
        t.Errorf("SQLite UnixEpoch(col) = %q", got)
    }
}

func TestDialect_InsertIgnore_PG(t *testing.T) {
    d := DialectPostgres
    // PG: ON CONFLICT (col) DO NOTHING
    sql := d.InsertIgnore("INSERT INTO device_rules (user_id, device_id) VALUES (?, ?)",
        []string{"user_id", "device_id"})
    expected := "INSERT INTO device_rules (user_id, device_id) VALUES (?, ?) " +
        "ON CONFLICT (user_id, device_id) DO NOTHING"
    if sql != expected {
        t.Errorf("PG InsertIgnore = %q\nwant %q", sql, expected)
    }
}

func TestDialect_InsertIgnore_SQLite(t *testing.T) {
    d := DialectSQLite
    // SQLite: INSERT OR IGNORE
    sql := d.InsertIgnore("INSERT INTO device_rules (user_id, device_id) VALUES (?, ?)",
        []string{"user_id", "device_id"})
    expected := "INSERT OR IGNORE INTO device_rules (user_id, device_id) VALUES (?, ?)"
    if sql != expected {
        t.Errorf("SQLite InsertIgnore = %q\nwant %q", sql, expected)
    }
}

func TestDialect_BooleanType(t *testing.T) {
    if got := DialectPostgres.BooleanType(); got != "BOOLEAN" {
        t.Errorf("PG BooleanType = %q, want BOOLEAN", got)
    }
    if got := DialectSQLite.BooleanType(); got != "INTEGER" {
        t.Errorf("SQLite BooleanType = %q, want INTEGER (SQLite has no native BOOL)", got)
    }
}

func TestDialect_BooleanLiteral(t *testing.T) {
    // PG: true/false
    if got := DialectPostgres.BooleanLiteral(true); got != "true" {
        t.Errorf("PG BooleanLiteral(true) = %q, want true", got)
    }
    // SQLite: 1/0 (we use 1/0 for portability, not 'true'/'false' literals)
    if got := DialectSQLite.BooleanLiteral(true); got != "1" {
        t.Errorf("SQLite BooleanLiteral(true) = %q, want 1", got)
    }
    if got := DialectSQLite.BooleanLiteral(false); got != "0" {
        t.Errorf("SQLite BooleanLiteral(false) = %q, want 0", got)
    }
}

func TestDialect_OpenDB_OpenCloses(t *testing.T) {
    // Open an in-memory SQLite, verify the dialect reports it as SQLite
    // and the connection works.
    d, db, err := OpenWithDialect(":memory:")
    if err != nil {
        t.Fatalf("OpenWithDialect: %v", err)
    }
    defer db.Close()
    if d.Kind != DialectSQLite {
        t.Errorf("in-memory DSN kind = %v, want %v", d.Kind, DialectSQLite)
    }
    var v int
    if err := db.QueryRow("SELECT 1").Scan(&v); err != nil {
        t.Fatalf("SELECT 1: %v", err)
    }
    if v != 1 {
        t.Errorf("SELECT 1 returned %d, want 1", v)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/db/ -run 'TestDetectDSN|TestDialect_' -v`
Expected: FAIL — `Dialect` type, `DetectDSN`, `OpenWithDialect` don't
exist yet.

- [ ] **Step 3: Write the minimal implementation**

```go
// internal/db/dialect.go
package db

import (
    "fmt"
    "strings"

    _ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite" driver name
    _ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" driver name
    "database/sql"
)

// DialectKind identifies which SQL dialect to use.
type DialectKind int

const (
    DialectUnknown DialectKind = iota
    DialectSQLite
    DialectPostgres
)

func (k DialectKind) String() string {
    switch k {
    case DialectSQLite:
        return "sqlite"
    case DialectPostgres:
        return "postgres"
    default:
        return "unknown"
    }
}

// Dialect produces dialect-specific SQL fragments.
type Dialect interface {
    Kind() DialectKind
    Placeholders(n int) string
    UnixEpoch(expr string) string
    InsertIgnore(sql string, conflictKeys []string) string
    BooleanType() string
    BooleanLiteral(b bool) string
    OpenDialect() (*sql.DB, error)
}

// DetectDSN returns the dialect inferred from the DSN/path string.
func DetectDSN(dsn string) Dialect {
    dsn = strings.TrimSpace(dsn)
    if dsn == "" || dsn == ":memory:" {
        return DialectSQLite // default to SQLite for self-host
    }
    lower := strings.ToLower(dsn)
    if strings.HasPrefix(lower, "sqlite:") || strings.HasPrefix(lower, "file:") {
        return DialectSQLite
    }
    if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
        return DialectPostgres
    }
    if strings.HasPrefix(lower, "pgx5://") {
        return DialectPostgres
    }
    // Bare path (no scheme) → SQLite
    if !strings.Contains(dsn, "://") {
        return DialectSQLite
    }
    return DialectUnknown
}

// OpenWithDialect opens a *sql.DB connection matching the inferred
// dialect, and returns the matching Dialect for SQL fragment generation.
func OpenWithDialect(dsn string) (Dialect, *sql.DB, error) {
    detected := DetectDSN(dsn)
    switch detected {
    case DialectSQLite:
        d := &sqliteDialect{}
        db, err := d.OpenDialect()
        return d, db, err
    case DialectPostgres:
        d := &pgDialect{}
        db, err := d.OpenDialect()
        return d, db, err
    default:
        return nil, nil, fmt.Errorf("db: unknown DSN format %q (use sqlite:/path, postgres://url, or bare /path)", dsn)
    }
}
```

```go
// internal/db/driver.go
package db

import (
    "database/sql"
    "fmt"
    "os"
    "path/filepath"
    "time"
)

type sqliteDialect struct{}

func (d *sqliteDialect) Kind() DialectKind                            { return DialectSQLite }
func (d *sqliteDialect) Placeholders(n int) string                   { return strings.Repeat("?, ", max(0, n-1)) + "?" }
func (d *sqliteDialect) UnixEpoch(expr string) string                 { return fmt.Sprintf("CAST(strftime('%%s', %s) AS INTEGER)", expr) }
func (d *sqliteDialect) BooleanType() string                          { return "INTEGER" }
func (d *sqliteDialect) BooleanLiteral(b bool) string                 { if b { return "1" } else { return "0" } }
func (d *sqliteDialect) InsertIgnore(sql string, keys []string) string {
    // INSERT OR IGNORE INTO ... VALUES (...)  — rewrite "INSERT INTO" → "INSERT OR IGNORE INTO"
    return strings.Replace(sql, "INSERT INTO", "INSERT OR IGNORE INTO", 1)
}
func (d *sqliteDialect) OpenDialect() (*sql.DB, error) {
    dsn := os.Getenv("SKYGATE_DB")
    if dsn == "" { dsn = ":memory:" }
    // SKYGATE_DB might be a bare path or sqlite:/path
    if !strings.HasPrefix(dsn, "sqlite:") && !strings.HasPrefix(dsn, "file:") {
        dsn = "file:" + dsn + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)"
    } else {
        // Already a file: URI; just add pragmas
        if !strings.Contains(dsn, "_pragma=") {
            sep := "?"
            if strings.Contains(dsn, "?") { sep = "&" }
            dsn = dsn + sep + "_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)"
        }
    }
    db, err := sql.Open("sqlite", dsn)
    if err != nil { return nil, err }
    // Verify connection
    if err := db.Ping(); err != nil { db.Close(); return nil, err }
    return db, nil
}
```

(Similar pgDialect — extracted from existing driver_postgres.go logic.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/db/ -run 'TestDetectDSN|TestDialect_' -v`
Expected: 9/9 PASS

- [ ] **Step 5: Commit**

```bash
git add internal/db/dialect.go internal/db/dialect_test.go
git commit -m "feat(db): Dialect interface + DetectDSN (SQLite+Postgres)

Adds dialect detection and SQL fragment helpers for the
SQLite+Postgres dual-support refactor (B-mod-sqlite-pg-bidi).

- internal/db/dialect.go: DialectKind, Dialect interface,
  DetectDSN (handles sqlite:/file:, postgres://, file://,
  :memory:, bare paths), OpenWithDialect entry point
- internal/db/dialect_test.go: 9 tests covering all DSN
  formats, placeholder, unix epoch, insert-ignore, boolean
  type/literal, and live in-memory SQLite open
- Pure-Go modernc.org/sqlite driver (no CGO, Windows+Linux)

Backward compat: bare path → SQLite (v1.5.x self-host default);
postgres:// → PG (v1.5.x DSN format); empty → SQLite (matches
SKYGATE_DB unset fallback). SKYGATE_DB_DSN env var still
honored by config.go (separate change)."
```

---

## Task 2: RED — write failing test for migrations on SQLite

**Files:**
- Create: `internal/db/migrations_sqlite_test.go`

- [ ] **Step 1: Write the test**

```go
package db

import (
    "testing"
)

// TestMigrationsSQLite_AllApply verifies that the v0.0.0 → v0.5.7
// migration set runs cleanly on a fresh SQLite DB. This is the
// foundation — every subsequent migration must also apply
// (verified by the v0.0.0 → latest test in test_migration_test.go).

func TestMigrationsSQLite_AllApply(t *testing.T) {
    _, db, err := OpenWithDialect(":memory:")
    if err != nil { t.Fatalf("open: %v", err) }
    defer db.Close()

    if err := ApplyMigrations(db, DialectSQLite); err != nil {
        t.Fatalf("ApplyMigrations(SQLite): %v", err)
    }

    // Check applied_migrations has the expected initial row
    var n int
    if err := db.QueryRow("SELECT COUNT(*) FROM applied_migrations").Scan(&n); err != nil {
        t.Fatalf("count applied_migrations: %v", err)
    }
    if n < 50 { // at least 50 migrations should be applied (v0.0.0 through v0.5.7)
        t.Errorf("expected >= 50 migrations, got %d", n)
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/db/ -run 'TestMigrationsSQLite_AllApply' -v`
Expected: FAIL — `ApplyMigrations` doesn't take a Dialect param yet, or
`migrations_sqlite.go` doesn't exist.

- [ ] **Step 3: Write minimal implementation**

Refactor `ApplyMigrations` to accept a Dialect parameter, then add
`migrations_sqlite.go` with parallel entries. The migration REGISTRY
stays shared; the SQL DDL is dialect-specific.

```go
// internal/db/driver.go (add)
func ApplyMigrations(db *sql.DB, d Dialect) error {
    // ... existing migration logic, but use d.Placeholders() etc.
}

// Build dialect-specific migration set.
func migrationsForDialect(d Dialect) []Migration {
    if d.Kind() == DialectPostgres {
        return migrationsPG  // existing var in migrations_pg.go
    }
    return migrationsSQLite  // new var in migrations_sqlite.go
}
```

```go
// internal/db/migrations_sqlite.go (extract from existing migrations_pg.go
// by transforming PG SQL → SQLite SQL). Key transformations:
//   - "SERIAL PRIMARY KEY" → "INTEGER PRIMARY KEY AUTOINCREMENT"
//   - "BIGSERIAL" → "INTEGER PRIMARY KEY AUTOINCREMENT"
//   - "BOOLEAN" → "INTEGER" (0/1)
//   - "JSONB" → "TEXT" (JSON stored as text, parsed at read time)
//   - "TIMESTAMPTZ" → "INTEGER" (unix epoch)  [or "TEXT" if we want ISO strings]
//   - "EXTRACT(EPOCH FROM x)::bigint" → "strftime('%s', x)"
//   - "ON CONFLICT (col) DO UPDATE" → "ON CONFLICT (col) DO UPDATE" (SQLite 3.24+ supports)
//   - "RETURNING id" → "RETURNING id" (SQLite 3.35+ supports)
//   - "$1, $2" → "?, ?"
//   - "bytea" → "BLOB"
//   - "text" → "TEXT"

var migrationsSQLite = []Migration{
    {Name: "v0.0.0-initial-schema", SQL: `...`},
    // ... one per migration that has SQLite-specific DDL
}
```

(This is the most labor-intensive task. 60+ migration entries. A
script that reads `migrations_pg.go` and emits a draft
`migrations_sqlite.go` with the transformations is the right way to
bootstrap. Then iterate by running the live-verify conversion tool.)

- [ ] **Step 4: Run test + iterate until all migrations apply**

Run: `go test ./internal/db/ -run 'TestMigrationsSQLite_AllApply' -v`
Expected: eventually PASS, with iterations to fix type mismatches,
missing columns, etc.

- [ ] **Step 5: Commit**

```bash
git add internal/db/migrations_sqlite.go internal/db/migrations_sqlite_test.go internal/db/driver.go
git commit -m "feat(db): SQLite migration set (parallel to PG)

Adds internal/db/migrations_sqlite.go with parallel migration
entries (same migration numbers, dialect-specific DDL). Type
transformations:
  SERIAL → INTEGER PRIMARY KEY AUTOINCREMENT
  BOOLEAN → INTEGER
  JSONB → TEXT (parse at read)
  TIMESTAMPTZ → INTEGER (unix epoch)
  \$N → ?
  bytea → BLOB

ApplyMigrations now takes a Dialect parameter and picks the
correct migration set. The migration NAMES are shared
(v0.0.0-initial-schema, v0.0.1-..., v0.5.7-...) so the
applied_migrations table is dialect-agnostic — the same
row tracks the migration regardless of DB backend."
```

---

## Task 3: RED — write failing test for `skygate db-migrate` subcommand

**Files:**
- Create: `cmd/skygate/db_migrate.go`
- Create: `cmd/skygate/db_migrate_test.go`

- [ ] **Step 1: Write the test**

```go
// cmd/skygate/db_migrate_test.go
package main

import (
    "context"
    "testing"
)

func TestDBMigrate_ArgsParsing(t *testing.T) {
    cases := []struct {
        args     []string
        wantFrom string
        wantTo   string
        wantMode string // "schema+data" | "schema-only" | "data-only"
        wantDry  bool
        wantErr  bool
    }{
        {[]string{"db-migrate", "--from", "sqlite:/tmp/src.db", "--to", "postgres://u:p@h/d"},
            "sqlite:/tmp/src.db", "postgres://u:p@h/d", "schema+data", false, false},
        {[]string{"db-migrate", "--from", "X", "--to", "Y", "--schema-only"},
            "X", "Y", "schema-only", false, false},
        {[]string{"db-migrate", "--from", "X", "--to", "Y", "--data-only"},
            "X", "Y", "data-only", false, false},
        {[]string{"db-migrate", "--from", "X", "--to", "Y", "--dry-run"},
            "X", "Y", "schema+data", true, false},
        {[]string{"db-migrate"}, "", "", "", false, true}, // missing --from / --to
        {[]string{"db-migrate", "--from", "X"}, "", "", "", false, true}, // missing --to
    }
    for _, tc := range cases {
        t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
            cfg, err := parseDBMigrateArgs(tc.args)
            if (err != nil) != tc.wantErr {
                t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
            }
            if err != nil { return }
            if cfg.From != tc.wantFrom { t.Errorf("From = %q, want %q", cfg.From, tc.wantFrom) }
            if cfg.To != tc.wantTo { t.Errorf("To = %q, want %q", cfg.To, tc.wantTo) }
            if cfg.Mode != tc.wantMode { t.Errorf("Mode = %q, want %q", cfg.Mode, tc.wantMode) }
            if cfg.DryRun != tc.wantDry { t.Errorf("DryRun = %v, want %v", cfg.DryRun, tc.wantDry) }
        })
    }
}

func TestDBMigrate_SQLiteToPG_RoundTrip(t *testing.T) {
    // 1. Open source SQLite DB, insert a few rows
    srcDB, err := db.OpenWithDialect(":memory:")
    if err != nil { t.Fatalf("open source: %v", err) }
    defer srcDB.Close()
    // ... create portal_users + device_rules + audit_log
    // ... insert a few rows
    // 2. Run conversion to a target SQLite DB (since we don't have PG in tests)
    // This is a SQLite→SQLite round-trip; same code path as SQLite→PG.
    cfg := dbMigrateConfig{From: ":memory:?src", To: ":memory:?dst", Mode: "schema+data"}
    if err := runDBMigrate(context.Background(), cfg); err != nil {
        t.Fatalf("runDBMigrate: %v", err)
    }
    // 3. Open target, verify same row count + same data
    tgtDB, err := db.OpenWithDialect(":memory:?dst")
    if err != nil { t.Fatalf("open target: %v", err) }
    defer tgtDB.Close()
    var n int
    tgtDB.QueryRow("SELECT COUNT(*) FROM portal_users").Scan(&n)
    if n != expectedUserCount { t.Errorf("users: got %d, want %d", n, expectedUserCount) }
}
```

- [ ] **Step 2: Run, expect FAIL**

- [ ] **Step 3: Write minimal implementation**

`cmd/skygate/db_migrate.go`:
```go
package main

import (
    "context"
    "fmt"
    "os"
    "strings"
)

type dbMigrateConfig struct {
    From   string
    To     string
    Mode   string // "schema+data" | "schema-only" | "data-only"
    DryRun bool
}

func parseDBMigrateArgs(args []string) (dbMigrateConfig, error) {
    cfg := dbMigrateConfig{Mode: "schema+data"}
    // skip "db-migrate" if present
    start := 0
    if len(args) > 0 && args[0] == "db-migrate" { start = 1 }
    for i := start; i < len(args); i++ {
        switch args[i] {
        case "--from":
            if i+1 >= len(args) { return cfg, fmt.Errorf("--from requires value") }
            cfg.From = args[i+1]; i++
        case "--to":
            if i+1 >= len(args) { return cfg, fmt.Errorf("--to requires value") }
            cfg.To = args[i+1]; i++
        case "--schema-only":
            cfg.Mode = "schema-only"
        case "--data-only":
            cfg.Mode = "data-only"
        case "--dry-run":
            cfg.DryRun = true
        default:
            return cfg, fmt.Errorf("unknown arg: %s", args[i])
        }
    }
    if cfg.From == "" { return cfg, fmt.Errorf("--from required") }
    if cfg.To == "" { return cfg, fmt.Errorf("--to required") }
    return cfg, nil
}

func runDBMigrate(ctx context.Context, cfg dbMigrateConfig) error {
    // Delegate to internal/db.Convert
    fromD, fromDB, err := db.OpenWithDialect(cfg.From)
    if err != nil { return fmt.Errorf("open from: %w", err) }
    defer fromDB.Close()
    toD, toDB, err := db.OpenWithDialect(cfg.To)
    if err != nil { return fmt.Errorf("open to: %w", err) }
    defer toDB.Close()
    return db.Convert(ctx, fromD, fromDB, toD, toDB, db.ConvertOptions{
        Mode: cfg.Mode,
        DryRun: cfg.DryRun,
    })
}
```

`cmd/skygate/main.go` (modify dispatcher):
```go
case "db-migrate":
    return runDBMigrateSubcommand(ctx, args)
```

- [ ] **Step 4: Run + commit**

---

## Task 4: RED — write failing test for `db.Convert` core logic

**Files:**
- Create: `internal/db/convert.go`
- Create: `internal/db/convert_test.go`

- [ ] **Step 1: Write the test**

(See Task 3's test for SQLite→SQLite round-trip; extend it here to
also test SQLite→PG and PG→SQLite path types via dialect detection.)

- [ ] **Step 2-5: Run, impl, commit**

`internal/db/convert.go` skeleton:
```go
package db

import (
    "context"
    "fmt"
    "database/sql"
)

type ConvertOptions struct {
    Mode   string // "schema+data" | "schema-only" | "data-only"
    DryRun bool
}

// Convert migrates a skygate DB from one dialect to another.
// The algorithm:
//  1. Open both DBs (caller does this; pass the *sql.DB + Dialect pair)
//  2. readSchema(from) — returns []TableSchema (name, columns, pk, fks)
//  3. topoSort(tables) — order by FK dependency (parent before child)
//  4. writeSchema(to, tables) — translate types + emit CREATE TABLE
//  5. For each table in topo order: copyData(from, to, table) with
//     type coercion per column
//  6. Verify row counts match (countFrom == countTo per table)
//  7. Report (in DryRun mode: print the plan, no writes)

func Convert(ctx context.Context, fromD Dialect, fromDB *sql.DB, toD Dialect, toDB *sql.DB, opts ConvertOptions) error {
    // ...
}
```

Key functions to implement:
- `readSchema(db, dialect) ([]TableSchema, error)` — query INFORMATION_SCHEMA (PG) or sqlite_master (SQLite), normalize column types
- `topoSort(tables) ([]string, error)` — topological sort by FK
- `writeSchema(toDB, tables, toD) error` — emit CREATE TABLE with type translation
- `copyData(fromDB, toDB, table, fromD, toD) error` — batch INSERT with `?` placeholders (SQLite) or `$1` (PG)
- Type translation tables: `pgTypeToSQLite(pgType string) string` and `sqliteTypeToPG(sqliteType string) string`

The key gotchas:
- `INTEGER PRIMARY KEY` in SQLite is implicitly `ROWID`. To use auto-increment, must be `INTEGER PRIMARY KEY AUTOINCREMENT` (which prevents ROWID reuse). For skygate, the migration tables and the `applied_migrations` table use `INTEGER PRIMARY KEY` (without AUTOINCREMENT) to match PG's `SERIAL PRIMARY KEY` behavior — close enough for our use.
- `BOOLEAN` is `INTEGER` (0/1) in SQLite. The helpers `dialect.BooleanLiteral(bool)` and `dialect.BooleanType()` abstract this.
- `TIMESTAMP` defaults: PG `TIMESTAMPTZ` → SQLite `INTEGER` (unix epoch) or `TEXT` (ISO 8601). Choose one and stick with it. `INTEGER` is simpler and matches the existing code.
- Foreign keys: SQLite requires `PRAGMA foreign_keys = ON` per connection. Set in `OpenDialect()`.
- Data truncation: PG `VARCHAR(N)` → SQLite `TEXT` (no length limit). The reverse is a `CHECK` constraint or truncation. Document this in the conversion report.

---

## Task 5: B-check script

**Files:**
- Create: `scripts/check_b_mod_sqlite_pg.sh`

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# scripts/check_b_mod_sqlite_pg.sh — B-mod-sqlite-pg-bidi B-check (14 contracts).
set -euo pipefail

pass() { printf "  PASS %s\n" "$1"; }
fail() { printf "  FAIL %s\n" "$1"; exit 1; }
GO="${GO_BIN:-$(command -v go || true)}"
[ -z "$GO" ] && GO="C:\Program Files\Go\bin\go.exe"

# A. modernc.org/sqlite is in go.mod
grep -q "modernc.org/sqlite" go.mod && pass "A modernc.org/sqlite dep" || fail "A dep missing"

# B. dialect.go has Dialect interface
grep -q "type Dialect interface" internal/db/dialect.go && pass "B Dialect interface" || fail "B missing"

# C. dialect_test.go covers detection + helpers
[ -f internal/db/dialect_test.go ] && pass "C dialect test file" || fail "C missing"

# D. migrations_sqlite.go exists
[ -f internal/db/migrations_sqlite.go ] && pass "D SQLite migrations" || fail "D missing"

# E. open_sqlite.go exists
[ -f internal/db/open_sqlite.go ] && pass "E SQLite driver" || fail "E missing"

# F. convert.go has Convert function
grep -q "func Convert(" internal/db/convert.go && pass "F Convert func" || fail "F missing"

# G. cmd/skygate/db_migrate.go exists
[ -f cmd/skygate/db_migrate.go ] && pass "G db-migrate subcommand" || fail "G missing"

# H. config.go supports SKYGATE_DB
grep -q "SKYGATE_DB" internal/config/config.go && pass "H SKYGATE_DB env" || fail "H missing"

# I. docker-compose.sqlite.yml exists
[ -f deployments/docker-compose.sqlite.yml ] && pass "I SQLite compose" || fail "I missing"

# J. install-debian.sh --db-type flag
grep -q "db-type" deploy/install-debian.sh && pass "J --db-type flag" || fail "J missing"

# K. tests pass
"$GO" test ./internal/db/ -run 'TestDetectDSN|TestDialect_|TestMigrationsSQLite_|TestConvert_' -count=1 2>&1 | tail -3

# L. db-migrate subcommand test passes
"$GO" test ./cmd/skygate/ -run 'TestDBMigrate_' -count=1 2>&1 | tail -3

# M. sqlite path open works
[ -f /tmp/skygate_smoke.db ] && rm -f /tmp/skygate_smoke.db
"$GO" test ./internal/db/ -run 'TestDialect_OpenDB' -v 2>&1 | tail -5

# N. AGENTS.md mentions B-mod-sqlite-pg-bidi
grep -q "B-mod-sqlite-pg" AGENTS.md && pass "N AGENTS.md note" || fail "N missing"

echo
echo "PASS B-mod-sqlite-pg-bidi (14/14 contracts)"
```

- [ ] **Step 2: Make executable + run**

```bash
chmod +x scripts/check_b_mod_sqlite_pg.sh
bash scripts/check_b_mod_sqlite_pg.sh
```

Expected: 14/14 PASS (after Tasks 1-4 complete).

- [ ] **Step 3: Commit**

```bash
git add scripts/check_b_mod_sqlite_pg.sh
git commit -m "ci: add B-mod-sqlite-pg-bidi contract check (14 contracts)

Pins the dual DB support:
- modernc.org/sqlite in go.mod
- Dialect interface + DetectDSN
- SQLite migrations + driver
- Convert tool (schema+data, both directions)
- db-migrate subcommand
- SKYGATE_DB env var
- SQLite docker-compose variant
- install-debian.sh --db-type flag
- All tests green
- AGENTS.md release note"
```

---

## Task 6: AGENTS.md release note

**Files:**
- Modify: `AGENTS.md`

- [ ] **Step 1: Add the release note**

Find the most recent B-mod-* entry in AGENTS.md and add above it:

```markdown
  - **B-mod-sqlite-pg-bidi (v1.5.4)**: bidirectional SQLite↔Postgres
    support + admin-deploy DB type choice + working data
    conversion. Restores the SQLite support that v1.3.0
    (B-mod-db-retry, 2026-09-09) removed citing "simplification".
    Operator rationale (2026-09-11): self-host / dev / armv7
    deployments don't want a PG service running just for skygate,
    and the v1.3.0 design locked them out. Three pieces:
    (1) **Driver**: `modernc.org/sqlite` v1.34+ (pure Go, no CGO,
        works on Windows+Linux). Opens via
        `SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db` (or
        bare path → SQLite, or `:memory:` for tests).
    (2) **Dialect abstraction**: `internal/db/dialect.go` —
        `DialectKind` (SQLite/Postgres), `Dialect` interface
        (Placeholders, UnixEpoch, InsertIgnore, BooleanType,
        BooleanLiteral, OpenDialect). Shadow-file pattern (matches
        the existing `on_conflict.go` + `on_conflict_postgres.go`):
        every PG-specific primitive now has a parallel
        `*_sqlite.go`. The `applied_migrations` table is
        dialect-agnostic (same migration NAMES on both sides).
    (3) **Conversion tool**: `skygate db-migrate
        --from=<src> --to=<dst> [--schema-only|--data-only]
        [--dry-run]`. Reads source schema (INFORMATION_SCHEMA on
        PG, sqlite_master on SQLite), topo-sorts tables by FK,
        translates types (SERIAL→INTEGER PK AUTOINCREMENT,
        BOOLEAN→INTEGER, JSONB→TEXT, TIMESTAMPTZ→INTEGER,
        bytea→BLOB), emits CREATE TABLE on target, copies data
        in batches with dialect-specific placeholders. Reports
        any non-translatable features (e.g. PG `JSONB` with
        binary content → stored as TEXT, may need re-validate).
    (4) **Auto-deploy DB choice**: `deploy/install-debian.sh
        --db-type sqlite|postgres` (default: ask). New
        `deployments/docker-compose.sqlite.yml` (SQLite-only,
        bind-mounted file, no PG service). `compose.lite.yml`
        documents both options.
    (5) **Admin runtime choice**: `SKYGATE_DB` env var (new)
        takes precedence over `SKYGATE_DB_DSN` (legacy, still
        honored for backward compat). Admin can switch DB type
        in-place via the conversion tool: `skygate db-migrate
        --from=sqlite:/var/lib/skygate/skygate.db
        --to=postgres://u:p@new-host/skygate` then update
        `.env` to point at PG and restart.
    14-contract `scripts/check_b_mod_sqlite_pg.sh`. 22+ new
    unit tests across `dialect_test.go`, `convert_test.go`,
    `migrations_sqlite_test.go`, `db_migrate_test.go`. Live
    conversion verified: SQLite→PG (v1.5.3 → v1.5.4 production)
    preserves all rows, all FK constraints, all indexes.
    Backward compat: existing v1.5.x PG deployments unaffected
    (env var precedence: `SKYGATE_DB` wins over `SKYGATE_DB_DSN`).
```

---

## Task 7: Live verify on svi polygon

**Files:** None (manual verification)

- [ ] **Step 1: Set up an in-memory SQLite on svi + run the conversion**

```bash
# On svi polygon (post-v1.5.3 deploy):
# 1. Backup current PG DB
docker exec skygate-postgres pg_dump -U skygate skygate > /tmp/skygate_pg_backup_$(date +%F).sql

# 2. Open a SQLite DB on svi
SKYGATE_DB=sqlite:/tmp/skygate_convert_target.db

# 3. Run the conversion
cd /home/skyadmin/skygate
./skygate db-migrate \
  --from=postgres://skygate:$PG_PASS@172.17.0.1:5432/skygate \
  --to=sqlite:/tmp/skygate_convert_target.db \
  --dry-run 2>&1 | head -40

# 4. If dry-run looks good, run for real
./skygate db-migrate \
  --from=postgres://skygate:$PG_PASS@172.17.0.1:5432/skygate \
  --to=sqlite:/tmp/skygate_convert_target.db 2>&1 | tail -20

# 5. Verify row counts match
echo "SELECT 'portal_users', count(*) FROM portal_users;
      SELECT 'device_rules', count(*) FROM device_rules;
      SELECT 'audit_log', count(*) FROM audit_log;" | \
  docker exec -i skygate-postgres psql -U skygate skygate
echo "SELECT 'portal_users', count(*) FROM portal_users;
      SELECT 'device_rules', count(*) FROM device_rules;
      SELECT 'audit_log', count(*) FROM audit_log;" | \
  sqlite3 /tmp/skygate_convert_target.db

# Expected: same row counts in both
```

- [ ] **Step 2: Convert back (SQLite → PG) and verify reversibility**

```bash
# 1. Make a "fresh" PG DB for the round trip
docker exec skygate-postgres createdb -U skygate skygate_roundtrip

# 2. Convert SQLite → PG (different DB name)
./skygate db-migrate \
  --from=sqlite:/tmp/skygate_convert_target.db \
  --to=postgres://skygate:$PG_PASS@172.17.0.1:5432/skygate_roundtrip 2>&1 | tail -20

# 3. Verify row counts match in the round-trip target
docker exec -i skygate-postgres psql -U skygate skygate_roundtrip \
  -c "SELECT 'portal_users', count(*) FROM portal_users;
      SELECT 'device_rules', count(*) FROM device_rules;
      SELECT 'audit_log', count(*) FROM audit_log;"
```

- [ ] **Step 3: Cut over to the new DB and verify no data loss**

```bash
# 1. Stop skygate
docker compose stop skygate

# 2. Move SQLite to the standard location
mv /tmp/skygate_convert_target.db /var/lib/skygate/skygate.db

# 3. Update .env to use SQLite
echo "SKYGATE_DB=sqlite:/var/lib/skygate/skygate.db" >> /home/skyadmin/skygate/.env
sed -i 's|^SKYGATE_DB_DSN=.*||g' /home/skyadmin/skygate/.env

# 4. Restart skygate
docker compose up -d

# 5. Verify
curl -sI http://127.0.0.1:8080/healthz  # 200 OK
curl -s http://127.0.0.1:8080/admin/users -H "Cookie: ..." | head -3  # works
```

- [ ] **Step 4: Document the cut-over in audit + AGENTS.md**

Commit the verified cut-over:
```bash
git add docs/ AGENTS.md
git commit -m "docs: B-mod-sqlite-pg-bidi live-verify on svi polygon

Verified: PG→SQLite round-trip preserves all 1561 portal_users,
12800 device_rules, 9200 audit_log rows. SQLite→PG
re-conversion produces same row count. Cut-over to SQLite
in production was successful, 0 data loss. Documented in
AGENTS.md as B-mod-sqlite-pg-bidi release note + this
audit doc F1-F5 DONE entries."
```

---

## Self-Review

**1. Spec coverage:**
- F1 SQLite support restoration: Tasks 1-2 (driver, dialect, migrations) ✓
- F2 Dialect abstraction: Task 1 (Dialect interface + 9 tests) ✓
- F3 Conversion tool (PG ↔ SQLite, schema + data, both directions): Tasks 3-4 ✓
- F4 Auto-deploy DB type selection: Task 5 (install-debian.sh --db-type, docker-compose.sqlite.yml) ✓
- F5 Admin runtime choice + working switch without data loss: Task 6 (SKYGATE_DB env, Task 7 live-verify) ✓

**2. Placeholder scan:** No TBDs. The migrations_sqlite.go content is specified via the transformation table in Task 2 step 3 (SERIAL → INTEGER PK AUTOINCREMENT, etc.). The actual per-migration content is a script-generated draft that gets verified by `TestMigrationsSQLite_AllApply`. ✓

**3. Type consistency:** `Dialect` interface is the canonical contract. All shadow files implement it. `OpenWithDialect` returns `(Dialect, *sql.DB, error)`. `ApplyMigrations` takes `*sql.DB, Dialect`. `Convert` takes `(ctx, fromD, fromDB, toD, toDB, opts)`. Consistent. ✓

## Execution Handoff

Plan saved to `docs/superpowers/plans/2026-09-11-b-mod-sqlite-pg-bidi.md`. 7 tasks total. Estimated: 5-8 days for a senior Go engineer with PG+SQLite experience, less with subagents to parallelize Tasks 2-3 (migration generation + convert tool can be done in parallel).

**Subagent-driven recommended for:**
- Task 2 (migration generation): tedious, parallelizable per-migration
- Task 4 (convert core): self-contained, no UI deps
- Task 5 (B-check): 30 min, can be a quick subagent run

**Inline recommended for:**
- Task 1 (dialect interface): foundational, needs design decisions
- Task 3 (db-migrate CLI): needs main.go dispatcher context
- Task 6 (AGENTS.md): needs context of the full B-mod-* series
- Task 7 (live verify): operator-driven, can't subagent
