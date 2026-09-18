#!/usr/bin/env bash
set +e
# Trigger policy regenerate (which forces fresh lookups)
cat > /tmp/regen.sh << 'EOFCMD'
# Touch the policy file to force reload
sudo touch /var/lib/docker/volumes/headscale_headscale_data/_data/db.sqlite
# Check current state
docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c "
import json,sys
for n in json.load(sys.stdin):
    if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
        u = n.get(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72), {})
        print(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72), u)
"
echo ---
# Try the policy set to a no-op (forces reload)
docker exec headscale headscale policy check 2>&1 | head -5
EOFCMD
scp -q /tmp/regen.sh skyadmin@192.168.13.69:/tmp/regen.sh
ssh skyadmin@192.168.13.69 "bash /tmp/regen.sh" 2>&1 | head -10
