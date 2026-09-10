#!/usr/bin/env bash
set +e
# Move node 45 (svyatoslava-1) to user 'infra' (id=85) directly in headscale DB.
# This is the only way to fix svi's user mapping since headscale v0.29.1
# doesn't have a 'nodes move' CLI command.
ssh skyadmin@192.168.13.69 '
echo "== before =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "SELECT id, given_name, user_id FROM nodes WHERE id=45;" 2>&1
echo
echo "== move node 45 to user 85 (infra) =="
sudo sqlite3 /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite "UPDATE nodes SET user_id=85 WHERE id=45; SELECT id, given_name, user_id FROM nodes WHERE id=45;" 2>&1
echo
echo "== verify via headscale CLI =="
docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c "
import json,sys
for n in json.load(sys.stdin):
    if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
        u = n.get(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72),{})
        print(chr(0x69)+chr(0x64), n.get(chr(0x69)+chr(0x64)), chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65), n.get(chr(0x67)+chr(0x69)+chr(0x76)+chr(0x65)+chr(0x6e)+chr(0x5f)+chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65)), chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72), u.get(chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65)), chr(0x69)+chr(0x64), u.get(chr(0x69)+chr(0x64)))
"
' 2>&1
