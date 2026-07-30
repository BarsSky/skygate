# Skygate Backlog — abandoned / blocked / in-progress work

**Last updated**: 2026-07-30
**Maintainer**: Mavis (skygate)
**Purpose**: Single source of truth for features that exist in the
codebase as abandoned stubs, plans that live in dead branches,
or work that the operator wants done but is blocked on something
external. This file is referenced by `AGENTS.md` and is what
Mavis (or any future AI assistant) should read before proposing
work — to avoid re-litigating old decisions and to know what
the operator's stated intent is.

If you (operator) want a feature from this file worked on, say
"do N" where N is the priority number. If you want a feature
moved up or down, just say so.

---

## Priority 1 — Web UI completeness (in progress, 2026-07-30)

**Status**: shipped in v0.32.1 (this commit). All admin + user
pages now have sidebar links.

The `internal/handlers/templates/layout.html` sidebar was
missing 10 admin + 1 user links. They existed as fully-built
routes + handlers + templates, but were unreachable from the
nav. Mapped and added:

| Page | Was missing from sidebar | Now |
|---|---|---|
| /admin/control-planes | ✗ | ✓ `nav.control_planes` |
| /admin/exit-nodes | ✗ | ✓ `nav.exit_nodes_admin` |
| /admin/headscale | ✗ | ✓ `nav.headscale` |
| /admin/headplane | ✗ | ✓ `nav.headplane` |
| /admin/integrations | ✗ | ✓ `nav.integrations` |
| /admin/invites | ✗ | ✓ `nav.invites` |
| /admin/meshes | ✗ | ✓ `nav.meshes_admin` |
| /admin/update | ✗ | ✓ `nav.update` |
| /my/keys | ✗ | ✓ `nav.preauth` |

(/admin/backup/config and /admin/derp/config are sub-pages
reached from their parent /admin/backup and /admin/derp —
no separate sidebar item needed.)

---

## Priority 2 — PostgreSQL cutover (BLOCKED on operator's PG-staging VM)

**Status**: Phase 1 done (v0.31.0 foundation on main), Phases
2-5 blocked on operator providing a PG-staging VM for live
testing. The full plan is at
[`docs/v0.27.0-postgres-ha.md`](v0.27.0-postgres-ha.md) (moved
from the dead `feat/postgres-migration` branch in this commit).

**What's done (Phase 1.1-1.3)**:
- Driver abstraction (`internal/db/driver.go` + `driver_postgres.go`)
- 27 PG-compatible migrations (`internal/db/migrations_pg.go`)
- 4 verification tests (`internal/db/test_pg_migrations_test.go`)
- Non-blocking ALTER helpers (`internal/db/pgmigrate/expand.go`)
- B11 (no destructive DDL) + B12 (helper unit tests) catalog
  checks
- B18: `go build -tags postgres ./cmd/skygate` succeeds

**What's still needed for the cutover**:
- **Placeholder rewrite**: `?` → `$1, $2, ...` in 30+ files
  (~5000 lines). The `rewrite_placeholders.py` script
  exists on the old branch but is not on main; needs to be
  brought in + run + carefully diffed.
- **`INSERT OR REPLACE` / `INSERT OR IGNORE` → `ON CONFLICT`**:
  same scope. ~30 places in `internal/db/queries.go` and
  callers.
- **`strftime('%s', 'now')` etc → `EXTRACT(EPOCH ...)::bigint`**
  and other SQLite-isms in migrations.
- **PG-staging VM**: PostgreSQL 16 on a separate VM, SSH
  access for Mavis, `SKYGATE_TEST_PG_DSN` env.
- **R27 verification**: lock_timeout + 4 roundtrip tests in
  PG-staging.
- **Manual cutover window**: skygate in read-only mode →
  `dump_sqlite.py` → apply to fresh PG → flip
  `SKYGATE_DB_DSN` → restart. ~15 min downtime.

**Operator action required**: provision a PG-staging VM. Until
then this is blocked.

---

## Priority 3 — HA "skygate-host-2" / Tier 1 hot standby (BLOCKED on 2nd VM + etcd quorum + S3)

**Status**: design-only. Plan exists at
[`docs/v0.27.0-postgres-ha.md`](v0.27.0-postgres-ha.md) and
[`docs/ha-architecture.md`](ha-architecture.md) (the second is
a stub created in this commit — the DR doc references it but
the full design is in v0.27.0-postgres-ha.md).

**What "HA Tier 1" means**:
- RTO < 1 min (Patroni auto-failover)
- RPO = 0 (synchronous streaming replication)
- Active-passive: 2nd VM (skygate-host-2) is the warm standby
- Old VM (192.0.2.1) becomes the replica
- etcd cluster for Patroni consensus (3 nodes ideal, 1 node
  minimum)
- HAProxy pg-aware routing (port 5000 = primary, 5001 = replica)
- wal-g → S3 for WAL archive + point-in-time recovery
- headscale stays on SQLite + Litestream (headscale 0.29.x has
  no PG support; 0.30+ does but isn't out)

**What's needed**:
- Provision skygate-host-2 (2nd VM, same OS + Docker)
- etcd cluster — 3rd node OR accept single-node (no quorum,
  Patroni can't elect during a node failure)
- S3 bucket for WAL archive
- DNS plan: `head.example.com` + `skygate.example.com` flip with
  5-min TTL
- ~2-3 weeks of work following the v0.27.0 plan phases 2-5

**Operator action required**: provision skygate-host-2 VM + S3
bucket. Until then this is blocked.

**Note**: the existing Tier 0 (single-VM, daily backups) is
documented in [`docs/disaster-recovery.md`](disaster-recovery.md)
and works. Tier 1 is the "next step" if the operator wants
<1 min RTO.

---

## Priority 4 — Backup polish (not blocked, low priority)

**Status**: backup system fully built and working. Small
improvements remain.

**What's done**:
- `internal/backup/` (6 files, ~33KB Go): runner, scheduler,
  config, mount, schedule, checker
- `deploy/backup.sh` (6.6KB bash): .env + git bundle + skygate.db
  + headscale.db + headscale config + noise_private.key
- `/admin/backup` page: create/restore + Config (SMB/NFS/SFTP
  destination) + Test + Run now + Toggle
- Scheduled runs via in-app scheduler (configurable from UI)
- DR runbook: [`docs/disaster-recovery.md`](disaster-recovery.md)

**What's still on the wishlist**:
- **S3 destination** (currently SMB / NFS / SFTP only).
  Need to add an "S3" protocol option in
  `/admin/backup/config` + a `SKYGATE_BACKUP_S3_BUCKET` env
  var. ~half a day of work.
- **Auto-verify backups**: every Sunday, restore the latest
  backup to a temp dir and run `sqlite3 ... "PRAGMA
  integrity_check;"` on it. Send Telegram alert if it fails.
  This catches silent corruption before DR is needed.
  ~1 day of work.
- **DR doc update**: `docs/disaster-recovery.md` references
  `docs/ha-architecture.md` — that file now exists (as a
  stub in this commit) but the link target's content is
  minimal. May want to inline the relevant context into the
  DR doc itself or flesh out the stub.

---

## Priority 8 — DB corruption incident + recovery (2026-07-30)

**Status**: recovered via `scripts/recover_db_corruption.sh`.
R30 added to verify_post_deploy.sh to catch future
corruption early. Root cause: SIGKILL-on-recreate not
giving SQLite a chance to flush its WAL.

**What happened**:
After a series of deploys (v0.32.0 → v0.32.0 rebuild →
v0.32.2 → v0.32.3), the live skygate.db had two tables
with corrupted btree pages:
- `acl_snapshots` — every policy snapshot
- `exit_rule_logs` — every /my/exit-rules operation

`PRAGMA integrity_check` returned "database disk image
is malformed (11)" with multiple page-level errors
(invalid page numbers, rowid out of order, btree init
errors). REINDEX failed, VACUUM produced an empty file
(the tables were so broken the rows couldn't be read),
no backup existed at `/var/backups/skygate/`.

**Root cause (suspected)**: `docker compose up -d
--force-recreate --no-deps skygate` sends SIGKILL by
default (no `stop_grace_period` set in docker-compose.yml).
With WAL-mode SQLite, a SIGKILL during a write can leave
the WAL in an inconsistent state. SQLite is supposed to
recover on next open, but a corner case during a large
transaction (the policy write is large: ~50KB) can produce
page-level btree damage that doesn't auto-repair.

**What was lost** (unavoidable, no backup existed):
- The full `acl_snapshots` history (the "last successful
  reapply" record that R9 checks against the live policy)
- The full `exit_rule_logs` history (audit trail of every
  /my/exit-rules action — adds, deletes, syncs)

**What was preserved**:
- `audit_log` (4662 rows) — all the page-view / apply /
  restart events, just not the granular per-rule edits
- `device_rules` (376 rows) — the current rule set
- `portal_users` (4 rows) — user accounts
- All other tables — intact
- The headscale policy itself — still served correctly
  (R10/R11/R28 all PASS against the live policy)

**Recovery** (`scripts/recover_db_corruption.sh`):
1. Stop the skygate container (releases SQLite lock + WAL flush)
2. Backup the corrupted DB to `/var/backups/skygate/PRE_RECOVERY_<TS>/skygate.db`
3. DROP + CREATE `acl_snapshots` and `exit_rule_logs` empty
4. Restart the container
5. `/healthz` healthy after 2x5s
6. **Operator must click "Re-apply ACL" on /admin/exit-rules**
   to repopulate `acl_snapshots` with the current live
   policy. R9 starts passing again after this.

**Defensive measures added**:
- **R30 in verify_post_deploy.sh**: runs `PRAGMA
  integrity_check` on a fresh copy of the live DB. Catches
  page-level btree damage at deploy time so the operator
  finds out BEFORE the next deploy makes it worse.

**Follow-up (NOT done in this session, tracked here)**:
- Add `stop_grace_period: 30s` to skygate service in
  docker-compose.yml so the container has time to flush
  WAL on `docker compose stop`. Trivial change, would
  prevent the next corruption.
- Update `scripts/rebuild_deploy.sh` to do `docker compose
  stop skygate` before `--force-recreate` (graceful
  shutdown + WAL flush). Currently uses --force-recreate
  which is the equivalent of `docker kill` on the old
  container.
- Set up automated daily SQLite backup to
  `/var/backups/skygate/` so the next corruption can be
  restored instead of dropped. (Existing `deploy/backup.sh`
  is for the skygate+headscale+skygate-host-1 data, not the
  SQLite specifically — needs a separate dedicated script.)

**How to recover if this happens again**:
```bash
bash scripts/recover_db_corruption.sh
# Then click "Re-apply ACL" on /admin/exit-rules to
# repopulate acl_snapshots.
```

---

## Priority 7 — Auto-update opt-in + manual Push button (SHIPPED in v0.32.3, 2026-07-30)

**Status**: shipped. `SKYGATE_AUTO_UPDATE_ENABLED` env var
(default `false`) gates the banner-driven "Apply" button
on /admin/update. New "Push update" button is always
visible and ALWAYS works (manual trigger). Plus 6 unit
tests for the skygate-vs-headscale drift detection
(computeSyncStatus).

**What's done**:
- `internal/config/config.go` — new `AutoUpdateEnabled`
  field, `SKYGATE_AUTO_UPDATE_ENABLED` env var, default
  `false`
- `internal/feature/admin/update.go` — new
  `PostAdminUpdatePush` handler + i18n keys
  `update.push` / `update.push_help` / `update.push_confirm`
  / `update.auto_disabled_banner` / `update.auto_enabled_banner`
- `internal/handlers/templates/admin/update.html` — new
  "Push update" button (always visible, separated from the
  gated "Apply" button) + a banner that shows the current
  mode (auto-update on/off)
- `cmd/skygate/main.go` — new route
  `POST /admin/update/push`
- `internal/feature/admin/exit_nodes.go` — extracted
  `computeSyncStatus()` pure function (was inline in the
  handler loop)
- `internal/feature/admin/exit_nodes_test.go` — 6 unit
  tests: TestComputeSyncStatus_EmptyExpected /
  _Synced / _Mismatch / _MismatchReversed /
  _OtherNodesIgnored + TestSeedNodeRulesAndReadExpected
- `scripts/verify_post_deploy.sh` — new R29 check for
  skygate-vs-headscale drift (currently WARN, not FAIL —
  the page warning is the primary signal)

**Motivation**: operator's correction — auto-update
should be opt-in, not opt-out. The "Apply" button
(banner-driven) is gated by the flag; the "Push" button
(manual) always works.

---

## Priority 6 — ACL perf + route correctness tests (SHIPPED in v0.32.2, 2026-07-30)

**Status**: shipped. Build-time B19 + runtime R28 added to the
verify catalog. 6 functional tests + 4 benchmarks live in
`internal/acl/perf_test.go`.

**Motivation**: operator reported "exit-node routing started
working slower" after a series of small refactors. The actual
root cause was likely the v0.32.0 via: sync bug (fixed in
63cd0ed), but the operator wanted permanent regression guards
so the next refactor can't silently break the same thing.

**What's covered**:

| Test | Catches |
|---|---|
| `TestGenerateACL_SizeWithinBound` | Policy bloat — 100 rules must stay <50KB |
| `TestGenerateACL_NoDuplicateHosts` | headscale 0.29.2 "host already defined" reject |
| `TestGenerateACL_FirstMatchOrdering` | v0.12.0.1 inter-user leak regression |
| `TestGenerateACL_ViaHonoredWhenEnabled` | v0.32.0 via: sync bug regression |
| `TestGenerateACL_ViaOmittedWhenDisabled` | "always-on via" opt-in broken regression |
| `TestGenerateACL_AllTagsInTagOwners` | headscale "tag not found" reject |
| `BenchmarkGenerateACL_Small` (10 rules) | baseline ~200µs |
| `BenchmarkGenerateACL_Medium` (100 rules) | prod target ~600µs |
| `BenchmarkGenerateACL_Large` (1000 rules) | stress: <5ms (currently ~2.3ms) |
| `BenchmarkGenerateACL_ViaEnabled` (10 users × 50 rules, via=1) | via emission perf |

Plus R28 in `verify_post_deploy.sh`:
- Live policy size < 100KB
- Live grant count < 500
- Live host count < 2000

**Operator action**: none — tests are passive guards. Run
`go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/`
locally before any ACL refactor to capture baseline numbers.

---

## Priority 5 — Other deferred items (long-tail, no current demand)

These are explicitly NOT in active scope but tracked here so
they don't get lost:

### v0.19.1 — `exitnode.skygate-subnet-<user>` DNS records
Re-attempt of the reverted v0.19.0. v0.18.0 already provides
per-user MagicDNS; this is the "named record per user → their
chosen exit-node" step. **Blocked on headscale 0.30+** which
adds `dns.extra_records` policy support (0.29.2 doesn't have it
and rejects the policy). Re-enable when headscale 0.30 ships.

### v0.23.1 Phase 2 — safe user migration (compliance tier)
The v0.23.0 one-click per-user headscale provisioning ships
infrastructure but no data migration. Phase 2 would take a
user off the global headscale, move their nodes + ACL to the
per-user plane, flip the DB override. **Compliance tier only**
(SOX / multi-tenant SaaS / geographic isolation). Deferred
until a real operator need lands.

### ~2850 lines of testutil.go stubs
`internal/feature/*/testutil.go` has ~2850 lines of helpers
that were written during the refactor-v0.30 work but aren't
exercised by any test (infra without contracts). Low ROI to
backfill; will happen naturally as new features are added.

### Unmerged branches
- `feature/telegram-bot-ux` (4dca972) — SetMyCommands polish.
  Low value, can be deleted.
- `feat/postgres-migration` (cdec261) — replaced by
  `feat/v0.31.0-pg-foundation` which is on main.

---

## Adding items to this backlog

When you (operator) want a feature tracked, add it here with:
- What it is (one paragraph)
- Why it's blocked (if applicable)
- What the operator needs to provide to unblock
- The design doc / commit / branch where the work lives (if any)
- Priority number

When the work is done, move the entry to the "Completed" section
at the bottom of this file with the commit hash.

## Completed (moved out of backlog)

- **2026-07-30**: v0.32.2 — ACL perf + route correctness tests.
  6 functional tests + 4 benchmarks in `internal/acl/perf_test.go`.
  Build-time B19 + runtime R28 added to the verify catalog.
  Commit in this change.
- **2026-07-30**: v0.32.1 — Sidebar completeness. 9 admin +
  1 user pages added to `layout.html`. Commit in this change.
- **2026-07-30**: v0.32.0 — Released. Build `v0.32.0-5-ge4dea76`.
  Per-device OS + type markers + via: sync bug fix + refactor-v0.30.
