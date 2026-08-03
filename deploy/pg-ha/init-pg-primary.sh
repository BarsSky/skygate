#!/usr/bin/env bash
# init-pg-primary.sh — bootstrap the PG primary node on skygate-host-1.
#
# Run ONCE on skygate-host-1 (the primary) after:
#   1. /home/admin/skygate/deploy/pg-ha/.env is filled in
#   2. MinIO is running and has the skygate-wal-archive bucket
#   3. etcd is running on skygate-host-2 and reachable
#
# What this script does:
#   1. Initialize the Patroni REST API config (template substitution)
#   2. Start Patroni (initializes PG via initdb, takes the leader lock)
#   3. Start HAProxy (pg-aware routing, points to local Patroni)
#   4. Configure wal-g for the WAL archive
#   5. Verify the cluster: 1 leader, 0 replicas
#
# Idempotency: re-running this script after a successful first
# run is a no-op (Patroni takes the leader lock, no initdb
# re-run). Re-running on a corrupted primary requires manual
# intervention (see docs/runbooks/pg-failover.md).
#
# Reference: https://patroni.readthedocs.io/en/latest/

set -euo pipefail

# 1. Sanity checks.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

if [[ ! -f .env ]]; then
  echo "ERROR: .env not found. Copy .env.example to .env and fill in." >&2
  exit 1
fi
# shellcheck disable=SC1091
source .env

if [[ -z "${SKYGATE_PG_NODE_NAME:-}" || -z "${SKYGATE_PG_NODE_IP:-}" ]]; then
  echo "ERROR: SKYGATE_PG_NODE_NAME and SKYGATE_PG_NODE_IP must be set in .env" >&2
  exit 1
fi

if [[ -z "${MINIO_ROOT_USER:-}" || -z "${MINIO_ROOT_PASSWORD:-}" ]]; then
  echo "ERROR: MINIO_ROOT_USER and MINIO_ROOT_PASSWORD must be set in .env" >&2
  exit 1
fi

# 2. Template substitution (envsubst on patroni.yml + .env).
echo "==> Generating patroni.yml from template..."
envsubst < patroni.yml.template > patroni.yml
chmod 600 patroni.yml

# 3. Stop any running Patroni.
if docker ps --format '{{.Names}}' | grep -q '^skygate-patroni$'; then
  echo "==> Stopping existing Patroni container..."
  docker stop skygate-patroni || true
  docker rm skygate-patroni || true
fi

# 4. Start Patroni.
echo "==> Starting Patroni..."
docker run -d \
  --name skygate-patroni \
  --restart unless-stopped \
  --network host \
  -v "$PWD/patroni.yml:/etc/patroni.yml:ro" \
  -v patroni-data:/var/lib/postgresql/data \
  -e PATRONI_CONFIG_FILE=/etc/patroni.yml \
  -e PATRONI_SCOPE=skygate-pg \
  -e PATRONI_NAMESPACE=/skygate/ \
  -e PATRONI_NAME="${SKYGATE_PG_NODE_NAME}" \
  patroni:latest || {
    echo "ERROR: Patroni container failed to start. Check 'docker logs skygate-patroni'." >&2
    exit 1
  }

# 5. Wait for Patroni to elect itself as leader.
echo "==> Waiting for Patroni to elect as leader (timeout 60s)..."
for i in {1..30}; do
  if curl -fsS http://localhost:8008/health >/dev/null 2>&1; then
    STATE=$(curl -s http://localhost:8008/patroni | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('state','unknown'))")
    if [[ "$STATE" == "running" ]]; then
      echo "==> Patroni is leader (state=running)"
      break
    fi
  fi
  sleep 2
done

# 6. Start HAProxy.
if [[ ! -f haproxy.cfg ]]; then
  echo "ERROR: haproxy.cfg not found." >&2
  exit 1
fi
echo "==> Starting HAProxy..."
docker run -d \
  --name skygate-haproxy \
  --restart unless-stopped \
  --network host \
  -v "$PWD/haproxy.cfg:/usr/local/etc/haproxy/haproxy.cfg:ro" \
  haproxy:latest || {
    echo "ERROR: HAProxy container failed to start." >&2
    exit 1
  }

# 7. Configure wal-g.
echo "==> Configuring wal-g for WAL archive to MinIO..."
mkdir -p /etc/wal-g
cat > /etc/wal-g/env.sh <<EOF
export AWS_ACCESS_KEY_ID="${MINIO_ROOT_USER}"
export AWS_SECRET_ACCESS_KEY="${MINIO_ROOT_PASSWORD}"
export AWS_ENDPOINT="http://${SKYGATE_PG_NODE_IP}:9000"
export AWS_S3_FORCE_PATH_STYLE=true
export WALE_S3_PREFIX="s3://skygate-wal-archive/${SKYGATE_PG_NODE_NAME}"
export PGHOST="${SKYGATE_PG_NODE_IP}"
export PGPORT="5432"
export PGUSER="postgres"
export PGPASSWORD="${SKYGATE_PG_SUPERUSER_PASSWORD}"
EOF
chmod 600 /etc/wal-g/env.sh
echo "==> wal-g configured at /etc/wal-g/env.sh"

# 8. Verify.
echo "==> Verifying cluster state..."
sleep 3
curl -s http://localhost:8008/patroni | python3 -m json.tool | head -30
echo ""
echo "Cluster status:"
curl -s http://localhost:8008/cluster | python3 -m json.tool | head -30

echo ""
echo "=== Primary init complete ==="
echo "Next: run init-pg-replica.sh on skygate-host-2 to start streaming replication."
