#!/usr/bin/env bash
# B223 live-verify on the agent.
#
# The agent's skygate container has the
# `tailscale` binary but `tailscaled` is NOT
# running (Tailscale is broken on the agent per
# the per-session notes). So the live-verify
# exercises the FAILURE path:
#   1. POST /admin/cluster/discover → expect a
#      303 with err= flash ("tailscaled not
#      running") + a cluster.discovery.error
#      audit_log row.
#   2. Verify the /admin/cluster page renders
#      without crashing (the B223 UI button is
#      present).
#   3. The unit tests in
#      internal/cluster/discovery_b223_test.go
#      cover the happy path via the
#      tailscaleStatusFn mock hook.
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

# Build the B223 binary.
SKYGATE_BIN="/tmp/skygate_b223"
$GO_BIN build -o "$SKYGATE_BIN" ./cmd/skygate

echo "=== Step 1: snapshot pre-B223 state ==="
PRE_DISCOVERED_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM cluster_audit WHERE action='node_discovered'")
PRE_RUN_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='cluster.discovery.run'")
PRE_ERROR_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='cluster.discovery.error'")
echo "  node_discovered cluster_audit rows: $PRE_DISCOVERED_AUDIT"
echo "  cluster.discovery.run audit_log rows: $PRE_RUN_AUDIT"
echo "  cluster.discovery.error audit_log rows: $PRE_ERROR_AUDIT"
echo ""

# Mint a session JWT.
TOK=$(SKYGATE_JWT_SECRET="$SKYGATE_JWT_SECRET" "$GO_BIN" run ./cmd/jwt-mint 2>&1)
if [ -z "$TOK" ]; then
  echo "FATAL: could not mint JWT" >&2
  exit 1
fi
echo "=== Step 2: mint session JWT (length ${#TOK}) ==="
echo ""

echo "=== Step 3: restart skygate with B223 binary ==="
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

echo "=== Step 4: confirm tailscaled is not running (expected on this agent) ==="
# The skygate container has the `tailscale` binary
# but tailscaled is broken. The orchestrator's
# runDiscoveryTicker would fail silently in the
# background; the HTTP handler surfaces the
# error to the operator.
docker exec "$SKYGATE_CONTAINER" tailscale status --json 2>&1 | head -1
echo ""

echo "=== Step 5: POST /admin/cluster/discover (FAILURE path: tailscaled down) ==="
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' \
  -X POST \
  -b "skygate_session=${TOK}" \
  "http://127.0.0.1:8080/admin/cluster/discover" 2>&1)
echo "  POST → 303 redirect to: $LOC"
if echo "$LOC" | grep -q "err=discovery+failed"; then
  echo "  [ok]   discovery error surfaced (tailscaled not running)"
  ERR_OK=1
else
  echo "  [FAIL] expected err=discovery+failed flash, got: $LOC"
  ERR_OK=0
fi
echo ""

echo "=== Step 6: assert cluster.discovery.error audit row written ==="
POST_ERROR_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='cluster.discovery.error'")
ERROR_DELTA=$((POST_ERROR_AUDIT - PRE_ERROR_AUDIT))
echo "  post-verify cluster.discovery.error rows: $POST_ERROR_AUDIT (delta: $ERROR_DELTA)"
if [ "$ERROR_DELTA" -ge 1 ]; then
  echo "  [ok]   cluster.discovery.error B221 audit row written"
  ERROR_AUDIT_OK=1
else
  echo "  [FAIL] no cluster.discovery.error audit row (delta=$ERROR_DELTA)"
  ERROR_AUDIT_OK=0
fi
echo ""

echo "=== Step 7: GET /admin/cluster (B223 button rendered, no crash) ==="
curl -s -b "skygate_session=${TOK}" "http://127.0.0.1:8080/admin/cluster" -o /tmp/cluster_b223.html -w "  HTTP=%{http_code} bytes=%{size_download}\n"
if grep -q 'action="/admin/cluster/discover"' /tmp/cluster_b223.html; then
  echo "  [ok]   /admin/cluster renders the 'Run Tailscale discovery' button"
  PAGE_OK=1
else
  echo "  [FAIL] /admin/cluster missing the discover button"
  PAGE_OK=0
fi
echo ""

echo "=== Step 8: cleanup ==="
# Remove the test cluster.discovery.error audit row
# (we generated 1 during step 5).
$DSN_RUN -c "DELETE FROM audit_log WHERE action='cluster.discovery.error' AND id > $PRE_ERROR_AUDIT" > /dev/null
echo "  removed test cluster.discovery.error rows"
echo ""

echo "=== Step 9: final summary ==="
if [ "$ERR_OK" = "1" ] && [ "$ERROR_AUDIT_OK" = "1" ] && [ "$PAGE_OK" = "1" ]; then
  echo "  B223 LIVE-VERIFY: PASS (FAILURE path on the agent — tailscaled down)"
  echo "    - POST /admin/cluster/discover surfaces a 303+err= flash: ✓"
  echo "    - cluster.discovery.error B221 audit row written: ✓"
  echo "    - /admin/cluster page renders the discover button: ✓"
  echo "    - happy path (new peer → pending row): SKIPPED (covered by unit test)"
else
  echo "  B223 LIVE-VERIFY: PARTIAL"
  echo "    err flash:         $ERR_OK"
  echo "    error audit row:   $ERROR_AUDIT_OK"
  echo "    /admin/cluster:    $PAGE_OK"
  exit 1
fi
echo ""
echo "=== B223 live-verify DONE ==="
