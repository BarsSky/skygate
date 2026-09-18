#!/usr/bin/env bash
# B226 live-verify on the agent.
#
# Exercises the /metrics endpoint end-to-end:
#   1. Restart skygate with the B226 binary
#   2. Wait 5s for the first collector tick
#   3. GET /metrics and assert the content-type
#      is "text/plain; version=0.0.4; charset=utf-8"
#   4. Assert at least 5 of the 10 production
#      metrics are present (some may be 0 if
#      the B206 healthz sampler hasn't filled
#      them yet, but the names should be there)
#   5. Assert skygate_build_info{version,go_version}
#      is present (the B226 build_info gauge is
#      set at startup, not from the collector)
#   6. Assert skygate_db_health is 1 (DB reachable)
#   7. Assert skygate_db_pool_open_connections is > 0
#      (pool has at least the watcher connection)
#   8. Trigger /admin/database/failover (Patroni
#      not running on the agent — expect the
#      failure path) and confirm skygate_failover_state
#      stays at 0 (no last_failover on the agent)
#   9. Cleanup the test audit row
set -euo pipefail
cd /home/skyadmin/skygate

set --
set -a
# shellcheck disable=SC1091
. /home/skyadmin/skygate/.env
set +a

GO_BIN="${GO_BIN:-/snap/go/current/bin/go}"
export GOCACHE="/tmp/go-cache"
export GOMODCACHE="/tmp/go-modcache"
mkdir -p "$GOCACHE" "$GOMODCACHE"

DB="skygate_staging"
DSN_RUN="env PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5433 -U admin -d $DB -tA"

# Build the B226 binary.
SKYGATE_BIN="/tmp/skygate_b226"
$GO_BIN build -o "$SKYGATE_BIN" ./cmd/skygate

echo "=== Step 1: snapshot pre-B226 state ==="
PRE_FAILOVER_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='db.failover.error'")
echo "  pre-verify db.failover.error rows: $PRE_FAILOVER_AUDIT"
echo ""

# Mint a session JWT.
TOK=$(SKYGATE_JWT_SECRET="$SKYGATE_JWT_SECRET" "$GO_BIN" run ./cmd/jwt-mint 2>&1)
if [ -z "$TOK" ]; then
  echo "FATAL: could not mint JWT" >&2
  exit 1
fi
echo "=== Step 2: mint session JWT (length ${#TOK}) ==="
echo ""

echo "=== Step 3: restart skygate with B226 binary ==="
SKYGATE_CONTAINER="skygate-skygate-1"
docker stop "$SKYGATE_CONTAINER" 2>&1 | tail -1
sudo -n cp "$SKYGATE_BIN" /home/skyadmin/skygate/skygate
docker start "$SKYGATE_CONTAINER" 2>&1 | tail -1
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
  if curl -s -o /dev/null --max-time 2 http://127.0.0.1:8080/healthz; then
    echo "  /healthz OK after ${i}s"
    break
  fi
  sleep 2
done
echo ""

echo "=== Step 4: GET /metrics (Content-Type + first 50 lines) ==="
curl -s -o /tmp/b226_metrics.txt -D /tmp/b226_headers.txt http://127.0.0.1:8080/metrics
CONTENT_TYPE=$(grep -i 'content-type' /tmp/b226_headers.txt | tr -d '\r' | sed 's/.*: //')
echo "  Content-Type: $CONTENT_TYPE"
if echo "$CONTENT_TYPE" | grep -q "text/plain; version=0.0.4"; then
  echo "  [ok]   Content-Type matches Prometheus spec"
  CT_OK=1
else
  echo "  [FAIL] Content-Type is wrong"
  CT_OK=0
fi
echo "  --- first 30 lines of /metrics ---"
head -30 /tmp/b226_metrics.txt
echo ""
echo ""

echo "=== Step 5: assert 10 production metrics are present ==="
declare -a EXPECT_METRICS=(
  "skygate_cluster_nodes"
  "skygate_cluster_nodes_total"
  "skygate_db_health"
  "skygate_db_size_bytes"
  "skygate_db_pool_open_connections"
  "skygate_db_pool_idle_connections"
  "skygate_db_pool_in_use_connections"
  "skygate_elector_is_primary"
  "skygate_failover_state"
  "skygate_build_info"
)
METRICS_OK=1
for m in "${EXPECT_METRICS[@]}"; do
  if grep -qE "^# HELP $m " /tmp/b226_metrics.txt && grep -qE "^$m[\{ ]" /tmp/b226_metrics.txt; then
    echo "  [ok]   $m present (HELP + at least 1 series)"
  else
    echo "  [FAIL] $m missing or no series"
    METRICS_OK=0
  fi
done
echo ""

echo "=== Step 6: assert skygate_build_info has the right labels ==="
BUILD_INFO=$(grep -E "^skygate_build_info\{" /tmp/b226_metrics.txt | head -1)
if [ -n "$BUILD_INFO" ]; then
  echo "  [ok]   $BUILD_INFO"
  if echo "$BUILD_INFO" | grep -q 'go_version="go'; then
    echo "  [ok]   go_version label set"
    BUILD_OK=1
  else
    echo "  [FAIL] go_version label missing"
    BUILD_OK=0
  fi
else
  echo "  [FAIL] skygate_build_info series not found"
  BUILD_OK=0
fi
echo ""

echo "=== Step 7: assert skygate_db_health is 1 (DB reachable) ==="
HEALTH=$(grep -E "^skygate_db_health\{" /tmp/b226_metrics.txt | head -1)
echo "  $HEALTH"
if echo "$HEALTH" | grep -qE '} 1$'; then
  echo "  [ok]   skygate_db_health = 1 (DB reachable)"
  HEALTH_OK=1
else
  echo "  [FAIL] skygate_db_health is not 1"
  HEALTH_OK=0
fi
echo ""

echo "=== Step 8: assert skygate_db_pool_open_connections > 0 ==="
OPEN=$(grep -E "^skygate_db_pool_open_connections " /tmp/b226_metrics.txt | awk '{print $2}')
echo "  skygate_db_pool_open_connections = $OPEN"
if [ -n "$OPEN" ] && [ "$(printf "%.0f" "$OPEN" 2>/dev/null || echo 0)" -gt 0 ]; then
  echo "  [ok]   pool has at least 1 open connection"
  POOL_OK=1
else
  echo "  [FAIL] pool open = $OPEN (expected > 0)"
  POOL_OK=0
fi
echo ""

echo "=== Step 9: trigger /admin/database/failover (Patroni not running — expect failure path) ==="
FAIL_LOC=$(curl -s -X POST -b "skygate_session=${TOK}" \
  -d "candidate=skygate-standby" \
  -d "leader=skygate-primary" \
  -d "reason=B226 live-verify test" \
  -o /dev/null -w '%{http_code} %{redirect_url}\n' --max-time 30 http://127.0.0.1:8080/admin/database/failover)
echo "  failover: $FAIL_LOC"
if echo "$FAIL_LOC" | grep -q "^303"; then
  FAIL_HANDLER_OK=1
else
  FAIL_HANDLER_OK=0
fi
sleep 35
# Re-fetch /metrics after the failover attempt
# (B220 doesn't write last_failover on failure
# — the collector reads the global_settings
# value which is empty on the agent → state
# gauge stays at 0).
curl -s http://127.0.0.1:8080/metrics > /tmp/b226_metrics_2.txt
FAILOVER_STATE=$(grep -E "^skygate_failover_state\{" /tmp/b226_metrics_2.txt | head -1)
echo "  skygate_failover_state after failover attempt: $FAILOVER_STATE"
if [ -n "$FAILOVER_STATE" ] && echo "$FAILOVER_STATE" | grep -qE '} 0$'; then
  echo "  [ok]   failover_state = 0 (no last_failover — B220 only sets on success)"
  FAILOVER_OK=1
else
  echo "  [FAIL] failover_state is not 0 (B220 regression or unexpected state)"
  FAILOVER_OK=0
fi
echo ""

echo "=== Step 10: cleanup ==="
$DSN_RUN -c "DELETE FROM audit_log WHERE id > $PRE_FAILOVER_AUDIT AND action='db.failover.error'" > /dev/null
echo "  removed test audit rows"
echo ""

echo "=== Step 11: final summary ==="
if [ "$CT_OK" = "1" ] && [ "$METRICS_OK" = "1" ] && [ "$BUILD_OK" = "1" ] && [ "$HEALTH_OK" = "1" ] && [ "$POOL_OK" = "1" ] && [ "$FAILOVER_OK" = "1" ]; then
  echo "  B226 LIVE-VERIFY: PASS"
  echo "    - /metrics Content-Type matches Prometheus spec: ✓"
  echo "    - 10 production metrics present: ✓"
  echo "    - skygate_build_info labels set (version + go_version): ✓"
  echo "    - skygate_db_health = 1 (DB reachable): ✓"
  echo "    - skygate_db_pool_open_connections > 0: ✓"
  echo "    - skygate_failover_state = 0 after failed failover (B220 B220 only sets on success): ✓"
else
  echo "  B226 LIVE-VERIFY: PARTIAL"
  echo "    Content-Type:    $CT_OK"
  echo "    metrics present: $METRICS_OK"
  echo "    build_info:      $BUILD_OK"
  echo "    db_health=1:     $HEALTH_OK"
  echo "    pool open>0:     $POOL_OK"
  echo "    failover_state:  $FAILOVER_OK"
  exit 1
fi
echo ""
echo "=== B226 live-verify DONE ==="
