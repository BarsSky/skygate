#!/usr/bin/env bash
# Final diagnosis of svyatoslava-1 — confirm dead state
set +e
echo "=== svyatoslava-1 connectivity matrix ==="
echo ""
echo "1. Direct public IP 45.152.198.217:22"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@45.152.198.217 'echo OK' 2>&1 | head -2
echo ""
echo "2. Tailscale IP 100.64.0.24 (from skygate)"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 'ssh -o ConnectTimeout=3 -o BatchMode=yes skyadmin@100.64.0.24 "echo OK" 2>&1 | head -2' 2>&1 | head -2
echo ""
echo "3. headscale status of node 45"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys,time
now=int(time.time())
for n in json.load(sys.stdin):
    if str(n.get(\"id\")) == \"45\":
        ls=n.get(\"last_seen\",{}).get(\"seconds\",0)
        print(f\"  online={n.get(\\\"online\\\")} last_seen_age={int(now-ls)}s endpoints={n.get(\\\"endpoints\\\")} hostinfo={bool(n.get(\\\"hostinfo\\\"))}\")
'" 2>&1
echo ""
echo "4. Tailscale route on skygate (table 52)"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ip route show table 52 2>&1; echo '==='; echo 'svyatoslava-1 (100.64.0.24) NOT in table 52 = no peer'" 2>&1
echo ""
echo "5. svyatoslava-1 in skygate's peer list (status --json)?"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale status --json 2>&1 | python3 -c '
import json,sys
d=json.load(sys.stdin)
peers=d.get(\"Peer\",{})
print(f\"  peer count: {len(peers)}\")
svyat=[p for p in peers.values() if \"svyat\" in p.get(\"HostName\",\"\").lower()]
print(f\"  svyatoslava in peer list: {bool(svyat)}\")
'" 2>&1
echo ""
echo "6. systemd-resolved stub listener (127.0.0.53:53) misbehaving"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "dig +short +tries=1 +time=2 @127.0.0.53 svyatoslava-1 2>&1; echo '==='; getent hosts svyatoslava-1 2>&1" 2>&1
