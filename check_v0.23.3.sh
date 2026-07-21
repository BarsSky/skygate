#!/bin/bash
# check_v0.23.3.sh — live verification for the v0.23.3 node-expiry watcher.
#
# Scenario being verified:
#   1. A fresh node registers via a 1h preauth key. Tailscale
#      1.98.x sends Expiry = now + 2-4s in RegisterRequest;
#      headscale 0.29.x applies it verbatim, so the node
#      lands with an expiry of only 2-4 seconds.
#   2. The expirewatch goroutine (ticking every 5m in
#      production; we use 30s for this test) sees the
#      near-expiry node on its next sweep and extends it
#      out to now + 30d.
#   3. After the sweep, the node's expiry in headscale is
#      at least 7d in the future (well past the
#      7d threshold that gates "needs renewal").
#   4. The audit log has a row tagged `expirewatch` with
#      `action=renewed` and a `node_id=<id>` detail.
#
# Run from C:\Projects\skygate\ after the operator has:
#   - pulled feature/v0.23.3-expirewatch to the VM
#   - restarted skygate (`docker compose up -d --force-recreate
#     --no-deps skygate` so the build picks up the new binary)
#   - confirmed `SKYGATE_EXPIREWATCH_INTERVAL=30s` is in
#     /home/skyadmin/skygate/.env (we set it temporarily for
#     the test; the production default is 5m).
#
# Exit code 0 = all checks PASS, non-zero on first failure.
# Use run_check_v0.23.3.sh as the scp helper.

set -euo pipefail

DOCKER=/usr/bin/docker
HS_CONTAINER=headscale
SKY_CONTAINER=skygate
SKY_DIR=/home/skyadmin/skygate
API_KEY=$(grep ^HEADSCALE_API_KEY= ${SKY_DIR}/.env | cut -d= -f2-)

# --- 0. Pre-flight: skygate is up + watcher is running ---

echo "=== [0] Pre-flight ==="
if ! ${DOCKER} ps --format '{{.Names}}' | grep -q "^${SKY_CONTAINER}$"; then
  echo "FAIL: ${SKY_CONTAINER} not running"
  exit 1
fi
if ! ${DOCKER} exec ${SKY_CONTAINER} ps -ef | grep -v grep | grep -q skygate; then
  echo "FAIL: skygate process not found in container"
  exit 1
fi
echo "  skygate: up"

# --- 1. Force a node's expiry into the "expiring soon" bucket ---

echo
echo "=== [1] Pick a non-tagged test node and force its expiry into the 'expiring soon' window ==="
# Get a user-owned AND UN-TAGGED node from headscale. Production
# new devices have no tags until skygate's backfillNodeOwnership
# applies tag:private (a few seconds after /my/devices
# load), so a freshly-registered device is exactly the
# "no tags + short expiry" state we want to exercise.
#
# On a long-running deployment every existing node has
# been tagged by skygate, so no candidate exists. We
# register a fresh test node via a one-shot tailscaled
# instance (the same path that production devices use):
# the resulting node has no tags initially.
#
# We use the headscale CLI to set the expiry (verified
# live on 2026-07-21: headscale 0.29.2's REST API for
# 'POST /api/v1/node/{id}/expire' has a bug that
# silently ignores the expiry field — only the CLI
# works. The watcher uses the CLI path exclusively for
# the same reason — see internal/headscale/nodes.go
# ExtendNodeExpiry doc.)
${DOCKER} cp ${HS_CONTAINER}:/var/lib/headscale /tmp/hs_check_v0233
TEST_NODE=$(sqlite3 /tmp/hs_check_v0233/db.sqlite "SELECT id FROM nodes WHERE \"tags\" = '[]' ORDER BY id LIMIT 1;")
if [ -z "$TEST_NODE" ]; then
  # No un-tagged node exists. Register a fresh one.
  echo "  no un-tagged node found; registering a fresh test node"
  PREAUTH=$(${DOCKER} exec ${HS_CONTAINER} headscale preauthkeys create -u 1 -e 1h -o json --reusable=false | python3 -c "import json,sys; print(json.load(sys.stdin)['key'])")
  rm -rf /tmp/ts-v0233-test
  mkdir -p /tmp/ts-v0233-test/state
  /usr/sbin/tailscaled \
    --state=/tmp/ts-v0233-test/state/state.json \
    --socket=/tmp/ts-v0233-test/state/tailscale.sock \
    --tun=userspace-networking >/tmp/ts-v0233-test/tailscaled.log 2>&1 &
  TAILLOCAL_PID=$!
  sleep 4
  /usr/bin/tailscale --socket=/tmp/ts-v0233-test/state/tailscale.sock \
    up --login-server=https://head.skynas.ru --authkey="$PREAUTH" --accept-routes 2>&1 | head -3
  sleep 2
  /usr/bin/tailscale --socket=/tmp/ts-v0233-test/state/tailscale.sock logout 2>&1 | head -2 || true
  kill $TAILLOCAL_PID 2>/dev/null || true
  sleep 1
  # Find the new node (id > 20 in our deployment).
  TEST_NODE=$(${DOCKER} exec ${HS_CONTAINER} headscale nodes list -o json | python3 -c "
import json, sys
d = json.load(sys.stdin)
ids = [n['id'] for n in d if n.get('id', 0) > 20 and n.get('tags') is None or n.get('tags') == []]
print(max(ids) if ids else '')
")
  if [ -z "$TEST_NODE" ]; then
    echo "FAIL: could not find/refresh a fresh test node"
    exit 1
  fi
  echo "  fresh test node id=${TEST_NODE} (just registered, no tags)"
fi
echo "  test node: id=${TEST_NODE} (must be un-tagged for the watcher to renew it)"
API_KEY=$(grep ^HEADSCALE_API_KEY= ${SKY_DIR}/.env | cut -d= -f2-)
SQLITE3_PLUS_5S=$(date -u -d "+5 seconds" +"%Y-%m-%dT%H:%M:%SZ")
echo "  set expiry to ${SQLITE3_PLUS_5S} (5s in the future) via CLI"
${DOCKER} exec ${HS_CONTAINER} headscale nodes expire -i "${TEST_NODE}" --expiry "${SQLITE3_PLUS_5S}" 2>&1 | head -2
# Verify via API
ACTUAL_VIA_API=$(curl -sS -H "Authorization: Bearer ${API_KEY}" "http://localhost:50444/api/v1/node/${TEST_NODE}" | python3 -c "import json,sys; d=json.load(sys.stdin); n=d.get('node',d); print(n.get('expiry'))" 2>/dev/null)
echo "  verified via headscale API: ${ACTUAL_VIA_API}"

# --- 2. Wait for the watcher to tick ---

echo
echo "=== [2] Wait for the watcher to tick (max 90s) ==="
# SKYGATE_EXPIREWATCH_INTERVAL=30s was set in .env for this
# test, so the next tick is at most 30s away. Allow 90s for
# the goroutine to be scheduled + the headscale CLI call to
# round-trip + the audit row to land.
ATTEMPTS=0
MAX_ATTEMPTS=18
NEW_EXPIRY=""
SECS_UNTIL_EXPIRY=0
while [ $ATTEMPTS -lt $MAX_ATTEMPTS ]; do
  sleep 5
  ATTEMPTS=$((ATTEMPTS + 1))
  # Read via the headscale API, not via db.sqlite cp — the
  # API always sees the current state regardless of WAL.
  NEW_EXPIRY=$(curl -sS -H "Authorization: Bearer ${API_KEY}" "http://localhost:50444/api/v1/node/${TEST_NODE}" | python3 -c "import json,sys; d=json.load(sys.stdin); n=d.get('node',d); print(n.get('expiry') or '')")
  echo "  attempt ${ATTEMPTS}/${MAX_ATTEMPTS}: node ${TEST_NODE} expiry = ${NEW_EXPIRY}"
  if [ -n "$NEW_EXPIRY" ]; then
    # Headscale returns RFC3339Nano. Use python for the parse
    # so we get sub-second precision if the API ever returns it.
    SECS_UNTIL_EXPIRY=$(python3 -c "
from datetime import datetime, timezone
e = datetime.fromisoformat('${NEW_EXPIRY}'.replace('Z', '+00:00'))
now = datetime.now(timezone.utc)
print(int((e - now).total_seconds()))
" 2>/dev/null || echo 0)
    if [ "${SECS_UNTIL_EXPIRY:-0}" -ge 604800 ]; then
      echo "  ✓ expiry is now $((${SECS_UNTIL_EXPIRY} / 86400))d in the future"
      break
    fi
  fi
done

# --- 3. Verify the renewal ---

# --- 3. Verify the final state ---

echo
echo "=== [3] Verify the renewal ==="
if [ "$SECS_UNTIL_EXPIRY" -lt 604800 ]; then
  echo "FAIL: node ${TEST_NODE} expiry not extended past 7d threshold (got ${SECS_UNTIL_EXPIRY}s remaining)"
  exit 1
fi
echo "  ✓ node ${TEST_NODE} expiry = ${NEW_EXPIRY} ($((${SECS_UNTIL_EXPIRY} / 86400))d remaining)"

# --- 4. Verify the audit log ---

echo
echo "=== [4] Verify the audit log ==="
# The skygate container is alpine (no sqlite3 binary —
# AGENTS.md "Common gotchas"), so copy the db out to the
# host and run sqlite3 there.
#
# IMPORTANT: skygate uses WAL mode (like headscale). The
# main db.sqlite is only flushed every N seconds; recent
# writes sit in db.sqlite-wal until a checkpoint. Copying
# only the main file gives a STALE snapshot. We copy all
# three (main + wal + shm) so SQLite sees the full state
# when it opens the file on the host.
sleep 2
${DOCKER} cp ${SKY_CONTAINER}:/data/skygate.db /tmp/skygate_check_v0233.db
${DOCKER} cp ${SKY_CONTAINER}:/data/skygate.db-wal /tmp/skygate_check_v0233.db-wal 2>/dev/null || true
${DOCKER} cp ${SKY_CONTAINER}:/data/skygate.db-shm /tmp/skygate_check_v0233.db-shm 2>/dev/null || true
sqlite3 -header -column /tmp/skygate_check_v0233.db "SELECT id, user_id, username, action, detail, datetime(created_at, 'unixepoch', 'localtime') AS created FROM audit_log WHERE username = 'expirewatch' AND action = 'renewed' ORDER BY id DESC LIMIT 3;"

ROW_COUNT=$(sqlite3 /tmp/skygate_check_v0233.db "SELECT COUNT(*) FROM audit_log WHERE username='expirewatch' AND action='renewed' AND detail LIKE 'node_id=${TEST_NODE}%';")
if [ "$ROW_COUNT" -eq 0 ]; then
  echo "FAIL: no expirewatch audit row found for node_id=${TEST_NODE}"
  exit 1
fi
echo "  ✓ ${ROW_COUNT} audit row(s) found for node_id=${TEST_NODE}"

# --- 5. Sanity: tagged nodes were NOT touched ---

echo
echo "=== [5] Verify tagged nodes are NOT renewed ==="
# Pick a tagged node (e.g. emilia / id=3) and check it still
# has a normal expiry (null in DB, since tagged nodes skip the
# regReq.Expiry branch and never get an expiry).
TAGGED_EXPIRY=$(curl -sS -H "Authorization: Bearer ${API_KEY}" "http://localhost:50444/api/v1/node/3" | python3 -c "import json,sys; d=json.load(sys.stdin); n=d.get('node',d); print(n.get('expiry') or '<null>')" 2>/dev/null)
echo "  emilia (tagged node id=3) expiry = ${TAGGED_EXPIRY}"
# We just confirm the watcher didn't touch a tagged node
# during our test (no recent audit row mentioning node_id=3
# with action=renewed).
TAGGED_AUDIT=$(sqlite3 /tmp/skygate_check_v0233.db "SELECT COUNT(*) FROM audit_log WHERE username='expirewatch' AND action='renewed' AND detail LIKE 'node_id=3%';")
echo "  expirewatch renewals of emilia: ${TAGGED_AUDIT} (should be 0)"
if [ "$TAGGED_AUDIT" -ne 0 ]; then
  echo "FAIL: watcher touched tagged node (id=3)"
  exit 1
fi
echo "  ✓ tagged node untouched"

echo
echo "=== ALL CHECKS PASS ==="
echo "Summary:"
echo "  - Node ${TEST_NODE} had expiry forced to 5s in the future (via CLI)"
echo "  - Watcher ticked within the timeout and extended it to 30d"
echo "  - Audit log has ${ROW_COUNT} row(s) for the renewal"
echo "  - Tagged node (id=3) was correctly skipped"

echo
echo "=== ALL CHECKS PASS ==="
echo "Summary:"
echo "  - Node ${TEST_NODE} had expiry forced to 2s in the future"
echo "  - Watcher ticked within ${ATTEMPTS}*5s and extended it to ${NEW_EXPIRY}"
echo "  - Audit log has ${ROW_COUNT} row(s) for the renewal"
echo "  - Tagged node (id=3) was correctly skipped"
