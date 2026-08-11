# wal-g setup for skygate-pg

Continuous WAL archiving + base backup to MinIO for the skygate-pg
Patroni/etcd HA cluster.

## Layout

* **MinIO (Synology at `192.168.13.13`)** — S3-compatible storage
  * bucket: `skygate-pg-wal` (already created)
  * existing data: 49 `wal_005/0000000100000000000000D*.lz4` files
    (66-69 KB each, from a previous wal-g setup that ran briefly on
    2026-07-23)
  * exposed publicly as `https://minio.skynas.ru` (resolves to
    `95.165.170.190`, reverse-proxied to home MinIO) — for VPS
    nodes that can't reach the home LAN
* **svyatoslava (45.152.198.217)** — Patroni 4.1.0 **PRIMARY**
  * `archive_command: '. /etc/wal-g/env && wal-g wal-push %p'`
    (single file, NOT envdir — daemontools isn't always installed)
  * writes to `https://minio.skynas.ru` (public, no LAN route)
  * base backups: `wal-g backup-push /var/lib/postgresql/data`
* **skygate-vm (192.168.13.69)** — Patroni 4.1.0 **REPLICA**
  * wal-g installed + env written (2026-08-04)
  * reads from `http://192.168.13.13:9000` (LAN, faster)
  * backup-list / wal-show / backup-fetch work
  * backup-push works but slow (remote mode — needs data_dir)

## Install

Run on **each** PG node (primary and replica):

```bash
# On svyatoslava (VPS, public network):
SKYGATE_MINIO_ACCESS_KEY=skyadmin \
SKYGATE_MINIO_SECRET_KEY='Vfrttdf97' \
SKYGATE_MINIO_BUCKET=skygate-pg-wal \
SKYGATE_MINIO_ENDPOINT=https://minio.skynas.ru \
sudo bash /home/skyadmin/skygate/deploy/pg-ha/wal-g/install_wal_g.sh

# On skygate-vm (home LAN):
SKYGATE_MINIO_ACCESS_KEY=skyadmin \
SKYGATE_MINIO_SECRET_KEY='Vfrttdf97' \
SKYGATE_MINIO_BUCKET=skygate-pg-wal \
SKYGATE_MINIO_ENDPOINT=http://192.168.13.13:9000 \
sudo bash /home/skyadmin/skygate/deploy/pg-ha/wal-g/install_wal_g.sh
```

The script:
1. `apt install lz4` (and daemontools on the primary, for `envdir` — optional)
2. Download wal-g v3.0.8 (`wal-g-pg-22.04-amd64.tar.gz`) from GitHub
3. Install at `/usr/local/bin/wal-g`
4. Write `/etc/wal-g/env` (root:postgres mode 640) with all WALG + AWS_* vars
5. Verify: `wal-g backup-list` + `wal-g wal-show`

## Primary-only setup (svyatoslava)

After the install script runs on svyatoslava, add the `archive_command`
to Patroni's `postgresql.parameters` (in `/etc/patroni/patroni.yml`):

```yaml
postgresql:
  parameters:
    unix_socket_directories: '/var/run/postgresql'
    archive_mode: on
    archive_command: '. /etc/wal-g/env && wal-g wal-push %p'
    archive_timeout: 60
```

Then reload + restart Patroni:

```bash
# Reload (sighup) — Patroni schedules a pending restart
curl -X POST http://127.0.0.1:8008/reload

# Apply (causes brief failover: skygate-vm becomes primary for ~5s,
# svyatoslava comes back as replica, gets re-promoted)
yes | sudo -u postgres patronictl -c /etc/patroni/patroni.yml restart skygate-pg svyatoslava --force
```

Verify:

```bash
sudo -u postgres psql -c "SHOW archive_mode; SHOW archive_command; SHOW archive_timeout;"
sudo -u postgres psql -c "SELECT * FROM pg_stat_archiver;"
```

After ~60s, new WAL segments start streaming to MinIO under
`wal_005/0000000200000000000000XX.lz4` (TLI 02 because Patroni
promoted this cluster fresh).

## Base backups (primary)

```bash
# Local mode (fast — uses data_dir for tar):
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-push /var/lib/postgresql/data'

# First backup: full (~12 MB for an empty skygate-pg)
# Subsequent: DELTA (only changed files since last backup, ~40 KB)

# List existing:
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-list'

# Show WAL segments in storage:
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g wal-show'
```

## Two v3.0.8 quirks (verified 2026-08-04)

### 1. `AWS_ENDPOINT` is the working name for the S3 URL

v3.0.0 release notes claim "Add WALG_ prefix to all the s3 related
envs" (PR #1512) but in v3.0.8 only `WALG_S3_PREFIX` actually
maps. The endpoint, access key, secret, region still use the `AWS_*`
family. Using `WALG_S3_ENDPOINT` or `AWS_S3_ENDPOINT` causes MinIO
to return `InvalidAccessKeyId: 403` because the AWS SDK signs the
request differently.

Verified working env:

```bash
export WALG_S3_PREFIX=s3://skygate-pg-wal
export AWS_ENDPOINT=https://minio.skynas.ru
export AWS_ACCESS_KEY_ID=skyadmin
export AWS_SECRET_ACCESS_KEY=Vfrttdf97
export AWS_REGION=us-east-1
export AWS_S3_FORCE_PATH_STYLE=true
# all other WALG_* knobs (COMPRESSION_METHOD, etc.)
```

### 2. `archive_command` must source, not envdir

`envdir` (from daemontools) expects a DIRECTORY of single-var
files, not a single file. The wal-g README examples use `envdir`,
but the simpler `'. /etc/wal-g/env && wal-g ...'` works in
`/bin/sh -c` (what PG's archive_command runs in) and doesn't
require daemontools.

```ini
# CORRECT (works on minimal Ubuntu without daemontools):
archive_command = '. /etc/wal-g/env && wal-g wal-push %p'

# WRONG (envdir expects a directory):
archive_command = 'envdir /etc/wal-g/env wal-g wal-push %p'
# → envdir: fatal: unable to switch to directory /etc/wal-g/env: not a directory
```

## Restore (DR)

When you need to restore:

```bash
# 1. Stop Patroni + PG on a target node
sudo systemctl stop patroni 2>/dev/null || sudo pkill -9 -f "patroni /etc/patroni"

# 2. Wipe data dir (or use a new one)
sudo rm -rf /var/lib/postgresql/data
sudo -u postgres mkdir /var/lib/postgresql/data
sudo -u postgres chmod 700 /var/lib/postgresql/data

# 3. Fetch the latest base backup
sudo -u postgres bash -c '. /etc/wal-g/env && \
  wal-g backup-fetch /var/lib/postgresql/data LATEST'

# 4. Configure recovery
cat > /var/lib/postgresql/data/recovery.signal <<EOF
restore_command = '. /etc/wal-g/env && wal-g wal-fetch %f %p'
recovery_target_timeline = 'latest'
EOF
sudo -u postgres chown postgres:postgres /var/lib/postgresql/data/recovery.signal

# 5. Start PG (will replay WAL up to latest)
sudo systemctl start postgresql 2>/dev/null || \
  sudo -u postgres /opt/patroni/venv/bin/patroni /etc/patroni.yml
# OR (if Patroni is supposed to manage): patronictl -c /etc/patroni/patroni.yml \
#   restart skygate-pg
```

Note: do NOT just run `wal-g backup-fetch` while Patroni is running.
It will conflict with Patroni's data dir management.

## Why both skygate-vm AND svyatoslava have wal-g

* **skygate-vm** (replica) — operator-friendly check from the LAN:
  `wal-g backup-list` shows what's in MinIO without going through
  the public DNS
* **svyatoslava** (primary) — does the actual work: archive_command
  writes WAL on every commit, `wal-g backup-push` takes base backups

Both write to the same `skygate-pg-wal` bucket, so the data is
unified. R28 in `verify_post_deploy.sh` checks skygate-vm (the
LAN-attached node the operator SSHes into for the catalog run).

## Verification commands (after install)

```bash
# 1. wal-g sees the bucket
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-list'

# 2. wal-g sees existing WAL segments (on svyatoslava should grow
#    over time as archive_command pushes new ones)
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g wal-show'

# 3. base backup from the primary (with data_dir for local mode)
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-push /var/lib/postgresql/data'

# 4. continuous WAL streaming (only on primary after archive_command is set)
sudo -u postgres psql -c "SELECT * FROM pg_stat_archiver;"
# expected: archived_count > 0 and growing
```

## Why this exists

The skygate v0.32.29 deployment cut over to PG/Patroni HA but had no
backup — only etcd consensus (which is config, not data). wal-g
provides:
* **Continuous WAL archiving** — point-in-time recovery to any second
  in the last N days (depending on retention)
* **Base backups** — full cluster snapshot, taken daily via cron
* **Offsite copies** — MinIO is on the same Synology as the VM but
  can be replicated offsite via Synology's Hyper Backup

Retention: default wal-g keeps 5 base backups + all WAL. For 1 week
of PITR we need ~5 base backups × 500 MB = 2.5 GB + ~7 days × 50 MB
WAL/day = 350 MB. Well within the Synology's 4 TB capacity.

## Phase 3 completion (v0.32.30+)

* ✅ Step 1: Patroni/etcd (v0.32.21, was already done)
* ✅ Step 2: HAProxy on skygate-vm (v0.32.30)
* ✅ Step 3: wal-g install on both nodes (v0.32.30 + this update)
* ✅ Step 4: archive_command + base backup (this update)
* ✅ Step 5: failover test — limited (skygate-vm killed, svyatoslava
  stayed primary, archive continued, skygate-vm rejoined as replica).
  Full test (svyatoslava down → skygate-vm promotes) needs operator
  action since I have no SSH from svyatoslava to skygate-vm.
