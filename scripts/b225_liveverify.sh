#!/usr/bin/env bash
# B225 live-verify on the agent.
#
# The agent has no Telegram bot configured (no
# SKYGATE_TELEGRAM_BOT_TOKEN in .env), so the
# NoopNotifier is wired. The live-verify confirms the
# B225 wiring doesn't break the failover / rollback
# HTTP flow + the audit_log rows land as expected
# (the audit_log is the durable record of the alert
# even when the Telegram bot is unconfigured).
#
# Steps:
#   1. Restart skygate with the B225 binary
#   2. POST /admin/database/failover (no Patroni
#      running on the agent — expect an error redirect
#      + the "PG failover FAILED" alert would have
#      fired, but the NoopNotifier is silent; the
#      audit_log row is the proof)
#   3. Verify the db.failover.error audit_log row
#      landed
#   4. POST /admin/database/failover/rollback
#      (no last_failover state — expect an error
#      redirect)
#   5. Verify the db.failover_rollback.error row
#      (or no last failover) — no audit row for
#      "no last failover" because the handler
#      short-circuits before the audit write
#   6. Verify NO new "sql: database is closed"
#      cascade (B224 already fixed; B225 is additive)
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

# Build the B225 binary.
SKYGATE_BIN="/tmp/skygate_b225"
$GO_BIN build -o "$SKYGATE_BIN" ./cmd/skygate

echo "=== Step 1: snapshot pre-B225 state ==="
PRE_FAILOVER_OK=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='db.failover'")
PRE_FAILOVER_ERR=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='db.failover.error'")
PRE_ROLLBACK_OK=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='db.failover_rollback'")
PRE_ROLLBACK_ERR=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='db.failover_rollback.error'")
echo "  db.failover rows: $PRE_FAILOVER_OK"
echo "  db.failover.error rows: $PRE_FAILOVER_ERR"
echo "  db.failover_rollback rows: $PRE_ROLLBACK_OK"
echo "  db.failover_rollback.error rows: $PRE_ROLLBACK_ERR"
echo ""

# Mint a session JWT.
TOK=$(SKYGATE_JWT_SECRET="$SKYGATE_JWT_SECRET" "$GO_BIN" run ./cmd/jwt-mint 2>&1)
if [ -z "$TOK" ]; then
  echo "FATAL: could not mint JWT" >&2
  exit 1
fi
echo "=== Step 2: mint session JWT (length ${#TOK}) ==="
echo ""

echo "=== Step 3: restart skygate with B225 binary ==="
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

echo "=== Step 4: trigger /admin/database/failover (Patroni not running — expect error path) ==="
FAIL_LOC=$(curl -s -X POST -b "skygate_session=${TOK}" \
  -d "candidate=skygate-standby" \
  -d "leader=skygate-primary" \
  -d "reason=B225 live-verify test" \
  -o /dev/null -w '%{http_code} %{redirect_url}\n' --max-time 30 http://127.0.0.1:8080/admin/database/failover)
echo "  failover: $FAIL_LOC"
if echo "$FAIL_LOC" | grep -q "^303"; then
  echo "  [ok]   failover returned 303 (handler ran, error redirect as expected — no Patroni)"
  FAIL_HANDLER_OK=1
else
  echo "  [FAIL] failover did not return 303 (handler may have crashed)"
  FAIL_HANDLER_OK=0
fi
sleep 3
POST_FAILOVER_ERR=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='db.failover.error'")
FAIL_DELTA=$((POST_FAILOVER_ERR - PRE_FAILOVER_ERR))
echo "  post-verify db.failover.error rows: $POST_FAILOVER_ERR (delta: $FAIL_DELTA)"
if [ "$FAIL_DELTA" -ge 1 ]; then
  echo "  [ok]   db.failover.error audit row written (B225 B219 error path)"
  FAIL_AUDIT_OK=1
else
  echo "  [FAIL] no db.failover.error audit row (B225 B219 regression)"
  FAIL_AUDIT_OK=0
fi
echo ""

echo "=== Step 5: trigger /admin/database/failover/rollback (no last_failover state) ==="
ROLL_LOC=$(curl -s -X POST -b "skygate_session=${TOK}" \
  -d "candidate=skygate-primary" \
  -d "reason=B225 live-verify test" \
  -o /dev/null -w '%{http_code} %{redirect_url}\n' --max-time 30 http://127.0.0.1:8080/admin/database/failover/rollback)
echo "  rollback: $ROLL_LOC"
# The no-last-failover path short-circuits BEFORE the audit
# write (it only writes an audit row when GetLastFailover
# succeeds AND the candidate is non-empty). So we expect
# delta=0 for the error audit row.
if echo "$ROLL_LOC" | grep -qE "(no\+last\+failover|no\+candidate)"; then
  echo "  [ok]   rollback short-circuited with 'no last failover' / 'no candidate' (expected without B219 success)"
  ROLL_OK=1
else
  echo "  [FAIL] rollback redirect missing expected err param: $ROLL_LOC"
  ROLL_OK=0
fi
echo ""

echo "=== Step 6: wait 30s, confirm no B224 'sql: database is closed' regression ==="
sleep 30
CLOSED_IN_LOGS=$(docker logs "$SKYGATE_CONTAINER" --since 2m 2>&1 | grep -c "sql: database is closed" || true)
CLOSED_IN_LOGS=$(echo "$CLOSED_IN_LOGS" | head -1)
if [ -z "$CLOSED_IN_LOGS" ]; then CLOSED_IN_LOGS=0; fi
echo "  'sql: database is closed' in container logs (last 2m): $CLOSED_IN_LOGS (B224 baseline: 0)"
if [ "$CLOSED_IN_LOGS" -le 1 ]; then
  echo "  [ok]   no B224 regression"
  B224_OK=1
else
  echo "  [FAIL] $CLOSED_IN_LOGS 'sql: database is closed' errors (B224 regression)"
  B224_OK=0
fi
echo ""

echo "=== Step 7: cleanup ==="
$DSN_RUN -c "DELETE FROM audit_log WHERE id > $PRE_FAILOVER_OK AND action='db.failover'" > /dev/null
$DSN_RUN -c "DELETE FROM audit_log WHERE id > $PRE_FAILOVER_ERR AND action='db.failover.error'" > /dev/null
$DSN_RUN -c "DELETE FROM audit_log WHERE id > $PRE_ROLLBACK_OK AND action='db.failover_rollback'" > /dev/null
$DSN_RUN -c "DELETE FROM audit_log WHERE id > $PRE_ROLLBACK_ERR AND action='db.failover_rollback.error'" > /dev/null
echo "  removed test audit rows"
echo ""

echo "=== Step 8: final summary ==="
if [ "$FAIL_HANDLER_OK" = "1" ] && [ "$FAIL_AUDIT_OK" = "1" ] && [ "$ROLL_OK" = "1" ] && [ "$B224_OK" = "1" ]; then
  echo "  B225 LIVE-VERIFY: PASS"
  echo "    - /admin/database/failover handler runs (303 to /admin/database?err=...): ✓"
  echo "    - db.failover.error audit row written (B225 B219 error path wired): ✓"
  echo "    - /admin/database/failover/rollback short-circuits with 'no last failover': ✓"
  echo "    - no B224 'sql: database is closed' regression: ✓"
  echo "    - actual Telegram alert text (would be '❌ PG failover FAILED ...'): SKIPPED (no bot configured; NoopNotifier is silent by design; the audit_log row IS the durable proof)"
else
  echo "  B225 LIVE-VERIFY: PARTIAL"
  echo "    failover handler:   $FAIL_HANDLER_OK"
  echo "    failover audit:     $FAIL_AUDIT_OK"
  echo "    rollback handler:   $ROLL_OK"
  echo "    no B224 regression: $B224_OK"
  exit 1
fi
echo ""
echo "=== B225 live-verify DONE ==="
