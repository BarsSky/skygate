# v0.25.1 — Closing the loose ends (2026-07-22)

The "before we add HA, let's clean up the corners"
release. Three small items, all backward-compatible.

## 1. Per-user audit log export (CSV/JSON)

New endpoint: `GET /my/account/audit?format=csv&since=7d`.

Each portal user (admin or not) can now download their
own audit trail. The query is scoped to `(user_id = ?
OR (user_id = 0 AND username = ?))` so:

- ✅ the user's own `login_ok`, `subnet_provision`,
  `preauth_issued`, etc. rows are included
- ✅ system events on the user's behalf
  (`telegram_restart`, `telegram_ack` recorded with
  `user_id=0, username="skyadmin"`) are also included
  — they ARE the user's audit trail
- ❌ other users' rows are NEVER included (no
  cross-user leak)

Query parameters:
- `format` — `csv` (default) or `json`
- `since` — `7d`, `30d`, `24h`, `<unix-seconds>`,
  or RFC3339 timestamp. Empty = no lower bound.
- `limit` — max rows (default 10000, hard cap)
- `offset` — pagination

Response:
- `Content-Type: text/csv; charset=utf-8` (or
  `application/json; charset=utf-8`)
- `Content-Disposition: attachment;
  filename="skygate-audit-<username>-<YYYYMMDDTHHMMSS>.<ext>"`
- `X-Content-Type-Options: nosniff`

Self-audit: every export writes a `audit_export` row to
`audit_log` (with `format=`, `since=`, `limit=`,
`rows=` in the detail). So the operator can see
"skyadmin exported 1109 rows on 2026-07-22 21:55:05" in
`/admin/audit?action=audit_export`.

## 2. `docs/disaster-recovery.md` — DR runbook

Full single-VM recovery runbook (~330 lines). Covers:

- **TL;DR** — 15-minute recovery procedure for the case
  where the VM dies but the nightly backup is intact.
- **What's backed up** — every artifact `deploy/backup.sh`
  captures, with sizes, paths, and notes on what's
  intentionally NOT backed up (Tailscale client keys,
  in-flight WAL writes, etc.).
- **RPO/RPO targets** — 1 hour (backup frequency) /
  30 min (manual recovery with rehearsed runbook).
- **Step-by-step recovery** — provision VM, restore
  headscale (DB + config + noise_private.key), restore
  skygate (DB + .env), repoint DNS (60s TTL pre-set),
  run `make smoke`, document the recovery in audit_log.
- **What the v0.26.0 HA work changes** — RPO → 0s,
  RTO → 30s, but the cold-restore path stays as the
  "if all HA fails" backstop.
- **DR drill cadence** — quarterly practice, time the
  RTO, fix any step that takes > 30 min.

## 3. Cleanup

### .gitignore

The root-level investigation/verify/debug scripts
(22+ entries in `git status` after every ops session)
are now ignored. Affected patterns:

```
check_*.sh  verify_*.sh  test_*.sh  copy_*.sh
debug_*.sh  reset_*.sh   fix_*.sh  cleanup_*.sh
deploy_*.sh run_*.sh     wait_*.sh
check_*.py  verify_*.py  test_*.py
check_*.ps1 verify_*.ps1 test_*.ps1
```

`scripts/test_*.py` (in the scripts/ subdir) is still
allowed — it has test files the Makefile `test` target
needs. The cleanup is one-line per file: move to
`scripts/` and re-add explicitly if a script graduates
to "kept" status.

### Per-user bot routing (v0.12.1 follow-up) — already done

Discovered during this work that `BotEnv.HSForPortalUser`
has been wired through every bot command since v0.12.0
(2026-07-15). The followup was tracked as a backlog
item but is no longer needed. Closing it as already-done
in this release notes; no code change.

### 21 audit mesh cleanup — script, not code

`scripts/cleanup_orphan_meshes.sh` is a one-off
operator-script (NOT auto-run, NOT a cron). It:

- Lists meshes with no members AND created > 30 days ago
- Prompts for confirmation (unless `CLEANUP_NO_PROMPT=1`)
- Defaults to `status='inactive'` (audit-trail preserved);
  set `CLEANUP_MODE=delete` to actually DELETE rows
- Writes an `audit_log` row with `action='mesh_cleanup_*'`

The 21 meshes created during v0.22.0 testing are still
in the DB (visible in `/admin/meshes` as `active` with
0 members). The script is ready to run when the operator
wants to clean them up.

## What did NOT change

- No new env vars.
- No schema migration.
- No new packages.
- 17/17 packages green.
- Production deploy is identical: 4 users, 11 headscale
  nodes, 3 subnets live, watcher + sidecar + monitoring
  unchanged.
- 1 unit test (TestListAuditLogForUser) covers the
  audit export query, including the user+system-username
  OR fallback and pagination.

## Files

- `internal/db/audit_log.go` (+30 lines) — `AuditRow`
  struct, `ListAuditLogForUser` query
- `internal/db/audit_log_v0_25_1_test.go` (+96 lines) —
  test for the export query
- `internal/handlers/handlers_my_audit.go` (new, +180
  lines) — `GetMyAccountAuditExport` handler +
  `parseSinceParam` helper
- `cmd/skygate/main.go` (+5 lines) — new route
  registration
- `docs/disaster-recovery.md` (new, +330 lines) — DR
  runbook
- `scripts/cleanup_orphan_meshes.sh` (new, +95 lines)
  — optional 21-mesh cleanup
- `.gitignore` (+25 lines) — root-level scratch-script
  patterns

## What comes next (v0.26.0)

- **PostgreSQL dual-mode** — `DATABASE_URL` env var,
  `pgx` driver, `database/sql` calls stay portable.
- **Stateless design** — session/cookie stored in DB
  (not in memory), `APP_INSTANCE_ID` for log
  correlation.
- **Health endpoints** — `/healthz` (liveness) +
  `/readyz` (DB + headscale reachable).
- **Read replica** — `DATABASE_READ_URL` for SELECT
  routing.
- **Documentation** — `docs/ha-architecture.md` with the
  Tier 1 hot-standby design.

The HA work is **design-only** in v0.26.0 — no actual HA
deploy. The goal is to put the code in a state where
adding PostgreSQL + a standby VM is a 1-day operation,
not a 1-week refactor.
