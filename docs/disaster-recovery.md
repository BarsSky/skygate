# Disaster Recovery — skygate + headscale

**Audience:** operator who needs to recover the system
after a hardware failure, an `rm -rf` mistake, or a
successful attacker who wiped both the skygate DB and
the headscale DB.

**Scope:** covers RTO ≤ 30 min, RPO ≤ 1 hour on the
single-VM deployment. Tier 1 (hot standby with PostgreSQL
streaming replication) is **NOT** covered here — that's a
separate architecture (see `docs/internal/internal/ha-architecture.md` in
v0.26.0). This runbook is the "low-budget" recovery that
4 users can rely on.

---

## TL;DR (15 min recovery)

```bash
# 1. Provision a fresh VM with the same OS, same Docker
#    version, same `/home/admin/skygate` and
#    `/home/admin/headscale` paths.

# 2. Restore headscale from the most recent backup
#    (~5 min).
sudo systemctl stop headscale 2>/dev/null || true
sudo cp /tmp/headscale-backup-*/db.sqlite* /var/lib/headscale/
sudo cp /tmp/headscale-backup-*/acl.hujson* /etc/headscale/
sudo systemctl start headscale

# 3. Restore the PG cluster (v1.3.0+; was a SQLite file copy before)
#    (~3 min — depends on cluster size + local-PG vs external).
#    (a) local docker-compose PG (default for fresh installs):
docker compose --profile local-pg up -d
until docker exec skygate-postgres-1 pg_isready -U skygate -d skygate; do
  sleep 2
done
#        Apply the dump (idempotent — --clean --if-exists drops existing):
docker run --rm -i --network headscale_default \
  -e PGPASSWORD="$PG_DB_PASSWORD" postgres:15-alpine \
  psql -h postgres -U skygate -d skygate -v ON_ERROR_STOP=1 < \
  /tmp/skygate-backup-*/skygate-pg.sql
#    (b) external PG (HA Patroni, RDS, etc.):
#        See `deploy/pg-ha/README.md` §"Restore" for the
#        Patroni + wal-g + S3 restore flow. TL;DR: the
#        skygate-pg.sql from the backup is the source of
#        truth; `psql -h <cluster> -U skygate -d skygate -f
#        skygate-pg.sql` rehydrates an empty cluster.
#
#    Pre-v1.3.0 archives contain `skygate.db` (SQLite file)
#    instead of `skygate-pg.sql`. Those archives need the
#    one-time SQLite → PG conversion; see
#    docs/deploy.md#11-postgresql-migration-from-sqlite.
#    This runbook's rest of step 3 (the "skygate 502" check
#    below) is also different in that case.

# 4. Restore skygate from the most recent backup
#    (~2 min). v1.3.0+ skygate binary opens SKYGATE_DB_DSN
#    from .env; ensure it's set before this step.
docker compose stop skygate
docker compose up -d --force-recreate --no-deps skygate

# 4. Repoint DNS to the new VM's public IP
#    (TTL was set to 5 min for head.example.com and
#    gate.example.com, so propagation is <5 min).

# 5. Verify with the smoke test
#    (5 min — see "Verification" below).
```

Total: ~15-20 min if the backups are recent and the
backup restoration is rehearsed.

---

## What's backed up, and what isn't

### Backed up by `deploy/backup.sh` (cron daily at 03:00)

| Path | Contents | Size | Notes |
|---|---|---|---|
| `/var/lib/headscale/db.sqlite` | headscale nodes, ACL, preauth keys, users | ~10 MB | + `db.sqlite-wal`, `db.sqlite-shm` |
| `/etc/headscale/config.yaml` | DERP, OIDC, base_domain | ~5 KB | |
| `/etc/headscale/acl.hujson` | The deployed policy | ~10 KB | |
| `/var/lib/headscale/noise_private.key` | Tailscale node-key (Tailscale identity for THIS headscale) | 100 B | **CRITICAL**: identical noise key = identical control server identity, all client preauths / pre-keys remain valid |
| **v1.3.0+**: `skygate-pg.sql` (text-format pg_dump) inside the backup archive | skygate: portal_users, device_rules, audit_log, user_subnets, mesh_members, acl_snapshots, etc. | ~50 KB-5 MB (depends on table count) | Replayable with `psql -f skygate-pg.sql`. Pre-v1.3.0 archives had `skygate.db` (SQLite file) instead. |
| **pre-v1.3.0**: `/var/lib/docker/volumes/skygate-data/_data/skygate.db` | skygate SQLite (legacy only) | ~30 MB | + wal, shm. v1.3.0+ ignores this path — see "PostgreSQL migration from SQLite" in deploy.md for the one-time conversion. |
| `/home/admin/skygate/.env` | SKYGATE_* env vars (DB DSN, headscale URL, API key, Telegram token, Caddy DNS) | ~3 KB | **secrets** — backup SHOULD be encrypted; cron uses gpg if SKYGATE_BACKUP_GPG_RECIPIENT is set |
| `/home/admin/skygate/deploy/templates/headscale-config.yaml` | The skygate-side config | ~3 KB | |
| `/home/admin/headscale/headscale-config/` | the skygate-managed headscale config files (versioned in skygate repo) | ~10 KB | |

### NOT backed up

| Path | Why | Impact |
|---|---|---|
| Tailscale client keys | They live on each client device, not on the server. | Clients re-login to the new server with their preauths. |
| DERP map (live peer connections) | Ephemeral, rebuilt on demand. | ~30 sec disruption to direct-conn traffic; relay traffic re-resolves. |
| Live `tailscale up` sessions | Re-establish on next poll. | Tailscale clients retry automatically every 60-90 sec. |
| In-flight audit log writes (last 1-2 sec) | Buffered in WAL. | Lost on the wire; the next `audit_log` row will be there. |

---

## RPO (Recovery Point Objective)

- **For skygate PG (v1.3.0+)**: 1 hour (cron runs every hour via
  `deploy/backup.sh`, the `SKYGATE_BACKUP_FREQ` env var controls this;
  default 1h). The backup is a `pg_dump --clean --if-exists` text-format
  dump — replayable on any PG 15+ cluster. For sub-hour RPO use
  Patroni streaming replication (see `deploy/pg-ha/README.md`).
- **For skygate SQLite (pre-v1.3.0, legacy)**: 1 hour.
- **For headscale SQLite**: 1 hour (same cron).
- **For the deploy scripts / config**: every commit is
  pushed to `github.com/BarsSky/skygate`, so even if the
  VM is gone, the deployment is reproducible from
  `git clone` + `deploy/deploy.sh --from-path /backup`.

To get RPO < 1 min you'd need streaming replication
(PostgreSQL). That's the Tier 1 HA work (v0.26.0+).

## RTO (Recovery Time Objective)

- **Best case** (backups verified, fresh VM already
  provisioned, DNS low TTL): **15 min**.
- **Worst case** (backup on slow disk, need to
  re-provision from scratch, DNS propagation slow):
  **60-90 min**.

Steps that take the most time in practice:

1. **DNS propagation** (5-15 min for low TTL). Mitigate
   by setting TTL to 60 sec on head.example.com and
   gate.example.com.
2. **Docker image pull + Go build** (5 min). Mitigate by
   pre-pulling on a hot spare VM.
3. **Human coordination** (you need to type things, make
   decisions, read logs). Practice quarterly.

---

## Step-by-step recovery

### 0. Decide what you're recovering from

- **Single VM dead, disks recoverable**: just
  re-mount the disks in a new VM. The /home/admin
  tree and the /var/lib/headscale tree will be intact.
  RTO: 5 min.
- **Single VM dead, disks lost**: full restore from
  backup. RTO: 30 min. Continue with step 1.
- **DBs corrupted but VM alive**: stop services, restore
  only the SQLite files from the most recent backup, no
  re-provisioning. RTO: 10 min.
- **Whole cluster compromised (attacker got root)**:
  also rotate `noise_private.key` (treats all client
  pre-keys as suspect — clients must re-auth). RTO:
  60 min.

### 1. Identify the most recent backup

```bash
ls -la /var/backups/skygate/  # if backup.sh is configured
# or
ls -la /tmp/skygate-backup-*
ls -la /tmp/headscale-backup-*
```

The latest directory is the recovery point. **Verify
the backup is not also corrupt** before relying on it:

```bash
sqlite3 /var/backups/skygate/latest/skygate.db \
    "SELECT COUNT(*) FROM portal_users;"
# expected: ~4 (admin, user1, user3, user2)
sqlite3 /var/backups/headscale/latest/db.sqlite \
    "SELECT COUNT(*) FROM nodes;"
# expected: ~11 (after v0.25.0; check current prod count)
```

If the count is 0 or the file is < 1 KB, the backup is
broken — go back one timestamp and try again.

### 2. Provision a fresh VM

The replacement VM should have:

- Same OS (Ubuntu 22.04 LTS — what `deploy/deploy.sh`
  is tested against).
- Same Docker version (24.x).
- Same `/home/admin/skygate` and `/home/admin/headscale`
  paths (the deploy scripts assume these).
- SSH key authorized for the operator.
- Public IP (or the same IP as the dead VM, if the cloud
  provider supports IP migration).

Easiest path: most cloud providers have "rebuild from
image" — snapshot the dead VM's image, restore to a
new VM, attach the data disk. This skips step 1-3
because the data is already there.

### 3. Restore headscale

```bash
# 3.1 Stop the running headscale (it may be half-dead
# from the old VM or running with a corrupt DB).
sudo systemctl stop headscale || true

# 3.2 Restore the database. The path differs by
# install method:
#   - Docker: /var/lib/docker/volumes/headscale-data/_data/
#   - Native systemd: /var/lib/headscale/
DEST="/var/lib/docker/volumes/headscale-data/_data"
mkdir -p "$DEST"
cp /var/backups/headscale/latest/db.sqlite* "$DEST/"

# 3.3 Restore config + identity.
DEST_CONF="/etc/headscale"
cp /var/backups/headscale/latest/config.yaml "$DEST_CONF/"
cp /var/backups/headscale/latest/acl.hujson "$DEST_CONF/"
cp /var/backups/headscale/latest/noise_private.key \
   "$(dirname "$DEST")/"

# 3.4 Start headscale.
docker compose -f /home/admin/headscale/docker-compose.yml \
    up -d --force-recreate headscale
# or for native: sudo systemctl start headscale
```

### 4. Restore skygate

```bash
# 4.1 Stop skygate (the PG cluster from step 3 stays up;
# only the skygate container restarts).
cd /home/admin/skygate
docker compose stop skygate

# 4.2 Restore .env (with secrets) — must include
# SKYGATE_DB_DSN and PG_DB_PASSWORD for v1.3.0+.
# Use a different backup target if SKYGATE_BACKUP_GPG_RECIPIENT
# is set.
cp /var/backups/skygate/latest/.env /home/admin/skygate/

# 4.3 Restart skygate. The runtime opens SKYGATE_DB_DSN
# at startup and runs MigratePostgres (idempotent — the
# migrations are no-op on a freshly-replayed cluster).
docker compose up -d --force-recreate --no-deps skygate

# 4.4 Wait for /healthz (5-7 min the first time after
# a re-provision — the entrypoint.sh runs `go build`; faster
# on warm caches).
for i in $(seq 1 60); do
    sleep 5
    if curl -fsS -m 2 http://localhost:8080/healthz >/dev/null 2>&1; then
        echo "skygate healthy after ${i}*5s"
        break
    fi
done

# 4.5 (optional) sanity-check the PG cluster from inside skygate:
docker exec skygate-postgres-1 psql -U skygate -d skygate -c \
  "SELECT count(*) FROM pg_tables WHERE schemaname='public'"
#   should be >=20 after v1.3.0 migrations
```

Pre-v1.3.0 step 4.2 was a `cp skygate.db /var/lib/docker/volumes/skygate-data/_data/`
of the SQLite file. v1.3.0+ removed the file from the container
(mattn/go-sqlite3 deleted in commit `b1baa4a`); the runtime is
PG-only. The "skygate 502" failure mode from the verification
section below no longer applies to a missing .db file — instead, if
`SKYGATE_DB_DSN` is wrong or PG is unreachable, the v1.3.0+ binary
logs "dial tcp ...: connection refused" on every query, /readyz
returns 503 with `db:fail`, and the container stays up (loops
reconnecting).

### 5. Repoint DNS

If the VM's public IP changed, update the A records
for `head.example.com` and `gate.example.com`. TTL should
already be 60 sec (set this in advance!). Cloudflare
or Route53 both honor the TTL — propagation is typically
< 5 min.

```bash
# Cloudflare:
curl -fsS -X PATCH "https://api.cloudflare.com/.../dns_records/$RECORD_ID" \
    -d '{"content": "'"$NEW_IP"'"}'
```

### 6. Verify

Run the bilingual smoke test (`make smoke`):

```bash
cd /home/admin/skygate
make smoke
```

Pass criteria (look for "SMOKE TEST PASSED" at the end of
the script's output, or HTTP 200 on each step):

- `/login` returns 200 and shows the form
- POST `/login` with admin creds returns 302 to `/dashboard`
- `/my/devices` returns 200 and shows the user's
  devices
- `/admin/subnets` shows the 4 prod subnets
- `/admin/devices` shows the 11 headscale nodes

If any of these fail, the recovery is incomplete —
**do not announce recovery as done** until smoke is
green. Common failure modes:

- **skygate 502 (v0.32.x / pre-v1.3.0)**: skygate.db restore
  was incomplete (forgot `-wal` / `-shm`). Re-run with all 3 files.
- **skygate /readyz returns 503 with `db:fail` (v1.3.0+)**:
  SKYGATE_DB_DSN in .env is wrong, OR the PG cluster is not
  reachable from the skygate container. Verify:
  - `docker exec skygate-postgres-1 pg_isready -U skygate -d skygate`
    (local-PG case) — should return "accepting connections"
  - `grep SKYGATE_DB_DSN /home/admin/skygate/.env` — URL form
    is correct, password matches
  - The skygate container is on the `headscale_default` network:
    `docker inspect skygate -f '{{.NetworkSettings.Networks}}'`
- **headscale 500**: noise_private.key mismatch. The
  preauth keys clients were issued are bound to the
  OLD server's identity. Either:
  - Restore the EXACT same noise_private.key (this is
    why we back it up — it's tiny), OR
  - Issue new preauths to every client (`/my/preauth`).
- **Login fails for known good password**: .env restore
  was wrong. `SKYGATE_ADMIN_PASS` must match.
- **PG dump replay errors mid-restore (v1.3.0+)**: the
  dump was taken from a cluster with a newer PG version than
  the target. Check the `dump.sql` header for `-- Dumped from
  database version X.Y`; the target must be ≥ that version.
  PostgreSQL major versions are forward-compatible only.

### 7. Document the recovery

After recovery is green, write a one-line audit entry:

```sql
INSERT INTO audit_log (user_id, username, action, detail)
VALUES (1, 'admin', 'disaster_recovery',
        'reason=<what failed> from_backup=<timestamp>');
```

This means the next audit review sees that recovery
happened, and can correlate any anomalies (e.g. "client
X re-authed at 14:32, that's our recovery time").

---

## What the v0.26.0 HA work changes

This runbook will get **shorter** once v0.26.0 lands:

- **RPO goes from 1h to ~0s** (PostgreSQL streaming
  replication).
- **RTO goes from 15-30 min to ~30 sec** (auto-failover
  via Pacemaker or DNS TTL).
- The "decide what you're recovering from" step goes
  away — there's nothing to decide, the standby is hot.

But the **single VM** setup (current) keeps this runbook
relevant as the "if all HA fails" path. Even with v0.26.0
HA, you still need a backup-driven cold restore for the
case of "we accidentally deleted the production DB and
replication copied the deletion in 200ms".

---

## Practice (quarterly DR drill)

```bash
# Pick a Saturday morning. Announce "DR drill in 30 min"
# in the operator's Telegram. Then:

# 1. Spin up a throwaway VM with the same /home/admin
#    paths.
# 2. Restore from the most recent backup.
# 3. Run `make smoke`.
# 4. Time the RTO.
# 5. Compare to the documented RTO. If > 30 min, fix
#    the slow step before the next drill.
# 6. Discard the throwaway VM.
```

If you skip drills, you discover problems during a real
recovery, at 2am, with users waiting. Don't skip.

---

## See also

- `deploy/backup.sh` — what actually runs nightly.
  Includes WAL checkpoint, noise_private.key backup,
  .env backup (gpg if configured), and an age-based
  "is backup stale?" check.
- `scripts/verify_backup.sh` — v0.33.1.42 B2: weekly
  `sqlite3 ... "PRAGMA integrity_check"` on the latest
  archive. Recommended cron: `0 4 * * 0` (Sunday 04:00,
  1h after the nightly 03:00 backup so the freshest
  archive is the verification target). On failure, the
  script marks `backup.last_verify_status=fail` in
  `global_settings` + writes to `exit_rule_logs` (which
  the in-app /admin/backup page reads). Wire your
  Telegram bot in the cron wrapper to send an alert
  on the fail path (the subcommand doesn't have in-
  process Notifier access).
- `docs/internal/ha-architecture.md` (v0.26.0) — the Tier 1
  hot standby design (comprehensive, not a stub). The
  DR doc used to say this was a stub — it isn't; the
  file is 175 lines covering Tier 0/0.5/1, failure
  modes, and the 18-day implementation plan.
- `Makefile` — the `test` target runs `smoke +
  check-nodes + check-https` which is the smoke test
  referenced in step 6.
- `AGENTS.md` — "Working environment" section, the
  source-of-truth for the VM layout.
