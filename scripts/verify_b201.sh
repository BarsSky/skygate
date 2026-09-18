#!/bin/bash
# verify_b201.sh — end-to-end B200 → B201 test.
COOKIE="${SKYGATE_COOKIE:?set SKYGATE_COOKIE}"
BASE="http://127.0.0.1:8080"

echo "=== 1. Generate TWO invites (one for happy path, one for hostname mismatch) ==="
extract_token() {
  local resp_file="$1"
  local LOCATION
  LOCATION=$(grep -i '^Location:' "$resp_file" | sed 's/^Location: //I' | tr -d '\r' | tr -d '\n')
  echo "$LOCATION" | python3 -c "
import sys, urllib.parse, re
s = sys.stdin.read()
s = urllib.parse.unquote(s)
m = re.search(r'sgn1\.[A-Za-z0-9._-]+', s)
print(m.group(0) if m else '')
"
}
# invite #1: for the happy path + bad-token test + invite-reused test
curl -s -b "skygate_session=$COOKIE" -X POST \
  -d "role=skygate-standby&target_hostname=b201-test-node&ttl_hours=1" \
  -i -o /tmp/invite1.txt -w "  invite #1 = %{http_code}\n" \
  $BASE/admin/cluster/invite/generate
TOKEN=$(extract_token /tmp/invite1.txt)
# invite #2: for the hostname-mismatch test (must be pending
# when we try the wrong hostname).
curl -s -b "skygate_session=$COOKIE" -X POST \
  -d "role=skygate-standby&target_hostname=b201-test-node&ttl_hours=1" \
  -i -o /tmp/invite2.txt -w "  invite #2 = %{http_code}\n" \
  $BASE/admin/cluster/invite/generate
TOKEN2=$(extract_token /tmp/invite2.txt)
if [ -z "$TOKEN" ] || [ "${TOKEN#sgn1.}" = "$TOKEN" ] || [ -z "$TOKEN2" ] || [ "${TOKEN2#sgn1.}" = "$TOKEN2" ]; then
  echo "  ERROR: could not extract tokens"
  exit 1
fi
echo "  token #1 (first 60 chars): ${TOKEN:0:60}..."
echo "  token #2 (first 60 chars): ${TOKEN2:0:60}..."

echo ""
echo "=== 2. POST /api/cluster/join ==="
curl -s -X POST -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"hostname\":\"b201-test-node\",\"tailscale_ip\":\"100.64.0.99\",\"skygate_version\":\"v1.5.2-b201-test\",\"roles\":\"skygate-standby\"}" \
  -i -o /tmp/join_resp.txt -w "  POST /api/cluster/join = %{http_code}\n" \
  $BASE/api/cluster/join
echo "  Response body:"
sed -n '/^{/,/^}$/p' /tmp/join_resp.txt | head -5
NODE_ID=$(sed -n '/^{/,/^}$/p' /tmp/join_resp.txt | python3 -c "import sys, json; d=json.load(sys.stdin); print(d.get('node_id',''))" 2>/dev/null)
echo "  node_id: $NODE_ID"

echo ""
echo "=== 3. Verify cluster_node row in DB ==="
PGPASSWORD=skygate_admin_pass psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT id, hostname, tailscale_ip, roles, state FROM cluster_node WHERE hostname='b201-test-node';"

echo ""
echo "=== 4. POST /api/cluster/heartbeat (1st) ==="
curl -s -X POST -H "Content-Type: application/json" \
  -d "{\"node_id\":\"$NODE_ID\",\"token\":\"$TOKEN\"}" \
  -i -o /tmp/hb1.txt -w "  POST /api/cluster/heartbeat = %{http_code}\n" \
  $BASE/api/cluster/heartbeat
echo "  Response body:"
sed -n '/^{/,/^}$/p' /tmp/hb1.txt | head -5

echo ""
echo "=== 5. Verify state is now ready ==="
PGPASSWORD=skygate_admin_pass psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT hostname, state, last_seen_at FROM cluster_node WHERE hostname='b201-test-node';"

echo ""
echo "=== 6. POST /api/cluster/heartbeat (2nd, idempotent) ==="
curl -s -X POST -H "Content-Type: application/json" \
  -d "{\"node_id\":\"$NODE_ID\",\"token\":\"$TOKEN\"}" \
  -o /tmp/hb2.txt -w "  2nd heartbeat = %{http_code}\n" \
  $BASE/api/cluster/heartbeat
sed -n '/^{/,/^}$/p' /tmp/hb2.txt | head -5

echo ""
echo "=== 7. Error path: empty body (expect 400) ==="
curl -s -X POST -H "Content-Type: application/json" -d '{}' \
  -o /tmp/err_empty.txt -w "  empty body = %{http_code}\n" \
  $BASE/api/cluster/join
cat /tmp/err_empty.txt

echo ""
echo "=== 8. Error path: bad token (expect 401) ==="
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"token":"sgn1.AAAA.BBBB","hostname":"b201-test-node"}' \
  -o /tmp/err_bad.txt -w "  bad token = %{http_code}\n" \
  $BASE/api/cluster/join
cat /tmp/err_bad.txt

echo ""
echo "=== 9. Error path: hostname mismatch (expect 403) — fresh invite #2 ==="
# Use TOKEN2 (still pending) with a wrong hostname.
curl -s -X POST -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN2\",\"hostname\":\"evil-host\"}" \
  -o /tmp/err_host.txt -w "  hostname mismatch = %{http_code}\n" \
  $BASE/api/cluster/join
cat /tmp/err_host.txt

echo ""
echo "=== 10. Error path: invite reused (expect 409) ==="
curl -s -X POST -H "Content-Type: application/json" \
  -d "{\"token\":\"$TOKEN\",\"hostname\":\"b201-test-node\"}" \
  -o /tmp/err_dup.txt -w "  invite reused = %{http_code}\n" \
  $BASE/api/cluster/join
cat /tmp/err_dup.txt

echo ""
echo "=== 11. Audit log: cluster.* + join events ==="
PGPASSWORD=skygate_admin_pass psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT username, action, detail FROM audit_log WHERE action LIKE 'cluster.%' ORDER BY id DESC LIMIT 5;"

echo ""
echo "=== 12. Cleanup: remove the test node + revoke both invites ==="
curl -s -b "skygate_session=$COOKIE" -X POST -d "hostname=b201-test-node" \
  -o /dev/null -w "  node/remove = %{http_code}\n" \
  $BASE/admin/cluster/node/remove
for INV_ID in $(PGPASSWORD=skygate_admin_pass psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -t -A -c \
  "SELECT id FROM cluster_invite WHERE target_hostname='b201-test-node' AND status='pending';"); do
  curl -s -b "skygate_session=$COOKIE" -X POST -d "invite_id=$INV_ID" \
    -o /dev/null -w "  invite/revoke $INV_ID = %{http_code}\n" \
    $BASE/admin/cluster/invite/revoke
done
echo "  Final state:"
PGPASSWORD=skygate_admin_pass psql -h 127.0.0.1 -p 5433 -U admin -d skygate_staging -c \
  "SELECT 'nodes' AS t, count(*) FROM cluster_node WHERE hostname='b201-test-node' UNION ALL SELECT 'invites_pending', count(*) FROM cluster_invite WHERE target_hostname='b201-test-node' AND status='pending';"
