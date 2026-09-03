#!/usr/bin/env bash
# B221 live-verify on the agent.
#
# Exercises the full B221 surface:
#   1. V067 migration auto-applies on skygate boot
#      (the OpenPostgres → MigratePostgres flow added
#      in v0.33.1). Confirm target_type + target_id
#      columns exist on audit_log.
#   2. AppendAuditLogWithTarget is exercised end-to-end
#      via the existing /admin/cluster/invite/generate
#      endpoint (B200 handler, B221-writer). The audit
#      row is verified to have non-empty target_type +
#      target_id.
#   3. /admin/audit page renders without crashing (the
#      pre-B221 7-column SELECT+Scan was a latent bug
#      that would have crashed the moment the audit_log
#      branch started projecting 10 columns).
#   4. Cleanup: the generated invite + audit row are
#      removed.
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
# bash tries to execute PGPASSWORD=... as a command).
DSN_RUN="env PGPASSWORD=skygate_admin_pass psql -h 172.17.0.1 -p 5433 -U admin -d $DB -tA"

# Build the B221 binary.
SKYGATE_BIN="/tmp/skygate_b221"
$GO_BIN build -o "$SKYGATE_BIN" ./cmd/skygate

echo "=== Step 1: snapshot pre-B221 state ==="
PRE_HAS_TARGET_TYPE=$($DSN_RUN -c "SELECT count(*) FROM information_schema.columns WHERE table_name='audit_log' AND column_name='target_type'")
PRE_HAS_TARGET_ID=$($DSN_RUN -c "SELECT count(*) FROM information_schema.columns WHERE table_name='audit_log' AND column_name='target_id'")
PRE_INVITE_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='cluster.invite.generate'")
PRE_INVITE_ROWS=$($DSN_RUN -c "SELECT count(*) FROM cluster_invite")
echo "  audit_log.target_type exists: $PRE_HAS_TARGET_TYPE (want 0 — V067 not yet applied)"
echo "  audit_log.target_id   exists: $PRE_HAS_TARGET_ID"
echo "  cluster.invite.generate audit rows: $PRE_INVITE_AUDIT"
echo "  cluster_invite rows: $PRE_INVITE_ROWS"
echo ""

echo "=== Step 2: restart skygate with B221 binary (auto-applies V067) ==="
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

echo "=== Step 3: assert V067 migration applied + new columns present ==="
POST_HAS_TARGET_TYPE=$($DSN_RUN -c "SELECT count(*) FROM information_schema.columns WHERE table_name='audit_log' AND column_name='target_type'")
POST_HAS_TARGET_ID=$($DSN_RUN -c "SELECT count(*) FROM information_schema.columns WHERE table_name='audit_log' AND column_name='target_id'")
POST_COL_DEFAULTS=$($DSN_RUN -c "SELECT column_default FROM information_schema.columns WHERE table_name='audit_log' AND column_name='target_type'")
echo "  audit_log.target_type exists: $POST_HAS_TARGET_TYPE (want 1)"
echo "  audit_log.target_id   exists: $POST_HAS_TARGET_ID (want 1)"
echo "  target_type default: $POST_COL_DEFAULTS (want empty string default)"
if [ "$POST_HAS_TARGET_TYPE" = "1" ] && [ "$POST_HAS_TARGET_ID" = "1" ]; then
  echo "  [ok]   V067 migration applied successfully"
  MIGRATION_OK=1
else
  echo "  [FAIL] V067 migration did not add the columns"
  MIGRATION_OK=0
fi
echo ""

echo "=== Step 4: assert pre-existing rows have target_type=target_id='' (DEFAULT) ==="
# These are rows that existed BEFORE B221 ran. The DEFAULT '' applies
# to them — visible in /admin/audit as "—".
PRE_EXISTING_EMPTY=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE (target_type IS NULL OR target_type='') AND (target_id IS NULL OR target_id='') AND id < (SELECT COALESCE(MAX(id),0) FROM audit_log WHERE target_type='cluster_invite')")
echo "  pre-existing rows with empty target: $PRE_EXISTING_EMPTY (want > 0 — B0/B195 era rows)"
if [ "${PRE_EXISTING_EMPTY:-0}" -gt 0 ]; then
  echo "  [ok]   pre-existing rows visible as '—' in /admin/audit"
  BACKWARD_OK=1
else
  echo "  [FAIL] no pre-existing rows with empty target (DEFAULT did not apply)"
  BACKWARD_OK=0
fi
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
echo "=== Step 5: mint session JWT ==="
echo "  issued JWT (1h TTL, length ${#TOK})"
echo ""

echo "=== Step 6: trigger /admin/cluster/invite/generate (exercises AppendAuditLogWithTarget) ==="
TEST_HOSTNAME="b221-liveverify-test"
LOC=$(curl -s -o /dev/null -w '%{redirect_url}' \
  -X POST \
  -b "skygate_session=${TOK}" \
  -d "target_hostname=${TEST_HOSTNAME}" \
  -d "role=skygate-standby" \
  -d "ttl_hours=1" \
  "http://127.0.0.1:8080/admin/cluster/invite/generate" 2>&1)
echo "  POST → 303 redirect to: $LOC"
if echo "$LOC" | grep -q "ok="; then
  echo "  [ok]   invite generated (302/303 with ok= flash)"
  INVITE_OK=1
else
  echo "  [FAIL] expected ok= flash, got: $LOC"
  INVITE_OK=0
fi
echo ""

echo "=== Step 7: assert the new audit_log row has target_type='cluster_invite' + non-empty target_id ==="
NEW_INVITE_AUDIT=$($DSN_RUN -c "SELECT count(*) FROM audit_log WHERE action='cluster.invite.generate' AND target_type='cluster_invite' AND target_id != ''")
NEW_INVITE_AUDIT_DELTA=$((NEW_INVITE_AUDIT - PRE_INVITE_AUDIT))
echo "  post-verify cluster.invite.generate with target_type='cluster_invite' AND target_id != '': $NEW_INVITE_AUDIT (delta: $NEW_INVITE_AUDIT_DELTA)"
if [ "$NEW_INVITE_AUDIT_DELTA" -ge 1 ]; then
  echo "  [ok]   AppendAuditLogWithTarget wrote the structured target"
  AUDIT_ROW_OK=1
  # Show the actual target_type + target_id values
  LATEST_TARGET=$($DSN_RUN -c "SELECT target_type || ':' || target_id FROM audit_log WHERE action='cluster.invite.generate' ORDER BY id DESC LIMIT 1")
  echo "  latest row target: $LATEST_TARGET"
else
  echo "  [FAIL] no cluster.invite.generate audit row with structured target (delta=$NEW_INVITE_AUDIT_DELTA)"
  AUDIT_ROW_OK=0
fi
echo ""

echo "=== Step 8: GET /admin/audit (the pre-B221 7-column Scan was a latent bug) ==="
AUDIT_HTML=$(curl -s -b "skygate_session=${TOK}" "http://127.0.0.1:8080/admin/audit" 2>&1)
if echo "$AUDIT_HTML" | grep -q "cluster.invite.generate"; then
  echo "  [ok]   /admin/audit page rendered with the new cluster_invite row"
  PAGE_OK=1
else
  echo "  [FAIL] /admin/audit page did not contain cluster.invite.generate row"
  PAGE_OK=0
fi
if echo "$AUDIT_HTML" | grep -q "cluster_invite:"; then
  echo "  [ok]   /admin/audit renders the combined 'cluster_invite:<id>' target"
  TARGET_OK=1
else
  echo "  [FAIL] /admin/audit missing the combined 'cluster_invite:<id>' target"
  TARGET_OK=0
fi
echo ""

echo "=== Step 9: cleanup ==="
# Remove the test invite row + audit row we created
TEST_INVITE_ID=$($DSN_RUN -c "SELECT id FROM cluster_invite WHERE target_hostname='${TEST_HOSTNAME}' ORDER BY id DESC LIMIT 1")
if [ -n "$TEST_INVITE_ID" ]; then
  $DSN_RUN -c "DELETE FROM cluster_invite WHERE id='$TEST_INVITE_ID'" > /dev/null
  echo "  removed cluster_invite id=$TEST_INVITE_ID"
fi
$DSN_RUN -c "DELETE FROM audit_log WHERE action='cluster.invite.generate' AND id > $PRE_INVITE_AUDIT" > /dev/null
echo "  removed test audit rows"
echo ""

echo "=== Step 10: final summary ==="
if [ "$MIGRATION_OK" = "1" ] && [ "$BACKWARD_OK" = "1" ] && [ "$INVITE_OK" = "1" ] && [ "$AUDIT_ROW_OK" = "1" ] && [ "$PAGE_OK" = "1" ] && [ "$TARGET_OK" = "1" ]; then
  echo "  B221 LIVE-VERIFY: PASS"
  echo "    - V067 migration applied (target_type + target_id columns exist) ✓"
  echo "    - pre-existing rows show empty target (DEFAULT '' applied) ✓"
  echo "    - AppendAuditLogWithTarget wrote cluster_invite:<id> ✓"
  echo "    - /admin/audit page renders without crashing ✓"
  echo "    - combined 'cluster_invite:<id>' target visible in the page ✓"
else
  echo "  B221 LIVE-VERIFY: PARTIAL"
  echo "    migration:        $MIGRATION_OK"
  echo "    backward compat:  $BACKWARD_OK"
  echo "    invite ok flash:  $INVITE_OK"
  echo "    audit row:        $AUDIT_ROW_OK"
  echo "    /admin/audit:     $PAGE_OK"
  echo "    target visible:   $TARGET_OK"
  exit 1
fi
echo ""
echo "=== B221 live-verify DONE ==="
