#!/usr/bin/env bash
# B220 live-verify on the agent.
#
# Exercises the FAILURE path twice:
#   1. POST /admin/database/failover/rollback with no
#      prior db.last_failover state → expect the
#      "no last failover recorded" error (guard
#      rail that prevents rolling back to nothing).
#   2. Insert a fake db.last_failover state via SQL
#      → POST /admin/database/failover/rollback
#      again → expect the Patroni "connection
#      refused" error (no real Patroni running on
#      the agent) + the db.failover_rollback.error
#      audit row written + the db.last_failover
#      state NOT cleared (so the operator can retry
#      the rollback after fixing Patroni).
#
# Pre-check: snapshot global_settings.db.last_failover
# presence + db.failover_rollback* audit count.
# Post-check: same + new db.failover_rollback.error row.
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

# Build the B220 binary.
SKYGATE_BIN="/tmp/skygate_b220"
$GO_BIN build -o "$SKYGATE_BIN" ./cmd/skygate

echo "=== Step 1: snapshot pre-B220 state ==="
PRE_FAILOVER_STATE=$($DSN_RUN -c "SELECT count(*) FROM global_settings WHERE key = 'db.last_failover'")
PRE_ROLLBACK_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action LIKE 'db.failover_rollback%'")
echo "  db.last_failover present: $PRE_FAILOVER_STATE (want 0 — fresh start)"
echo "  db.failover_rollback* audit rows: $PRE_ROLLBACK_AUDIT"
echo ""

# Mint a session JWT.
SECRET="${SKYGATE_JWT_SECRET:-${SKYGATE_SECRET_KEY:-}}"
if [ -z "$SECRET" ]; then
  echo "FATAL: SKYGATE_JWT_SECRET not set" >&2
  exit 1
fi
TOK=$(SKYGATE_JWT_SECRET="$SECRET" "$GO_BIN" run ./cmd/jwt-mint 2>&1)
if [ -z "$TOK" ]; then
  echo "FATAL: could not mint JWT" >&2
  exit 1
fi
echo "  issued JWT (1h TTL, length ${#TOK})"
echo ""

# Restart skygate with the B220 binary.
echo "=== Step 2: restart skygate with B220 binary ==="
SKYGATE_CONTAINER="skygate-skygate-1"
docker stop "$SKYGATE_CONTAINER" 2>&1 | tail -1
sudo -n cp "$SKYGATE_BIN" /home/skyadmin/skygate/skygate
docker start "$SKYGATE_CONTAINER" 2>&1 | tail -1
sleep 30
for i in $(seq 1 30); do
  if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:8080/healthz"; then
    echo "  /healthz OK after ${i}s"
    break
  fi
  sleep 2
done
echo ""

echo "=== Step 3: POST /admin/database/failover/rollback with no prior state ==="
echo "  (guard rail: the rollback should refuse because there's nothing to roll back to)"
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' \
  -X POST \
  -b "skygate_session=${TOK}" \
  -d "candidate=test-b220-rollback-candidate" \
  -d "reason=B220 live-verify test (no prior state)" \
  "http://127.0.0.1:8080/admin/database/failover/rollback" 2>&1)
echo "  POST → 303 redirect to: $LOC"
if echo "$LOC" | grep -q "no+last+failover+recorded"; then
  echo "  [ok]   guard rail fired: 'no last failover recorded' error"
  GUARD_OK=1
else
  echo "  [FAIL] expected 'no last failover recorded' error, got: $LOC"
  GUARD_OK=0
fi
echo ""

echo "=== Step 4: insert a fake db.last_failover state (simulating the B219 success path) ==="
FAKE_OLD="pg-old-primary-test-b220"
FAKE_NEW="pg-new-primary-test-b220"
FAKE_TS=$(date +%s)
$DSN_RUN -c "DELETE FROM global_settings WHERE key = 'db.last_failover'" > /dev/null
$DSN_RUN -c "INSERT INTO global_settings (key, value, updated_at) VALUES ('db.last_failover', '{\"old\":\"$FAKE_OLD\",\"new\":\"$FAKE_NEW\",\"ts\":$FAKE_TS,\"operator\":\"skyadmin\",\"reason\":\"B220 live-verify fake\"}', $FAKE_TS)" > /dev/null
echo "  inserted fake state: old=$FAKE_OLD new=$FAKE_NEW ts=$FAKE_TS"
PRE_ROLLBACK_AUDIT2=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action LIKE 'db.failover_rollback%'")
echo ""

echo "=== Step 5: POST /admin/database/failover/rollback (FAILURE path) ==="
LOC2=$(curl -s -o /dev/null -w '%{redirect_url}' \
  -X POST \
  -b "skygate_session=${TOK}" \
  -d "candidate=$FAKE_OLD" \
  -d "reason=B220 live-verify test (Patroni not configured)" \
  "http://127.0.0.1:8080/admin/database/failover/rollback" 2>&1)
echo "  POST → 303 redirect to: $LOC2"
if echo "$LOC2" | grep -q "/admin/database"; then
  echo "  [ok]   redirect to /admin/database"
  REDIRECT_OK=1
else
  echo "  [FAIL] unexpected redirect: $LOC2"
  REDIRECT_OK=0
fi
if echo "$LOC2" | grep -q "err="; then
  echo "  [ok]   redirect carries err= flash (Patroni call failed as expected)"
  ERR_OK=1
else
  echo "  [FAIL] redirect missing err= param (handler didn't surface Patroni error)"
  ERR_OK=0
fi
echo ""

echo "=== Step 6: assert B220 behavior on the FAILURE path ==="
# 6a. db.failover_rollback.error audit row was written
NEW_ROLLBACK_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action LIKE 'db.failover_rollback%'")
DELTA=$((NEW_ROLLBACK_AUDIT - PRE_ROLLBACK_AUDIT2))
echo "  post-rollback db.failover_rollback* audit rows: $NEW_ROLLBACK_AUDIT (delta: $DELTA)"
if [ "$DELTA" -ge 1 ]; then
  echo "  [ok]   db.failover_rollback.error audit row written"
  AUDIT_OK=1
  # Also confirm the action is db.failover_rollback.error
  # (not db.failover_rollback which would be a SUCCESS).
  ERROR_AUDIT_COUNT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action = 'db.failover_rollback.error'")
  if [ "${ERROR_AUDIT_COUNT:-0}" -ge 1 ]; then
    echo "  [ok]   db.failover_rollback.error action verified (not db.failover_rollback)"
    ERROR_ACTION_OK=1
  else
    echo "  [FAIL] no db.failover_rollback.error row (got $DELTA new rows but none with action='db.failover_rollback.error')"
    ERROR_ACTION_OK=0
  fi
else
  echo "  [FAIL] expected at least 1 db.failover_rollback* audit row, got delta=$DELTA"
  AUDIT_OK=0
  ERROR_ACTION_OK=0
fi

# 6b. db.last_failover state is NOT cleared (operator should be able to retry)
STATE_AFTER=$($DSN_RUN -c "SELECT count(*) FROM global_settings WHERE key = 'db.last_failover'")
echo "  post-rollback db.last_failover present: $STATE_AFTER"
if [ "$STATE_AFTER" = "1" ]; then
  echo "  [ok]   db.last_failover state preserved (not cleared on failure — operator can retry)"
  STATE_OK=1
else
  echo "  [FAIL] db.last_failover state was cleared despite Patroni failure (should preserve for retry)"
  STATE_OK=0
fi
echo ""

echo "=== Step 7: cleanup ==="
$DSN_RUN -c "DELETE FROM global_settings WHERE key = 'db.last_failover'" > /dev/null
echo "  removed fake db.last_failover state"
# Drop the db.failover_rollback.error audit rows we
# created (keep the audit log clean for future
# live-verifies).
$DSN_RUN -c "DELETE FROM audit_log WHERE action LIKE 'db.failover_rollback%' AND id > $PRE_ROLLBACK_AUDIT2" > /dev/null
echo "  removed test audit rows"
echo ""

echo "=== Step 8: final summary ==="
if [ "$GUARD_OK" = "1" ] && [ "$REDIRECT_OK" = "1" ] && [ "$ERR_OK" = "1" ] && [ "$AUDIT_OK" = "1" ] && [ "$ERROR_ACTION_OK" = "1" ] && [ "$STATE_OK" = "1" ]; then
  echo "  B220 LIVE-VERIFY: PASS (FAILURE path)"
  echo "    - guard rail fired (no last failover recorded) ✓"
  echo "    - 303 redirect to /admin/database with err= flash ✓"
  echo "    - db.failover_rollback.error audit row written ✓"
  echo "    - db.last_failover state preserved on failure (retry possible) ✓"
else
  echo "  B220 LIVE-VERIFY: PARTIAL"
  echo "    guard:       $GUARD_OK"
  echo "    redirect:    $REDIRECT_OK"
  echo "    err= flash:  $ERR_OK"
  echo "    audit row:   $AUDIT_OK"
  echo "    err action:  $ERROR_ACTION_OK"
  echo "    state:       $STATE_OK"
  exit 1
fi
echo ""
echo "=== B220 live-verify DONE ==="
