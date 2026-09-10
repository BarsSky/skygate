#!/usr/bin/env bash
set +e
echo "=== Recheck from this Windows host ==="
ping -n 3 -w 4 45.152.198.217 2>&1 | tail -4
echo ""
echo "=== Recheck from skygate ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "echo '-- 22 --'; timeout 5 nc -zv 45.152.198.217 22 2>&1; echo '-- 53 --'; timeout 5 nc -zv 45.152.198.217 53 2>&1; echo '-- 2222 --'; timeout 5 nc -zv 45.152.198.217 2222 2>&1; echo '-- 80 --'; timeout 5 nc -zv 45.152.198.217 80 2>&1; echo '-- 443 --'; timeout 5 nc -zv 45.152.198.217 443 2>&1" 2>&1
echo ""
echo "=== headscale node 45 (svyat) last_seen ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys,time
now=int(time.time())
for n in json.load(sys.stdin):
    if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
        ls=n.get(chr(0x6c)+chr(0x61)+chr(0x73)+chr(0x74)+chr(0x5f)+chr(0x73)+chr(0x65)+chr(0x65)+chr(0x6e),{}).get(chr(0x73)+chr(0x65)+chr(0x63)+chr(0x6f)+chr(0x6e)+chr(0x64)+chr(0x73),0)
        print(f\"  last_seen_age={int(now-ls)}s online={n.get(chr(0x6f)+chr(0x6e)+chr(0x6c)+chr(0x69)+chr(0x6e)+chr(0x65))} endpoints={n.get(chr(0x65)+chr(0x6e)+chr(0x64)+chr(0x70)+chr(0x6f)+chr(0x69)+chr(0x6e)+chr(0x74)+chr(0x73))} hostinfo={bool(n.get(chr(0x68)+chr(0x6f)+chr(0x73)+chr(0x74)+chr(0x69)+chr(0x6e)+chr(0x66)+chr(0x6f))}\")
'" 2>&1
