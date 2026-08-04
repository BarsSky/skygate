# wal-g setup for skygate-pg

Continuous WAL archiving + base backup to MinIO for the skygate-pg
Patroni/etcd HA cluster.

## Layout

* **MinIO (Synology at `192.168.13.13`)** — S3-compatible storage
  * bucket: `skygate-pg-wal` (already created)
  * existing data: 49 `wal_005/0000000100000000000000D*.lz4` files
    (66-69 KB each, from a previous wal-g setup that ran briefly on
    2026-07-23)
* **svyatoslava (45.152.198.217)** — Patroni 4.1.0 **PRIMARY**
  * needs `archive_command` set to `envdir /etc/wal-g/env wal-g wal-push %p`
  * needs `wal-g` binary at `/usr/local/bin/wal-g`
  * needs `/etc/wal-g/env` (mode 600)
* **skygate-vm (192.168.13.69)** — Patroni 4.1.0 **REPLICA**
  * wal-g installed + env written (2026-08-04, verified)
  * only `backup-list`, `wal-show`, `backup-fetch` work (replica can't
    do base backup or push WAL on its own)

## Install

Run on **each** PG node (primary and replica):

```bash
# On skygate-vm (this VM) — env vars are the operator's MinIO creds:
SKYGATE_MINIO_ACCESS_KEY=skyadmin \
SKYGATE_MINIO_SECRET_KEY='Vfrttdf97' \
SKYGATE_MINIO_BUCKET=skygate-pg-wal \
SKYGATE_MINIO_ENDPOINT=http://192.168.13.13:9000 \
sudo bash /home/skyadmin/skygate/deploy/pg-ha/wal-g/install_wal_g.sh

# On svyatoslava — same command (operator runs it, no SSH from
# the Windows workspace)
```

The script:
1. `apt install lz4`
2. Download wal-g v3.0.8 (`wal-g-pg-22.04-amd64.tar.gz`) from GitHub
3. Install at `/usr/local/bin/wal-g`
4. Write `/etc/wal-g/env` (root:postgres mode 640) with all WALG + AWS_* vars
5. Verify: `wal-g backup-list` + `wal-g wal-show`

## Primary-only setup (svyatoslava)

After the install script runs on svyatoslava, add the `archive_command`
to Patroni's `postgresql.parameters` (in `patroni.yml`):

```yaml
postgresql:
  parameters:
    archive_mode: on
    archive_command: 'envdir /etc/wal-g/env wal-g wal-push %p'
    archive_timeout: 60
  # ... rest of config
```

Then reload Patroni:

```bash
sudo systemctl reload patroni
# OR (zero-downtime): patronictl -c /etc/patroni.yml restart skygate-pg
```

Verify:

```bash
# On svyatoslava:
sudo -u postgres psql -c "SHOW archive_command;"
sudo -u postgres psql -c "SELECT * FROM pg_stat_archiver;"
```

After the first 60s, new WAL segments start streaming to MinIO
under `wal_005/0000000200000000000000XX.lz4` (the `02` timeline is
because Patroni promoted this cluster fresh).

## Why AWS_ENDPOINT and not WALG_S3_ENDPOINT

This is a v3.0.8 quirk verified 2026-08-04. The wal-g v3.0.0 release
notes mention "Add WALG_ prefix to all the s3 related envs" (PR #1512)
but in v3.0.8 only `WALG_S3_PREFIX` actually maps. The endpoint, access
key, secret, region still use the `AWS_*` family. Using `WALG_S3_ENDPOINT`
or `AWS_S3_ENDPOINT` causes MinIO to return `InvalidAccessKeyId: 403`
because the AWS SDK signs the request differently.

Verified working env:

```bash
export WALG_S3_PREFIX=s3://skygate-pg-wal
export AWS_ENDPOINT=http://192.168.13.13:9000
export AWS_ACCESS_KEY_ID=skyadmin
export AWS_SECRET_ACCESS_KEY=Vfrttdf97
export AWS_REGION=us-east-1
export AWS_S3_FORCE_PATH_STYLE=true
# all other WALG_* knobs (COMPRESSION_METHOD, etc.)
```

## Restore (DR)

When you need to restore:

```bash
# 1. Stop Patroni + PG on a target node
sudo systemctl stop patroni

# 2. Wipe data dir (or use a new one)
sudo rm -rf /var/lib/postgresql/18/main
sudo -u postgres mkdir /var/lib/postgresql/18/main
sudo -u postgres chmod 700 /var/lib/postgresql/18/main

# 3. Fetch the latest base backup
sudo -u postgres bash -c '. /etc/wal-g/env && \
  wal-g backup-fetch /var/lib/postgresql/18/main LATEST'

# 4. Configure recovery
cat > /var/lib/postgresql/18/main/recovery.signal <<EOF
restore_command = 'envdir /etc/wal-g/env wal-g wal-fetch %f %p'
recovery_target_timeline = 'latest'
EOF
sudo -u postgres chown postgres:postgres /var/lib/postgresql/18/main/recovery.signal

# 5. Start PG (will replay WAL up to latest)
sudo systemctl start postgresql
# OR (if Patroni is supposed to manage): patronictl -c /etc/patroni.yml \
#   restart skygate-pg
```

Note: do NOT just run `wal-g backup-fetch` while Patroni is running.
It will conflict with Patroni's data dir management.

## Verification commands (after install)

```bash
# 1. wal-g sees the bucket
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-list'
# expected: "No backups found" (first install) or a list of base backups

# 2. wal-g sees existing WAL segments
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g wal-show'
# expected: table with 49+ rows (TLI 1, segments 0x0D-0x3D or more)

# 3. base backup from the primary
sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-push'
# expected: success message, new base_XXXX folder under basebackups_005/

# 4. continuous WAL streaming
sudo -u postgres psql -c "SELECT * FROM pg_stat_archiver;"
# expected: archived_count > 0 after a few minutes of traffic
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
