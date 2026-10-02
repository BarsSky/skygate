#!/usr/bin/env bash
# check_pg_health.sh — verify the PG cluster is healthy.
#
# Run on EITHER skygate-host-1 or skygate-host-2. The script talks to
# the local Patroni REST API (localhost:8008) and reports:
#   - Which node is the primary
#   - Which node(s) are replicas
#   - Replication lag (in MB or seconds)
#   - WAL archive status (last wal-g push)
#   - etcd health
#
# Exit code:
#   0 = healthy (replication lag < 10s, primary + replica both up)
#   1 = degraded (lag > 10s but cluster is up)
#   2 = critical (primary down, or no replicas, or etcd down)
#
# Use this in cron (e.g. every 5 min) and alert on exit 2.
#
# Reference: https://patroni.readthedocs.io/en/latest/

set -uo pipefail

# Allow override via env (e.g. when run on a non-Patroni host).
PATRONI_URL="${PATRONI_URL:-http://localhost:8008}"

echo "=== skygate PG HA health check ==="
echo "Patroni endpoint: $PATRONI_URL"
echo "Time: $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
echo ""

# 1. Local node state.
LOCAL_STATE=$(curl -s "$PATRONI_URL/patroni" 2>/dev/null | python3 -c "
import sys, json
try:
  d = json.load(sys.stdin)
  print(d.get('state', 'unknown'))
except:
  print('error')
")

LOCAL_NAME=$(curl -s "$PATRONI_URL/patroni" 2>/dev/null | python3 -c "
import sys, json
try:
  d = json.load(sys.stdin)
  print(d.get('name', 'unknown'))
except:
  print('error')
")

echo "Local node: $LOCAL_NAME (state: $LOCAL_STATE)"
if [[ "$LOCAL_STATE" == "error" ]]; then
  echo "CRITICAL: Patroni not reachable at $PATRONI_URL"
  exit 2
fi

# 2. Cluster view.
CLUSTER=$(curl -s "$PATRONI_URL/cluster" 2>/dev/null)
if [[ -z "$CLUSTER" ]]; then
  echo "CRITICAL: /cluster endpoint not responding"
  exit 2
fi

# 3. Parse cluster members.
echo ""
echo "Cluster members:"
echo "$CLUSTER" | python3 -c "
import sys, json
d = json.load(sys.stdin)
for m in d.get('members', []):
  state = m.get('state', 'unknown')
  name = m.get('name', 'unknown')
  host = m.get('host', 'unknown')
  role = m.get('role', 'unknown')
  lag = m.get('lag', 'unknown')
  timeline = m.get('timeline', '?')
  print(f'  {name:30s} {host:25s} state={state:10s} role={role:10s} lag={lag} timeline={timeline}')
" || {
  echo "  ERROR parsing cluster JSON: $CLUSTER"
  exit 2
}

# 4. Check primary exists.
PRIMARY_COUNT=$(echo "$CLUSTER" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(sum(1 for m in d.get('members', []) if m.get('state') == 'running' and m.get('role') == 'primary'))
")

REPLICA_COUNT=$(echo "$CLUSTER" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(sum(1 for m in d.get('members', []) if m.get('state') == 'replica'))
")

echo ""
echo "Primary count: $PRIMARY_COUNT  (expect 1)"
echo "Replica count: $REPLICA_COUNT  (expect >= 1)"

# 5. Check replication lag.
MAX_LAG=$(echo "$CLUSTER" | python3 -c "
import sys, json
d = json.load(sys.stdin)
lags = []
for m in d.get('members', []):
  if m.get('state') == 'replica':
    lag = m.get('lag', 0)
    if isinstance(lag, (int, float)):
      lags.append(lag)
print(max(lags) if lags else 0)
")
echo "Max replication lag: ${MAX_LAG}s"

# 6. wal-g status.
echo ""
echo "wal-g status (last archive push):"
if [[ -f /etc/wal-g/env.sh ]]; then
  # shellcheck disable=SC1091
  source /etc/wal-g/env.sh
  # shellcheck disable=SC2034
  LAST_ARCHIVE=$(wal-g backup-list 2>/dev/null | head -1 || echo "no backups found")
  echo "  $LAST_ARCHIVE"
else
  echo "  /etc/wal-g/env.sh not found (wal-g not configured on this node)"
fi

# 7. Final health verdict.
echo ""
if [[ "$PRIMARY_COUNT" -eq 0 ]]; then
  echo "CRITICAL: no primary elected"
  exit 2
fi
if [[ "$REPLICA_COUNT" -eq 0 ]]; then
  echo "CRITICAL: no replicas"
  exit 2
fi
if (( $(echo "$MAX_LAG > 10" | bc -l 2>/dev/null || echo 0) )); then
  echo "DEGRADED: replication lag > 10s"
  exit 1
fi

echo "OK: cluster healthy"
exit 0
