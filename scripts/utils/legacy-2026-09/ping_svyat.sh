#!/usr/bin/env bash
# Try direct Tailscale IP 100.64.0.24 + MagicDNS via real DNS
set +e
echo "=== ping Tailscale IP 100.64.0.24 directly (via skygate VM) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ping -c 2 -W 3 100.64.0.24 2>&1 | tail -5" 2>&1
echo ""
echo "=== tailscale ping <polygon-vm-hostname> (with explicit IP) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale ping --c 1 100.64.0.24 2>&1 | head -5" 2>&1
echo ""
echo "=== ssh direct to 100.64.0.24 ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@100.64.0.24 'echo SSH_OK; hostname; uname -a' 2>&1 | head -5" 2>&1
echo ""
echo "=== headscale last seen 45 (<polygon-vm-hostname>) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys
for n in json.load(sys.stdin):
    if str(n.get(\"id\")) == \"45\":
        print(json.dumps(n, indent=2))'" 2>&1 | Select-Object -First 30
echo ""
echo "=== route table on skygate to 100.64.0.0/10 ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ip route get 100.64.0.24 2>&1; echo '---'; ip route show table 52 2>&1 | head -5" 2>&1
