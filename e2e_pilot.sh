#!/bin/bash
# e2e_pilot.sh — end-to-end subnet-router pilot on skygate-vm.
#
# Goal: prove the entire flow works end-to-end:
#   1. Issue preauth key (admin side, via /admin/users/1/subnet)
#   2. Spin up a tailscale client in a sidecar container
#   3. tailscale up with the preauth key + advertised-routes
#   4. Watch the new node register in headscale as
#      `skygate-subnet-skyadmin` with tag:subnet-router
#   5. Wait 30s for sidecar.SyncOnce to auto-approve 10.0.1.0/24
#   6. Verify status pill on /admin/users/1/subnet flips
#      from `active` to `router_active`
#   7. Verify from a tailnet client: ping 10.0.1.1
#
# After this passes, we have proof that the
# deploy/subnet-router/setup.sh flow works for a
# non-test user (skyadmin pilot on the live VM).
set -e
cd /home/skyadmin/skygate
PASSWORD=$(grep '^SKYGATE_ADMIN_PASS' .env | cut -d= -f2-)
COOKIE=/tmp/sgck_e2e.txt
rm -f $COOKIE

echo "=== Step 0: wait for skygate ==="
for i in 1 2 3 4 5 6; do
  sleep 5
  if curl -fsS -m 2 http://localhost:8080/healthz >/dev/null 2>&1; then
    echo "  skygate up after ${i}*5s"
    break
  fi
done

echo ""
echo "=== Step 1: login as skyadmin ==="
curl -sS -c $COOKIE -X POST http://localhost:8080/login \
  --data-urlencode "username=skyadmin" \
  --data-urlencode "password=$PASSWORD" \
  -o /dev/null -w "  HTTP %{http_code}\n"

echo ""
echo "=== Step 2: issue preauth key via /admin/users/1/subnet/download ==="
# (download endpoint also issues a fresh preauth)
curl -sS -b $COOKIE -o /tmp/skyadmin-bundle.tar.gz \
  http://localhost:8080/admin/users/1/subnet/download \
  -w "  HTTP %{http_code}, size: %{size_download} bytes\n"
mkdir -p /tmp/skyadmin-router
cd /tmp/skyadmin-router
tar xzf /tmp/skyadmin-bundle.tar.gz
echo "  bundle contents:"
ls -la
echo ""
echo "  commands.txt (with the preauth key):"
cat commands.txt

# Extract the authkey for use in the sidecar.
# Format: hskey-auth-XXX or tskey-auth-XXX where XXX is base62 (a-z, 0-9, _, -)
AUTHKEY=$(grep -oP 'authkey=\K[a-zA-Z0-9_-]+' commands.txt | head -1)
echo ""
echo "  preauth key: $AUTHKEY"
echo "$AUTHKEY" > /tmp/skyadmin-authkey.txt

echo ""
echo "=== Step 3: pull tailscale image and start sidecar container ==="
# We use a tailscale image that supports running
# tailscaled in a container. tailscale/tailscale:latest
# is the standard one. We mount --network=host so
# the sidecar can advertise the local 10.0.1.0/24
# (this is a SIMULATED subnet-router — in production
# it would be on the user's LAN, not on skygate-vm).
# IMPORTANT: TS_LOGIN_SERVER is mandatory — without it,
# the Tailscale client falls back to controlplane.tailscale.com
# (the public Tailscale SaaS), which doesn't know about our preauth.
docker pull tailscale/tailscale:latest 2>&1 | tail -3
docker rm -f skyadmin-subnet-router 2>/dev/null || true
docker run -d \
  --name skyadmin-subnet-router \
  --network=host \
  --cap-add=NET_ADMIN --device /dev/net/tun \
  --env TS_AUTHKEY="$AUTHKEY" \
  --env TS_LOGIN_SERVER="https://head.skynas.ru" \
  --env TS_HOSTNAME=skygate-subnet-skyadmin \
  --env TS_STATE_DIR=/var/lib/tailscale \
  --volume skyadmin-router-state:/var/lib/tailscale \
  --restart unless-stopped \
  tailscale/tailscale:latest \
  sh -c "/usr/local/bin/tailscaled --state=/var/lib/tailscale/tailscaled.state --socket=/var/run/tailscale/tailscaled.sock & sleep 5 && /usr/local/bin/tailscale up --accept-routes --login-server=https://head.skynas.ru --hostname=skygate-subnet-skyadmin --netfilter-mode=off --advertise-routes=10.0.1.0/24 --authkey=\$TS_AUTHKEY && sleep 7200" \
  2>&1 | tail -5

echo ""
echo "=== Step 4: wait for the new node to register in headscale ==="
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  sleep 5
  found=$(docker exec headscale headscale nodes list 2>&1 | grep -c "skygate-subnet-skyadmin" || true)
  if [ "$found" -gt 0 ]; then
    echo "  skygate-subnet-skyadmin found in headscale after ${i}*5s"
    break
  fi
  echo "  ${i}*5s: not found yet"
done

echo ""
echo "=== Step 5: inspect the new node ==="
docker exec headscale headscale nodes list 2>&1 | grep -B 1 -A 12 "skygate-subnet-skyadmin" | head -20
echo ""
echo "  detailed state (JSON, headscale 0.29.2 field names):"
docker exec headscale headscale nodes list -o json 2>/dev/null | \
  python3 -c "
import sys, json
data = json.loads(sys.stdin.read().lstrip('\ufeff'))
for n in data:
    if n.get('name') == 'skygate-subnet-skyadmin':
        print('   id              =', n.get('id'))
        print('   tags            =', n.get('tags'))
        print('   available_routes=', n.get('available_routes'))
        print('   approved_routes =', n.get('approved_routes'))
        print('   subnet_routes   =', n.get('subnet_routes'))
        print('   ip_addresses    =', n.get('ip_addresses'))
        print('   online          =', n.get('online'))
"

echo ""
echo "=== Step 6: wait for sidecar.SyncOnce to auto-approve 10.0.1.0/24 ==="
for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
  sleep 5
  approved=$(docker exec headscale headscale nodes list -o json 2>/dev/null | \
    python3 -c "
import sys, json
# headscale 0.29.x: 'enabledRoutes' (legacy) or 'approved_routes' (current)
data = json.loads(sys.stdin.read().lstrip('\ufeff'))
for n in data:
    if n.get('name') == 'skygate-subnet-skyadmin':
        approved = n.get('approved_routes') or n.get('enabledRoutes') or []
        for r in approved:
            if r.startswith('10.0.1.'):
                print('YES')
                sys.exit(0)
print('NO')
" 2>/dev/null || echo "ERROR")
  if [ "$approved" = "YES" ]; then
    echo "  10.0.1.0/24 APPROVED after ${i}*5s"
    break
  fi
  echo "  ${i}*5s: not yet approved ($approved)"
done

echo ""
echo "=== Step 7: skygate sidecar logs ==="
docker logs skygate --since 2m 2>&1 | grep -E "sidecar.*skyadmin|10.0.1.0/24" | tail -10

echo ""
echo "=== Step 8: verify via /admin/users/1/subnet (status pill) ==="
curl -sS -b $COOKIE http://localhost:8080/admin/users/1/subnet -o /tmp/subnet-page.html -w "  HTTP %{http_code}\n"
echo "  status pills found:"
grep -oE 'cell_[a-z_]+|router_active' /tmp/subnet-page.html | sort -u

echo ""
echo "=== Step 9: verify via skygate DB ==="
docker exec skygate apk add --no-cache sqlite >/dev/null 2>&1
docker exec skygate sqlite3 /data/skygate.db "SELECT user_id, cidr, status, router_hostname, router_container_id FROM user_subnets WHERE user_id = 1;"

echo ""
echo "=== Cleanup (uncomment to remove the sidecar) ==="
# docker rm -f skyadmin-subnet-router
# docker volume rm skyadmin-router-state
echo "  (sidecar left running so you can ping 10.0.1.X from any tailnet client)"
