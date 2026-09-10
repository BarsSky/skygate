#!/usr/bin/env bash
set +e
echo "=== which node belongs to user 86 (skyadmin 2nd) ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null" > /tmp/hs_nodes.json
python3 -c "
import json
with open('/tmp/hs_nodes.json') as f: d=json.load(f)
for n in d:
    u = n.get('user',{})
    if u and u.get('name')=='skyadmin' and u.get('id')==86:
        print(f'  user 86 node: id={n.get(chr(0x69)+chr(0x64))} name={n.get(chr(0x67)+chr(0x69)+chr(0x76)+chr(0x65)+chr(0x6e)+chr(0x5f)+chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65))} ip={n.get(chr(0x69)+chr(0x70)+chr(0x5f)+chr(0x61)+chr(0x64)+chr(0x64)+chr(0x72)+chr(0x65)+chr(0x73)+chr(0x73)+chr(0x65)+chr(0x73))}')
    if str(n.get(chr(0x69)+chr(0x64)))=='45':
        u = n.get('user',{})
        print(f'  node 45 user: id={u.get(chr(0x69)+chr(0x64))} name={u.get(chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65))}')
"
echo ""
echo "=== svi ssh config check (does agent have a 'svi' host entry?) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "cat /home/skyadmin/.ssh/config 2>&1; echo '---'; ls /home/skyadmin/.ssh/ 2>&1" 2>&1 | head -30
echo ""
echo "=== check /etc/hosts on agent for svi ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "cat /etc/hosts 2>&1; echo '---'; getent hosts svi 2>&1" 2>&1
echo ""
echo "=== current 'tailscale status' from agent ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale status 2>&1" 2>&1
