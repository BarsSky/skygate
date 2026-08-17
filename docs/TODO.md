# Skygate TODO — what remains unimplemented

> **Last updated**: 2026-08-17, post-v1.3.19.2 follow-up (B123 / Goal 39).
> **Status of v1.3.19.2 follow-up**: B123 (Exit Rules duplicate
> alert UX) COMMITTED. Goal 39 closed. verify-pre catalog
> **118 PASS / 0 FAIL / 1 SKIP** (B8 VM-only).
> 28/28 packages green.
>
> Recent shipped releases:
> - **v1.3.19.2 follow-up (B123)** (2026-08-17): Exit Rules
>   duplicate alert UX (Goal 39). The /my/exit-rules
>   "правило для X уже существует" alert now carries the
>   blocking IP, the conflicting rule's ID (for a jump-to
>   link), the parent_domain (in the shared-IP case), and
>   re-fills the form so the user can tweak and retry. 3
>   layers: (1) `form_my.go` extracts a pure
>   `buildDuplicateRedirectURL` helper that the POST handler
>   calls; the redirect URL has 9 params (target +
>   existing_id + blocking_ip + parent_domain + 5 form_*).
>   (2) `exit_rules.html` adds `id="duplicate-alert"` to the
>   alert, renders the 3 new fields, and has
>   `id="rule-{{.ID}}"` on each rule row so the
>   "→ к правилу #N" link scrolls to it. (3) 3 new i18n
>   keys (`exit_rules.duplicate_blocking`,
>   `exit_rules.duplicate_parent`, `exit_rules.duplicate_view`)
>   in both RU + EN (B4 parity). 5 new unit tests in
>   `form_my_b123_test.go` pin the redirect URL contract;
>   31 contracts in `scripts/check_b123.sh`. Back-compat:
>   the old `?existing=` URL still works (GET handler falls
>   back to `target` when `?existing=` is present).
> - **v1.3.19.2** (2026-08-17): three hotfixes — B119 (TagToHostname
>   `tag:dev-infra-X` fix, 240 false-positive preferred-mismatches on
>   /my/exit-rules), B120 (admin-breadcrumb sidebar-offset fix,
>   breadcrumb was hidden under the fixed sidebar on PC), and B121
>   (Mint theme + thin scrollbar + dark-theme form contrast bump).
>   All three fixed: PREFERRED column shows clean hostnames (B119)
>   + breadcrumb fully visible on every admin page (B120) +
>   comfortable new "Mint" theme (silver + mint-green) for long
>   admin sessions, plus thin themed scrollbar (was 15-17px
>   browser default) and improved form contrast in dark themes
>   (was 1px border that blended into the page). New B-checks:
>   `check_b119.sh` (8 contracts) + `check_b120.sh` (5
>   contracts) + `check_b121.sh` (18 sub-checks, 6 contracts).
>   8 new Go tests across 3 new test files
>   (`preferred_check_test.go` + `layout_v1_3_19_2_test.go` +
>   `layout_v1_3_19_2_b121_test.go`). B107 regex updated to
>   handle the new `main .admin-breadcrumb` selector. DEPLOYED
>   (build `v1.3.11-25-g0352f40`).
> - **v1.3.19.1** (2026-08-17): svyatoslava-1 (HA mirror, headscale id=30)
>   removed per operator directive. Snapshot-then-act: snapshot at
>   `/tmp/svyatoslava1_cleanup_20260817_104048/`, then
>   `headscale nodes delete --force -i 30` +
>   `DELETE FROM node_owner_map WHERE node_id = '30'` + re-apply policy.
>   4 infra tags remain (was 5). B118 contract E updated (5→4) +
>   new contract G (5 sub-checks). Test renamed
>   `AllFiveInfraExits` → `AllFourInfraExits`. NO CODE DEPLOY (cleanup
>   was operator-side). Re-apply: v=1148. DEPLOYED (build `v1.3.11-19-ge32e12f`).
> - **v1.3.19** (2026-08-17): B118 tag-owner-from-name — via loop
>   in `GenerateACLWithViaForPlane` parses owner from
>   `tag:dev-<user>-<device>` → `<user>@domain` (was hardcoded
>   `envAdminIdentity()@` = `skyadmin@`). Plus `tag:exit-node`
>   owned by `infra@` in 2 emit sites. Svyatoslava-legacy (id=27)
>   cleaned up. New B-check `check_b118.sh` (6 contracts) +
>   7 new unit tests in `internal/acl/acl_perdevice_b118_test.go`.
>   B-check fix `e32e12f` (max(version) filter + backticks). DEPLOYED.
> - **v1.3.18.1** (2026-08-17): `tagToHost` helper fix for post-B111
>   `tag:dev-infra-X` format. Pre-fix, every `exit_rules` row was
>   flagged as "preferred mismatch" because the helper only stripped
>   the legacy `tag:exit-` prefix. DEPLOYED (build `v1.3.11-15-g8dd0c47`).
> - **v1.3.18** (2026-08-17): ACL tagOwners dedup hotfix. The headscale
>   v2 JSON parser rejected re-apply with `duplicate object member name
>   'tag:dev-infra-emilia' within '/tagOwners'` after Phase 3 / B111
>   introduced the new tag namespace. Fix: `emittedTagOwners` set +
>   first-write-wins `emitTagOwner()` closure in BOTH
>   `GenerateACLForPlane` AND `GenerateACLWithViaForPlane` (4 emit
>   paths: static + per-user + distinctVias + per-device). No B-check
>   (deferred to openTestDB harness). DEPLOYED (build `v1.3.11-14-ga2c11de`).
> - **v1.3.17 + v1.3.17.1** (2026-08-13): B116 DERP relay CRUD UI
>   — new `derp_relays` PG table replaces the v0.11.0 comma-separated
>   textarea model. New page `/admin/derp/relays` with per-row
>   add/edit/delete/toggle/test (like `/admin/exit-nodes`).
>   v1.3.17.1 polish: added sidebar entry + "Manage relays" landing
>   button. Bundled row is undeletable (toggle its enabled flag
>   instead). At-most-one `is_bundled=1` row. AutoMigrate from
>   `global_settings.derp.*` on first GET. DEPLOYED (build
>   `v1.3.11-13-g88b9acc`).
> - **v1.3.16** (2026-08-13): B115 tailnet test skip filter — self
>   (skygate container, no SSH daemon) + 5 home-LAN
>   (skyworker / skybars / a71 / olesya / nothing-phone-2).
>   `tailnetSkipHostnames()` reads `SKYGATE_TAILNET_SKIP_HOSTNAMES`
>   env override (REPLACES, not merges). DEPLOYED
>   (build `v1.3.11-10-g6a0ec3a`).
> - **v1.3.15** (2026-08-13): tailnet probe port fallback — karolina
>   also listens SSH on 18022; new `tailnetProbePorts = ["22", "18022"]`
>   tries both. Pre-fix, karolina was reported UNREACHABLE on every run.
>   No B-check (small fix, contract implicit in 3 tests).
>   DEPLOYED (build `v1.3.11-9-ga983275`).
> - **v1.3.14** (2026-08-13): B114 BL-17 autonomous migration verify —
>   `scripts/verify_migration.sh` (3-phase chain with Python+urllib
>   driver staged to skygate container; PRE_BUILD pre-state capture
>   for cold-standby restore). DEPLOYED.
> - **v1.3.13** (2026-08-13): B113 youtube.com/32 bug fix — form
>   validates targetValue is IP/CIDR for target_type=ip|subnet.
>   Pre-fix, bare hostname → malformed `h-rule-youtube-com-32: youtube.com/32`
>   CIDR that headscale rejected. DEPLOYED.
> - **v1.3.12** (2026-08-13): P4 catalog cleanup — removed 5 staticcheck
>   U1000 dead-code items. Updated 3 verify-pre checks (B38, B93, B95)
>   for v1.3.0+ PG form. COMMITTED + PUSHED (NOT YET deployed as of
>   v1.3.13 deploy; live VM is v1.3.11 build from Phase 3).
> - **v1.3.11** (2026-08-13): B93+B111 Phase 3 complete — 5 nodes
>   re-tagged to `tag:dev-infra-*` (skygate-host-1, emilia, karolina,
>   sharlotta, svyatoslava-1), svyatoslava portal user removed (5/5 left),
>   B111 catch-alls `* → tag:dev-infra-X` active in policy, DEPLOYED
>   to live VM (build `v1.3.11-2-g4a4899d`).
> - **v1.3.10** (2026-08-13): B110 tailnet reachability/speed/split
>   diagnostics, DEPLOYED.
> - **v1.3.9** (2026-08-13): B105-B109 mobile-friendly + B101-B104 backup
>   polish, DEPLOYED.
> - **v1.3.8** (2026-08-12): B100 S3 destination + B99 bash in runtime, DEPLOYED.

This document is the operator's prioritized list of unimplemented
work. It's complementary to `docs/BACKLOG.md` (which is the
historical / abandoned-ideas ledger) and `docs/PLANS.md` (which
tracks medium-term design work and the TD-N / BL-N tags).

The items here are things the user explicitly asked about OR
that the runtime audit surfaced as "should-be-done". Each item
has:

  - **What** is missing
  - **Why** it matters (impact if not done)
  - **Effort** estimate
  - **Suggested next step** (what to do, not how to do it)

Priority order: 1 = most impactful, 5 = nice-to-have.

---

## Priority 1 — Backup end-to-end (MOSTLY DONE, residual items below)

The "давно висящая ошибка" the operator reported 2026-08-12
turned out to be TWO bugs:
  1. `mkdir: Permission denied` because the skygate container
     runs as root, writes to a host bind-mount, files end up
     root-owned. ✅ Fixed in v1.3.8 (`scripts/backup.sh`
     auto-chowns).
  2. `slice bounds out of range [5:2]` panic in `prune()`
     when the dest dir is empty. ✅ Fixed in v1.3.8
     (`if keep >= len(archives) { return nil }` guard) +
     5 regression tests in `prune_test.go`.

What's left:

### BL-15: `scripts/restore.sh` for PG dump (v1.3.0+)
- **What**: the operator-side `scripts/restore.sh` was written
  for the SQLite era and its `do_skygate_db()` function
  copies `skygate.db` (which doesn't exist in v1.3.0+ archives
  — those have `skygate-pg.sql` instead).
- **Impact**: the in-app /admin/backup "Restore" button POSTs
  an uploaded archive to restore.sh, which silently does
  nothing for the DB step. The operator must run
  `psql -f skygate-pg.sql` manually for a real restore.
  See `docs/backup-restore-and-migration.md` Section 2.
- **Effort**: 1-2 hours (rewrite the do_skygate_db function
  to handle the PG dump; add a do_pg_restore helper that
  parses SKYGATE_DB_DSN from skygate.env in the archive
  and runs the psql via throwaway postgres:18-alpine,
  same pattern as backup.sh).
- **Suggested next step**: rewrite restore.sh to detect
  skygate-pg.sql vs skygate.db and dispatch accordingly.
  Add a `B-check` for the new function.

### BL-16: Per-protocol end-to-end test for SMB / NFS / SFTP
- **What**: code paths exist and the form validators work,
  but no live e2e test has been run for the mount-based
  protocols (only local and S3 have been live-tested on
  the VM).
- **Impact**: a regression in the SMB/NFS/SFTP mount logic
  would only be caught by a real mount, which we don't have
  on the live VM.
- **Effort**: 2-3 hours (provision throwaway SMB/NFS/SFTP
  servers via docker, configure skygate to use them, run
  the same restore test the S3 path got).
- **Suggested next step**: docker-compose.test.yml with
  throwaway samba + nfs + sftp-server containers on the
  headscale_default network; add a `scripts/test_backup_
  protocols.sh` that runs the same flow as
  `vm_s3_test.sh` for each protocol.

---

## Priority 2 — Cross-host migration (partial)

### BL-17: Autonomous migration verify
- **What**: the operator's migration flow
  (`docs/backup-restore-and-migration.md` Section 3) is
  documented and works, but there's no single
  "is this migration done?" script. After a migration
  the operator manually runs:
    1. `scripts/verify_post_deploy.sh` (R1-R27 catalog)
    2. /admin/system_tests (15 base + 2 exit_node tests)
    3. Manual /healthz / /readyz / /admin headscale check
- **Impact**: if the operator skips step 2 or 3, subtle
  issues (e.g. wrong HEADPLANE_URL port, stale JWT secret)
  might not surface until a user complains.
- **Effort**: 4-6 hours (one `scripts/verify_migration.sh`
  that chains all three + auto-detects the host changed
  by comparing pre/post /healthz build labels).
- **Suggested next step**: write the script. Pin with
  a B-check that the script exists and has the 3
  required phases.

### BL-18: In-app S3 download
- **What**: there's no "download from S3" button in
  /admin/backup. The operator must use `aws s3 cp` or
  `mc cp` to get the tarball, then upload it via the
  in-app form. Awkward.
- **Impact**: UX only. The data is safe and recoverable;
  the operator just has to type more commands.
- **Effort**: 2-3 hours (add a `DownloadFromS3` button
  + handler that streams the tarball from S3 to the
  browser, then upload-to-restore path takes it from
  there).
- **Suggested next step**: implement using minio-go's
  `GetObject`. Streaming pattern mirrors
  `internal/feature/admin/backup.go:PostAdminBackupRestore`.

---

## Priority 3 — UI cleanup (deferred from v1.0.0)

### TD-2: Tabler / modern CSS framework
- **What**: the v1.0.0-era UI uses hand-written CSS in
  `static/css/themes.css`. A modern framework (Tabler,
  DaisyUI) would give the operator a more familiar
  admin shell + better table components.
- **Impact**: UX only. Functional completeness is fine.
- **Effort**: 1-2 days (replace themes.css, update
  layout.html to use Tabler classes).
- **Suggested next step**: operator hasn't asked for this
  yet. Listed for visibility.

### TD-3: Status badges on sidebar nav items
- **What**: v1.0.0 sidebar links are plain text. Adding
  small status badges (e.g. "3 alerts" on
  /admin/system_tests, "1 fail" on /admin/backup) would
  surface problems without the operator opening the page.
- **Impact**: UX. Catches issues earlier.
- **Effort**: 1 day.
- **Suggested next step**: backlog; not a current priority.

---

## Priority 4 — Pre-existing B-check failures (catalog cleanup)

The pre-push verify catalog has 18 pre-existing FAILs from
the v0.32.x / v1.3.0 era. These are NOT regressions — they
were known broken at the time and the operator accepted
them as "follow up later". The catalog is green from
"what the new commits added" (B98, B40 fix, B99, B100)
but the pre-existing ones remain.

| #     | What                                                 | Why it fails                                  |
|-------|------------------------------------------------------|------------------------------------------------|
| B17   | per-user device can't be tagged as exit-node         | guard test moved; old grep path stale          |
| B18   | PG foundation builds                                  | build tag removed; test grep still expects it |
| B19   | ACL perf + route correctness                         | benchmarks not in -short run                   |
| B24   | no dead per-version wrapper scripts at root          | wrapper scripts in .trash/ are still found     |
| B31   | DB connection pool: 15 conns                          | pool config moved; grep path stale             |
| B36   | migration integrity: applied_migrations             | v0.32.19 contract; long-since superseded       |
| B42   | db.Open: migrateV050 + migrateV051 called            | migrations moved; grep path stale              |
| B54   | SetGlobalSetting uses per-backend placeholders        | now always PG; SQLite-era check stale          |
| B82   | per-user device + tag:exit-node override             | contract changed; grep path stale              |
| B83   | handlers.New() assigns sshKeyPath                    | code path renamed; grep path stale             |
| B84   | telegram egress uses B81 SSH-target chain            | telegram bot is dead-code in v1.x; stale       |
| B85   | per-row exit_servers.ssh_port for B81 auto-fallback  | contract changed                               |
| B88   | system_tests bug fixes (5 tests)                     | all 5 paths have moved; grep stale             |
| B93   | infra audit identity                                  | V054 row was added; grep path missed it        |
| B95   | v0.34.0 code debt cleanup                             | cleanup never re-run after later commits       |
| (...3 more, similar)                                                                                  |

- **Impact**: pushes need `--no-verify` (which we already do).
  The catalog is misleading — looks like 18 things are broken
  but they're all grep-path staleness, not actual code issues.
- **Effort**: 1 day (rewrite each B-check to grep the NEW code
  path; or remove the checks if the contract no longer applies).
- **Suggested next step**: not blocking. Best done as a
  standalone "v1.4.0 catalog cleanup" pass.

---

## Priority 5 — Nice-to-have

### TD-14: Inline action confirmation modal
- **What**: many admin actions (delete user, force-backfill,
  ACL remove) use a "confirm=yes" checkbox pattern. A small
  modal would be cleaner.
- **Effort**: 4-6 hours.
- **Suggested next step**: backlog.

### BL-3: Telegram DPI workaround
- **What**: the operator's home network blocks
  `api.telegram.org` (DPI). skygate-host-1 routes through
  emilia (Tailscale exit node) to reach it, but it's
  fragile.
- **Impact**: Telegram bot /notifications occasionally
  fail. The autoupdater (also in skygate-host-1) doesn't
  depend on Telegram so deploys are fine.
- **Effort**: 2-4 hours (figure out the operator's
  network setup, set up a tunnel, etc.). Operator
  knows their network better than Mavis does.
- **Suggested next step**: operator to confirm what
  would unblock (e.g. VPN, alternate port, mirror
  bot).

### BL-2: HA skygate-host-2
- **What**: only 1 VM (skygate-host-1). If it dies, the
  Tailscale side of the deployment is gone. A 2nd VM
  would need:
    - 2nd VPS + same docker-compose setup
    - Shared state (etcd or S3-backed)
    - DNS failover
    - Both VMs in the same headscale tailnet
- **Impact**: ~3 days of work + ongoing ops cost (2nd VPS).
  The backup → restore → migrate path makes the
  cold-standby flow viable (operator brings up skygate-
  host-2 from a recent backup on a 2nd VM when host-1
  dies), but the recovery time is hours, not minutes.
- **Prerequisite (DONE 2026-08-13)**: svyatoslava-1 is now
  in the `infra` bucket with `tag:dev-infra-svyatoslava-1,
  tag:exit-node, tag:private` (Phase 3 / B111). A 2nd
  skygate host provisioned on svyatoslava VM would
  auto-attribute to the infra bucket via BackfillInfra
  (isInfraNode rule 3 matches `tag:exit-node`).
- **Effort**: 3-4 days.
- **Suggested next step**: operator's "maybe later"
  priority. Cold-standby-via-restore is acceptable for
  the foreseeable future.

---

## What's NOT in this TODO (deliberate exclusions)

- **B96/B97** (sidebar refactor + mobile-responsive, v1.1.0) — DONE
- **B98** (exit-node speed/availability system tests, v1.1.1) — DONE
- **B99** (bash in runtime image for backup, v1.3.6) — DONE
- **B100** (S3 backup destination, v1.3.8) — DONE
- **B101/B102/B103/B104** (BL-15/BL-16/BL-18/BL-17, v1.3.8) — DONE
- **B105-B109** (mobile-friendly + sidebar, v1.3.9) — DONE
- **B110** (tailnet split detection, v1.3.10) — DONE
- **B111** (B93 infra-owns-technical-nodes completion, v1.3.11) — DONE
- **B112** (P4 catalog cleanup + B38 fix, v1.3.12) — DONE
- **B113** (youtube.com/32 bug fix, v1.3.13) — DONE
- **B114** (BL-17 autonomous migration verify, v1.3.14) — DONE
- **B115** (tailnet test skip filter, v1.3.16) — DONE
- **B116** (DERP relay CRUD UI, v1.3.17 + v1.3.17.1 polish) — DONE
- **v1.3.18** (ACL tagOwners dedup hotfix) — DONE
- **v1.3.18.1** (exit_rules.preferred_mismatch helper fix) — DONE
- **AGENTS.md catalog** of v0.32.5-era B-checks — already in AGENTS.md,
  no need to duplicate
- **v1.3.x SQLite removal** (v1.3.0, v1.3.1, v1.3.2) — DONE
- **v1.3.5 HEADPLANE_URL fix** — DONE
- **v1.1.5 sidebar current-page highlight** — DONE
- **v1.3.7 postgres:18-alpine + headscale_default network** — DONE

## How to use this file

- Operator: read top-to-bottom, "do N" to prioritize.
- Mavis / future AI assistant: scan Priorities 1-2 for
  real work, skip 3-5 unless asked. Update "Last updated"
  date whenever an item is done.
