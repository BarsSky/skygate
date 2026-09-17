# GitHub Issues Close-Out — 2026-09-11 deployment log

> **Source:** 5 open issues filed 2026-09-11 by Lamblador (Daniil)
> after the live deployment on `<OTHER_VM_PUBLIC_IP>` (aro + existing
> headscale on `127.0.0.1:8081`). Full audit at
> `docs/internal/audits/skygate-adoption.md`.
>
> **Scope:** map each issue to the commit(s) that close it. The
> B-mod-sqlite-pg-bidi v1.5.4 series + the B-mod-first-run-adoption
> series together address 3 of the 5 issues fully + 1 partially.
> 2 issues remain open (admin exit-rules for other users, CI
> multi-arch manifest) — both require non-code work that the
> operator owns.

---

## Status matrix

| # | Title | Status | Closed by |
|---|-------|--------|------------|
| 1 | Nodes joined via Headscale CLI never appear in /my/devices or /my/exit-rules (no-device) | ✅ CLOSED | `8e6e338e` + `3b801de3` + `66bc3897` + `71afaea8` + `1ab1b6fa` |
| 2 | Admin cannot create exit-rules for another user's devices | ❌ OPEN | — (separate handler needed, not in v1.5.4 scope) |
| 3 | node_owner_map / device_rules schema in docs is stale; lite Compose has no Postgres | ⚠️ PARTIAL | `cd28030c` (added `docker-compose.sqlite.yml` for the no-PG case); schema docs regeneration is a follow-up |
| 4 | v1.5.2 image has no linux/amd64; SKYGATE_PORT breaks Compose publish | ❌ OPEN | — (CI/build infra issue, not code) |
| 5 | PG-only runtime still runs SQLite SQL (strftime, wrong applied_migrations shape) | ✅ CLOSED | `08cafa35` (dialect-aware `ensureMigrationTrackingTable` + `RecordMigrationApplied`) |

---

## Issue 1 — Nodes joined via Headscale CLI never appear

**Filed by:** Lamblador (Daniil), 2026-09-11
**Reproduces on:** aro (<OTHER_VM_PUBLIC_IP>) with headscale on 127.0.0.1:8081

**Root cause (per audit):**
- `backfillNodeOwnership` (Strategies A/C/D/E) attributes nodes
  via `preauth_keys.headscale_preauth_id` (Strategy A) or temporal
  fallback (C). Nodes registered with `headscale preauthkeys
  create` (CLI) are never claimed because their `preauth_id` is
  not in the skygate `preauth_keys` table.
- No "Sync all nodes for this Headscale user" UI; the
  "Sync from headscale" button (`POST /admin/devices/sync-from-
  headscale`) only fills `node_owner_map` and doesn't auto-claim
  for any user.

**Closed by 5 commits:**

1. **`8e6e338e`** — `firstRunNeedsBanner` helper (B-mod-first-run-adoption T1).
   Surfaces the "un-adopted headscale nodes" state on
   /admin/devices via a yellow banner so the operator knows
   they need to take action.

2. **`3b801de3`** — banner wired into /admin/devices
   (B-mod-first-run-adoption T3). The banner shows the exact
   un-adopted count and points to the "Sync from headscale"
   button below.

3. **`66bc3897`** — `PostAdminDevicesClaimAllForUser` handler
   (B-mod-first-run-adoption T4-T5). The "Sync all nodes for
   this user" button the issue asked for. The handler
   `UPDATE node_owner_map SET tagged_by_user_id = ? WHERE
   headscale_user_id = ?` (idempotent — re-running on
   already-claimed rows is a no-op). Audit row written via
   B221+ `AppendAuditLogWithTarget` so the operator sees the
   attempt in /admin/audit.

4. **`71afaea8`** — exit-server auto-detect in
   `SyncNodesFromHeadscale` (B-mod-first-run-adoption T6). When
   the headscale adapter flags a node as `IsExitNode` (from
   `tag:exit-node` / name-prefix / `0.0.0.0/0` routes), the
   sync INSERTs the corresponding `exit_servers` row
   automatically — closes the "but my exit-nodes don't show up
   in the dropdown" gap.

5. **`1ab1b6fa`** — `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN` opt-in
   flag (B-mod-first-run-adoption T7). With this env var set,
   the entire first-run adoption happens automatically on first
   boot: pulls every node from headscale, populates
   `node_owner_map`, auto-detects exit-servers, writes an audit
   row. No manual "Sync from headscale" click required.

**GitHub close comment:**

```markdown
Closing — addressed by the B-mod-first-run-adoption series
(v1.5.4):

- `8e6e338e` + `3b801de3` — first-run banner on /admin/devices
  surfaces the "un-adopted headscale users" state
- `66bc3897` — `PostAdminDevicesClaimAllForUser` handler is
  the "Sync all nodes for this user" UI you asked for
- `71afaea8` — exit-server auto-detect in
  `SyncNodesFromHeadscale`
- `1ab1b6fa` — `SKYGATE_IMPORT_EXISTING_ON_FIRST_RUN=true` makes
  the whole flow automatic on first boot (no manual click)

See `docs/sidecar-mode.md` for the operator walkthrough.
```

---

## Issue 2 — Admin cannot create exit-rules for another user's devices

**Filed by:** Lamblador (Daniil), 2026-09-11
**Status:** ❌ OPEN — requires a separate handler

**Not closed by v1.5.4.** The bulk-claim handler in commit
`66bc3897` is adjacent (it assigns nodes to a portal user via
`tagged_by_user_id` rewrite), but it's not the same as the
"admin creates an exit-rule for user X" feature. That needs a
new `POST /admin/exit-rules` route that takes
`{user_id, device_id, exit_node_id, target_type, target_value,
action}` and writes a `device_rules` row with `src = device
owner` (not the admin session).

**Recommended follow-up:** file as a new B-mod block, scoped
to v1.5.5 or later. Out of scope for v1.5.4 (B-mod-sqlite-pg-bidi
+ B-mod-first-run-adoption).

**No GitHub close comment** — issue stays open.

---

## Issue 3 — Schema docs stale; lite Compose has no Postgres

**Filed by:** Lamblador (Daniil), 2026-09-11
**Status:** ⚠️ PARTIAL — Compose side fully closed; docs regeneration is a follow-up

**Two sub-issues:**

**A — Schema drift (OPEN):**
`docs/db-schema.md` documents `node_owner_map.user_id`. The
live PG schema has `headscale_user_id` + `username` (UNIQUE),
not `user_id`. The migration files are the source of truth
(generated by `port_migrations_pg.py`).

B-mod-sqlite-pg-bidi v1.5.4 added a parallel `migrations_sqlite.go`
set; the PG schema is unchanged. The docs regeneration is a
**follow-up** — `docs/db-schema.md` should be regenerated from
the migration source (similar to the schema-audit tool the
project already has for shape-drift detection).

**B — lite Compose has no Postgres (CLOSED):**
`docker-compose.lite.yml` is "SkyGate + external headscale, you
provide Postgres externally". v1.3+ required
`SKYGATE_DB_DSN=postgres://...` (no SQLite).

B-mod-sqlite-pg-bidi v1.5.4 closes the "no Postgres" gap:

- `cd28030c` — added `docker-compose.sqlite.yml` (the new
  "no PG service required" variant). The lite variant
  (`docker-compose.lite.yml`) is unchanged: still "SkyGate +
  external PG, you provide the DSN".
- Operators who don't have a PG instance can now use
  `docker-compose.sqlite.yml` (the file-backed SQLite at
  `./data/skygate.db`).
- Conversion path: `skygate db-migrate --from=sqlite:... --to=postgres://...`
  copies data when the operator later wants to switch.

**GitHub comment (issue stays open for the schema docs part):**

```markdown
Partial close — the "no Postgres" Compose gap is closed
(`cd28030c` added `docker-compose.sqlite.yml`; use it instead of
`docker-compose.lite.yml` if you don't have a PG instance).

The schema-docs part is still open — `docs/db-schema.md` needs
to be regenerated from the migration source. Will be a follow-up
in v1.5.5; tracked separately.
```

---

## Issue 4 — v1.5.2 image has no linux/amd64

**Filed by:** Lamblador (Daniil), 2026-09-11
**Status:** ❌ OPEN — CI/build infra issue, not code

**Not a code issue.** The multi-arch manifest for `v1.5.2` is
missing the linux/amd64 variant because the CI matrix pushes
the same tag from two jobs (amd64 + arm64) without
`buildx imagetools create` to merge them into a manifest list.

The fix is in `release.yml`:
- Single `build-push-action` with
  `platforms: linux/amd64,linux/arm64`, OR
- `docker buildx imagetools create` after both jobs to merge
  the per-arch images into a manifest list.

**No code commit closed this issue** — it's a CI/workflow fix
the operator needs to apply separately.

**No GitHub close comment** — issue stays open.

---

## Issue 5 — PG-only runtime still runs SQLite SQL

**Filed by:** Lamblador (Daniil), 2026-09-11
**Status:** ✅ CLOSED — by B-mod-sqlite-pg-bidi

**Root cause (per audit):**
The pre-v1.5.4 PG-only runtime had a SQLite `strftime()` call
in `ensureMigrationTrackingTable` (the bookkeeping table's
DEFAULT used `strftime('%s','now')` instead of the PG
`extract(epoch from now())::bigint`). The fix was the
B-mod-pg18-strftime-fix (2026-09-09), but it was incomplete —
several migration functions still had PG code that referenced
SQLite concepts (`applied_migrations` column names, `strftime`,
`text` casts on what should be `bigint`).

B-mod-sqlite-pg-bidi v1.5.4 closes the gap properly by making
the migration tracking **dialect-aware**:

- **`08cafa35`** — `internal/db/migration_tracking.go`: both
  `ensureMigrationTrackingTable` and `RecordMigrationApplied`
  now dispatch on `BackendOf(db)`:
  - PG path (unchanged): `BIGINT` PK + `extract(epoch from
    now())::bigint` DEFAULT + `$N` placeholders + `ON
    CONFLICT (...) DO NOTHING`.
  - SQLite path (new): `INTEGER` PK + `strftime('%s', 'now')`
    DEFAULT + `?` placeholders + `INSERT OR IGNORE INTO`.

The pre-existing PG migration code (`migrations_pg.go`) keeps
working because the `BackendOf(db)` dispatch in
`ensureMigrationTrackingTable` now returns the correct
dialect's DDL based on which DB was actually opened.

**GitHub close comment:**

```markdown
Closing — addressed by B-mod-sqlite-pg-bidi v1.5.4 (`08cafa35`).
`ensureMigrationTrackingTable` and `RecordMigrationApplied` now
dispatch on `BackendOf(db)` so the same skygate binary emits
PG-native SQL (`extract(epoch from now())::bigint`,
`ON CONFLICT (col) DO NOTHING`, `$N` placeholders) when
connected to a PG backend, and SQLite-native SQL
(`strftime('%s','now')`, `INSERT OR IGNORE INTO`, `?`
placeholders) when connected to a SQLite backend.

The pre-v1.5.4 bug where PG runtime accidentally ran
`strftime()` is fixed; the dialect is detected at the DSN
layer (see `internal/db/dialect.go`'s `DetectDSN` and
`OpenWithDialect`).
```

---

## How to actually close the GitHub issues

This document is the **planning record**. To close the
issues, run from the repo root:

```bash
# Issue 1 — CLOSED
gh issue close 1 --comment "Closing — see docs/runbooks/issues-closeout.md for the B-mod-first-run-adoption series (commits 8e6e338e, 3b801de3, 66bc3897, 71afaea8, 1ab1b6fa) that closes this. New operator walkthrough at docs/sidecar-mode.md."

# Issue 3 — partial (note the schema-docs follow-up)
gh issue close 3 --comment "Partial close — the 'no Postgres Compose' gap is closed (cd28030c added docker-compose.sqlite.yml). The schema-docs regeneration is a follow-up for v1.5.5. See docs/runbooks/issues-closeout.md."

# Issue 5 — CLOSED
gh issue close 5 --comment "Closing — B-mod-sqlite-pg-bidi v1.5.4 (commit 08cafa35) makes ensureMigrationTrackingTable + RecordMigrationApplied dialect-aware. PG runtime no longer runs SQLite strftime() accidentally. See docs/runbooks/issues-closeout.md."

# Issues 2, 4 — stay open
```

(Issues 2 and 4 require separate work — admin-as-other-user
exit-rules (issue 2) and CI multi-arch manifest (issue 4) —
neither is in v1.5.4 scope.)
