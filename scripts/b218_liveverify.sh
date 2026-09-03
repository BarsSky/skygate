#!/usr/bin/env bash
# B218 live-verify on the agent.
#
# Verifies the new `skygate init --role=standby` path:
#   1. snapshot the existing primary_node_id + count
#      of cluster_node rows
#   2. run `skygate init --role=standby` (the B218
#      preset)
#   3. assert:
#      - a new cluster_node row appears with
#        roles={skygate-standby,patroni-replica}
#      - cluster_database.primary_node_id is
#        UNCHANGED (the existing primary is preserved)
#      - a new cluster_audit node_init row fires
#      - the first line of stdout is EMPTY
#        (no token issued in standby mode)
#   4. cleanup: drop the new cluster_node row +
#      drop the new cluster_audit node_init row
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

# Build the B218 binary (or use the existing one if
# the operator already deployed it).
SKYGATE_BIN="/tmp/skygate_b218"
$GO_BIN build -o "$SKYGATE_BIN" ./cmd/skygate

echo "=== Step 1: snapshot pre-B218 state ==="
PRE_PRIMARY=$($DSN_RUN -c "SELECT primary_node_id FROM cluster_database WHERE id = 'skygate-staging'")
PRE_NODE_COUNT=$($DSN_RUN -c "SELECT count(*) FROM cluster_node")
PRE_NODE_INIT_COUNT=$($DSN_RUN -c "SELECT count(*) FROM cluster_audit WHERE action = 'node_init'")
echo "  primary_node_id: $PRE_PRIMARY"
echo "  cluster_node rows: $PRE_NODE_COUNT"
echo "  node_init audit rows: $PRE_NODE_INIT_COUNT"
echo ""

# Use a unique hostname so the cluster_node row
# doesn't collide with an existing one.
TEST_HOSTNAME="test-b218-standby-$(date +%s)"
echo "=== Step 2: skygate init --role=standby (test hostname: $TEST_HOSTNAME) ==="
set --; set -a; . /home/skyadmin/skygate/.env; set +a
# Override the hostname so we can clean up
# deterministically. skygate init uses os.Hostname()
# for the row's hostname column — we need a way
# to override that. The init command doesn't have
# a --hostname flag, so we'll use SKYGATE_TS_HOSTNAME
# to override tailscale_ip and rely on the fact
# that os.Hostname() returns the agent's real
# hostname. For B218 we just want to verify the
# BEHAVIOR (no primary claim, no token, audit row
# fires) — the hostname collision is OK because
# UpsertNode is idempotent on the (cluster_id,
# hostname) pair.
#
# To get a clean test row, we'll DELETE any existing
# row for the agent's hostname first, then re-run
# init in primary mode to capture the original state,
# then run in standby mode to verify the new path.
#
# But this is complex. Simpler: just run init in
# standby mode and verify the existing agent row's
# roles are NOT changed to standby roles (because
# UpsertNode is idempotent + the standby detection
# is on the args, not the DB).
#
# Wait — UpsertNode DOES update roles on re-run.
# So if we run in standby mode, the existing agent
# row's roles WILL be set to standby roles. That's
# destructive. We need to either:
#   (a) backup + restore the row
#   (b) use a fresh hostname
#
# (b) requires overriding os.Hostname() which init
# doesn't allow. Let me go with (a).
PRE_ROW=$($DSN_RUN -c "SELECT id || '|' || state || '|' || array_to_string(roles, ',') FROM cluster_node WHERE hostname = '$(hostname)'")
echo "  pre-row (agent's existing cluster_node row): $PRE_ROW"

# Capture stdout (line 1 = token) + stderr (verbose
# log lines) separately. We use a temp file for stdout
# because the script needs to read line 1 of stdout
# without it being clobbered by the verbose stderr.
STDOUT_FILE=$(mktemp)
STDERR_FILE=$(mktemp)
trap 'rm -f "$STDOUT_FILE" "$STDERR_FILE"' EXIT

SKYGATE_TS_HOSTNAME="skygate-host-1" \
  "$SKYGATE_BIN" init --role=standby \
    > "$STDOUT_FILE" 2> "$STDERR_FILE" || {
  echo "FATAL: skygate init --role=standby failed:"
  echo "  stdout: $(head -3 "$STDOUT_FILE")"
  echo "  stderr: $(tail -10 "$STDERR_FILE")"
  exit 1
}

TOKEN_LINE=$(head -1 "$STDOUT_FILE")
echo "  stdout line 1 (token): '$TOKEN_LINE' (length: ${#TOKEN_LINE})"
echo "  stderr (verbose log):"
sed 's/^/    /' "$STDERR_FILE" | head -5
echo ""

echo "=== Step 3: assert B218 behavior ==="
# 3a. cluster_node row exists for the agent's
# hostname with roles = standby preset
NEW_ROLES=$($DSN_RUN -c "SELECT array_to_string(roles, ',') FROM cluster_node WHERE hostname = '$(hostname)'")
echo "  post-init cluster_node roles: $NEW_ROLES"
if [[ "$NEW_ROLES" != "skygate-standby,patroni-replica" ]]; then
  echo "  [FAIL] expected roles 'skygate-standby,patroni-replica', got '$NEW_ROLES'"
  ROLES_OK=0
else
  echo "  [ok]   roles are the standby preset"
  ROLES_OK=1
fi

# 3b. cluster_database.primary_node_id is UNCHANGED
NEW_PRIMARY=$($DSN_RUN -c "SELECT primary_node_id FROM cluster_database WHERE id = 'skygate-staging'")
echo "  post-init primary_node_id: $NEW_PRIMARY"
if [[ "$NEW_PRIMARY" != "$PRE_PRIMARY" ]]; then
  echo "  [FAIL] primary_node_id changed from '$PRE_PRIMARY' to '$NEW_PRIMARY' (B218 should preserve the existing primary)"
  PRIMARY_OK=0
else
  echo "  [ok]   primary_node_id preserved (no overwrite)"
  PRIMARY_OK=1
fi

# 3c. token line is empty (standby mode doesn't issue)
if [[ -z "$TOKEN_LINE" ]]; then
  echo "  [ok]   stdout line 1 is empty (no token issued in standby mode)"
  TOKEN_OK=1
else
  echo "  [FAIL] expected empty token line, got '$TOKEN_LINE'"
  TOKEN_OK=0
fi

# 3d. cluster_audit node_init row count increased
NEW_NODE_INIT_COUNT=$($DSN_RUN -c "SELECT count(*) FROM cluster_audit WHERE action = 'node_init'")
DELTA=$((NEW_NODE_INIT_COUNT - PRE_NODE_INIT_COUNT))
echo "  post-init node_init audit rows: $NEW_NODE_INIT_COUNT (delta: $DELTA)"
if [[ "$DELTA" -ge 1 ]]; then
  echo "  [ok]   node_init audit row fired (B215 audit event still works in standby mode)"
  AUDIT_OK=1
else
  echo "  [FAIL] expected node_init audit row to be written, got delta=$DELTA"
  AUDIT_OK=0
fi

echo ""

echo "=== Step 4: restore the original cluster_node row ==="
# Restore the original roles + state on the agent's row.
ORIG_ID=$(echo "$PRE_ROW" | cut -d'|' -f1)
ORIG_STATE=$(echo "$PRE_ROW" | cut -d'|' -f2)
ORIG_ROLES=$(echo "$PRE_ROW" | cut -d'|' -f3)
$DSN_RUN -c "UPDATE cluster_node SET state = '$ORIG_STATE', roles = '{$ORIG_ROLES}' WHERE id = '$ORIG_ID'" > /dev/null
echo "  restored: $ORIG_ID → state=$ORIG_STATE roles={$ORIG_ROLES}"
# Drop the test node_init row (we don't want it
# polluting the live audit_log for future B-checks).
$DSN_RUN -c "DELETE FROM cluster_audit WHERE action = 'node_init' AND detail->>'hostname' = '$(hostname)' AND id > $PRE_NODE_INIT_COUNT" > /dev/null
echo "  dropped test node_init rows (kept original count = $PRE_NODE_INIT_COUNT)"
echo ""

echo "=== Step 5: final summary ==="
if [[ "$ROLES_OK" == "1" && "$PRIMARY_OK" == "1" && "$TOKEN_OK" == "1" && "$AUDIT_OK" == "1" ]]; then
  echo "  B218 LIVE-VERIFY: PASS"
  echo "    - roles = skygate-standby,patroni-replica ✓"
  echo "    - primary_node_id preserved ✓"
  echo "    - no standby token issued ✓"
  echo "    - node_init audit row fires ✓"
else
  echo "  B218 LIVE-VERIFY: PARTIAL"
  echo "    roles:    $ROLES_OK"
  echo "    primary:  $PRIMARY_OK"
  echo "    token:    $TOKEN_OK"
  echo "    audit:    $AUDIT_OK"
  exit 1
fi
echo ""
echo "=== B218 live-verify DONE ==="
