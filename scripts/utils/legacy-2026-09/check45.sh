#!/usr/bin/env bash
set +e
# Get node 45 via direct headscale binary
cat > /tmp/c45.sh << 'EOFCMD'
docker exec headscale /ko-app/headscale nodes list -o json 2>/dev/null | python3 -c "
import json, sys
data = sys.stdin.read()
for n in json.loads(data):
    if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
        print(json.dumps(n, indent=2))
"
EOFCMD
scp -q /tmp/c45.sh skyadmin@192.168.13.69:/tmp/c45.sh
ssh skyadmin@192.168.13.69 "bash /tmp/c45.sh" 2>&1 | head -50
