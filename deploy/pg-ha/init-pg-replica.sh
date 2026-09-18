#!/usr/bin/env bash
# init-pg-replica.sh — bootstrap the PG replica node on skygate-host-2.
#
# Run ONCE on skygate-host-2 AFTER init-pg-primary.sh has completed
# successfully on skygate-host-1. The replica takes a basebackup
# from the primary and starts streaming replication.
#
# Reference: https://patroni.readthedocs.io/en/latest/

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

if [[ ! -f .env ]]; then
  echo "ERROR: .env not found." >&2
  exit 1
fi
# shellcheck disable=SC1091
source .env

if [[ -z "${SKYGATE_PG_NODE_NAME:-}" || -z "${SKYGATE_PG_NODE_IP:-}" ]]; then
  echo "ERROR: SKYGATE_PG_NODE_NAME and SKYGATE_PG_NODE_IP must be set in .env" >&2
  exit 1
fi

# 1. Sanity: primary must be reachable.
PRIMARY_IP="${SKYGATE_PRIMARY_IP:-192.0.2.1}"
if ! curl -fsS "http://${PRIMARY_IP}:8008/health" >/dev/null 2>&1; then
  echo "ERROR: primary Patroni not reachable at ${PRIMARY_IP}:8008. Has init-pg-primary.sh been run on skygate-host-1?" >&2
  exit 1
fi

# 2. Template substitution.
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
#    Patroni auto-joins the cluster, takes a basebackup from
#    the primary, and starts streaming replication.
echo "==> Starting Patroni on replica..."
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

# 5. Wait for replica to catch up.
echo "==> Waiting for replica to catch up (timeout 120s)..."
for i in {1..60}; do
  STATE=$(curl -s http://localhost:8008/patroni 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('state','unknown'))" 2>/dev/null || echo "unknown")
  LAG=$(curl -s "http://${PRIMARY_IP}:8008/cluster" 2>/dev/null | python3 -c "
import sys, json
try:
  d = json.load(sys.stdin)
  for m in d.get('members', []):
    if m.get('name') == '${SKYGATE_PG_NODE_NAME}':
      print(m.get('lag', 'unknown'))
      break
except:
  print('unknown')
" 2>/dev/null || echo "unknown")
  if [[ "$STATE" == "replica" ]]; then
    echo "==> Replica caught up: state=replica, lag=${LAG}"
    break
  fi
  sleep 2
done

# 6. Start HAProxy (same as primary, but the primary-route
#    will forward to the primary on skygate-host-1).
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
echo "==> Configuring wal-g on replica (for restore-from-archive if needed)..."
mkdir -p /etc/wal-g
cat > /etc/wal-g/env.sh <<EOF
export AWS_ACCESS_KEY_ID="${MINIO_ROOT_USER}"
export AWS_SECRET_ACCESS_KEY="${MINIO_ROOT_PASSWORD}"
export AWS_ENDPOINT="http://${SKYGATE_PRIMARY_IP}:9000"
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
echo "==> Verifying replica state..."
sleep 3
curl -s http://localhost:8008/patroni | python3 -m json.tool | head -30
echo ""
echo "Cluster status (from this node's view):"
curl -s http://localhost:8008/cluster | python3 -m json.tool | head -30

echo ""
echo "=== Replica init complete ==="
echo "Run check_pg_health.sh on both VMs to verify replication lag and primary state."
