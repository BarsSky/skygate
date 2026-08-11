#!/bin/bash
# Step 3-4: install wal-g on a skygate-pg node + configure for MinIO
# Works on both primary (svyatoslava) and replica (skygate-vm).
# On the REPLICA: backup-list, wal-show, backup-fetch work; backup-push
# requires data_dir and only does a remote (slow) backup.
# On the PRIMARY: archive_command writes WAL on every commit; backup-push
# with data_dir does fast local base backups (recommended).
#
# Verified 2026-08-04 on BOTH nodes:
#   - skygate-vm: backup-list PASS, 49 WAL segments visible (pre-existing)
#   - svyatoslava: archive_command writes 22+ WAL segments in 12 min,
#     first base backup (12MB full + 40KB delta) in MinIO
#
# CRITICAL v3.0.8 env var naming (verified 2026-08-04):
#   - WALG_S3_PREFIX  → bucket (e.g. s3://skygate-pg-wal)
#   - AWS_ENDPOINT    → S3 endpoint URL (NOT WALG_S3_ENDPOINT! NOT AWS_S3_ENDPOINT!)
#   - AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY → auth
#   - AWS_S3_FORCE_PATH_STYLE=true → for MinIO (hosted/virtual-style buckets fail)
#   - Every var must be `export`ed so the wal-g subprocess sees it
#
# MinIO endpoint choice depends on the node's network:
#   - skygate-vm (operator's <VM_HOST>) is on the home LAN → use http://<LAN_MINIO>:9000
#   - svyatoslava (45.152.198.217) is on a public VPS → use https://minio.skynas.ru
#     (resolves to 95.165.170.190, reverse-proxied to home MinIO)
# Pass the right endpoint via SKYGATE_MINIO_ENDPOINT env var.
#
# archive_command gotcha: daemontools' `envdir` expects a DIRECTORY of
# single-var files, not a single file. We use `. /etc/wal-g/env && wal-g ...`
# instead — works in /bin/sh -c which is what PG's archive_command runs in.
set -e
export DEBIAN_FRONTEND=noninteractive

WALG_VERSION=v3.0.8
ASSET_NAME="wal-g-pg-22.04-amd64"

# Operator's MinIO creds — read from .env if present, else fall back to placeholder
MINIO_ACCESS_KEY="${SKYGATE_MINIO_ACCESS_KEY:-skyadmin}"
MINIO_SECRET_KEY="${SKYGATE_MINIO_SECRET_KEY:-REPLACE_ME}"
MINIO_BUCKET="${SKYGATE_MINIO_BUCKET:-skygate-pg-wal}"
MINIO_ENDPOINT="${SKYGATE_MINIO_ENDPOINT:-http://192.0.2.1:9000}"

echo "=== Step 1: install lz4 (wal-g dependencies) ==="
sudo -E DEBIAN_FRONTEND=noninteractive apt-get install -y lz4 2>&1 | tail -3
echo

echo "=== Step 2: download wal-g binary (${WALG_VERSION} / ${ASSET_NAME}.tar.gz) ==="
if ! which wal-g > /dev/null 2>&1; then
    cd "$HOME"
    if ! curl -fsSL -o wal-g.tar.gz \
        "https://github.com/wal-g/wal-g/releases/download/${WALG_VERSION}/${ASSET_NAME}.tar.gz"; then
        echo "  ERROR: download failed (HTTP)" >&2
        exit 1
    fi
    ls -la wal-g.tar.gz
    file wal-g.tar.gz
    if ! tar -tzf wal-g.tar.gz > /dev/null 2>&1; then
        echo "  ERROR: downloaded file is not a valid gzip" >&2
        head -c 500 wal-g.tar.gz
        exit 1
    fi
    tar -xzf wal-g.tar.gz "${ASSET_NAME}"
    sudo mv "${ASSET_NAME}" /usr/local/bin/wal-g
    sudo chmod +x /usr/local/bin/wal-g
    rm -f wal-g.tar.gz
fi
wal-g --version 2>&1 | head -3
echo

echo "=== Step 3: write /etc/wal-g/env (root:postgres 0640) ==="
sudo mkdir -p /etc/wal-g
sudo tee /etc/wal-g/env > /dev/null <<WALG_ENV_EOF
# wal-g configuration for skygate-pg
# S3 backend: MinIO at ${MINIO_ENDPOINT}
# Operator credentials (per operator 2026-08-04):
#   AccessKey = ${MINIO_ACCESS_KEY}
#   Bucket    = ${MINIO_BUCKET}
# v3.0.8 env naming (verified 2026-08-04):
#   - WALG_S3_PREFIX  → bucket
#   - AWS_ENDPOINT    → S3 URL (NOT WALG_S3_ENDPOINT! NOT AWS_S3_ENDPOINT!)
#   - AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY → auth
#   - AWS_S3_FORCE_PATH_STYLE=true → for MinIO
# All vars must be \`export\`ed so wal-g subprocess sees them
export WALG_S3_PREFIX=s3://${MINIO_BUCKET}
export AWS_ENDPOINT=${MINIO_ENDPOINT}
export AWS_ACCESS_KEY_ID=${MINIO_ACCESS_KEY}
export AWS_SECRET_ACCESS_KEY=${MINIO_SECRET_KEY}
export AWS_REGION=us-east-1
export AWS_S3_FORCE_PATH_STYLE=true
export WALG_COMPRESSION_METHOD=lz4
export WALG_DELTA_MAX_STEPS=5
export WALG_LIBSODIUM_KEY=
export WALG_LIBSODIUM_KEY_TRANSFORM=
export WALG_PG_WAL_SIZE=16
export WALG_PG_DATASYNC_COPIES=0
export WALG_PG_PRESERVE_WAL=1
export WALG_USE_WAL_DELTA=true
export PGHOST=/var/run/postgresql
export PGUSER=postgres
export PGDATABASE=postgres
WALG_ENV_EOF
sudo chown root:postgres /etc/wal-g/env
sudo chmod 640 /etc/wal-g/env
echo "  /etc/wal-g/env written (root:postgres mode 640 — readable by postgres for archive_command)"
echo

echo "=== Step 4: verify wal-g can talk to MinIO ==="
. /etc/wal-g/env
echo "--- backup-list ---"
wal-g backup-list 2>&1 | tail -5
echo
echo "--- wal-show (last 5 segments) ---"
wal-g wal-show 2>&1 | tail -10
echo
echo "=== Step 5: role check + backup-push (primary only) ==="
ROLE=$(sudo -u postgres psql -t -c "SELECT pg_is_in_recovery();" 2>&1 | tr -d ' \n')
echo "  pg_is_in_recovery = $ROLE  (false = primary, true = replica)"
echo
if [ "$ROLE" = "f" ]; then
    echo "--- This is the PRIMARY: attempting backup-push (with data_dir for local mode) ---"
    set +e
    sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-push /var/lib/postgresql/data' 2>&1 | tail -10
    set -e
    echo
    echo "--- archive_command setup (REQUIRED for continuous WAL archiving) ---"
    echo "  Add to /etc/patroni/patroni.yml postgresql.parameters:"
    echo "    archive_mode: on"
    echo "    archive_command: '. /etc/wal-g/env && wal-g wal-push %p'"
    echo "    archive_timeout: 60"
    echo "  Then: curl -X POST http://127.0.0.1:8008/reload"
    echo "        yes | patronictl -c /etc/patroni/patroni.yml restart skygate-pg svyatoslava --force"
    echo "  (restart causes a brief failover, then svyatoslava rejoins as primary)"
else
    echo "--- This is a REPLICA: backup-push skipped (replica can't base-backup) ---"
fi
echo
echo "=== Done ==="
echo "  wal-g:        $(which wal-g)"
echo "  env file:     /etc/wal-g/env  (root:postgres mode 640)"
echo "  test command: sudo -u postgres bash -c '. /etc/wal-g/env && wal-g backup-list'"
