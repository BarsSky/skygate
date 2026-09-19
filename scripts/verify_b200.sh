#!/bin/sh
. "$(dirname "$0")/lib/db_credentials.sh"
SKYGATE_DB_PASSWORD="${SKYGATE_DB_PASSWORD:-$(skygate_db_password)}"
# verify_b200.sh — live post-deploy verification.
# Run from the agent with a valid admin session cookie.
COOKIE="${SKYGATE_COOKIE:?set SKYGATE_COOKIE}"
BASE="http://127.0.0.1:8080"

echo "=== healthz ==="
curl -s -o /dev/null -w "  GET /healthz = %{http_code}\n" $BASE/healthz

echo "=== GET /admin/cluster (B200 page should have forms) ==="
curl -s -b "skygate_session=$COOKIE" -o /tmp/cluster.html -w "  GET /admin/cluster = %{http_code}, %{size_download} bytes\n" $BASE/admin/cluster
echo "  Add node form:"; grep -c 'admin/cluster/node/add' /tmp/cluster.html
echo "  Generate invite form:"; grep -c 'admin/cluster/invite/generate' /tmp/cluster.html
echo "  Card sections (expect 5):"; grep -oE 'class="card"' /tmp/cluster.html | wc -l
echo "  No template errors:"; grep -c 'no such template' /tmp/cluster.html

echo "=== POST /admin/cluster/invite/generate ==="
curl -s -b "skygate_session=$COOKIE" -X POST \
  -d 'role=skygate-standby&target_hostname=svi-1&ttl_hours=24' \
  -i -o /tmp/invite_resp.txt -w "  POST invite/generate = %{http_code}\n" \
  $BASE/admin/cluster/invite/generate
echo "  Location header:"; grep -i '^Location:' /tmp/invite_resp.txt | head -1
echo "  Cookie set?"; grep -i 'Set-Cookie' /tmp/invite_resp.txt | head -1

echo "=== verify invite in DB ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -c \
  "SELECT id, role, target_hostname, status FROM cluster_invite ORDER BY issued_at DESC LIMIT 3;"

echo "=== POST /admin/cluster/node/add ==="
curl -s -b "skygate_session=$COOKIE" -X POST \
  -d 'hostname=svi-1&tailscale_ip=100.64.0.24&roles=skygate-standby,patroni-replica&skygate_version=v1.5.2-test' \
  -o /tmp/node_resp.txt -w "  POST node/add = %{http_code}\n" \
  $BASE/admin/cluster/node/add
echo "  Location header:"; grep -i '^Location:' /tmp/node_resp.txt | head -1

echo "=== verify node in DB ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -c \
  "SELECT id, hostname, tailscale_ip, roles, state FROM cluster_node ORDER BY hostname;"

echo "=== verify GET /admin/cluster now shows the new row ==="
curl -s -b "skygate_session=$COOKIE" -o /tmp/cluster_after.html $BASE/admin/cluster
echo "  svi-1 in page:"; grep -c 'svi-1' /tmp/cluster_after.html
echo "  skygate-standby role badge:"; grep -c 'skygate-standby' /tmp/cluster_after.html
echo "  pending invite visible:"; grep -c 'cluster_invite\|Invite' /tmp/cluster_after.html

echo "=== POST /admin/cluster/invite/revoke (the one we just made) ==="
INV_ID=$(PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
  "SELECT id FROM cluster_invite WHERE status='pending' ORDER BY issued_at DESC LIMIT 1;")
echo "  Revoking invite id=$INV_ID"
if [ -n "$INV_ID" ]; then
  curl -s -b "skygate_session=$COOKIE" -X POST \
    -d "invite_id=$INV_ID" \
    -o /tmp/revoke_resp.txt -w "  POST invite/revoke = %{http_code}\n" \
    $BASE/admin/cluster/invite/revoke
  echo "  Post-revoke status in DB:"
  PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -c \
    "SELECT id, status FROM cluster_invite WHERE id='$INV_ID';"
fi

echo "=== POST /admin/cluster/node/remove (the svi-1 we just added) ==="
curl -s -b "skygate_session=$COOKIE" -X POST \
  -d 'hostname=svi-1' \
  -o /tmp/remove_resp.txt -w "  POST node/remove = %{http_code}\n" \
  $BASE/admin/cluster/node/remove
echo "  Post-remove rows in DB:"
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -c \
  "SELECT count(*) AS remaining_nodes FROM cluster_node;"

echo "=== audit log (last 5 cluster.* actions) ==="
PGPASSWORD=${SKYGATE_DB_PASSWORD} psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT username, action, detail, created_at FROM audit_log WHERE action LIKE 'cluster.%' ORDER BY id DESC LIMIT 5;"
