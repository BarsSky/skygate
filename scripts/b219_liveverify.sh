#!/usr/bin/env bash
# B219 live-verify on the agent.
#
# The agent doesn't have Patroni running, so the
# live-verify exercises the FAILURE path. The
# handler should:
#   1. Return a 303 redirect to /admin/database with
#      an error flash message
#   2. Write a db.failover.error audit_log row
#      (the failed attempt is on the audit log for
#      post-mortem)
#   3. NOT change cluster_node or cluster_database
#      rows (the failed attempt is a true no-op for
#      the cluster state)
#
# Pre-check: snapshot cluster_node count + cluster_database
# primary_node_id + last db.failover* audit row count.
# Run: POST /admin/database/failover with a test
# candidate.
# Post-check: same counts + new db.failover.error row.
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
# PGPASSWORD env var prefix needs `env` (not just
# `PGPASSWORD=... cmd` as a variable assignment —
# that fails because bash tries to execute
# PGPASSWORD=... as a command).
DSN_RUN="env PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5433 -U admin -d $DB -tA"

# Build the B219 binary (or use the existing one if
# already deployed).
SKYGATE_BIN="/tmp/skygate_b219"
$GO_BIN build -o "$SKYGATE_BIN" ./cmd/skygate

echo "=== Step 1: snapshot pre-B219 state ==="
PRE_NODE_COUNT=$($DSN_RUN -c "SELECT count(*) FROM cluster_node")
PRE_PRIMARY=$($DSN_RUN -c "SELECT primary_node_id FROM cluster_database WHERE id = 'skygate-staging'")
PRE_FAILOVER_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action LIKE 'db.failover%'")
echo "  cluster_node rows: $PRE_NODE_COUNT"
echo "  primary_node_id: $PRE_PRIMARY"
echo "  db.failover* audit rows: $PRE_FAILOVER_AUDIT"
echo ""

# Mint a session JWT for admin (same pattern as
# the B216 / B217 live-verifies). The binary uses
# SKYGATE_JWT_SECRET (NOT SKYGATE_SECRET_KEY) per
# internal/config/config.go:443 — we tripped on
# this in B216; pin the right env var here.
SECRET="${SKYGATE_JWT_SECRET:-${SKYGATE_SECRET_KEY:-}}"
if [ -z "$SECRET" ]; then
  echo "FATAL: SKYGATE_JWT_SECRET not set" >&2
  exit 1
fi
TOK=$(SKYGATE_JWT_SECRET="$SECRET" "$GO_BIN" run ./cmd/jwt-mint 2>&1)
if [ -z "$TOK" ]; then
  echo "FATAL: could not mint JWT (sync cmd/jwt-mint/main.go + scripts/b219_liveverify.sh first)" >&2
  echo "  SECRET length: ${#SECRET}" >&2
  exit 1
fi
echo "  issued JWT (1h TTL, length ${#TOK})"
echo ""

# Restart skygate with the B219 binary.
echo "=== Step 2: restart skygate with B219 binary ==="
SKYGATE_CONTAINER="skygate-skygate-1"
# The host binary at /home/skyadmin/skygate/skygate
# is owned by root and currently in-use by the
# running container (cp would fail with "Text file
# busy"). The bind-mount /home/skyadmin/skygate/:/app
# means the container's /app/skygate is the same
# file. To replace it cleanly, we:
#   1. docker exec to kill the running skygate
#      (the container's restart: unless-stopped
#      policy will respawn it)
#   2. WAIT for the old process to fully exit
#      (otherwise the file is still locked)
#   3. docker cp the new binary in place
#   4. let the respawn pick up the new binary
echo "  stopping the running skygate container..."
docker stop "$SKYGATE_CONTAINER" 2>&1 | tail -1
# docker stop waits for the container to fully exit,
# so the binary file is unlocked.
echo "  replacing /home/skyadmin/skygate/skygate with the B219 binary..."
sudo -n cp "$SKYGATE_BIN" /home/skyadmin/skygate/skygate
echo "  binary replaced ($(stat -c '%s' /home/skyadmin/skygate/skygate) bytes)"
echo "  starting skygate (will pick up the B219 binary)..."
docker start "$SKYGATE_CONTAINER" 2>&1 | tail -1
echo "  waiting for skygate to come up..."
sleep 30
echo "  skygate status: $(docker ps --filter "name=$SKYGATE_CONTAINER" --format '{{.Status}}')"
# Wait for /healthz to return 200 (up to 60s).
for i in $(seq 1 30); do
  if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:8080/healthz"; then
    echo "  /healthz OK after ${i}s"
    break
  fi
  sleep 2
done
echo ""

echo "=== Step 3: POST /admin/database/failover (FAILURE path) ==="
TEST_CANDIDATE="test-b219-nonexistent-$(date +%s)"
# Use curl with the session cookie. Expect 303
# redirect + an error in the ?err= param.
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' \
  -X POST \
  -b "skygate_session=${TOK}" \
  -d "candidate=${TEST_CANDIDATE}" \
  -d "leader=" \
  -d "reason=B219 live-verify test (Patroni not configured)" \
  "http://127.0.0.1:8080/admin/database/failover" 2>&1)
echo "  POST → 303 redirect to: $LOC"
if echo "$LOC" | grep -q "/admin/database"; then
  echo "  [ok]   redirect to /admin/database"
  REDIRECT_OK=1
else
  echo "  [FAIL] unexpected redirect: $LOC"
  REDIRECT_OK=0
fi
if echo "$LOC" | grep -q "err="; then
  echo "  [ok]   redirect carries err= flash (Patroni call failed as expected)"
  ERR_OK=1
else
  echo "  [FAIL] redirect missing err= param (handler didn't surface Patroni error)"
  ERR_OK=0
fi
echo ""

echo "=== Step 4: assert B219 behavior on the FAILURE path ==="
# 4a. cluster_node count unchanged
NEW_NODE_COUNT=$($DSN_RUN -c "SELECT count(*) FROM cluster_node")
if [ "$NEW_NODE_COUNT" = "$PRE_NODE_COUNT" ]; then
  echo "  [ok]   cluster_node count unchanged ($PRE_NODE_COUNT → $NEW_NODE_COUNT)"
  NODE_OK=1
else
  echo "  [FAIL] cluster_node count changed ($PRE_NODE_COUNT → $NEW_NODE_COUNT)"
  NODE_OK=0
fi

# 4b. cluster_database.primary_node_id unchanged
NEW_PRIMARY=$($DSN_RUN -c "SELECT primary_node_id FROM cluster_database WHERE id = 'skygate-staging'")
if [ "$NEW_PRIMARY" = "$PRE_PRIMARY" ]; then
  echo "  [ok]   primary_node_id unchanged ($PRE_PRIMARY → $NEW_PRIMARY)"
  PRIMARY_OK=1
else
  echo "  [FAIL] primary_node_id changed ($PRE_PRIMARY → $NEW_PRIMARY)"
  PRIMARY_OK=0
fi

# 4c. db.failover.error audit row was written
NEW_FAILOVER_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action LIKE 'db.failover%'")
DELTA=$((NEW_FAILOVER_AUDIT - PRE_FAILOVER_AUDIT))
echo "  post-failover db.failover* audit rows: $NEW_FAILOVER_AUDIT (delta: $DELTA)"
if [ "$DELTA" -ge 1 ]; then
  echo "  [ok]   db.failover.error audit row written (failed attempt on the audit log)"
  AUDIT_OK=1
  # Also confirm the action is db.failover.error specifically
  # (not db.failover which would be a SUCCESS row).
  ERROR_AUDIT_COUNT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action = 'db.failover.error'")
  if [ "${ERROR_AUDIT_COUNT:-0}" -ge 1 ]; then
    echo "  [ok]   db.failover.error action verified (not db.failover)"
    ERROR_ACTION_OK=1
  else
    echo "  [FAIL] no db.failover.error row (got $DELTA new rows but none with action='db.failover.error')"
    ERROR_ACTION_OK=0
  fi
else
  echo "  [FAIL] expected at least 1 db.failover* audit row, got delta=$DELTA"
  AUDIT_OK=0
  ERROR_ACTION_OK=0
fi

echo ""

echo "=== Step 5: final summary ==="
if [ "$REDIRECT_OK" = "1" ] && [ "$ERR_OK" = "1" ] && [ "$NODE_OK" = "1" ] && [ "$PRIMARY_OK" = "1" ] && [ "$AUDIT_OK" = "1" ] && [ "$ERROR_ACTION_OK" = "1" ]; then
  echo "  B219 LIVE-VERIFY: PASS (FAILURE path)"
  echo "    - 303 redirect to /admin/database with err= flash ✓"
  echo "    - cluster_node count unchanged ✓"
  echo "    - primary_node_id preserved ✓"
  echo "    - db.failover.error audit row written ✓"
else
  echo "  B219 LIVE-VERIFY: PARTIAL"
  echo "    redirect:        $REDIRECT_OK"
  echo "    err= param:      $ERR_OK"
  echo "    node unchanged:  $NODE_OK"
  echo "    primary:         $PRIMARY_OK"
  echo "    audit row:       $AUDIT_OK"
  echo "    error action:    $ERROR_ACTION_OK"
  exit 1
fi
echo ""
echo "=== B219 live-verify DONE ==="
