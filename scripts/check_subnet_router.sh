#!/bin/bash
# check_subnet_router.sh — operator-side health check for a per-user
# subnet-router deployment. Run this on the skygate host to verify:
#   1. The user has a subnet allocated in `user_subnets`
#   2. The denormalised portal_users row matches
#   3. A live node with tag:subnet-router exists in headscale
#   4. The 10.0.<uid>.0/24 route is approved on that node
#   5. The status pill in the UI will show 'router_active'
#
# Usage: check_subnet_router.sh <portal-user-id-or-username>
#        check_subnet_router.sh admin
#        check_subnet_router.sh 1
#
# Returns 0 if everything is green, 1 if any check fails.
# Each check prints a [OK] or [FAIL] tag so the output is
# easy to grep / monitor.

set -e

if [ -z "$1" ]; then
  echo "usage: $0 <portal-user-id-or-username>"
  echo "  examples:"
  echo "    $0 admin"
  echo "    $0 user1"
  echo "    $0 1"
  exit 2
fi

cd /home/admin/skygate
USER_INPUT="$1"

# If the input is a number, use it as user_id; otherwise resolve
# the username to user_id via the skygate DB.
if [[ "$USER_INPUT" =~ ^[0-9]+$ ]]; then
  USER_ID="$USER_INPUT"
else
  USER_ID=$(docker exec skygate sqlite3 /data/skygate.db \
    "SELECT id FROM portal_users WHERE username='$USER_INPUT' LIMIT 1;" 2>/dev/null)
  if [ -z "$USER_ID" ]; then
    echo "[FAIL] no portal user named '$USER_INPUT'"
    exit 1
  fi
fi

# Make sure sqlite3 is in the skygate container (transient
# install; gets lost on every container restart, hence the
# silent re-install on every run).
docker exec skygate apk add --no-cache sqlite >/dev/null 2>&1 || true

echo "=== Check 1: user_subnets row for user_id=$USER_ID ==="
SUBNET=$(docker exec skygate sqlite3 /data/skygate.db \
  "SELECT cidr, status, router_hostname, router_node_id FROM user_subnets WHERE user_id=$USER_ID;")
if [ -z "$SUBNET" ]; then
  echo "[FAIL] no user_subnets row for user_id=$USER_ID"
  exit 1
fi
echo "  $SUBNET"
CIDR=$(echo "$SUBNET" | cut -d'|' -f1)
STATUS=$(echo "$SUBNET" | cut -d'|' -f2)
ROUTER_HOSTNAME=$(echo "$SUBNET" | cut -d'|' -f3)
ROUTER_NODE_ID=$(echo "$SUBNET" | cut -d'|' -f4)
echo "  CIDR=$CIDR  STATUS=$STATUS  ROUTER=$ROUTER_HOSTNAME  NODE_ID=$ROUTER_NODE_ID"

echo ""
echo "=== Check 2: denormalised portal_users row ==="
DENORM=$(docker exec skygate sqlite3 /data/skygate.db \
  "SELECT username, subnet_cidr, subnet_status, subnet_router_node_id FROM portal_users WHERE id=$USER_ID;")
echo "  $DENORM"
DENORM_STATUS=$(echo "$DENORM" | cut -d'|' -f3)
if [ "$DENORM_STATUS" != "$STATUS" ]; then
  echo "[WARN] denorm status ($DENORM_STATUS) != user_subnets status ($STATUS) — load /my/devices to trigger SyncStatus"
fi

echo ""
echo "=== Check 3: headscale node with tag:subnet-router for this user ==="
if [ -z "$ROUTER_NODE_ID" ] || [ "$ROUTER_NODE_ID" = "" ]; then
  echo "[FAIL] no router_node_id set — the sidecar's SyncOnce hasn't seen a tag:subnet-router node yet"
  echo "       (or the registered node has been deleted from headscale)"
  echo ""
  echo "  current headscale nodes with tag:subnet-router:"
  docker exec headscale headscale nodes list -o json 2>/dev/null > /tmp/hs-nodes.json
  python3 /home/admin/skygate/scripts/_check_subnet_nodes.py --list-with-tag
  exit 1
fi

# Query the headscale node directly. Write the python to a
# file to avoid shell-quoting headaches with `$ROUTER_NODE_ID`
# + `$CIDR` interpolation in `python3 -c`.
docker exec headscale headscale nodes list -o json 2>/dev/null > /tmp/hs-nodes.json
python3 /home/admin/skygate/scripts/_check_subnet_nodes.py \
  --node-id "$ROUTER_NODE_ID" --cidr "$CIDR" --json-file /tmp/hs-nodes.json || exit 1

echo ""
echo "=== Check 4: status pill in /admin/users/$USER_ID/subnet ==="
COOKIE=/tmp/sgck_check.txt
PASSWORD=$(grep '^SKYGATE_ADMIN_PASS' .env | cut -d= -f2-)
rm -f $COOKIE
curl -sS -c $COOKIE -X POST http://localhost:8080/login \
  --data-urlencode "username=admin" \
  --data-urlencode "password=$PASSWORD" \
  -o /dev/null -w "  login: HTTP %{http_code}\n"
curl -sS -b $COOKIE "http://localhost:8080/admin/users/$USER_ID/subnet" \
  -o /tmp/subnet-page.html -w "  /admin/users/$USER_ID/subnet: HTTP %{http_code}\n"
PILL=$(grep -oE 'status[^"]*"[^"]*"|cell_[a-z_]+' /tmp/subnet-page.html | sort -u | head -3)
echo "  status pill text fragments: $PILL"

echo ""
echo "=== Check 5: latest audit events ==="
docker exec skygate sqlite3 /data/skygate.db \
  "SELECT datetime(created_at, 'unixepoch'), action, substr(detail,1,60) FROM audit_log \
    WHERE (action LIKE 'subnet%' OR action LIKE 'sidecar%' OR username='sidecar') \
    ORDER BY id DESC LIMIT 5;"

echo ""
echo "=== Summary ==="
echo "  user_id=$USER_ID  cidr=$CIDR  status=$STATUS"
case "$STATUS" in
  router_active)
    echo "  [OK] status is router_active — subnet-router is live and the route is approved"
    exit 0
    ;;
  active)
    echo "  [WARN] status is active (devices exist, but no router). User has not yet run setup.sh."
    exit 0
    ;;
  pending)
    echo "  [WARN] status is pending (no devices in tailnet). User needs to install Tailscale first."
    exit 0
    ;;
  *)
    echo "  [FAIL] status is $STATUS — expected router_active, active, or pending"
    exit 1
    ;;
esac
