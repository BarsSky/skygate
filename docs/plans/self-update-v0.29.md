# Self-update mechanism — design plan (v0.29.0 candidate)

**Author:** Mavis (2026-07-25)
**Status:** Plan, not yet implemented
**Target version:** v0.29.0 (1-2 days work)
**Scope:** In-app update + DB migration + manual fallback

---

## Goal

A running skygate instance can update itself to a new GitHub release with one click
(or automatically on a schedule), applying any pending DB migrations before swapping
the binary. If the automated path fails (network, file permissions, DB migration error),
the operator has explicit, copy-pasteable instructions for both Docker and bare-binary
installs. Nothing is lost; the worst case is "the same version as before, with a row
in `audit_log` explaining why the update was rolled back."

This is the **release train** the operator has been asking for. Today every release
requires a manual SSH + `git pull` + `docker compose up -d` + `make verify-post`
sequence. After v0.29.0 the operator (or a scheduled job) can do it in two clicks.

## Non-goals

- **Auto-rollback on health check failure** (that's v0.30.0; needs a watchdog process)
- **Cross-platform package distribution** (deb/rpm/brew) — binary only
- **Multi-instance HA** — single skygate, single VM, with the v0.27.0 HA plan deferred
- **PG online schema migration tools** (pg_repack, pgroll, etc.) — we use simple
  `ALTER TABLE` with `IF NOT EXISTS` guards; the schema is small enough that this
  is fine for v0.29.0

---

## Architecture overview

Three layers:

1. **Detection** — `internal/update/checker.go` polls GitHub Releases API for a newer
   version than `App.BuildVersion`. No DB access, no filesystem writes, runs every
   24h (configurable, default on for `/admin/*` only). Result: a "new version
   available" banner on `/admin/dashboard` + a `/admin/update` page with details.

2. **Acquisition** — when the operator clicks "Update", the updater
   (`internal/update/updater.go`) downloads the new binary (or pulls a new Docker
   image if the host is Docker). It runs DB migrations BEFORE swapping the
   process. Migrations are forward-only and idempotent (guaranteed by the v0.28.5
   catalog's B5 / R20 checks).

3. **Restart** — the updater asks the skygate process to gracefully shut down
   (`SIGTERM`, 30s grace period), then the supervisor (docker compose / systemd
   / bare-binary supervisor script) restarts the new binary. The new binary boots,
   runs the same migrations on its own (idempotent — same DB state, same code),
   accepts traffic.

The first skygate startup after the swap is what completes the upgrade. If it
fails (bad binary, broken migration), the supervisor falls back to the
**previous binary** (kept on disk under a versioned name) and the operator gets
an error in the UI.

---

## File layout

New top-level package `internal/update/`:

```
internal/update/
  checker.go          — GitHub Releases API client (uses go-github or stdlib)
  checker_test.go     — mocks the HTTP server, asserts caching + rate limiting
  updater.go          — orchestrates download + migrate + restart
  updater_test.go     — end-to-end on a stub binary
  pg_migrate.go       — runs the v0.27+ migration chain against PG
  pg_migrate_test.go  — uses testcontainers-go (or a real PG in CI)
  state.go            — `update_state` table (history + audit)
  templates.go        — embedded HTML for /admin/update
  templates/
    update.html       — single-page update UI (status + button + log)
```

Plus UI hooks in `internal/handlers/`:
- `handlers_admin_update.go` — `/admin/update` (GET status, POST trigger, POST rollback)
- `templates/admin/update.html` — embedded above

Plus a CLI:
- `cmd/skygate-update/main.go` — standalone updater for bare-binary installs
  (because the running skygate can't replace itself, only the supervisor can)

---

## 1. Detection (`checker.go`)

### Behavior

- Polls `https://api.github.com/repos/BarsSky/skygate/releases/latest`
- Compares `tag_name` (e.g. `v0.29.0`) to `App.BuildVersion` (e.g. `0.28.6+abc1234`)
- Semver comparison: `0.29.0 > 0.28.6` → "new version available"
- Caches the result for 6h (configurable: `SKYGATE_UPDATE_CHECK_INTERVAL`)
- Caches failures for 15m (don't hammer GitHub on every poll)
- Honors `SKYGATE_UPDATE_CHECK=false` to disable entirely (e.g. air-gapped deploys)
- Honors `SKYGATE_UPDATE_CHANNEL=stable|rc|all` — default `stable` (only non-prerelease tags)

### Why not use go-github

We only need two calls:
- `GET /repos/{owner}/{repo}/releases/latest` (most recent stable)
- `GET /repos/{owner}/{repo}/releases/tags/{tag}` (specific release, for download URL)

Both are easy with `net/http` and `encoding/json`. go-github adds 100+ transitive
deps for two endpoints. Skip it.

### What about rate limits

GitHub unauthenticated: 60 req/h. We poll once per 24h + 1 on demand click. 25
reqs/day of headroom. If the operator hits the limit (running a fleet?),
`SKYGATE_GITHUB_TOKEN` adds Basic auth and bumps to 5000 req/h.

### What's exposed in the UI

A small banner on `/admin/dashboard`:
```
📦 v0.29.0 available (you have v0.28.6). [Details →]  [Update now →]
```

"Details" goes to `/admin/update` (full release notes + binary size + checksums).
"Update now" jumps to a confirmation page (with the operator's pinned version
shown for double-check, since this is a one-way door).

---

## 2. Acquisition (`updater.go`)

### Two paths: Docker vs bare-binary

```go
type InstallKind int
const (
    InstallDocker InstallKind = iota
    InstallBare
    InstallSystemd
)

func DetectInstall() InstallKind {
    // Check for /.dockerenv or /run/.containerenv
    if _, err := os.Stat("/.dockerenv"); err == nil {
        return InstallDocker
    }
    // Check for systemd init
    if _, err := os.Stat("/run/systemd/system"); err == nil {
        return InstallSystemd
    }
    return InstallBare
}
```

The updater's behavior branches on `InstallKind`. The actual swap mechanism
differs:

| Install kind | What updater does | Restart mechanism |
|--------------|-------------------|-------------------|
| Docker       | `docker pull barssky/skygate:v0.29.0` | `docker compose up -d skygate` (supervisor does it) |
| Systemd      | `systemctl stop skygate; install new binary; systemctl start` | systemd |
| Bare         | `mv skygate skygate.v0.28.6; install skygate.v0.29.0; ./skygate &` | manual or supervisor script |

In all three paths, the **migrations run inside the new binary** before it starts
accepting traffic. The old binary runs migrations to bring the DB to the schema
version the new code expects; the new binary re-runs the same migrations (which
are idempotent and short-circuit on the "already applied" check) and then opens
the HTTP listener.

### Why run migrations twice

Two reasons:
1. **Migrations are forward-only and idempotent.** A migration that "adds a column
   if it doesn't exist" is safe to run twice — the second run is a no-op. The
   v0.28.5 catalog (B5 / R20) pins this invariant.
2. **Zero-downtime requirement.** During the swap, the OLD binary is still
   serving traffic. It must be able to talk to the DB with the new schema in
   place. If a migration is destructive (drops a column, renames a table), the
   OLD binary would crash on the next request. So: migrations must NEVER
   destroy data the OLD binary needs.

This is the "expand-contract" pattern (Hellerstein's "Online Migrations at
Scale" talk). v0.29.0 enforces it:
- `ALTER TABLE ... ADD COLUMN` is fine (expand)
- `ALTER TABLE ... DROP COLUMN` is forbidden in the auto-update path (must
  go through a manual flag like `SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION=1`)

### Where the new binary comes from

GitHub release assets:
```
https://github.com/BarsSky/skygate/releases/download/v0.29.0/skygate-linux-amd64
https://github.com/BarsSky/skygate/releases/download/v0.29.0/skygate-linux-amd64.sha256
```

The updater verifies SHA256 BEFORE installing (defense against MITM or partial
downloads). On Docker, the image digest is verified against
`repos/barssky/skygate/tags/...` SHA.

The release process (`.github/workflows/release.yml` — separate from `ci.yml`)
builds the binary on every tag push, attaches it to the GitHub release, and
writes the SHA256 file. That's the single source of truth for "what's in v0.29.0".

---

## 3. DB migration for PG (`pg_migrate.go`)

The user explicitly said "только с PG, SQLite не учитывать". The reasoning:
SQLite migrations have been battle-tested for 23 versions. PG is new (v0.27.0
Phase 1-2.5 are still in `feat/postgres-migration`, not on main). The update
mechanism needs to be extra-careful for PG because:

- **PG transactions are real.** SQLite can do partial commits, PG can't. A
  mid-migration crash leaves the DB in a transactionally-consistent state
  either way, but the recovery story differs.
- **PG locks.** An `ALTER TABLE ... ADD COLUMN` (without `IF NOT EXISTS`)
  takes an `ACCESS EXCLUSIVE` lock. If a request comes in during this
  window, the OLD binary's queries pile up. With a 30s+ migration, the
  whole instance can hang.
- **PG has its own `pg_repack` / `pgroll` / `pg_dump` tools.** We don't need
  them yet (the schema is small), but the updater needs to know about them
  for future large migrations.

### The migration protocol (PG)

For v0.29.0, the protocol is simple:

1. Updater sets `app.updating=true` in `global_settings` (a single row,
   `UPDATE ... SET value='1' WHERE key='app.updating'`). All skygate
   instances respect this and return 503 with `Retry-After: 30` (drains
   traffic). For single-instance deploys (today's reality), this is a no-op
   beyond a clear audit trail.

2. Updater runs the migration chain against PG via the v0.27.0 driver
   abstraction (`internal/db.DB` interface — already in main, with
   `migrations_v0.40.go` switching the impl). All migrations in
   `internal/db/migrations_v*.go` are forward-only and idempotent.
   Migrations MUST NOT drop columns (see above).

3. Updater verifies post-migration state: `SELECT 1` on the connection
   pool, `PRAGMA user_version` (SQLite) or `SELECT version FROM skygate_migrations` (PG).

4. Updater atomically swaps the binary (or image tag) and asks the
   supervisor to restart.

5. New binary boots, runs migrations AGAIN (idempotent, no-op), opens HTTP.

6. New binary's `/healthz` returns 200; updater clears `app.updating`.

If ANY step from 2 onward fails, the updater:
- Aborts
- Logs to `audit_log` (`action='update_failed'`)
- Rolls back: reverts the binary to the previous version (kept on disk)
- Re-runs migrations on the new (old) binary to make sure DB is in a state
  the OLD code can read
- Tells the operator: "Update failed at step N. Reason: <error>. See
  /admin/update for the full log. The previous version is still running."

### Why not use a separate migration tool (golang-migrate, goose, atlas)

golang-migrate is solid but adds a binary + a protocol we don't otherwise
need. The existing v0.20+ migration pattern (one `migrateV0NN(d *sql.DB)`
function per version, all in one file) is simple, well-understood, and
backed by the v0.28.5 catalog. We keep it.

atlas is more sophisticated (declarative schema diff, plan/apply) but
requires running a separate `atlas` binary and writing a `schema.hcl`.
Not worth the complexity for ~30 versions of incremental changes.

### Where PG-specific migration code lives

`internal/db/migrations_v0.40.go` is the "switch to PG" commit. v0.27.0
already wraps `*sql.DB` and per-version migrations work on both. The
PG-specific concerns are:

- **Locking**: `ALTER TABLE` on PG takes exclusive locks. The v0.29.0
  updater runs migrations in a transaction with `SET LOCAL
  lock_timeout = '10s'` to fail fast if the table is busy (instead of
  hanging the migration).
- **Indexes**: `CREATE INDEX CONCURRENTLY` is required for production
  PG (no exclusive lock). v0.29.0 adds a helper `createIndexConcurrent`
  that wraps the CONCURRENTLY pattern. Existing migrations with plain
  `CREATE INDEX` get rewritten to use it (v0.29.0 follow-up).
- **Backups**: before any v0.29.0+ migration, the updater runs
  `pg_dump --schema-only` to `/var/backups/skygate-pre-migrate-<ts>.sql`.
  On rollback, this file is the source of truth for "what was the schema
  before we touched it". SQLite doesn't need this (the file IS the backup).
  PGBackups / barman / wal-g are out of scope for v0.29.0.

---

## 4. Manual update instructions (always available)

The `/admin/update` page has a "Manual steps" section that shows the exact
copy-paste commands for the operator's installation. This is the fallback
when:

- The in-app update button errors out (network, permissions, etc.)
- The operator doesn't trust the auto-update path
- The new binary boots but doesn't behave correctly
- The DB migration is destructive and needs human review

### Docker manual update (skygate + headscale + caddy in compose)

```bash
# 1. Backup current state
ssh skyadmin@192.168.13.69
cd /home/skyadmin/skygate
docker exec skygate sqlite3 /data/skygate.db ".backup /data/skygate.backup-$(date +%Y%m%d).db"

# 2. Pull the new version
git fetch --tags
git checkout v0.29.0   # or: git pull origin main
# (verify the diff is what you expect: git log --oneline v0.28.6..v0.29.0)

# 3. Apply DB migrations (BEFORE the new container starts)
docker exec skygate /app/skygate --migrate-only
# Output: "applied v0.48, v0.49 (2 new migrations)"

# 4. Stop the old container, start the new one
docker compose up -d --force-recreate --no-deps skygate

# 5. Wait for /healthz to return 200 (with the new build label)
until curl -fsS http://localhost:8080/healthz | grep -q v0.29.0; do sleep 2; done

# 6. Run the guarantee catalog to confirm the upgrade is clean
make verify-post
# Expected: 26 PASS, 0 FAIL
```

### Bare-binary manual update (no Docker)

```bash
# 1. Backup
cp skygate skygate.v0.28.6.bak
cp skygate.db skygate.db.v0.28.6.bak

# 2. Download new version
curl -fsSL -o skygate.v0.29.0 https://github.com/BarsSky/skygate/releases/download/v0.29.0/skygate-linux-amd64
curl -fsSL -o skygate.v0.29.0.sha256 https://github.com/BarsSky/skygate/releases/download/v0.29.0/skygate-linux-amd64.sha256
sha256sum -c skygate.v0.29.0.sha256

# 3. Apply migrations against the running DB
./skygate.v0.29.0 --migrate-only

# 4. Atomic swap (renames preserve the running process's open file)
mv skygate skygate.v0.28.6
mv skygate.v0.29.0 skygate
chmod +x skygate

# 5. Restart (adjust to your supervisor: systemd, supervisord, runit, etc.)
sudo systemctl restart skygate
# or: kill -TERM $(pidof skygate); ./skygate &

# 6. Verify
until curl -fsS http://localhost:8080/healthz | grep -q v0.29.0; do sleep 2; done
```

### Rollback manual steps

```bash
# Docker
docker compose down skygate
git checkout v0.28.6
docker compose up -d skygate

# Bare
sudo systemctl stop skygate
mv skygate skygate.bad
mv skygate.v0.28.6 skygate
sudo systemctl start skygate
```

The `/admin/update` page generates these commands dynamically based on the
detected install kind. There's also a "Copy to clipboard" button.

---

## 5. UI (`templates/admin/update.html`)

Single page. Layout:

```
┌─────────────────────────────────────────────────────────────┐
│ skygate v0.29.0 update                                       │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  Current version: v0.28.6 (build ea334fe)                   │
│  Latest stable:  v0.29.0 (released 2026-08-01)              │
│  Your install:   Docker compose (skyadmin@192.168.13.69)    │
│  Last check:     2 min ago                                  │
│                                                              │
│  [Update now →]    [Check now]    [View release notes]      │
│                                                              │
│ ─────────────────────────────────────────────────────────── │
│  Release notes (v0.29.0)                                    │
│  ── Self-update mechanism                                  │
│  ── DB migration hardening (PG lock_timeout)                │
│  ── 12 new i18n keys × 2 langs                              │
│  ...                                                        │
│                                                              │
│ ─────────────────────────────────────────────────────────── │
│  Manual steps                                               │
│  (copy-pasteable commands for Docker / Bare / Systemd)      │
│                                                              │
│ ─────────────────────────────────────────────────────────── │
│  Update history (last 5)                                    │
│  2026-07-25 16:42  v0.28.6  success  4.2s                  │
│  2026-07-22 11:15  v0.28.5  success  3.1s                  │
│  ...                                                        │
└─────────────────────────────────────────────────────────────┘
```

The "Update now" button → confirmation modal → kicks off the updater. The
updater streams its log to a tail (Server-Sent Events) at
`/admin/update/stream` so the operator sees progress in real time.

---

## 6. Testing

### Unit tests

- `checker_test.go` — mocks GitHub API, asserts caching, rate-limit handling,
  semver comparison
- `updater_test.go` — end-to-end on a stub binary: download → verify SHA →
  migrate → swap → restart
- `pg_migrate_test.go` — uses testcontainers-go to spin up a real PG
  in CI; runs the full migration chain; asserts the schema matches expectations

### Integration tests

- `internal/update/testdata/update_e2e.sh` — runs in CI on a fresh VM:
  1. Deploy v0.28.6
  2. Verify /healthz = v0.28.6
  3. POST /admin/update with a known test release
  4. Wait for /healthz = v0.29.0
  5. Verify make verify-post = 26 PASS

### Manual verification (operator's checklist)

- [ ] `Update now` from `/admin/update` on Docker compose — succeeds
- [ ] Same, on bare binary — succeeds
- [ ] `Update now` when GitHub is unreachable — fails gracefully, shows
      error, leaves old version running
- [ ] `Update now` when DB migration fails (inject a fake "v0.50"
      that always errors) — fails gracefully, rolls back, audit log entry
- [ ] `Update now` on destructive migration (`SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION=0`
      by default) — refused with clear error message
- [ ] Manual update steps are correct on both Docker and bare installs
- [ ] `make verify-post` is 26 PASS after the upgrade

---

## 7. Phased rollout (the v0.29.0 release itself)

The feature is itself complex. We don't want to ship "self-update" and have
it silently corrupt the operator's install. So:

**v0.29.0-rc.1**: Detection only. The `/admin/update` page shows the new
version + a "View release notes" button + manual update steps. The
"Update now" button is DISABLED. We deploy this, verify it works (the page
shows the right version, the manual steps are correct), and we move on.

**v0.29.0-rc.2**: Adds the bare-binary updater (simpler, no Docker
complexity). We test on the dev VM. The Docker updater is still disabled.

**v0.29.0**: Full feature. The "Update now" button works for both
install kinds. Operator uses it on the prod VM. We have a manual fallback
if it doesn't work.

This staged rollout matches the v0.27.0 strategy: each phase ships alone,
gets verified, then unlocks the next.

---

## 8. Open questions for the operator

1. **Auto-update on a schedule?** The detection runs every 24h, but the
   "Update now" click is always manual. Should we have
   `SKYGATE_AUTO_UPDATE=true` that updates automatically on a green
   health check? My recommendation: NO, the operator should always click.
   The whole point of v0.29.0 is to make the click safe (pre-checked
   migrations, verified binary, manual fallback). Auto-updating removes
   the human veto.

2. **Update channel**: `stable` only by default. Should we also expose
   `rc` for early adopters? My recommendation: NO. The operator has
   one VM. If they want RCs, they `git checkout feat/...` on the VM
   directly; the in-app updater is for stable releases only.

3. **Backup retention**: the updater writes `skygate.db.vN.bak` for every
   upgrade. After 10 upgrades, that's 10 backups × 8MB = 80MB. Keep all,
   or rotate? My recommendation: keep last 5, prune older. Same for the
   bare-binary `skygate.vN.bak`.

4. **Destructive migrations**: this plan refuses them by default. If the
   operator really needs to drop a column (e.g. for GDPR), they'd need
   to set `SKYGATE_ALLOW_DESTRUCTIVE_MIGRATION=1` and accept the risk
   that the OLD binary will crash on the next request until it restarts.
   Acceptable? My recommendation: yes, this is the right default.

---

## 9. Effort estimate

- `checker.go` + tests: 1 day
- `updater.go` (bare-binary path) + tests: 1 day
- `updater.go` (Docker path) + tests: 0.5 day
- `pg_migrate.go` + tests: 1 day
- UI (`/admin/update` + templates) + tests: 1 day
- Manual steps generation + UI integration: 0.5 day
- Phased rollout (rc.1, rc.2, stable): 0.5 day overhead

**Total: ~5 days.** v0.29.0 is a real release. v0.30.0 can be the
Provisioning UI redesign the operator also mentioned.

---

## 10. Why not just `apt upgrade`

The skygate operator is on a single Ubuntu VM, manually managed. A real
package distribution (apt/yum/brew) is significant overhead (build farm,
signing, repo hosting, GPG keys) for one operator. The in-app updater
gets us 90% of the value at 10% of the cost. We can revisit apt later
if skygate ever grows to a multi-tenant SaaS where the operator isn't
the user.
