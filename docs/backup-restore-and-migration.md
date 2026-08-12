# Skygate backup / restore / cross-host migration

> **Status (v1.3.8, 2026-08-12):** backup end-to-end verified on the
> live VM (192.168.13.69) with both `local` and `s3` protocols.
> Restore (replay pg_dump into a fresh DB) verified end-to-end with
> a 15 MiB S3 backup. Cross-host migration path is documented below
> and was executed successfully when the operator migrated from
> `45.152.198.217` (v0.32.25 era) → `192.168.13.69` (v1.0.0 era);
> the same flow applies to a future move to any other host.

This document is the operator's runbook for the three related flows.
Each section has:

  - what moves (file-by-file)
  - what needs to change on the new host
  - how to verify the migration succeeded
  - the failure modes the operator should check for

---

## 1. Backup

### What's in a `skygate-full-<ts>.tar.gz`

| File                          | Source                                  | Size (typical) |
|-------------------------------|-----------------------------------------|----------------|
| `skygate-repo.bundle`         | `git bundle create --all`               | ~13 MB         |
| `skygate.env`                 | `skygate/.env` (DB DSN, API keys, etc.) | ~3.5 KB        |
| `skygate-pg.sql`              | `pg_dump -Fp --clean --if-exists`        | ~45 MB         |
| `skygate-git-log.txt`         | last 5 commits                          | ~400 B         |
| `skygate-schema.txt`          | `pg_tables WHERE schemaname='public'`   | ~200 B         |
| `headscale-config/`           | `/home/admin/headscale/config/*`        | ~50 KB         |
| `headscale.db`                | headscale SQLite (when present)         | ~200 KB        |
| `headplane-data/`             | `headscale_headplane_data` volume       | ~10 MB         |
| `derper.conf`, `derpmap.json` | DERP relay configs                      | ~1 KB          |
| `docker-compose.yml`          | skygate compose                         | ~5 KB          |
| `Dockerfile`                  | skygate Dockerfile                       | ~4 KB          |
| `inventory.txt`               | hand-written manifest                   | ~650 B         |

The 5 most critical files for a restore are:
1. `skygate-pg.sql` — the entire skygate DB (users, devices, ACL, audit)
2. `skygate.env` — connection strings + secrets (DB DSN, headscale API key, JWT secret)
3. `headscale-config/` — headscale YAML, ACL, DNS config
4. `headplane-data/` — headplane state (web UI settings)
5. `skygate-repo.bundle` — the source code itself (so the new host can
   rebuild the same commit)

### Protocols (v1.3.0+)

| Protocol    | Transport                       | Verified |
|-------------|----------------------------------|----------|
| `local`     | `tar.gz` in `BACKUP_DIR`         | ✅ live   |
| `smb`       | mount.cifs + `tar.gz`            | code path |
| `nfs`       | mount.nfs + `tar.gz`             | code path |
| `sftp`      | sshfs + `tar.gz`                 | code path |
| `s3`        | PUT via minio-go (no FUSE)       | ✅ live (minio throwaway) |

The S3 path is the only one that uses an out-of-band transport
(PUT object). All others write the tarball to the mount point
directly. See `docs/TODO.md` for the per-protocol test status.

### How to trigger

Three ways, in increasing order of automation:

  1. **Manual / one-off:**
     `/admin/backup/config` → fill in fields → `Run now` button.
  2. **In-app scheduler:**
     `/admin/backup/config` → set `in_app_enabled=1` +
     `schedule=0 3 * * *` (cron). The goroutine in
     `internal/feature/backup/scheduler.go` checks every 60s.
  3. **System cron:**
     `bash scripts/backup_cron.sh install` → daily at 03:00.
     Survives the skygate container being down (the in-app
     scheduler dies with the process; system cron doesn't).

---

## 2. Restore

### The 2-step operator flow

#### Step A: download the archive

For `local` / `smb` / `nfs` / `sftp`, the tarball is on the
operator's filesystem or share — just `scp` or `rsync` it to the
new host.

For `s3`, the operator uses any S3 client:

```bash
# AWS CLI
aws s3 cp s3://my-skygate-backups/v1.3.8-test/skygate-full-...tar.gz .

# mc (MinIO client)
mc cp local/skygate-backups/v1.3.8-test/skygate-full-...tar.gz .

# Or via the in-app /admin/backup "download" button (the
# download endpoint serves from the local BACKUP_DIR; for S3
# the operator must download via the S3 client — there is no
# /admin/backup/download-from-s3 yet, see docs/TODO.md).
```

#### Step B: replay the archive

The current `scripts/restore.sh` is the operator's tool. It is
**interactive** (asks for "1-8 which to restore") and the
in-app `/admin/backup/restore` endpoint feeds it `"8\n"` for
"do everything". For PG-only deployments (v1.3.0+), the
sqlite-specific `do_skygate_db()` function in `restore.sh` is
NOT used — the actual PG replay is done via:

```bash
# 1. extract the archive
mkdir -p /tmp/skygate-restore
cd /tmp/skygate-restore
tar xzf /path/to/skygate-full-<ts>.tar.gz
cd skygate-full-<ts>/

# 2. apply the PG dump to a fresh database
#    (uses the same postgres:18-alpine throwaway pattern as
#    backup.sh — so the operator doesn't need psql installed
#    on the host)
docker run --rm --network host -e PGPASSWORD=$(cat /tmp/pgpass) \
  -v "$(pwd):/restore:ro" \
  postgres:18-alpine \
  psql -h <new-pg-host> -p <new-pg-port> -U <new-pg-user> -d <new-db> \
       -v ON_ERROR_STOP=1 -f /restore/skygate-pg.sql
```

End-to-end verification (proven 2026-08-12 with the v1.3.8 S3
test backup):

  - The new DB has 28 public tables (same as live)
  - `portal_users`, `acl_snapshots`, `global_settings`,
    `exit_servers` row counts match live exactly
  - `device_rules` and `audit_log` may differ by a few rows
    (data drift between backup time and now — expected)
  - The `skyadmin` user is present with `is_admin=1`

### Why `scripts/restore.sh` doesn't do the PG replay yet

The script was written for the v0.32.x SQLite era and its
`do_skygate_db()` copies `skygate.db` from the archive. The
v1.3.0+ archive has `skygate-pg.sql` instead. The script needs
to be updated (see `docs/TODO.md` BL-15 — "restore.sh for PG
dump"). The PG replay works fine when the operator runs the
`psql -f` command above manually, so the data is safe; the
script is just a convenience wrapper that's currently out of
sync with the new format.

---

## 3. Cross-host migration

### What's different from a same-host restore

A cross-host migration has 5 things to change, in order:

  1. **HEADSCALE_URL** — old host: `http://headscale:50444`,
     new host: typically the same (since headscale is in its
     own container on the same docker network). If the new
     host uses a different headscale endpoint (e.g. external
     HA), update `HEADSCALE_URL` in `skygate/.env` + restart.
  2. **Public domain / TLS cert** — `/admin/settings` →
     `PublicDomain` field. If the domain changed, the
     openresty container needs a fresh Let's Encrypt cert
     (`certbot renew` or re-issue).
  3. **DB DSN** — if PG moved (e.g. external RDS instead of
     local docker-compose), update `SKYGATE_DB_DSN` in
     `skygate/.env` + restart.
  4. **HEADPLANE_URL** — if headplane moved, update
     `HEADPLANE_URL` in `skygate/.env` + restart. Default
     port is 50445 for the distroless image (v1.3.5 fix).
  5. **HEADSCALE_API_KEY** — if headscale was reinstalled,
     the API key rotates. Update via `/admin/headscale` page
     or `skygate/.env`.

### The autonomous flow

For the operator's standard "move to a new VM" scenario, the
flow is:

```bash
# === on the OLD host ===
# 1. Take a fresh backup (skip if you already have one
#    recent enough — the S3/keep_count retention policy
#    determines "recent enough").
bash /home/admin/skygate/scripts/backup.sh /home/admin/skygate-backups
# OR for S3 destinations: click "Run now" on /admin/backup.

# 2. Copy the archive off-host.
scp /home/admin/skygate-backups/skygate-full-*.tar.gz skyadmin@<new-host>:

# === on the NEW host ===
# 3. Install Docker + clone skygate (the repo.bundle in the
#    archive also works if the new host has no internet).
sudo apt install -y docker.io docker-compose-plugin
sudo usermod -aG docker skyadmin
# (clone or extract skygate-repo.bundle)
git clone https://github.com/BarsSky/skygate.git
cd skygate

# 4. Provision a fresh PG (docker compose local-pg profile
#    OR external PG). The dump is replayed in step 6.
docker compose --profile local-pg up -d postgres
# OR: provision external PG and note the DSN.

# 5. Drop the .env (the one in the archive is from the old
#    host and has the old DB DSN). Make a fresh one and fill
#    in the NEW SKYGATE_DB_DSN.
cp .env.example .env
# edit .env → SKYGATE_DB_DSN=postgres://...

# 6. Replay the PG dump into the new DB.
docker run --rm --network host -e PGPASSWORD=<pgpass> \
  -v "$(pwd):/restore:ro" \
  postgres:18-alpine \
  psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging \
       -v ON_ERROR_STOP=1 -f /restore/skygate-pg.sql

# 7. Bring up skygate.
docker compose up -d skygate

# 8. Bring up headscale + headplane (the archive has the
#    headscale config + headplane data; restore those too).
sudo cp -r /path/to/extract/headscale-config/* /home/admin/headscale/config/
docker run --rm -v headscale_headplane_data:/data \
  -v "/path/to/extract:/restore:ro" alpine \
  sh -c "rm -rf /data/* && cp -r /restore/headplane-data/* /data/"

# 9. Verify.
curl -s http://localhost:8080/healthz
# expect: {"build":"<expected>","status":"ok"}
curl -s http://localhost:8080/readyz | jq .healthy
# expect: true
curl -s http://localhost:8080/admin/system_tests | grep -c "test-name"
# expect: ≥15 (15 base + 2 exit_nodes added in v1.1.1)
```

### Autonomous verification

The post-migration verification is **partially** automated:

  - `scripts/verify_post_deploy.sh` covers R1-R27 (runtime
    catalog). These run after every deploy and catch 90% of
    migration issues (network reachability, ACL shape, DB
    table count, Tailscale state, etc.).
  - The 4 B88 system_tests (`backup.recent`, `acl_admin_present`,
    `rules_sanity`, `preferred_mismatch`) cover the most
    common post-migration bugs (admin user present, ACL applied,
    no orphan exit rules, backup cron reachable).
  - There is **no** automated "this is a different host" test.
    The operator must run the `verify_post_deploy.sh` suite
    manually after a migration; see `docs/TODO.md` BL-17
    ("migration autonomous verify").

### What fails if you skip a step

| Skipped step                  | What breaks                                        |
|-------------------------------|----------------------------------------------------|
| Don't update HEADSCALE_URL    | skygate can't reach headscale → /readyz fail       |
| Don't update PublicDomain     | HTTPS cert doesn't match → 525 / browser warning   |
| Don't update SKYGATE_DB_DSN   | skygate can't reach PG → startup migration fail    |
| Don't update HEADPLANE_API_KEY| /admin/headplane errors with 401/403                |
| Don't replay PG dump          | New DB is empty → /login page shows no users       |
| Don't restore headplane-data  | /admin/headplane shows "no data" / empty state     |
| Don't restore headscale-config| Headscale has no users / ACL → all devices offline |

The runtime catalog (R1-R27) catches most of these on the
verify_post_deploy run.

---

## 4. Failure modes (operator runbook)

### "Permission denied" on backup destination

This is the v1.3.8 root cause. The skygate container runs as
root, writes to a host bind-mount, files end up root-owned,
operator (skyadmin) can't manage them.

**Fix (one-time, on the host):**
```bash
sudo chown -R skyadmin:skyadmin /home/skyadmin/skygate-backups
```

**Fix (permanent, in the backup):** v1.3.8's `scripts/backup.sh`
auto-chowns the destination at the start AND end of every run
when invoked as root. See `scripts/backup.sh` lines 217-247.

### "bash: not found" from runner.go

Pre-v1.3.6: `exec.Command("bash", scriptPath, dest)` failed
because Alpine ships only `ash`. Fixed in v1.3.6 by adding
`bash` to the Dockerfile's `apk add`. The B99 catalog check
pins this.

### "skygate-backups" bucket does not exist (S3)

The runner pre-creates the local staging dir but not the
remote S3 bucket. The operator must `mc mb local/my-bucket`
(or `aws s3 mb s3://my-bucket`) once. The error from the
runner is clear: "s3 bucket does not exist: foo".

### "minio: no such host" inside skygate container

When the skygate container's DNS can't resolve a container
added with `docker run --network <compose-net>`, the
embedded DNS at 127.0.0.11:53 returns SERVFAIL. Use the
container's IP (e.g. `172.18.0.5:9000`) as the S3 endpoint
instead of the hostname. In production this isn't an issue
(public S3 endpoints resolve via real DNS).

### Restore script doesn't replay PG dump

`scripts/restore.sh` was written for SQLite. For PG, run
`psql -f skygate-pg.sql` manually. See Section 2 above.

### "slice bounds out of range" panic in prune()

Pre-v1.3.8: when the dest dir had fewer archives than
`keep_count`, `archives[keep:]` panicked. Fixed in v1.3.8
by the `if keep >= len(archives) { return nil }` guard.
The 5 TestPrune_* tests in `internal/backup/prune_test.go`
pin the contract.

---

## 5. Files moved in a v1.3.x → v1.3.x (same host) update

| File                | What changes                  | Where                |
|---------------------|-------------------------------|----------------------|
| `skygate/.env`      | unchanged (or new keys)       | kept                 |
| `skygate-pg.sql`    | backup captures it            | from latest backup   |
| `skygate-repo.bundle` | backup captures it          | from latest backup   |
| `headscale-config/` | rarely changes                | from latest backup   |
| `headplane-data/`   | rarely changes                | from latest backup   |
| source code         | `git pull` (no bundle needed) | not in backup path   |

A same-host update does NOT require a restore — just `git pull`
+ `docker compose build skygate` + restart. The backup is for
**disaster recovery**, not routine updates.

---

## 6. End-to-end test results (2026-08-12)

| Scenario                            | Result |
|-------------------------------------|--------|
| Local backup, full cycle, /admin UI | ✅ PASS — 15 MiB tar.gz, status=ok |
| Local backup, manual `backup.sh`    | ✅ PASS — same tar.gz, skyadmin-owned |
| S3 backup (minio throwaway)         | ✅ PASS — PUT to bucket, status=ok in 1s, ETag returned |
| S3 → fresh PG replay                | ✅ PASS — 28 tables, 4/6 critical tables byte-equal, drift ≤26 rows on 2 tables |
| Permission-denied fix (one-shot)    | ✅ PASS — `sudo chown -R skyadmin:skyadmin` clears the lock |
| backup.sh chowns on every run       | ✅ PASS — dest dir + tarball always skyadmin-owned |

See `docs/TODO.md` for the unimplemented pieces (autonomous
migration verify, in-app S3 GET path for /admin/backup
download, per-protocol end-to-end test for SMB/NFS/SFTP).
