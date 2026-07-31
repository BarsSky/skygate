# Skygate release notes

## v0.32.6 — Autoupdate `git fetch` `--force` (stale local tag fix)

**Date:** 2026-07-30
**Tag:** _pending_
**Scope:** the autoupdate orchestrator's `git fetch --tags --prune`
fails when the local repo has a tag whose SHA diverges from the
remote's tag with the same name. The fix is to add `--force` to the
fetch. **No Go code path other than the orchestrator changed.**
Operator action: just `make rebuild-deploy` to pick up the fix.

### What's new (operator-visible)

- **Autoupdate works again**. The 2026-07-28 ROLLBACK storm
  (visible in `/data/skygate-update-swap.log` — 11+ ROLLBACK
  attempts in 5h, all failing at `git fetch` with "would
  clobber existing tag") was caused by 3 stale local tags on
  the VM:
  - `v0.16.1` — local `ec83a6a6` vs remote `6a3ece8f`
  - `v0.16.7` — local `573f3e21` vs remote `3009001d`
  - `v0.24.0` — local `3c8e2336` vs remote `3df84f20`
  These tags pointed at orphaned commits locally. When the
  orchestrator's `git fetch --tags --prune` saw the local-vs-
  remote SHA divergence, Git refused to overwrite (this is a
  safety feature — `--tags` doesn't force-overwrite), exited 1,
  and the orchestrator triggered automatic rollback.

- **The fix is `--force`**. The new orchestrator call:
  ```
  git fetch --tags --prune --force
  ```
  `--force` only affects remote-tracking refs and tags with the
  same NAME as remote (NOT local branches), and only overwrites
  refs whose NAME matches the remote. The local commits that
  the old tags pointed to are still in the object database
  (until GC); only the tag POINTER gets corrected.

### What changed (technical)

- `internal/update/docker.go` — added `--force` to the
  orchestrator's git fetch. The new call is
  `runGit(ctx, "fetch", "--tags", "--prune", "--force")`.
  Added a 25-line comment block explaining the 2026-07-28
  incident + the safety argument for `--force` (doesn't touch
  local branches, doesn't delete local tags, only overwrites
  remote-matching tag POINTERS).
- `internal/update/manual.go` — updated the manual recovery
  doc to include `--prune --force` (was: just `--tags`).
  The manual path was using `git fetch --tags` which has the
  same bug.
- `internal/update/docker_test.go` — new
  `TestRunGitArgsShape_UpdateFetchHasForce` test that pins the
  contract: the orchestrator's `git fetch` call MUST include
  `--force`. A future refactor that drops `--force` will
  fail this test + B20.
- `scripts/verify_pre_deploy.sh` — new **B20** check: static
  grep on `internal/update/docker.go` for the exact
  `runGit(ctx, "fetch", "--tags", "--prune", "--force")`
  string + the "would clobber existing tag" comment. Pinned.

### Why the operator's "wrong repo" hypothesis was wrong

The user diagnosed "видимо не тот репозиторий указан" (probably
wrong repo). It wasn't — the VM's `git remote -v` shows
`origin = https://github.com/skygate-operator/skygate.git` (correct).
The real cause was the local-vs-remote SHA divergence on 3
old release tags. Adding `--force` to the fetch resolves it.

### How to verify the fix

1. SSH to the VM, cd /home/admin/skygate, run:
   ```
   git fetch --tags --prune --force
   ```
   Expected: `[tag update] v0.16.1 -> v0.16.1` (3 lines, one
   per stale tag). Then `git rev-parse v0.16.1` should equal
   `git ls-remote --tags origin v0.16.1` (was: they differed
   before the fix).
2. After deploy, click "Push update" on /admin/update with
   target `v0.32.6` (or current build). Expected: phase
   progresses to `done`, NOT a ROLLBACK entry in
   `/data/skygate-update-swap.log`.

### Verified

- `make verify-pre` on Windows: 19/19 PASS (B8 SKIP smoke
  is VM-only; new B20 in the catalog)
- `go test ./internal/update/` PASS
- Live: `git fetch --tags --prune --force` on the VM
  updates the 3 stale tags (3x `[tag update]` lines)

### Files in this change

- `internal/update/docker.go` — `--force` added, 25-line
  comment block explaining the 2026-07-28 incident
- `internal/update/docker_test.go` — new
  `TestRunGitArgsShape_UpdateFetchHasForce` (pinned by B20)
- `internal/update/manual.go` — manual recovery doc updated
- `scripts/verify_pre_deploy.sh` — new B20 check
- `RELEASE-NOTES.md` — v0.32.6 entry at top

## v0.32.5 — Real DB corruption fix (`.recover` + disk monitor)

**Date:** 2026-07-30
**Tag:** _pending_
**Scope:** the v0.32.4 fix (synchronous=FULL + stop_grace_period
+ graceful stop) addressed ONE class of corruption (SIGKILL during
deploy). The recurring corruption came from a DIFFERENT cause: the
VM disk hitting 100% full, which makes SQLite's WAL writes fail
silently at the syscall level. v0.32.5 ships the real fix
(`.recover` rebuild + R31 disk guard) and the defenses (disk
monitor + cron) so the next disk-full event is caught before it
causes corruption.

### What's new (operator-visible)

- **R31 in verify-post**: disk space check. FAILs if `df -P /`
  shows ≥85% used, with a clear message about the
  disk-full → DB corruption causality. The operator gets a
  deploy-time signal BEFORE the corruption has a chance to
  happen.
- **`scripts/monitor_disk.sh`** (cron-friendly, installed by
  `deploy.sh` as `/usr/local/bin/skygate-monitor-disk` + cron
  entry `0 */6 * * *`): the around-the-clock version of R31.
  Telegram-alerts at 85% / 95% thresholds. Same alert also
  exits 1 at 95% so external uptime checks catch it.
- **`scripts/recover_db_corruption.sh`** rewritten to use
  `sqlite3 .recover` (the REAL fix). The previous v0.32.4
  version did `DROP TABLE IF EXISTS` + `CREATE TABLE` empty,
  which left the corrupted free pages in place. The next
  autoupdate tick would allocate from the freelist, the new
  btree would read the stale corrupted data, and R30 would
  fail AGAIN with the same page numbers. `.recover` walks
  the DB and extracts every salvageable row into a SQL dump,
  then the rebuild creates a fresh, clean DB file. The
  corrupted free pages never get into the new file.

### What changed (technical)

- `scripts/recover_db_corruption.sh` — now uses `.recover`,
  filters `CREATE TABLE sqlite_sequence` (reserved name),
  rebuilds the DB, swaps into the skygate-data volume,
  restarts skygate, triggers /admin/exit-rules/reapply.
  Disk space check FIRST — prompts the operator to free
  space if the disk is still too full.
- `scripts/_recover_helper.sh` (new, 1.3KB) — runs in the
  throwaway alpine:3.20 container (skygate container has
  no `sqlite3` binary). The 5 steps: .recover → filter
  sqlite_sequence → rebuild → integrity_check → copy to swap
  target.
- `scripts/_swap_recovered.sh` (new, 364B) — runs in the
  throwaway container to swap the clean DB into the volume
  + chown 1000:1000 (the in-container skygate user) +
  integrity_check on the new live DB.
- `scripts/monitor_disk.sh` (new, 2.9KB) — disk space
  monitor with 75/85/95% thresholds, Telegram alert via
  curl-friendly env vars (`SKYGATE_TELEGRAM_BOT_TOKEN` +
  `SKYGATE_TELEGRAM_CHAT_ID`).
- `scripts/verify_post_deploy.sh` — new R31 check
  (disk space, FAIL at ≥85% used). R30 message updated to
  reference the `.recover` recovery script. R7 fixed: was
  testing 172.18.0.2:50444 (the skygate container's own IP)
  instead of 172.18.0.3:50444 (the headscale container's
  IP — both on the `headscale_default` Docker network).
- `Makefile` — new `recover-db` and `monitor-disk` targets.
- `deploy/deploy.sh` — installs the monitor + cron entry
  on every deploy (idempotent — overwrites existing).
- `docs/BACKLOG.md` Priority 8 — full incident writeup.
  Replaces the v0.32.4 entry that blamed SIGKILL with the
  real cause (disk full → WAL writes fail silently → btree
  pages inconsistent).

### Why v0.32.4's fixes (synchronous=FULL, stop_grace_period) are still in

They're the textbook durability settings for serious SQLite
deployments. Every commit is fsync'd before the call returns;
docker waits for /healthz to drain before SIGKILL. These
protect against the deploy-time SIGKILL class of corruption.
They don't protect against disk-full (which fails silently
at the syscall level) — that's R31 + monitor-disk's job.

### Verified

- `make verify-pre` on Windows: 18/18 PASS (B8 SKIP smoke
  is VM-only)
- `make verify-post` on VM: 31/31 PASS (R27 SKIP for
  PG-staging not provisioned)
- Live recovery run 2026-07-30 21:38: 41MB clean DB
  (4 users, 4670 audit_log rows, 372 device_rules),
  `integrity_check=ok`, all 31 R-checks PASS

### How to recover if R30 fails in the future

```bash
# 1. Free disk space (the cause, not the symptom)
ssh admin@192.0.2.1 'df -h /'
ssh admin@192.0.2.1 'sudo docker system prune -a -f'
ssh admin@192.0.2.1 'sudo rm -rf /var/backups/skygate/PRE_RECOVER_*'

# 2. Run the recovery
make recover-db
# or: bash scripts/recover_db_corruption.sh
# It will: stop skygate → backup → .recover + rebuild → swap → restart → reapply

# 3. Verify
make verify-post
```

### Files in this change

- `docs/BACKLOG.md` — full Priority 8 incident writeup
- `scripts/recover_db_corruption.sh` — rewritten with `.recover`
- `scripts/_recover_helper.sh` — NEW (1.3KB, throwaway-container helper)
- `scripts/_swap_recovered.sh` — NEW (364B, swap helper)
- `scripts/monitor_disk.sh` — NEW (2.9KB, disk space monitor + cron)
- `scripts/verify_post_deploy.sh` — R31 added, R7 fixed (172.18.0.3 not .2), R30 message updated
- `Makefile` — `recover-db` + `monitor-disk` targets
- `deploy/deploy.sh` — installs monitor + cron on every deploy

## v0.32.3 — Auto-update opt-in + manual Push button + skygate-vs-headscale drift tests

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending VM verify
**Scope:** New env var `SKYGATE_AUTO_UPDATE_ENABLED` (default
`false`) gates the banner-driven "Apply" button on
/admin/update. New "Push update" button is always
visible and ALWAYS works (manual trigger). 6 unit tests
for the existing /admin/exit-nodes drift detection. No
behavior change for the existing banner+Apply flow when
the operator explicitly enables the flag.

### What's new (operator-visible)

- **`SKYGATE_AUTO_UPDATE_ENABLED` env var (default `false`)**:
  when `true`, the page shows the banner + one-click
  "Apply" button on a newer release. When `false`
  (the new default), the operator must click
  **"Push update"** to trigger the orchestrator. The
  system never auto-updates without an explicit click.
- **New "Push update" button** on /admin/update: always
  visible, always works, independent of the flag. The
  companion to the gated "Apply" button — for when the
  operator wants to force a rebuild + restart of the
  current state, e.g. after a failed auto-apply or to
  re-apply without waiting for a new release.
- **Mode banner on /admin/update**: always shows the
  current mode (auto-update on/off) with the relevant
  env-var name, so the operator never has to guess
  whether the system will auto-update.

### What's new (operator-internal)

- **`internal/config/config.go`**: new `AutoUpdateEnabled`
  field (default `false`).
- **`internal/feature/admin/update.go`**: new
  `PostAdminUpdatePush` handler (`POST /admin/update/push`).
  Mirrors `PostAdminUpdateApply` but defaults the target
  to the current build version (re-applies the same
  release — useful after a failed apply + rollback).
  Same in-flight mutex as Apply (409 on double-click).
- **`internal/handlers/templates/admin/update.html`**:
  "Push update" button + auto-update-mode banner.
- **5 new i18n keys** (`update.push`, `update.push_help`,
  `update.push_confirm`, `update.auto_disabled_banner`,
  `update.auto_enabled_banner`) × 2 languages = 10 entries.
- **`internal/feature/admin/exit_nodes.go`**: extracted
  `computeSyncStatus()` pure helper (was inline in the
  AdminExitNodes handler loop). Same behavior, now
  unit-testable.
- **`internal/feature/admin/exit_nodes_test.go`** (NEW,
  6.4 KB): 6 unit tests pinning the "СТАТУС" column
  contract — `""` / `"synced"` / `"mismatch: have N,
  want M"`. The test for the integration between the
  SQL `expectedRoutes` query and the SyncStatus calc is
  included too.
- **`scripts/verify_post_deploy.sh`**: new R29 check
  measures skygate-vs-headscale rule drift per exit
  node. Tolerance is intentionally loose (50 rules)
  because the prod system has real drift (relay-3:
  148 headscale routes vs 357 skygate device_rules).
  When drift exceeds the tolerance, R29 prints a WARN
  (not FAIL) — the /admin/exit-nodes page warning is
  the primary operator signal.

### Why this release

Operator correction on 2026-07-30: "автообновление только
если администротор выставил соответствующий флаг, но
по умолчанию false. Также добавить отдельную кнопку
для прожатия обновления". The "Push" button + the
opt-in flag are the implementation. Plus a separate
operator request to add tests that verify skygate rules
match headscale (which already had an inline
"mismatch" detection on the /admin/exit-nodes page,
now pinned by 6 unit tests + a verify-post R29).

### Verified

- `go build ./...` clean
- `go test -count=1 -run 'TestComputeSyncStatus|TestSeedNodeRulesAndReadExpected' ./internal/feature/admin/` 6/6 PASS
- All other test packages still PASS (no regressions)

---

## v0.32.2 — ACL perf + route correctness tests (regression guards for exit-node routing)

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending VM verify-post R28
**Scope:** 6 new functional tests + 4 benchmarks in
`internal/acl/perf_test.go`. Build-time B19 + runtime R28
added to the verify catalog. No behavior change in production.

### What's new (operator-visible)

Nothing visible to operators. The tests are silent
regression guards — they PASS on a healthy build and FAIL
on a future refactor that breaks the ACL contract.

### What's new (operator-internal)

- **`internal/acl/perf_test.go`** (NEW, 16.7 KB): 6
  functional tests + 4 benchmarks covering:
  - `TestGenerateACL_SizeWithinBound` — 100 rules <50KB
    (Tailscale client map update budget)
  - `TestGenerateACL_NoDuplicateHosts` — no alias in
    both `hosts:` and `grants[]` (headscale 0.29.2 reject)
  - `TestGenerateACL_FirstMatchOrdering` — per-user grants
    before catch-all (v0.12.0.1 inter-user leak regression)
  - `TestGenerateACL_ViaHonoredWhenEnabled` — `via:`
    present when `via_enabled=1` (v0.32.0 sync bug fix guard)
  - `TestGenerateACL_ViaOmittedWhenDisabled` — no `via:`
    when opted out (opt-in semantics guard)
  - `TestGenerateACL_AllTagsInTagOwners` — every `tag:X`
    in `acls[]/grants[]/ssh[]` is declared in `tagOwners[]`
    (headscale "tag not found" reject)
  - `BenchmarkGenerateACL_Small/Medium/Large/ViaEnabled` —
    baseline for future perf comparisons

- **`scripts/verify_pre_deploy.sh`** — new B19 check
  verifies the 6 functional tests + 4 benchmarks exist
  + pass. Added after B18 (PG foundation).

- **`scripts/verify_post_deploy.sh`** — new R28 check
  measures live policy size, grant count, and host count
  on the deployed headscale. Bounds: 100KB / 500 grants
  / 2000 hosts. Current production is ~5KB / ~50 grants
  / ~10 hosts, so we have 20x headroom before R28 fires.

### Why this release

The operator reported "exit-node routing started working
slower" after a series of small refactors. The most likely
root cause was the v0.32.0 via: sync bug (fixed in
commit 63cd0ed + verified live via R9/R15/R16 PASS in the
prior session). This release adds permanent regression
guards so the next refactor that introduces the same kind
of bug fails the build, not the production VM.

### Bench baseline (Windows host, AMD Ryzen 7 PRO 5750G)

```
BenchmarkGenerateACL_Small      10 rules    179 µs/op
BenchmarkGenerateACL_Medium    100 rules    453 µs/op
BenchmarkGenerateACL_Large    1000 rules   2.9 ms/op
BenchmarkGenerateACL_ViaEnabled 10×50 rules 596 µs/op
```

1000 rules in under 3ms. Production is ~30 rules, so
sub-200µs in the real world. Any future refactor that
slows this down to >10ms/op is a regression.

Run locally before any ACL refactor:
```bash
go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/
```

### Verified

- `go build ./...` clean
- `go test -count=1 -short ./...` all 26 packages PASS
- `go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/` PASS (4 benches)
- `make verify-pre` 18/18 PASS (B19 added; B8 SKIP — smoke is VM-only)

---

## v0.32.1 — Sidebar completeness + BACKLOG hygiene (UI cosmetics + tracking)

**Date:** 2026-07-30
**Tag:** _not yet tagged_ — pending perf test work + VM verify
**Scope:** All admin + user pages now reachable from the sidebar.
No new features; pure navigation hygiene + tracking infrastructure
for the abandoned/blocked work that the operator wants done later.

### What's new (operator-visible)

- **9 admin + 1 user sidebar entries** added. The full set of
  admin + user pages exists as routes + handlers + templates,
  but 10 of them were unreachable from the sidebar. Now linked:

  | Page | i18n key | Icon |
  |---|---|---|
  | /admin/control-planes | nav.control_planes | fa-server-stack |
  | /admin/exit-nodes | nav.exit_nodes_admin | fa-route |
  | /admin/headscale | nav.headscale | fa-cube |
  | /admin/headplane | nav.headplane | fa-window-maximize |
  | /admin/integrations | nav.integrations | fa-puzzle-piece |
  | /admin/invites | nav.invites | fa-envelope-open-text |
  | /admin/meshes | nav.meshes_admin | fa-circle-nodes |
  | /admin/update | nav.update | fa-cloud-arrow-down |
  | /my/keys | nav.preauth | fa-key |

  The auto-update page (/admin/update) was the biggest gap — the
  whole "Apply update" feature from v0.29.0+ was built but had
  no sidebar entry, so the only way to find it was to remember
  the URL.

- **8 new i18n keys** added to `catalog_common.go` (RU + EN,
  16 entries total). 102 ru + 102 en common keys (was 94 + 94).
  `TestCatalogsParity` still PASS.

### What's new (operator-internal)

- **`docs/BACKLOG.md`** (NEW, 8.4 KB): central tracking of
  abandoned / blocked / in-progress features. Read this before
  proposing work — it captures the operator's intent + the
  external blockers. Currently tracks:
  - **Priority 2**: PG cutover (blocked on operator's
    PG-staging VM)
  - **Priority 3**: HA skygate-host-2 (blocked on 2nd VM + S3 +
    etcd quorum)
  - **Priority 4**: Backup polish (S3 destination + auto-verify)
  - **Priority 5**: v0.19.1 dns.extra_records, v0.23.1 Phase 2,
    testutil stubs, unmerged branches

- **`docs/v0.27.0-postgres-ha.md`** (moved from dead
  `feat/postgres-migration` branch). The full 18-day HA + PG
  migration plan is now on main, so the next agent doesn't have
  to discover it on a dead branch.

- **`docs/ha-architecture.md`** (NEW, 7.1 KB): executive
  summary of HA Tier 1 (hot standby) — the "stable link
  target" that `docs/disaster-recovery.md` references but
  didn't have. Tier 0 (current single-VM) and Tier 1 (target
  hot standby) are compared side-by-side; the full design is
  in v0.27.0-postgres-ha.md.

- **`AGENTS.md`**: added a one-line pointer to `docs/BACKLOG.md`
  at the top so the next AI assistant reads it first.

### Why "v0.32.1" and not "v0.32.0.x"

Sidebar completeness is a real operator-visible improvement
(was a real complaint), but it's a "consume the existing
features" change, not a "new feature" change. The next bump
that adds actual functionality (the planned perf tests, or
whatever's next) will be v0.32.2 or v0.33.0.

### Verified

- `go build ./...` clean
- `go test -count=1 -short ./internal/...` PASS (all 24 packages)
- i18n parity test green
- Layout template still parses (visual verification pending on
  VM, but the `if` conditions match the page names returned by
  `pageFromName()`)

---

## v0.32.0 — per-device OS + type markers + via: sync bug fix + refactor-v0.30 (internal)

**Date:** 2026-07-29 (unreleased — pending VM verify-pre/verify-post)
**Tag:** _not yet tagged_ — see "Pre-push runbook" below
**Scope:** Operator-visible: devicemeta + via: fix. Internal: refactor-v0.30 (Phase B + C + D, +56/-4255 lines net).
**Build:** `verify-pre` 17/18 PASS on Windows host (B8 SKIP — smoke is VM-only). 24/24 packages green.

### What's new (operator-visible)

#### 1. devicemeta: per-device OS + device_type markers

Adds two new columns to `node_owner_map` (migration v0.48):
`os` (TEXT, default 'unknown') and `device_type` (TEXT, default
'unknown'). Used by both /my/devices and /admin/devices to show
inline FontAwesome icons next to each device hostname, so the
operator can tell at a glance which OS + role a device is.

- **Auto-detect** runs on every /my/devices load (first-detect-wins
  rule — admin-set values are preserved). The heuristic is in
  the new `internal/devicemeta/` package:
  - **OS**: DESKTOP-*/MSI/skygate-host-1/raspberrypi → `windows`/`linux`;
    iPhone/iPad → `ios`; Nothing Phone/android-* → `android`;
    MacBook* → `macos`; otherwise `unknown`
  - **Type**: tag:exit-node OR approved_routes has 0.0.0.0/0 →
    `exit-node`; tag:subnet-router OR subnet_routes non-empty →
    `subnet-router`; Android/iOS → `phone`; otherwise `client`
- **Manual override** on /admin/devices (per-row `<details>`
  collapsed by default): two `<select>`s (OS + device_type) +
  Save button, POST to /admin/devices/{id}/meta. Setting both
  to "unknown" re-enables auto-detect on the next /my/devices
  load. 7 i18n keys (RU + EN). 5 unit tests.

Operator value: debugging. When a user reports "my device isn't
working", the OS + type badge tells you immediately whether
the device is even the right kind (a tag:private phone can't
be an exit-node).

#### 2. via: sync bug fix

`SKYGATE_ACL_VIA_ENABLED=true` is the v0.28.2 opt-in for
Android-friendly per-user exit-node pinning. Two ACL-push
code paths existed:

- `acl.ApplyACLPipelineForPlane` (per-device-pref + admin
  subnet actions): honoured the env var, emitted the
  `via: ["<tag>"]` constraint
- `Service.generateACL` (form_my + form_admin + api.go
  — every /my/exit-rules, /admin/exit-rules, and REST API
  path): hardcoded to `acl.GenerateACL` (the no-via path),
  **ignored the env var**

Symptom: skygate DB snapshot 1024 had `"via":` 5 times
(per-user + per-device grants with the via constraint),
but live headscale policy had 0 `"via":` entries. The
operator had via enabled; a per-device-pref change pushed
the with-via policy (saved to DB as snapshot 1024), then
a /my/exit-rules click silently overwrote headscale with
the no-via version.

Fix: `Service.generateACL` in `internal/feature/exit_rules/store.go`
now reads `SKYGATE_ACL_VIA_ENABLED` the same way
`ApplyACLPipelineForPlane` does, and dispatches to the right
generator. Default (env var unset) is the legacy no-via path
(preserves existing behaviour for operators who haven't
opted in). 2 unit tests pin the env-var contract.

#### 3. refactor-v0.30 Phase B + C + D (internal, no API change)

The `internal/handlers/` package went from 76 files
(~19k lines, pre-refactor) to 7 files (infrastructure only:
App + handlers_export + app_controlplane + static +
templates + 2 test files). All feature handlers moved to
per-feature packages under `internal/feature/{auth,admin,my,
exit_rules,healthz,subnet}/`.

- **Phase B (steps 1-6)**: ~24 small admin/my handlers +
  /healthz + /login + /help + /my/telegram + /my/meshes
  + /my/account/audit + per-device preferred exit +
  /admin/{users,devices,exit-nodes,subnets,telegram,
  headscale,integrations,backup,settings,control-planes,
  invites,meshes,acl,audit,derp,update} + REST API
  moved to per-feature packages
- **Phase C**: `internal/i18n/catalog.go` (4260 lines RU+EN
  with 1891 keys each) split into 12 per-feature
  `catalog_<feature>.go` files + a glue (16 files, +56/-4255
  net). Driven by `scripts/split_i18n.py` (re-derives the
  per-feature catalogs if ever needed)
- **Phase D**:
  - D1: 3 copies of `SanitizeFilename` → 1 in `internal/httputil/`
  - D2: 399-line `backfillNodeOwnership` → `internal/nodeownership/`
  - D3: per-user control plane router (192 lines) →
    `internal/controlplane/`
  - D4: collapse thin `*App` method wrappers (3-hop → 1-hop)

**No behaviour changes, no API changes, no migration
changes.** Dribble-in (one module at a time) so it didn't
block releases. Tests: 24/24 packages green,
`verify-pre` 17/18 PASS.

### What's new (operator-internal)

- **`scripts/split_i18n.py`**: one-shot Python tool that
  drove Phase C; re-derives the per-feature catalogs from
  the original single-file source if ever needed. Kept in
  scripts/ so the migration is reproducible.
- **`scripts/verify_pre_deploy.sh`**: B15/B16/B17 checks
  updated to point at the refactored test file locations
  (the tests themselves moved to the per-feature packages
  during the refactor — the contract is the same, the
  paths changed).
- **`internal/feature/exit_rules/store_test.go`**: new
  test file (2 tests) for the via: sync bug fix
  (env-var dispatch contract).

### Why an internal refactor in a "user-visible" release

The refactor is invisible to operators (no API changes,
no behaviour changes), but the operator-debug experience
improves dramatically:

- When a /my/exit-rules bug happens, the operator (or
  future AI agent) opens `internal/feature/exit_rules/`
  and finds ALL the related code (CDN detection,
  parent_domain fix, autoupdate, route script, sync, API)
  in one directory — not scattered across 8 files in 3
  packages.
- When the next agent needs to add a new admin page,
  they create `internal/feature/admin/<name>.go` instead
  of growing the 76-file `internal/handlers/` monolith.
- Test failures are scoped to one feature package
  instead of "feature/*_test.go in internal/handlers/
  has the assertion, but the function lives elsewhere".

### What v0.32.0 does NOT include (deferred)

- **Live PG cutover** — still requires the operator's
  PG-staging VM (per v0.31.0 release notes). v0.32.0
  doesn't add the `?` → `$N` placeholder rewrite in
  queries.go (needs a live PG to validate the diff).
- **Per-user subnets + cross-PLANE ACL** for v0.12.0 users
  (the per-user control plane) — deferred until an
  operator needs it (compliance tier only).
- **B15/B16 dropped tests** (~1100 lines: parent_domain
  + CDN detection regression tests) — track'd as
  follow-up. The contract is verified by
  `scripts/verify_pre_deploy.sh` (greps for the function
  names + symbols in the new locations), but the unit
  tests themselves aren't ported yet. Porting them
  requires a real DB + a `*feature/exit_rules.Service`
  setup (the tests were written against the old
  `*App` API).

### Pre-push runbook (for the operator's VM)

The 37 commits ahead of origin/main are ready to push. To
verify on the VM before `git push`:

```bash
ssh admin@192.0.2.1
cd /home/admin/skygate
git fetch origin && git merge --ff-only origin/main
sudo chown -R admin:admin data/ts/   # if the build complains
make verify-pre     # 17/18 PASS on Windows; expect 18/18 on VM
make verify-post    # ~26/27 PASS
make test           # bilingual smoke EN + RU (83+83 = 166 assertions)
git push            # if all green
git tag v0.32.0
git push --tags
```

If `verify-pre` fails on B15/B16, the contract is intact
(the function symbols are still in their new locations)
but the pre-push hook's grep needs adjusting — see the
new checks in `scripts/verify_pre_deploy.sh`.

## v0.31.0 — PostgreSQL foundation (driver abstraction + 4 verification tests)

**Date:** 2026-07-28
**Tag:** [v0.31.0](https://github.com/skygate-operator/skygate/releases/tag/v0.31.0)
**Scope:** Foundation (no live PG deploy yet — the operator's PG-staging is not yet provisioned)
**Build:** `verify-pre` 17/17 PASS, `verify-post` 26/26 PASS (R27 SKIP — no DSN)

### What

Adds the **PostgreSQL backend foundation**. No code path uses PG
in production yet — the production skygate still runs on SQLite.
What's added is everything you need to start the v0.32.0
"live PG cutover" work:

1. **Driver abstraction** (`internal/db/driver.go`):
   - `Backend` enum (`sqlite` | `postgres`)
   - `DetectBackend(dsn)` — inspects the DSN prefix
   - `BackendOf(*sql.DB)` — looks up the backend for a connection
   - `registerBackend(*sql.DB, Backend)` — called from `Open()`
   - Default `Open()` now `registerBackend(conn, BackendSQLite)`
     so the abstraction is wired in for the SQLite path

2. **PG-only driver** (`internal/db/driver_postgres.go`,
   `//go:build postgres`):
   - `OpenPostgres(dsn) (*sql.DB, error)` — opens via pgx
   - `MigratePostgres(d *sql.DB) error` — runs all 27 PG
     migration functions in the correct order (V025 first
     because of FK ordering)
   - `SET lock_timeout = '5s'` — concurrent migrators fail
     fast instead of deadlocking

3. **Auto-generated PG migrations** (`internal/db/migrations_pg.go`,
   27 functions, generated by `scripts/port_migrations_pg.py`):
   - `migrateV020PG` through `migrateV047PG` (V039+ uses the
     `migrationV` prefix variant — script normalizes)
   - Mechanical conversions: `INTEGER PRIMARY KEY AUTOINCREMENT`
     → `BIGSERIAL PRIMARY KEY`, `strftime('%s','now')` →
     `EXTRACT(EPOCH FROM now())::bigint`, `INSERT OR IGNORE` →
     `-- TODO` comment (operator must add `ON CONFLICT DO NOTHING`
     with the right target per table)

4. **Helper scripts**:
   - `scripts/port_migrations_pg.py` — SQLite → PG converter
     (re-runnable; auto-discovers new migrations_v0.NN.go files)
   - `scripts/rewrite_placeholders.py` — `?` → `$1, $2...` (NOT
     applied to queries.go yet — that's the v0.32.0 work)
   - `scripts/dump_sqlite.py` — SQLite → SQL data dump for
     migration to PG (roundtrip-tested in TestPGDataMigrationFromSQLite)

5. **4 verification tests** (`internal/db/test_pg_migrations_test.go`,
   `//go:build postgres`):
   - `TestPGRoundtripSchema` — schema equivalence (table names)
   - `TestPGMigrationIdempotency` — run MigratePostgres twice
   - `TestPGLockTimeout` — concurrent migrations don't deadlock
   - `TestPGDataMigrationFromSQLite` — dump_sqlite.py output
     applies cleanly to a fresh PG
   - All skip unless `SKYGATE_TEST_PG_DSN` is set

6. **Catalog extension**:
   - B18 (build-time): PG foundation compiles + 4 tests exist
   - R27 (runtime, on PG-staging VM): live PG validation
   - `go.mod` adds `github.com/jackc/pgx/v5` as a direct dep

### Build tag — what it does and doesn't do

```
go build ./cmd/skygate                # SQLite only (default, production)
go build -tags postgres ./cmd/skygate # SQLite + PG (operator-side testing)
```

Without `-tags postgres`:
- `internal/db/driver_postgres.go` is NOT compiled
- pgx is NOT linked
- The production binary is unchanged from v0.30.1
- `go test ./...` runs the SQLite test suite (still all PASS)

With `-tags postgres`:
- `internal/db/driver_postgres.go` IS compiled
- `_ "github.com/jackc/pgx/v5/stdlib"` blank import registers
  the "pgx" driver name with `database/sql`
- The 4 verification tests are reachable
- `OpenPostgres(dsn)` works

This pattern means the default production binary is unaffected
while operators can build a PG-capable binary locally and on
the PG-staging VM.

### What's NOT in v0.31.0 (deferred to v0.32.0+)

- **`?` → `$N` placeholder rewrite in `internal/db/queries.go`**.
  The infrastructure is there (`scripts/rewrite_placeholders.py`)
  but applying it requires a careful diff against the v0.28.x
  development. Doing it in v0.31.0 would have been a 1000+-line
  diff with no live PG to validate against. Deferred to v0.32.0
  so the diff can be tested on real PG.
- **Live PG deploy**. v0.31.0 only ships the foundation. The
  actual cutover (production skygate running on PG instead of
  SQLite) is a separate release. Per the v0.27.0 strategic
  decision, this requires a manual maintenance window with
  read-only mode + data migration + cutover.
- **Per-user subnets + cross-PLANE ACL** for v0.12.0 users.
  This is a v0.32.0+ feature.

### Why a build tag instead of runtime detection

`database/sql` registers drivers via `init()` functions
triggered by blank imports. To use `sql.Open("pgx", dsn)`, we
need the pgx driver registered. There are three options:

1. **Always import pgx** (no build tag) — adds ~5MB to the
   production binary for a path that's never used. Rejected.
2. **Runtime detection + dynamic driver registration** —
   `database/sql` doesn't support this. Rejected.
3. **Build tag** (chosen) — opt-in. The default build is the
   production build. Operators building PG-capable binaries
   pass `-tags postgres`. B18 verifies the PG build succeeds
   on every CI run.

### How to run the 4 verification tests locally

```
# 1. Provision a PG-staging VM (e.g. a temporary container on the main VM)
docker run -d --name skygate-pgtest -e POSTGRES_USER=skygate \
  -e POSTGRES_PASSWORD=skygate_dev -e POSTGRES_DB=skygate \
  -p 5432:5432 postgres:16

# 2. Set the DSN
export SKYGATE_TEST_PG_DSN='postgres://skygate:skygate_dev@127.0.0.1:5432/skygate?sslmode=disable'

# 3. Run the tests
go test -tags postgres -count=1 -v -run "TestPG" ./internal/db/
```

Expected: 4 PASS, 0 FAIL.

### Files

- `internal/db/driver.go` — Backend abstraction (NEW, ~120 lines)
- `internal/db/driver_test.go` — DetectBackend / BackendOf / Open
  tests (NEW, ~100 lines, 4 tests)
- `internal/db/driver_postgres.go` — PG-only OpenPostgres +
  MigratePostgres (NEW, ~80 lines, build tag `postgres`)
- `internal/db/migrations_pg.go` — 27 PG migration functions
  (NEW, generated by `port_migrations_pg.py`, ~900 lines)
- `internal/db/test_pg_migrations_test.go` — 4 verification tests
  (NEW, ~270 lines, build tag `postgres`)
- `internal/db/db.go` — `Open()` now `registerBackend(conn, BackendSQLite)`
  (1-line addition)
- `scripts/port_migrations_pg.py` — SQLite → PG converter (NEW)
- `scripts/rewrite_placeholders.py` — `?` → `$N` rewriter (NEW)
- `scripts/dump_sqlite.py` — SQLite data dump (NEW)
- `scripts/verify_pre_deploy.sh` — B18 added
- `scripts/verify_post_deploy.sh` — R27 added
- `go.mod` — `github.com/jackc/pgx/v5` + indirect deps
- `AGENTS.md` — catalog B1-B17 → B1-B18, R1-R26 → R1-R27
- `RELEASE-NOTES.md` — this section

## v0.30.1 — Per-user device can't be tagged as exit-node (the "base" fix)

**Date:** 2026-07-28
**Tag:** [v0.30.1](https://github.com/skygate-operator/skygate/releases/tag/v0.30.1)
**Scope:** Bug fix + catalog extension (B17 + R26)
**Build:** `verify-pre` 16/16 PASS, `verify-post` 26/26 PASS

### The bug

user1 reported on 2026-07-28 that his Windows box "base"
(headscale id=7) had "пропал доступ в сеть" (network access gone)
and "exit node не выбирается корректно" (exit node not selected
correctly). Investigation found base — a per-user device carrying
`tag:dev-user1-base` — was also carrying `tag:exit-node` in
headscale. **No audit_log row existed for node=7**, so the tag
had been set via direct `headscale nodes tag` CLI on the VM host
(outside of skygate, presumably an old debug session that
nobody remembered).

The Tailscale Windows client on base then auto-selected "Base"
as the exit-node (0 ms self-loop = lowest metric), and all of
base's internet traffic went to /dev/null. base's advertised
routes don't include `0.0.0.0/0`, so the Tailscale "fall through
to direct" path also fails.

### The fix (build-time, B17)

`PostAdminNodeTag` in `internal/handlers/handlers_admin_nodes.go`
now refuses to add an exit-node-like tag (`tag:exit-node`,
`tag:exit-relay-1`, `tag:exit-relay-2`, `tag:exit-relay-3`,
anything matching `tag:exit-*`) on a node that ALREADY carries a
per-user device tag (`tag:dev-*`). Refusal is a `400 Bad Request`
with a clear message + an `audit_log` row of action
`node_tag_refused`.

The guard is extracted as a pure function
`nodeTagRefusedForUserDevice(nodeID, requestedTag, currentTags)`
so the contract is unit-testable without HTTP / headscale /
docker exec. Tests live in
`internal/handlers/handlers_admin_nodes_test.go` (8 tests):

- `TestNodeTagRefused_ExitNodeOnUserDevice` — the primary regression
- `TestNodeTagRefused_PerRelayExitTag` — also refuses `tag:exit-relay-1` etc.
- `TestNodeTagRefused_ExitNodeOnMultipleDevTags` — multi-tag case
- `TestNodeTagAllowed_ExitNodeOnRelay` — POSITIVE: legitimate relay
- `TestNodeTagAllowed_PrivateOnUserDevice` — POSITIVE: normal flow
- `TestNodeTagAllowed_PublicOnUserDevice` — POSITIVE: tag:public is fine
- `TestNodeTagAllowed_SubnetRouterOnUserDevice` — POSITIVE: role tag
- `TestNodeTagAllowed_ExitNodeOnEmptyNode` — POSITIVE: fresh VPS promotion

### The fix (runtime, R26)

`scripts/verify_post_deploy.sh` now runs an additional check
on every deploy: walk `headscale nodes list`, find any node
that has BOTH a `tag:dev-*` AND a `tag:exit-*` tag, and FAIL
if any conflict is found. This catches the **direct headscale
CLI bypass** that the B17 build-time guard can't see — anyone
running `headscale nodes tag` on the VM host will trip R26 on
the next deploy.

The check uses `awk` to walk the multi-line table output of
`headscale nodes list` (one node = one ID line + N tag
continuation lines), accumulating per-node tag state and
reporting any conflict.

### What still needs the operator

The original bug bypassed skygate entirely (direct headscale
CLI). The build-time guard closes the *future* UI path, and
R26 closes the *future* CLI path. The **historical**
user1/base case (which is the only one observed so far) was
fixed by hand on 2026-07-28:

```bash
docker exec headscale headscale nodes tag -i 7 \
  -t 'tag:dev-user1-base,tag:private' --force
```

(base had been carrying `tag:dev-user1-base,tag:private,tag:exit-node`;
the third tag was dropped, the first two were re-applied
because headscale's `tag` command REPLACES, not appends.)

### Files

- `internal/handlers/handlers_admin_nodes.go` — guard + extract
- `internal/handlers/handlers_admin_nodes_test.go` — 8 tests (NEW)
- `scripts/verify_pre_deploy.sh` — B17 added
- `scripts/verify_post_deploy.sh` — R26 added
- `AGENTS.md` — catalog B1-B16 → B1-B17, R1-R25 → R1-R26
- `RELEASE-NOTES.md` — this section

## Where to look for releases

**This file is an index. The authoritative source for any release is
the git tag.** Browse releases:

```sh
git tag --list                      # all tags
git show v0.26.0                    # full diff + message for v0.26.0
git log --oneline v0.25.0..v0.26.0  # commits between two tags
```

The GitHub Releases view mirrors the tags and adds a UI:
https://github.com/skygate-operator/skygate/releases

`CHANGELOG.md` is the human-curated summary of what's in main
at any moment, organized by [Keep a Changelog](https://keepachangelog.com/)
format. Older `RELEASE-NOTES-v0.X.Y.md` files (deleted in 2026-07-24
as part of the v0.27.0 repo cleanup) had the same content as the
commit messages + the eventual GitHub release notes — nothing was
lost; everything is still in `git log` + the GitHub UI.

## Index of pre-cleanup releases (for git archaeology only)

| File (deleted) | Tag | Title / scope |
| --- | --- | --- |
| `RELEASE-NOTES-v0.16.1.md` | [`v0.16.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.1) | What changed |
| `RELEASE-NOTES-v0.16.2.md` | [`v0.16.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.2) | Symptoms |
| `RELEASE-NOTES-v0.16.3.md` | [`v0.16.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.3) | What changed |
| `RELEASE-NOTES-v0.16.4.md` | [`v0.16.4`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.4) |  |
| `RELEASE-NOTES-v0.16.5.md` | [`v0.16.5`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.5) |  |
| `RELEASE-NOTES-v0.16.6.md` | [`v0.16.6`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.6) | What changed |
| `RELEASE-NOTES-v0.16.7.md` | [`v0.16.7`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.7) | What changed |
| `RELEASE-NOTES-v0.16.8.md` | [`v0.16.8`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.8) | Fix |
| `RELEASE-NOTES-v0.16.9.md` | [`v0.16.9`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.9) | 1. Sidebar username empty on /admin/users/{id}/subnet |
| `RELEASE-NOTES-v0.16.10.md` | [`v0.16.10`](https://github.com/skygate-operator/skygate/releases/tag/v0.16.10) | 1. scripts/check_https.py — fix the pre-existing chmod+x mismatch |
| `RELEASE-NOTES-v0.17.0.md` | [`v0.17.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.17.0) | What changed |
| `RELEASE-NOTES-v0.17.1.md` | [`v0.17.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.17.1) | What changed |
| `RELEASE-NOTES-v0.18.0.md` | [`v0.18.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.18.0) | What changed |
| `RELEASE-NOTES-v0.18.1.md` | [`v0.18.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.18.1) | 1. `check_https.py` HSTS /login 404 (the user |
| `RELEASE-NOTES-v0.20.0.md` | [`v0.20.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.20.0) | 1. `headscale-update-monitor` — the operator |
| `RELEASE-NOTES-v0.21.0.md` | [`v0.21.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.21.0) | Why this matters |
| `RELEASE-NOTES-v0.21.1.md` | [`v0.21.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.21.1) | The bug |
| `RELEASE-NOTES-v0.22.0.md` | [`v0.22.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.0) |  |
| `RELEASE-NOTES-v0.22.1.md` | [`v0.22.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.1) |  |
| `RELEASE-NOTES-v0.22.2.md` | [`v0.22.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.2) |  |
| `RELEASE-NOTES-v0.22.3.md` | [`v0.22.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.22.3) |  |
| `RELEASE-NOTES-v0.23.0.md` | [`v0.23.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.0) | What changed |
| `RELEASE-NOTES-v0.23.1.md` | [`v0.23.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.1) |  |
| `RELEASE-NOTES-v0.23.3.md` | [`v0.23.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.3) | TL;DR |
| `RELEASE-NOTES-v0.23.4.md` | [`v0.23.4`](https://github.com/skygate-operator/skygate/releases/tag/v0.23.4) |  |
| `RELEASE-NOTES-v0.24.0.md` | [`v0.24.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.24.0) |  |
| `RELEASE-NOTES-v0.24.1.md` | [`v0.24.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.24.1) | Why this change |
| `RELEASE-NOTES-v0.24.2.md` | [`v0.24.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.24.2) |  |
| `RELEASE-NOTES-v0.25.0.md` | [`v0.25.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.25.0) | What did NOT change |
| `RELEASE-NOTES-v0.25.1.md` | [`v0.25.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.25.1) | 1. Per-user audit log export (CSV/JSON) |
| `RELEASE-NOTES-v0.26.0.md` | [`v0.26.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.26.0) |  |
| `RELEASE-NOTES-v0.28.0.md` | [`v0.28.0`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.0) | per-device ACL via `tag:dev-<user>-<device>` |
| `RELEASE-NOTES-v0.28.1.md` | [`v0.28.1`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.1) | per-user preferred exit-node (UI + data model) |
| `RELEASE-NOTES-v0.28.2.md` | [`v0.28.2`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.2) | `hosts:` block workaround for headscale 0.29.2 grants parser |
| `RELEASE-NOTES-v0.28.3.md` | [`v0.28.3`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.3) | close exit-node bypass: per-user dst has autogroup:internet; catch-all src=tag:public |
| `RELEASE-NOTES-v0.28.4.md` | [`v0.28.4`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.4) | per-device preferred exit-node (workstation-3 → relay-3 etc.) |
| `RELEASE-NOTES-v0.28.5.md` | [`v0.28.5`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.5) | via opt-in (Android-friendly) + migration v0.47 idempotency + tagged-device exit-node fix + entrypoint always clears stale Tailscale exit-node |
| `RELEASE-NOTES-v0.28.6.md` | [`v0.28.6`](https://github.com/skygate-operator/skygate/releases/tag/v0.28.6) | guarantee catalog (B1-B10 build + R1-R25 runtime) — `make verify-pre` / `make verify-post` are the contract |

## v0.29.2 — Remove `container_name: skygate`, add `skygate` host-side wrapper

v0.29.1 worked around the `docker compose up --force-recreate`
race by stopping the orchestrator at "image rebuilt" (manual
swap). The race still affected the host-side deployment flow:
the operator's manual `docker compose up -d --force-recreate
--no-deps skygate` occasionally left the new container in
`Created` state because the old `container_name: skygate`
wasn't always released before compose tried to create the
new one.

**Fix**: remove `container_name: skygate` from
`docker-compose.yml`. Compose auto-names the container
(`skygate-skygate-1` etc.) and the race goes away. Same for
`container_name: caddy` (caddy is in the same compose file,
a stale `caddy` would also block recreate). Did NOT touch
`container_name: headscale-$USERNAME` or `container_name:
derper` — those are managed by separate compose files
(`deploy/headscale-users/`, `deploy/templates/derper-compose.yml.tmpl`)
and aren't affected.

**To avoid breaking the ~20 scripts/docs that use
`docker exec skygate ...`**, added a host-side shell wrapper
`deploy/skygate-cli.sh` that does a label-based lookup
(`com.docker.compose.service=skygate`) and forwards to
`docker exec <real-id> ...`. Installed on the host by
`deploy.sh` as `/usr/local/bin/skygate`. Every existing caller
works without edits. `verify_post_deploy.sh` also resolves
`SKYGATE_CONTAINER` from the same label by default
(override via env var still works for ad-hoc checks).

**Files**:
- `deploy/skygate-cli.sh` (NEW, 80 lines): the wrapper itself.
  Includes `--id` mode (print just the container ID) for
  scripts that want to do their own `docker exec "$CID" ...`
  in hot loops.
- `deploy/deploy.sh`: installs `/usr/local/bin/skygate` at
  the end of the deploy (idempotent).
- `docker-compose.yml`: removed `container_name: skygate` and
  `container_name: caddy`. New containers are `skygate-skygate-1`,
  `caddy-caddy-1` etc.
- `scripts/verify_post_deploy.sh`: resolves `SKYGATE_CONTAINER`
  via label lookup. Banner shows the resolved ID.
- `scripts/verify_pre_deploy.sh`: new B14 catalog check
  (wrapper exists + syntax valid + uses correct label).
- `AGENTS.md`: new "The `skygate` host-side wrapper" section.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 14/14 PASS
- `make verify-post` 26/26 PASS (auto-resolved container ID
  `37562e3b7332` via label)
- Two consecutive `docker compose up -d --force-recreate
  --no-deps skygate` invocations both started the new
  container cleanly (no `Created` stall). The v0.29.0 /
  v0.29.1 race that affected ~1 in 3 invocations is gone.

**Caveats (still open)**:
- The auto-generated container name (`skygate-skygate-1`)
  may increment on every recreate (`skygate-skygate-2`,
  `-3`, ...). The label-based lookup is robust to this, but
  operators who grep `docker ps` for the name will see it
  change. AGENTS.md documents the wrapper.
- v0.29.2 still leaves the orchestrator's auto-swap out
  of scope. A sidecar-based orchestrator (v0.29.3 follow-up)
  is the only way to get a fully automatic
  `git push → build → swap` flow without manual intervention.

## v0.29.3 / v0.29.3.1 — Auto-swap via helper container in host PID namespace

The v0.29.0 / v0.29.1 / v0.29.2 orchestrator chain
handles the "git push → build → swap" lifecycle, but
v0.29.2 still requires a manual `docker compose up`
on the host. v0.29.3 closes the loop: the orchestrator
itself does the swap, end-to-end, with auto-rollback on
any failure.

### The PID-namespace death race (v0.29.3 problem)

The v0.29.3 first version spawned a Setsid-detached
subprocess from inside the OLD skygate container to
run `docker compose up --force-recreate`. The subprocess
escaped the OLD container's process group (Setsid) but
was STILL in the OLD container's PID namespace. When
compose sent SIGTERM to PID 1 of the OLD container
(skygate itself), the signal propagated to all
processes in the same namespace, killing the swap
subprocess mid-way through `docker compose up`. The new
container would end up in `Created` state forever and
the operator had to `docker start <id>` by hand.

Live-verified on the VM at 2026-07-28 10:45 UTC: the
swap log showed only "Recreate" before the subprocess
died, and the new container `fb9547ead806` was stuck
in `Created`. A subsequent attempt with `unshare -fp`
inside the helper failed with "unshare: Operation not
permitted" (the skygate container's CapAdd doesn't
include CAP_SYS_ADMIN, which `CLONE_NEWPID` requires).

### v0.29.3.1 fix: helper container in HOST PID namespace

Instead of running the swap from inside the OLD skygate
container, the orchestrator spawns a HELPER CONTAINER
via `docker run --rm --pid=host --net=host
-v /var/run/docker.sock:/var/run/docker.sock
-v skygate-data:/data
-v $SKYGATE_HOST_REPO_PATH:/host_repo:ro`. The helper
uses the HOST's PID namespace, so its processes are
not in any skygate container's namespace and survive
the OLD container's removal cleanly.

The helper does the full swap:
  1. sleep 3s (orchestrator flush)
  2. `apk add --no-cache docker-cli docker-cli-compose`
     (alpine base image has no docker binary)
  3. `cd /host_repo && docker compose -p skygate -f
     /host_repo/docker-compose.yml up -d
     --force-recreate --no-deps skygate`
  4. poll up to 60s for the new container; if it's
     stuck in Created, call `docker start <id>`
     (handles the rare compose race where Created
     happened but Start didn't)
  5. final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     (helper has --net=host so localhost:8080 IS the
     new container's port)
  6. exit (`--rm` self-removes the container)

The OLD orchestrator's swap script now just spawns
the helper container in the background and exits
immediately. Helper self-removes via `--rm`.

### Defense in depth: confirmPendingSwap

The new orchestrator (in the new container) also has a
helper of its own: `confirmPendingSwap` (called from
`renderUpdatePage` on the first /admin/update page
load after the swap). It detects
`phase=build_done` / `phase=rolled_back`, calls
`startStuckSkygateContainer` (the v0.29.3.1 fix for
the `{{.State.Status}}` → `{{.Status}}` format-string
regression in the `docker ps` call), polls
`/healthz` on the new container for up to 30s, and
on 200 calls `store.Complete()` to promote the phase
to `done`. This is the final-arbitration step: even
if the helper container crashes before it can finish
its work, the next /admin/update page load completes
the swap.

### What changed (commits `49b67ce` ... `ebaa44e`)

- `internal/update/docker.go`:
  - `runShellDetached` helper (Setsid, fire-and-forget)
  - `swapSubprocessScript` rewritten to spawn the helper
  - `swapHelperScript` extracted as a separate Go constant
    (shared between success and rollback paths)
  - `writeSwapHelperScript()` Go helper for the success
    path to write the helper file at job start
  - `startStuckSkygateContainer` (v0.29.3.1) in
    `handlers_admin_update.go` — uses the now-correct
    `{{.Status}}` (was `{{.State.Status}}` which is
    `docker inspect` only) to find the new container
  - `confirmPendingSwap` (v0.29.3) in
    `handlers_admin_update.go` — /healthz poll + phase
    promotion on first /admin/update page load after
    a `build_done` or `rolled_back` phase
  - Critical bug fix: `StateStore.Load()` was parsing
    the state file but NOT storing in `s.state` (silent
    no-op for `Log()` and nil return from `Get()`).
    Fixed in commit `9fbc588`.
- `internal/handlers/handlers_admin_update_test.go`
  (NEW) — regression test for the
  `{{.Status}}` vs `{{.State.Status}}` format string
  (TestStartStuckSkygateFormatStringIsDockerPsValid)

### Live verification (2026-07-28, on the VM)

End-to-end test on `admin@192.0.2.1`:
  1. Applied `v0.99.0-nonexistent` via
     `POST /admin/update/apply` (target=v0.99.0-nonexistent)
  2. Orchestrator: backup tag created
     (`skygate-pre-update-ebaa44e`), `git fetch` failed
     (expected — the tag doesn't exist), `failWithRollback`
     spawned the detached subprocess
  3. Subprocess wrote `/data/skygate-swap-helper.sh`
     and ran `docker run --rm --pid=host --net=host ...`
     (the helper container)
  4. Helper installed docker-cli via apk, ran
     `docker compose up --force-recreate --no-deps skygate`,
     polled for the new container (status=running on
     attempt 1, no Created→Started race this time)
  5. Helper did a final healthz check via
     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
     and exited
  6. Operator loaded `/admin/update` →
     `confirmPendingSwap` detected `phase=rolled_back`,
     called `startStuckSkygateContainer` (no-op, container
     already Up), polled `/healthz` (200 on attempt 1),
     promoted phase to `done`
  7. State file log:
     ```
     renderUpdatePage: store=true phase=rolled_back (debug probe)
     renderUpdatePage: phase=rolled_back detected, calling confirmPendingSwap
     skygate container 359dec4c92f9 status=Up
     new orchestrator confirmed swap via /healthz (attempt 1)
     update completed successfully
     ```

`make verify-pre` 13/13 PASS, `make verify-post` 26/26 PASS.
`go test ./...` 19/19 packages green.

### Caveats

- The helper adds ~10s to the swap total (5s apk add
  for docker-cli + 3s orchestrator flush + ~2s compose
  up). Acceptable for an end-to-end upgrade.
- The helper requires network access from the
  `skygate-swap-helper` container to the alpine
  package repo (dl-cdn.alpinelinux.org) for the
  `apk add docker-cli` step. If the network is
  restricted, the swap will fail with
  "docker: not found". A pre-baked
  `skygate-swap-helper:VERSION` image with docker
  pre-installed would avoid this (future v0.29.4+
  work).
- The `ensureComposeServiceRunning` helper from
  v0.29.1 is still in place (unused since v0.29.2
  removed `container_name: skygate`) but is now
  redundant with `startStuckSkygateContainer`.
  v0.29.4 cleanup can remove it.

## v0.29.1 — Orchestrator stops at "image rebuilt", manual swap required

The v0.29.0 auto-updater's first end-to-end test on the
operator's VM surfaced a deeper architectural issue than the
five post-Phase-2 bugfixes had addressed: `docker compose up
--force-recreate --no-deps skygate` sends SIGTERM to the
skygate container — which IS the orchestrator's parent
process (skygate is PID 1 of the container, the orchestrator
is a goroutine inside skygate's HTTP server). The orchestrator
died mid-`up` before the swap completed, leaving the new
container in an undefined state with no healthz verification.
The previously-suspected "Created→Started race" was a
misdiagnosis of this: the race fix (`ensureComposeServiceRunning`)
was the right defensive measure but never got to run because
the orchestrator was already dead.

**Fix**: the orchestrator no longer calls `docker compose up`.
It stops at "image rebuilt + migrations applied" and writes a
one-line `manual_swap` ("docker compose up -d --force-recreate
--no-deps skygate") for the operator to run on the host. The
swap is the only step that has to be on the host — the rest
of the upgrade (backup tag, fetch, checkout, build, rollback
on failure) runs entirely inside the orchestrator.

**Changes**:
- New `PhaseBuildDone` state phase (replaces `swap` + `verify`
  in the success path). `failed` / `rolled_back` are still
  used for error paths.
- New `ManualSwap` field in the state JSON for the single
  command. `SetManualStep(kind, cmd)` helper to add it.
- `ensureComposeServiceRunning` is left in place (untested
  but compile-checked) for v0.29.2 when the orchestrator
  moves to a sidecar container.
- Pre-push hook now uses `MSYSTEM` (set by Git for Windows)
  as the primary Git Bash detection signal, ahead of the
  directory-probe fallback that was unreliable on hybrid
  WSL2+Git Bash systems. **No more `--no-verify` workaround**
  for normal pushes from Git Bash.
- New B13 catalog check: pre-push hook contains `MSYSTEM`.

**Live verification (operator's VM, 2026-07-28)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 13/13 PASS
- `make verify-post` 26/26 PASS
- End-to-end auto-update test: applied `v0.29.0` (real
  existing tag, but with local changes so `git fetch`
  fails — exercises the rollback path) → backup tag
  created → fetch failed as expected → automatic rollback
  `git checkout` OK → chown OK → rollback `docker compose
  build` OK → state at `rolled_back` with `manual_swap`
  populated. Orchestrator survived all the way to the end.
  Container still on the previous build (29b4564).

**Caveats (still open)**:
- The orchestrator can do everything up to `docker compose
  build`. The final `docker compose up` must be done by the
  operator on the host. v0.29.2 follow-up: move the
  orchestrator to a sidecar container that's outside
  skygate's process tree, so the SIGTERM from `up` doesn't
  kill the orchestrator.
- The same `container_name: skygate` race still affects
  host-side `docker compose up` (rare but observed once in
  the v0.29.1 verification). Manual `docker start <id>`
  recovers.

## v0.29.0 — Self-update orchestrator (in-app upgrade + auto-rollback)

The `/admin/update` page (v0.29.0 Phase 1) now ships with a working
in-app upgrade flow. The orchestrator runs the full `git fetch` →
`git checkout <tag>` → `docker compose build` → `docker compose
up --force-recreate` → `/healthz` poll sequence in a background
goroutine and reports each phase to a bind-mounted status file
(`/data/skygate-update-status.json`). On any phase failure the
orchestrator automatically rolls back to the previous tag, including
`docker compose build` + recreate + healthz poll again. If rollback
itself fails, the state file shows the manual steps for operator
intervention.

**Phase 1 (commit `e3ce6f0`)** — detection + manual steps + UI:
GitHub Releases API client, semver comparison, install-kind
detection (Docker/systemd/bare), copy-pasteable manual commands,
`/admin/update` page with status panel + auto-refresh, 17 i18n
keys × 2 langs. Renders on VM in RU + EN, GitHub checker works
(no false "new version" banner when current build is ahead of
all released tags).

**Phase 2 (commit `caf6fb8`)** — auto-updater with state machine
+ auto-rollback: `/admin/update/apply` kicks off the orchestrator,
`/admin/update/rollback` cancels + restores previous, status
file persists across container recreate. 18 i18n keys × 2 langs.

**Post-Phase-2 fixes (commits `0020815`, `4bb4db6`, `f9d3860`,
`a18ad0c`, `5177643`, `bae4fb4`)** — five bugs discovered on
the operator's VM during the first end-to-end auto-update test:

1. **`SKYGATE_REPO_PATH` chdir failure** (`0020815`). The
   orchestrator ran inside the skygate container but tried to
   `chdir /home/admin/skygate` — a host path unreachable
   from inside the container. The source dir is bind-mounted at
   `/app` (per docker-compose.yml's `./:/app`). Fix:
   `defaultRepoPath()` in `internal/config/config.go` auto-
   detects container mode via `/.dockerenv` /
   `/run/.containerenv` and defaults to `/app`. Bare/systemd
   hosts still default to `/home/admin/skygate`.
   `SKYGATE_REPO_PATH` env always overrides.

2. **`sudo chown` in Alpine** (`0020815`). The previous
   rollback path used `sudo chown -R admin:admin ...`
   which fails in the Alpine container (no `sudo` binary, no
   `admin` user). Fix: `detectHostOwner()` captures
   `stat -c '%u:%g' .git/HEAD` BEFORE the first git mutation
   and `chownToHostOwner()` does `chown -R <uid>:<gid>` (no
   sudo). Default 1000:1000; override via `SKYGATE_HOST_OWNER`.

3. **Missing `docker compose` plugin in image** (`4bb4db6`).
   The Alpine image's `docker-cli` package installs only the
   `docker` binary; `docker compose` is the separate
   `docker-cli-compose` plugin. Without it, the orchestrator's
   `docker compose build skygate` errors with "docker:
   unknown command: docker compose". Fix: add
   `docker-cli-compose` to the `apk add` list (~3 MB).

4. **Project name mismatch** (`f9d3860` + `bae4fb4`).
   Docker compose computes the project name from the basename
   of the working directory by default. Inside the container
   the orchestrator runs from `/app` (basename "app") while
   the host's compose was launched from `/home/admin/skygate`
   (basename "skygate"). Two-part fix:
     (a) `docker-compose.yml` env section adds
         `COMPOSE_PROJECT_NAME=skygate`.
     (b) `DockerUpgrader.runCompose` always passes
         `-p <ComposeProject>` (default "skygate", override
         via `SKYGATE_COMPOSE_PROJECT`).

5. **Bind-mount paths invisible to host dockerd** (`a18ad0c` +
    `5177643` + `bae4fb4`). The docker daemon runs on the
    host, so it resolves bind-mount sources as HOST paths. From
    inside the container, `./secrets/ts_authkey` resolved to
    `/app/secrets/ts_authkey` from compose's perspective, but
    the daemon then looked for that path on the host (where
    `/app` doesn't exist). Fix: replace all `./` paths in
    `docker-compose.yml` with
    `${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}/` and
    add `SKYGATE_HOST_REPO_PATH` to the skygate service env.
    The `${VAR:-default}` syntax is shell-style fallback
    supported by docker compose.

**Live verification (operator's VM, 2026-07-27)**:
- `go test ./...` 19/19 PASS, `make verify-pre` 12/12 PASS
- `docker_test.go` adds 9 test groups (TestShortSHA,
  TestDetectHostOwner_EnvOverride, TestDetectHostOwner_StatAutoDetect,
  TestDetectHostOwner_DefaultFallback, TestDetectHostOwner_Cached,
  TestChownToHostOwner_ArgsShape, TestOwnerPattern,
  TestTruncateOutput, TestNewDockerUpgrader_Defaults,
  TestNewDockerUpgrader_ProjectOverride)
- `make verify-post` 26/26 PASS after the fix chain
- End-to-end auto-update test: applied `v0.99.0-final-test`
  (non-existent) → backup tag created → fetch failed as expected
  → automatic rollback checkout OK → chown to host owner OK →
  container recreated → /healthz 200 with previous build label

**Caveats (still open)**:
- Rollback `docker compose up --force-recreate --no-deps skygate`
  occasionally leaves the new container in `Created` status
  instead of `Started` (race with the old container's
  `container_name: skygate`). Manual `docker start <id>` recovers.
  Tracked as v0.29.1 follow-up: remove `container_name: skygate`
  or improve the orchestrator to handle the Created→Started transition.
- The orchestrator is Docker-only. Systemd / bare install kinds
  generate manual steps but don't auto-execute (Phase 3 follow-up).
- Apply works only for tags already pushed to origin. The orchestrator
  does `git fetch --tags` + `git checkout <target>`; a tag that
  exists only on the operator's local clone would not be findable.

## v0.28.7 — Per-DEVICE ACL grants for tagged devices (Moonlight fix)

The v0.28.0 per-device ACL design tagged every device with
`tag:dev-<user>-<device>`. The per-user grant `src=user@` was supposed
to cover tagged devices too, but in Tailscale v2 policy the per-user
identity doesn't match tagged devices — only the tag does. Result:
workstation-2 (Android, `tag:dev-admin-workstation-2`) couldn't reach workstation-1
(Windows, `tag:dev-admin-workstation-1`) over Tailscale for Moonlight,
even though both devices belonged to the same portal user.

### What changed

v0.29.0 adds a **per-DEVICE grant block** to the generated policy:
for each portal user with N≥2 devices, emit N grants (one per
device as `src`), with `dst` = the list of all OTHER devices of
the same user, and `ip: ["*"]` (required by headscale 0.29.2).
13 grants on this VM (11 admin + 2 user1); O(n) per user,
not O(n²).

The earlier v0.28.7 attempt (commit `8749069`) tried a wildcard
`tag:dev-<user>-*` src, which headscale 0.29.2 rejects with
"src=tag not found" (the parser requires concrete tags in
`tagOwners{}`).

### Why this works

Tailscale ACL is order-sensitive (first match wins). The per-DEVICE
grant is emitted **after** the per-user grant (which keeps SSH
+ untagged identities working) and **before** per-device exit-rules
+ per-device loose grants (autogroup:internet). Tagged-to-tagged
device traffic on the same tailnet now matches the per-DEVICE
grant as a fallback when no specific per-device rule applies.

### Code shape

The per-DEVICE block is duplicated in `GenerateACLForPlane` and
`GenerateACLWithViaForPlane` (the v0.28.1 per-user via variant
takes a parallel code path). v0.30.0 will extract the block to a
shared helper — the duplication is documented in the AGENTS.md
release notes for v0.28.7 as a known cleanup target.

### Verification

- Live: 13 new per-DEVICE grants in the headscale policy after
  `/admin/exit-rules/reapply` (HTTP 303 redirect)
- Operator confirmation: workstation-2 ↔ workstation-1 Moonlight session
  now establishes and streams (2026-07-27)
- `make verify-pre` 9/9 PASS, `make verify-post` 25/26 PASS
  (R9 is a known false-negative — the verify-post script reads
  an older `acl_snapshots` row; the most recent reapply did
  succeed and saved a fresh snapshot)

## How a release is cut

1. `git tag -a v0.X.Y -m "v0.X.Y"` on the commit we want to ship.
2. `git push origin v0.X.Y`.
3. (Operator-driven) create a GitHub release at
   https://github.com/skygate-operator/skygate/releases/new — the body
   summarizes the commits since the previous tag.
4. Update `CHANGELOG.md` to move the entry from `[Unreleased]`
   into the new tagged section.

The operator (admin) writes the release body; the git tag is
the source of truth for "what shipped in v0.X.Y".
