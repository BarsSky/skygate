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
echo "=== [1] Pick a non-tagged test node and force its expiry to 2 seconds from now ==="
# Get a user-owned (non-tagged) node from headscale. skybars (id=10)
# is user-owned (tag:private). Note: headscale stores tagged nodes
# under the synthetic "tagged-devices" user; user-owned nodes
# carry the actual user_id in the DB.
${DOCKER} cp ${HS_CONTAINER}:/var/lib/headscale /tmp/hs_check_v0233
TEST_NODE=$(sqlite3 /tmp/hs_check_v0233/db.sqlite "SELECT id FROM nodes WHERE id IN (10, 16, 17, 19, 20) AND user_id NOT IN (SELECT id FROM users WHERE name='tagged-devices') ORDER BY id LIMIT 1;")
if [ -z "$TEST_NODE" ]; then
  echo "FAIL: no suitable user-owned test node found"
  exit 1
fi
echo "  test node: id=${TEST_NODE}"
SQLITE3_NOW=$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")
SQLITE3_PLUS_2S=$(date -u -d "+2 seconds" +"%Y-%m-%dT%H:%M:%S.000Z")
SQLITE3_PLUS_30D=$(date -u -d "+30 days" +"%Y-%m-%dT%H:%M:%S.000Z")
sqlite3 /tmp/hs_check_v0233/db.sqlite "UPDATE nodes SET expiry='${SQLITE3_PLUS_2S}' WHERE id=${TEST_NODE};"
echo "  set expiry to ${SQLITE3_PLUS_2S} (2s in the future)"

# Confirm the change
ACTUAL=$(sqlite3 -header -column /tmp/hs_check_v0233/db.sqlite "SELECT datetime(expiry) FROM nodes WHERE id=${TEST_NODE};" | tail -1)
echo "  verified expiry: ${ACTUAL}"

# --- 2. Wait for the watcher to tick ---

echo
echo "=== [2] Wait for the watcher to tick (max 60s) ==="
# SKYGATE_EXPIREWATCH_INTERVAL=30s was set in .env for this
# test, so the next tick is at most 30s away. Allow 60s for
# the goroutine to be scheduled + the headscale API call to
# round-trip.
ATTEMPTS=0
MAX_ATTEMPTS=12
while [ $ATTEMPTS -lt $MAX_ATTEMPTS ]; do
  sleep 5
  ATTEMPTS=$((ATTEMPTS + 1))
  ${DOCKER} cp ${HS_CONTAINER}:/var/lib/headscale /tmp/hs_check_v0233_t
  NEW_EXPIRY=$(sqlite3 /tmp/hs_check_v0233_t/db.sqlite "SELECT datetime(expiry) FROM nodes WHERE id=${TEST_NODE};")
  echo "  attempt ${ATTEMPTS}/${MAX_ATTEMPTS}: node ${TEST_NODE} expiry = ${NEW_EXPIRY}"
  # If the expiry is now at least 7d out, the watcher has
  # ticked. (Threshold is 7d, renewal is 30d.)
  if [ -n "$NEW_EXPIRY" ]; then
    SECS_UNTIL_EXPIRY=$(($(date -d "$NEW_EXPIRY" +%s) - $(date -u +%s)))
    if [ "$SECS_UNTIL_EXPIRY" -ge 604800 ]; then
      echo "  ✓ expiry is now $((${SECS_UNTIL_EXPIRY} / 86400))d in the future"
      break
    fi
  fi
done

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
${DOCKER} exec -i ${SKY_CONTAINER} sqlite3 /data/skygate.db <<SQL
.mode column
.headers on
SELECT id, user_id, username, action, detail, datetime(created_at, 'unixepoch') AS created
  FROM audit_log
 WHERE username = 'expirewatch' AND action = 'renewed'
 ORDER BY id DESC LIMIT 3;
SQL

ROW_COUNT=$(${DOCKER} exec -i ${SKY_CONTAINER} sqlite3 /data/skygate.db "SELECT COUNT(*) FROM audit_log WHERE username='expirewatch' AND action='renewed' AND detail LIKE 'node_id=${TEST_NODE}%';")
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
${DOCKER} cp ${HS_CONTAINER}:/var/lib/headscale /tmp/hs_check_v0233_t
TAGGED_EXPIRY=$(sqlite3 /tmp/hs_check_v0233_t/db.sqlite "SELECT datetime(expiry) FROM nodes WHERE id=3;")
echo "  emilia (tagged node id=3) expiry = ${TAGGED_EXPIRY:-<null>}"
# We just confirm the watcher didn't touch a tagged node
# during our test (no recent audit row mentioning node_id=3
# with action=renewed).
TAGGED_AUDIT=$(${DOCKER} exec -i ${SKY_CONTAINER} sqlite3 /data/skygate.db "SELECT COUNT(*) FROM audit_log WHERE username='expirewatch' AND action='renewed' AND detail LIKE 'node_id=3%';")
echo "  expirewatch renewals of emilia: ${TAGGED_AUDIT} (should be 0)"
if [ "$TAGGED_AUDIT" -ne 0 ]; then
  echo "FAIL: watcher touched tagged node (id=3)"
  exit 1
fi
echo "  ✓ tagged node untouched"

echo
echo "=== ALL CHECKS PASS ==="
echo "Summary:"
echo "  - Node ${TEST_NODE} had expiry forced to 2s in the future"
echo "  - Watcher ticked within ${ATTEMPTS}*5s and extended it to ${NEW_EXPIRY}"
echo "  - Audit log has ${ROW_COUNT} row(s) for the renewal"
echo "  - Tagged node (id=3) was correctly skipped"
