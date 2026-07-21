# Disaster Recovery — skygate + headscale

**Audience:** operator who needs to recover the system
after a hardware failure, an `rm -rf` mistake, or a
successful attacker who wiped both the skygate DB and
the headscale DB.

**Scope:** covers RTO ≤ 30 min, RPO ≤ 1 hour on the
single-VM deployment. Tier 1 (hot standby with PostgreSQL
streaming replication) is **NOT** covered here — that's a
separate architecture (see `docs/ha-architecture.md` in
v0.26.0). This runbook is the "low-budget" recovery that
4 users can rely on.

---

## TL;DR (15 min recovery)

```bash
# 1. Provision a fresh VM with the same OS, same Docker
#    version, same `/home/skyadmin/skygate` and
#    `/home/skyadmin/headscale` paths.

# 2. Restore headscale from the most recent backup
#    (~5 min).
sudo systemctl stop headscale 2>/dev/null || true
sudo cp /tmp/headscale-backup-*/db.sqlite* /var/lib/headscale/
sudo cp /tmp/headscale-backup-*/acl.hujson* /etc/headscale/
sudo systemctl start headscale

# 3. Restore skygate from the most recent backup
#    (~2 min).
docker compose stop skygate
docker cp /tmp/skygate-backup-*/skygate.db /var/lib/docker/volumes/skygate-data/_data/
docker compose up -d --force-recreate --no-deps skygate

# 4. Repoint DNS to the new VM's public IP
#    (TTL was set to 5 min for head.skynas.ru and
#    gate.skynas.ru, so propagation is <5 min).

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
| `/var/lib/docker/volumes/skygate-data/_data/skygate.db` | skygate: portal_users, audit_log, user_subnets, mesh_members, etc. | ~30 MB | + wal, shm |
| `/home/skyadmin/skygate/.env` | SKYGATE_* env vars (DB path, headscale URL, API key, Telegram token, Caddy DNS) | ~3 KB | **secrets** — backup SHOULD be encrypted; cron uses gpg if SKYGATE_BACKUP_GPG_RECIPIENT is set |
| `/home/skyadmin/skygate/deploy/templates/headscale-config.yaml` | The skygate-side config | ~3 KB | |
| `/home/skyadmin/headscale/headscale-config/` | the skygate-managed headscale config files (versioned in skygate repo) | ~10 KB | |

### NOT backed up

| Path | Why | Impact |
|---|---|---|
| Tailscale client keys | They live on each client device, not on the server. | Clients re-login to the new server with their preauths. |
| DERP map (live peer connections) | Ephemeral, rebuilt on demand. | ~30 sec disruption to direct-conn traffic; relay traffic re-resolves. |
| Live `tailscale up` sessions | Re-establish on next poll. | Tailscale clients retry automatically every 60-90 sec. |
| In-flight audit log writes (last 1-2 sec) | Buffered in WAL. | Lost on the wire; the next `audit_log` row will be there. |

---

## RPO (Recovery Point Objective)

- **For skygate SQLite**: 1 hour (cron runs every hour via
  `deploy/backup.sh`, the `SKYGATE_BACKUP_FREQ` env var
  controls this; default 1h).
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
   by setting TTL to 60 sec on head.skynas.ru and
   gate.skynas.ru.
2. **Docker image pull + Go build** (5 min). Mitigate by
   pre-pulling on a hot spare VM.
3. **Human coordination** (you need to type things, make
   decisions, read logs). Practice quarterly.

---

## Step-by-step recovery

### 0. Decide what you're recovering from

- **Single VM dead, disks recoverable**: just
  re-mount the disks in a new VM. The /home/skyadmin
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
# expected: ~4 (skyadmin, michail, guest, daniil)
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
- Same `/home/skyadmin/skygate` and `/home/skyadmin/headscale`
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
docker compose -f /home/skyadmin/headscale/docker-compose.yml \
    up -d --force-recreate headscale
# or for native: sudo systemctl start headscale
```

### 4. Restore skygate

```bash
# 4.1 Stop skygate.
cd /home/skyadmin/skygate
docker compose stop skygate

# 4.2 Restore the SQLite DB into the skygate-data
# docker volume.
VOL=/var/lib/docker/volumes/skygate-data/_data
cp /var/backups/skygate/latest/skygate.db "$VOL/"

# 4.3 Restore .env (with secrets) — use a different
# backup target if SKYGATE_BACKUP_GPG_RECIPIENT is set.
cp /var/backups/skygate/latest/.env /home/skyadmin/skygate/

# 4.4 Restart skygate.
docker compose up -d --force-recreate --no-deps skygate

# 4.5 Wait for the build (5-7 min the first time after
# a re-provision; faster on warm caches).
for i in $(seq 1 60); do
    sleep 5
    if curl -fsS -m 2 http://localhost:8080/version >/dev/null 2>&1; then
        echo "skygate healthy after ${i}*5s"
        break
    fi
done
```

### 5. Repoint DNS

If the VM's public IP changed, update the A records
for `head.skynas.ru` and `gate.skynas.ru`. TTL should
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
cd /home/skyadmin/skygate
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

- **skygate 502**: skygate.db restore was incomplete
  (forgot `-wal` / `-shm`). Re-run with all 3 files.
- **headscale 500**: noise_private.key mismatch. The
  preauth keys clients were issued are bound to the
  OLD server's identity. Either:
  - Restore the EXACT same noise_private.key (this is
    why we back it up — it's tiny), OR
  - Issue new preauths to every client (`/my/preauth`).
- **Login fails for known good password**: .env restore
  was wrong. `SKYGATE_ADMIN_PASS` must match.

### 7. Document the recovery

After recovery is green, write a one-line audit entry:

```sql
INSERT INTO audit_log (user_id, username, action, detail)
VALUES (1, 'skyadmin', 'disaster_recovery',
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

# 1. Spin up a throwaway VM with the same /home/skyadmin
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
- `docs/ha-architecture.md` (v0.26.0) — the Tier 1 hot
  standby design.
- `Makefile` — the `test` target runs `smoke +
  check-nodes + check-https` which is the smoke test
  referenced in step 6.
- `AGENTS.md` — "Working environment" section, the
  source-of-truth for the VM layout.
