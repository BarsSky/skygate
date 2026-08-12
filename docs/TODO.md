# Skygate TODO — what remains unimplemented

> **Last updated**: 2026-08-12, post-v1.3.8 (backup permission fix + S3).
> **Status of v1.3.8**: COMMITTED (`7f5d4fe`) + PUSHED + DEPLOYED to
> live VM 192.168.13.69 (build `v1.3.7-1-g7f5d4fe`). All
> `go test ./...` packages green (28/28), B100 catalog check 37/37
> PASS, S3 e2e verified end-to-end with minio throwaway.

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
