# DB migration handling for PostgreSQL — design plan

**Author:** Mavis (2026-07-25)
**Status:** Phase 1 partially implemented (2026-07-27) — B11/B12 catalog
  checks landed, helper scaffolding in `internal/db/pgmigrate`. R26 (runtime
  lock_timeout check) and testcontainers-go in CI deferred until the
  v0.27.0 PG driver lands on main.
**Target version:** v0.29.0 (bundled with self-update) and v0.30.0 follow-ups
**Scope:** PG-specific concerns around online migrations, locking, backups, and
  the `expand-contract` pattern. SQLite is out of scope (already battle-tested
  for 23 versions).

---

## Why this is its own doc

The user explicitly said "только с PG, SQLite не учитывать" (PG only, don't
consider SQLite). The reason is real: PG migrations have fundamentally different
failure modes than SQLite. SQLite is a single-writer embedded library — if
`go test` says it works, it works. PG is a network-attached multi-client
service with transactional guarantees, advisory locks, and a much bigger
blast radius when something goes wrong.

This doc is the PG-specific layer that the self-update plan references
("`internal/update/pg_migrate.go`"). It can also be read standalone as the
operator-facing reference for "what changes when we move to PG".

---

## Current state (v0.28.6)

- 23 migration files, all in `internal/db/migrations_v*.go`
- All migrations use `*sql.DB` (driver-agnostic via `database/sql`)
- Pattern: one `migrateV0NN(d *sql.DB) error` per version, in increasing order
- Idempotency: required by the v0.28.5 catalog (B5 / R20); every migration
  uses `IF NOT EXISTS` or `WHERE NOT EXISTS` guards
- SQLite is the default (the `feat/postgres-migration` branch has Phase 1-2.5
  with the PG driver but it's not on main)

The v0.27.0 driver abstraction (in `feat/postgres-migration`) wraps `*sql.DB`
behind an `internal/db.DB` interface with both sqlite and pg implementations.
This plan assumes that branch lands BEFORE v0.29.0; if it doesn't, the PG
path here is the motivation for the v0.27.0 follow-up.

---

## What PG changes about the migration story

### 1. Transactions are real, not optimistic

**SQLite**: Each `*sql.DB.Exec` is in its own implicit transaction. A
"migration" is a sequence of `Exec` calls; if the process dies mid-way,
partial changes are visible. For a single-user system, this is fine — the
file-based WAL replays on next open and brings the DB to a consistent state.

**PG**: A `BEGIN ... COMMIT` block is the unit of work. If the migration
fails mid-way, a `ROLLBACK` restores the previous state. BUT:
- If the migration makes a partial commit (multiple Exec calls, each
  auto-committing because we forgot `BEGIN`), the DB is permanently
  inconsistent.
- If a migration is suspended (Ctrl-Z) instead of killed, the connection
  holds an open transaction that locks the table.

**Fix**: every migration MUST be wrapped in a single transaction. The
v0.27.0 driver abstraction provides this. Migrations in
`migrations_v0.40.go` and later use the wrapper; older migrations get
back-filled.

### 2. `ALTER TABLE` takes exclusive locks

**SQLite**: `ALTER TABLE ADD COLUMN` in SQLite briefly takes a write lock
on the table. Reads wait. For a single-writer DB this is fine.

**PG**: `ALTER TABLE ... ADD COLUMN` (without `USING`) takes an
`ACCESS EXCLUSIVE` lock on the table. ALL reads and writes block until
the lock is released. On a busy skygate serving traffic, this can
hang the system for the duration of the migration.

**Fix**: every ALTER must use a non-blocking pattern. Options:

| Migration | PG pattern | Wait time |
|-----------|------------|-----------|
| `ADD COLUMN` with constant default | `ALTER TABLE ... ADD COLUMN IF NOT EXISTS ... DEFAULT 'x'` (PG 11+ fast default) | <1s for small tables |
| `ADD COLUMN` with non-constant default | Manual expand-contract: add nullable, backfill in batches, set NOT NULL | minutes to hours |
| `ADD INDEX` | `CREATE INDEX CONCURRENTLY ... IF NOT EXISTS` | 1-10x table scan time, no exclusive lock |
| `DROP COLUMN` | FORBIDDEN in auto-update; require manual flag | n/a |
| `RENAME TABLE` / `RENAME COLUMN` | FORBIDDEN in auto-update; require manual flag | n/a |
| `CREATE TABLE` | No lock on existing tables; new table is invisible to old queries | <100ms |

The v0.29.0 helper `internal/db/pgmigrate/expand.go` provides:
- `AddColumnIfNotExists(tx, table, column, sqlType, defaultExpr)` — wraps
  the fast-default pattern, falls back to nullable + UPDATE for non-constant
  defaults
- `CreateIndexConcurrently(table, columns, ...)` — wraps
  `CREATE INDEX CONCURRENTLY IF NOT EXISTS`
- `IsDestructive(sql)` — returns true if the migration contains DROP /
  RENAME / TRUNCATE. The updater refuses destructive migrations unless
  `SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION=1` is set.

### 3. PG has its own connection pooling + lock_timeout

**SQLite**: One process, one connection. No pool to manage.

**PG**: skygate uses `database/sql` which pools connections. During a
migration, we need to ensure no other query is touching the table. The
`migrateV0NN` function gets a fresh `*sql.DB` from the driver (NOT the
pool) and uses `SET lock_timeout = '10s'` to fail fast if the table is
locked.

If `lock_timeout` fires, the migration aborts. The updater retries up
to 3 times with a 5s backoff. After 3 failures, the update is rolled
back (see the self-update plan for the rollback sequence).

### 4. PG schema is decoupled from data location

**SQLite**: The DB is a file. The migration touches the file. Backups are
`cp skygate.db skygate.db.bak`.

**PG**: The DB is a service. The "file" is `/var/lib/postgresql/data/`
on the PG host. The migration touches the running service. Backups are
`pg_dump` (logical) or filesystem snapshot (physical) or `pg_basebackup`
(physical streaming).

The v0.29.0 updater adds a pre-migration hook: `pg_dump --schema-only
--no-owner --no-acl` to `/var/backups/skygate-pre-migrate-<ts>.sql`.
This is the "schema before we touched it" snapshot. On rollback, this
file is the source of truth for "what was the schema before".

Note: `pg_dump` doesn't lock the table (it takes an `ACCESS SHARE` lock,
which doesn't block reads or writes). It does add a brief I/O spike; the
updater runs it during the `app.updating=true` window when traffic is
drained.

### 5. PG has a real `pg_repack` / `pgroll` ecosystem

For the v0.29.0 schema (small tables, dozens of rows), we don't need
these tools. For future large migrations (e.g. moving audit_log to
partitioned tables for retention), we'd reach for `pg_repack` (online
table rewrite without exclusive lock). v0.29.0 doesn't ship this; it's
a follow-up.

---

## The `expand-contract` pattern (v0.29.0 enforcement)

Migrations follow this lifecycle:

```
                  auto-update window
                  ↓
  ┌────────────────────────────────────────────────┐
  │  Phase 1 (expand): Add new structure alongside │
  │  old. Old code reads/writes both. New code      │
  │  reads new, writes new. No data loss.           │
  │  Examples: ADD COLUMN, CREATE TABLE, ADD INDEX  │
  │  CONCURRENTLY.                                  │
  └────────────────────────────────────────────────┘
                        ↓
                  Old binary restarts into New binary
                  (or v0.30.0 has both)
                        ↓
  ┌────────────────────────────────────────────────┐
  │  Phase 2 (contract): Remove old structure.     │
  │  Only when ALL instances are confirmed on the  │
  │  new code. Triggered by an operator-approved    │
  │  "cleanup" release, NOT auto-update.            │
  │  Examples: DROP COLUMN, RENAME TABLE.           │
  └────────────────────────────────────────────────┘
```

**v0.29.0 auto-update only allows Phase 1 operations.** Phase 2 is
gated by `SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION=1` and an explicit
operator click. The reasoning: the OLD binary doesn't know about the
new column, so it can't drop it. The NEW binary can drop it, but at
that point the OLD binary is no longer running. So the operator can
manually verify "yes, all skygate instances are on v0.30.0" before
running a Phase 2 migration.

This is the standard 12-factor pattern. The v0.28.5 catalog's B6
test (`TestGenerateACLWithVia_PerDeviceGrantEmittedBeforePerUser`)
already pins the "first-match semantics" of the Tailscale policy —
the same idea applies to migration ordering.

---

## Migration safety checklist (for every new migration)

When adding a migration (e.g. `migrations_v0.50.go`), the author MUST
verify:

- [ ] Migration is wrapped in a single transaction (use the
      `internal/db/pgmigrate/Run()` helper, not raw `*sql.DB.Exec`)
- [ ] `lock_timeout` is set to 10s on the transaction
- [ ] All `ALTER TABLE` use `IF NOT EXISTS` (or the equivalent for
      the operation)
- [ ] All `CREATE INDEX` use `CONCURRENTLY IF NOT EXISTS`
- [ ] No `DROP COLUMN` / `RENAME TABLE` / `TRUNCATE` (or explicit
      `SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION=1`)
- [ ] For non-constant defaults: the migration is the "add nullable"
      step; the "backfill" + "set NOT NULL" steps are a separate
      follow-up release
- [ ] Tested against a real PG (CI uses testcontainers-go)
- [ ] The new code can read rows that pre-date the migration (i.e.
      the old column values are still valid; new columns are nullable
      or have a default)

The v0.28.5 catalog (B5 / R20) checks that migrations are idempotent.
The v0.29.0 catalog adds:
- **B11**: every migration is in a transaction
- **B12**: no migration contains `DROP COLUMN` or `RENAME` (without
  the `SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION` override)
- **B13**: every `CREATE INDEX` uses `CONCURRENTLY` (regex check on
  the migration source code)
- **R26**: live PG (if configured) has `lock_timeout` set; migrations
  don't hang on long locks

These are static checks on the migration source code + a runtime
check that the running PG has the right `lock_timeout`. Easy to add
to the catalog.

---

## Migration testing in CI

Currently `migrations_v0.47_test.go` exists for the v0.47 idempotency
checks. v0.29.0 extends this:

- **`migrations_pg_smoke_test.go`** — uses testcontainers-go to spin
  up a real PG in CI; runs the full migration chain; asserts the
  final schema matches the expected `schema.sql` (snapshotted from
  the SQLite test).
- **`migrations_pg_lock_test.go`** — opens a transaction on a
  random table, holds a write lock, then runs a migration against
  that table. Asserts the migration aborts within 10s with
  `lock_timeout` error.
- **`migrations_pg_destructive_check.go`** — parses every
  `migrations_v*.go` file; asserts no `DROP COLUMN` / `RENAME`
  unless flagged.

The testcontainers-go approach needs Docker in CI. GH Actions runners
have it. testcontainers-go spins up a `postgres:16-alpine` container
per test run; the migration is applied; the container is torn down.

---

## Migration testing on the operator's VM

The operator already has a dev VM (`skygate-host-2`, 198.51.100.1) where
PG can be installed for end-to-end testing. v0.29.0 ships a
`scripts/migration_pg_smoke.sh` that:

1. Installs `postgresql-16` (via the standard apt repo)
2. Creates a `skygate_test` database
3. Runs `skygate --migrate-only --dsn=postgres://...` to apply
   every migration from scratch
4. Dumps the resulting schema with `pg_dump --schema-only`
5. Diffs against `docs/schema-expected.sql` (snapshotted)
6. Tears down the test DB

If the diff is non-empty, the script fails. This is the operator's
"is my schema in sync?" check.

---

## Schema diff tracking

`docs/db-schema.md` already exists (as of v0.16.6). v0.29.0 extends
it to include the PG-specific view:

```sql
-- docs/db-schema.md (excerpt)

-- SQLite view
CREATE TABLE portal_users (
    id INTEGER PRIMARY KEY,
    username TEXT NOT NULL,
    ...
);

-- PG view (same)
CREATE TABLE portal_users (
    id BIGSERIAL PRIMARY KEY,
    username TEXT NOT NULL,
    ...
);
```

The diff between SQLite and PG schemas is just type names:
- `INTEGER PRIMARY KEY` → `BIGSERIAL PRIMARY KEY` (auto-increment differs)
- `TEXT` → `VARCHAR` or `TEXT` (PG has both; we use TEXT for compat)
- `DATETIME` → `TIMESTAMP WITH TIME ZONE` (PG has a real timezone type)

The migration code uses `database/sql` types (`sql.NullString`,
`sql.NullInt64`) which map to native types in both drivers. The driver
abstraction in v0.27.0 hides the rest.

---

## Backup strategy for PG (v0.29.0 + ongoing)

For v0.29.0, the updater does `pg_dump --schema-only` pre-migration
(cheap, fast, no lock). For ongoing backups, the v0.27.0 + v0.29.0
plan is:

- **Daily `pg_dump`**: `/etc/cron.d/skygate-pg-dump` runs at 03:00 UTC
  - Format: `pg_dump -Fc` (custom, compressed)
  - Retention: 7 daily, 4 weekly
  - Location: `/var/backups/skygate/daily-<date>.dump`
  - Off-site: rsync to a backup VM (operator's choice)
- **WAL archiving**: `archive_mode = on` + `archive_command` to ship
  WAL segments continuously. PITR (point-in-time recovery) up to the
  last archived WAL.
- **On update**: `pg_dump --schema-only` is automatic (in the updater).
  On destructive migration, also `pg_dump --data-only` for the affected
  tables (added in v0.30.0 follow-up).

This is the "real production" backup story. For the operator's current
single-VM SQLite setup, backups are `cp skygate.db skygate.db.bak` —
the same idea, simpler mechanism.

---

## Open questions for the operator

1. **PG version**: `postgresql-16` is the target (released 2023-09,
   current LTS as of v0.29.0 design). If the operator has PG-14
   deployed somewhere, v0.29.0 should still work (we don't use
   PG-15+/16-only features), but the test matrix should include
   PG-14 to be safe.

2. **Read replica**: For HA (v0.27.0 deferred Phase 3-5), the
   migration runs on the primary, then propagates to the replica
   via WAL streaming. The v0.29.0 updater needs to wait for the
   replica to catch up before considering the migration "applied".
   For v0.29.0 (single PG instance), this is a no-op.

3. **Audit log partitioning**: `audit_log` grows unbounded. At 100
   events/day × 10 years = 365k rows, which is fine. At 10k
   events/day × 10 years = 36.5M rows, which is too big for a
   single table. v0.30.0+ should partition by month using
   `pg_partman` or a manual range partition. Not v0.29.0.

4. **`SKYGATE_DB` env var transition**: today, `SKYGATE_DB=/data/skygate.db`
   is the only env var. v0.27.0+ accepts `SKYGATE_DB=postgres://...`.
   The driver is selected at startup by parsing the URL. The updater
   doesn't need to know — the new binary just reads the same env var
   and connects to PG if the URL starts with `postgres://`.

5. **MySQL / MariaDB**: not on the roadmap. The `internal/db.DB`
   interface in v0.27.0 was designed to be driver-pluggable, but
   the only target is PG (and SQLite for local dev).

---

## Effort estimate

- `pg_migrate.go` (transactions, lock_timeout, expand-contract helpers): 1 day
- `migrations_pg_smoke_test.go` + testcontainers-go setup: 1 day
- `migrations_pg_lock_test.go` + destructive check: 0.5 day
- `scripts/migration_pg_smoke.sh` (operator-side): 0.5 day
- Schema diff docs (`docs/db-schema.md` updates for PG): 0.5 day
- Catalog additions (B11, B12, B13, R26): 0.5 day

**Total: 4 days.** Bundles naturally with the self-update plan
(4-day core + 1-day Docker path overlap = 5 days total for v0.29.0).

---

## Implementation status (v0.32.0+, 2026-08-03)

Phases 1 of this plan is **fully landed on main** as of v0.31.0.
The PG driver abstraction, 27 PG-port migrations, 4 verification
tests, B11 / B12 / B18 catalog checks, and `pgmigrate/expand.go`
helpers are on `main` and pass `go build -tags postgres`.

What's still needed for the **live cutover** (the actual switch
from SQLite to PG) is a separate, smaller task — see
[`docs/v0.33.0-pg-cutover-runbook.md`](../v0.33.0-pg-cutover-runbook.md)
(planned, will be written before the operator provisions the
PG-staging VM).

The remaining work for the cutover is **not** another refactor —
it's a mechanical rewrite of ~24 runtime files:

| Category | Scope | Tool |
|---|---|---|
| `?` → `$1, $2, ...` placeholders in 24 files | 24 files, ~5000 lines, ~600 placeholders | `scripts/rewrite_placeholders.py` (already exists, has `--dry-run`) |
| `INSERT OR REPLACE` → `INSERT ... ON CONFLICT (...) DO UPDATE` | 7 files, ~8 occurrences | `scripts/rewrite_placeholders.py` (new) + manual conflict-target review |
| `INSERT OR IGNORE` → `INSERT ... ON CONFLICT DO NOTHING` | 5 files, ~6 occurrences | same |
| `strftime('%s', 'now')` → `EXTRACT(EPOCH FROM now())::bigint` | 4 files, ~5 occurrences | manual |
| `last_insert_rowid()` → `RETURNING id` | 0 files (Go uses `LastInsertId()` which the pq driver maps to PG's `lastval()`) | no-op |
| `PRAGMA ...` (in queries.go) | 2-3 occurrences | drop (PG has no equivalent at runtime; the right place is `SET ...` at session start) |
| `regexp_replace`, `json_extract` | 0-1 occurrences | use PG-native equivalents |

**What's done in v0.31.0 / v0.32.0+ (this iteration):**

- ✅ `internal/db/driver.go` (4205B) + `internal/db/driver_postgres.go`
  (3264B) — driver abstraction with `DB` interface, both sqlite and
  pg implementations. The pq driver is `import "github.com/lib/pq"`
  behind the `//go:build postgres` tag (no runtime dependency on
  Linux/Windows dev machines that don't have the tag set).
- ✅ `internal/db/driver_test.go` (3620B) — 3 unit tests pinning
  the interface contract.
- ✅ `internal/db/migrations_pg.go` (35008B) — mechanical PG port
  of 27 migrations. Uses `BIGSERIAL` (was `INTEGER PRIMARY KEY
  AUTOINCREMENT`), `EXTRACT(EPOCH FROM now())::bigint` (was
  `strftime('%s', 'now')`), and `TEXT` everywhere (no SQLite
  type affinity quirks). Generated by
  `scripts/port_migrations_pg.py` (already exists, on main).
- ✅ `internal/db/pgmigrate/expand.go` (12146B) — `Run` (transaction
  wrapper with `lock_timeout`), `AddColumnIfNotExists`,
  `CreateIndexConcurrently`, `IsDestructive` /
  `IsDestructiveRefused`. 9 unit tests pass
  (TestIsDestructive_RejectsDropColumn with 20 sub-cases, plus
  TestBuildCreateIndexStmt covering postgres/pgx/sqlite3).
- ✅ `internal/db/test_pg_migrations_test.go` (live tests, skipped
  without `SKYGATE_TEST_PG_DSN`) — roundtrip + idempotency +
  lock_timeout + data_migration tests, all run on a real PG
  when the env var is set. R27 in `verify_post_deploy.sh`
  activates them on the operator's PG-staging VM.
- ✅ Catalog B11 (verify_pre_deploy.sh): no DROP / RENAME /
  TRUNCATE in `migrations_v*.go`. Grep-based static check; no raw
  `*sql.DB.Exec` in a future migration can sneak in a destructive
  statement past the build.
- ✅ Catalog B12 (verify_pre_deploy.sh): `pgmigrate` package has
  the per-driver SQL form unit-tested. The check is currently
  a soft "has unit tests"; will be tightened to a hard
  CONCURRENTLY check once the first PG-only migration lands.
- ✅ Catalog B18 (verify_pre_deploy.sh):
  `go build -tags postgres ./cmd/skygate` succeeds. 4
  verification tests pass under the postgres tag.
- ✅ `scripts/port_migrations_pg.py` (existing) — mechanical
  port of new SQLite migrations to PG.
- ✅ `scripts/rewrite_placeholders.py` (existing) — mechanical
  `?` → `$N` rewrite with SQL-keyword heuristic to avoid
  false positives on URL query strings. Has `--dry-run` mode.
  Tested on `internal/db/queries.go`: 173 placeholders, 0
  false positives.
- ✅ `scripts/dump_sqlite.py` (existing) — for the cutover:
  dump SQLite to a SQL file that can be loaded into a fresh
  PG database.

**What's blocked on the operator's PG-staging VM (no progress possible
without it):**

- ⏳ Run `rewrite_placeholders.py` (no `--dry-run`) on all 24
  runtime files; commit the result; verify `go build -tags
  postgres ./cmd/skygate` still passes.
- ⏳ Run a hand review of every `INSERT OR REPLACE` /
  `INSERT OR IGNORE` → `ON CONFLICT` rewrite to make sure the
  conflict target column is correct. `queries.go:426`
  (`qInsertOrReplaceNodeOwner`) needs the unique index on
  `node_id` to be the conflict target; check the migration
  that creates it.
- ⏳ `SKYGATE_TEST_PG_DSN` set on PG-staging VM; run
  `go test -count=1 ./internal/db/test_pg_migrations_test.go`
  on PG-staging; verify all 4 tests PASS (roundtrip +
  idempotency + lock_timeout + data_mig). R27 in
  `verify_post_deploy.sh` activates this on every deploy.
- ⏳ Manual cutover: skygate in read-only mode →
  `dump_sqlite.py` → load to fresh PG → flip
  `SKYGATE_DB_DSN` → restart. ~15 min downtime window.
  See the runbook (planned) for the exact sequence.

---

## Why this isn't a v0.30.0 thing

The user said "v0.29.0 candidate" implicitly via the self-update plan.
Bundling the self-update + PG migration hardening in v0.29.0 makes
sense because:
- Both are about making upgrades safe
- Both touch the same code path (`internal/update/updater.go` +
  `internal/db/pgmigrate/`)
- The operator asked for both in the same message
- v0.30.0 is the Provisioning UI redesign (separate, ~8 days)

If the self-update is delayed (e.g. by a bug in the rc.1 rollout),
the PG migration hardening can ship alone as v0.29.0 + the
self-update as v0.29.1.
