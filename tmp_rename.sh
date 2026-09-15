#!/bin/sh
set +e
echo "=== STEP 1: переименовываем ноду 57 в skygate-host ==="
sudo docker exec headscale headscale nodes rename -i 57 skygate-host 2>&1 || sudo docker exec headscale headscale nodes set-name -i 57 skygate-host 2>&1
sleep 2
echo
echo "=== STEP 2: удаляем старую мёртвую ноду 33 ==="
sudo docker exec headscale headscale nodes delete -i 33 --force 2>&1
sleep 2
echo
echo "=== STEP 3: проверка headscale ==="
sudo docker exec headscale headscale nodes list -o json 2>/dev/null | \
  jq -r '.[] | select((.name//"")|test("skygate")) | "id=\(.id|tostring) name=\(.name // "?") given=\(.givenName // "?") user=\(.user.name) tags=\(.tags // []) online=\(.online // false)"'
echo
echo "=== STEP 4: node_owner_map в DB ==="
sudo docker exec skygate-pg-local psql -U admin -d skygate_staging -At -F'|' -c "SELECT node_id, username, headscale_user_id, tag, hostname FROM node_owner_map WHERE hostname LIKE 'skygate-host%' ORDER BY hostname"
echo
echo "=== STEP 5: tailscale status ==="
sudo tailscale status 2>&1 | head -8
echo
echo "=== STEP 6: проверка появления B227 алертов после rename ==="
sleep 5
sudo docker logs --since 1m skygate-skygate-1 2>&1 | grep -E "node 57|tag.autoupdate_failed|node-discovery|auto-apply dev tag" | tail -8