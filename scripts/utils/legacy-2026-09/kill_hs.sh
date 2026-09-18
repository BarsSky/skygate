#!/usr/bin/env bash
set +e
# Force headscale to reload by killing it (docker will restart it)
cat > /tmp/killhs.sh << 'EOFCMD'
# Stop the headscale container
docker stop headscale 2>&1
# Wait
sleep 3
# Start it again
docker start headscale 2>&1
# Wait for ready
sleep 10
# Now check
docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c "
import json,sys
for n in json.load(sys.stdin):
    if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
        u = n.get(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72), {})
        print(chr(0x75)+chr(0x73)+chr(0x65)+chr(0x72)+chr(0x3a), u.get(chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65)), chr(0x69)+chr(0x64), u.get(chr(0x69)+chr(0x64)))
"
EOFCMD
scp -q /tmp/killhs.sh skyadmin@192.168.13.69:/tmp/killhs.sh
ssh skyadmin@192.168.13.69 "bash /tmp/killhs.sh" 2>&1
