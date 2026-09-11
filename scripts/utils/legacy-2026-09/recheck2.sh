#!/usr/bin/env bash
set +e
echo "=== ping from this Windows host ==="
ping -n 3 -w 5 <polygon-vm-public-ip> 2>&1 | tail -4
echo ""
echo "=== port scan <polygon-vm-public-ip> from skygate ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 'for p in 22 53 80 443 2222 2379 41641 50443 50444 50445 8080 25 110 143 993 995 3389 5900 5432 3306 6379 11211 27017 8000 8888 19999 10000 3128 4443 5000 7443 9090 9200 9418 81 8081 8443 8001 3128 8883 5222 5672 7474 8083 8085 8880 8800 7443 222 8006 22222 800; do out=$(timeout 1 nc -zv <polygon-vm-public-ip> $p 2>&1); echo "$out" | grep -q "succeeded" && echo "  $p: OPEN"; done; echo DONE' 2>&1
echo ""
echo "=== headscale node 45 status ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list -o json 2>/dev/null | python3 -c '
import json,sys,time
now=int(time.time())
for n in json.load(sys.stdin):
    if str(n.get(chr(0x69)+chr(0x64))) == chr(0x34)+chr(0x35):
        ls=n.get(chr(0x6c)+chr(0x61)+chr(0x73)+chr(0x74)+chr(0x5f)+chr(0x73)+chr(0x65)+chr(0x65)+chr(0x6e),{}).get(chr(0x73)+chr(0x65)+chr(0x63)+chr(0x6f)+chr(0x6e)+chr(0x64)+chr(0x73),0)
        print(f\"  last_seen_age={int(now-ls)}s online={n.get(chr(0x6f)+chr(0x6e)+chr(0x6c)+chr(0x69)+chr(0x6e)+chr(0x65))} endpoints={n.get(chr(0x65)+chr(0x6e)+chr(0x64)+chr(0x70)+chr(0x6f)+chr(0x69)+chr(0x6e)+chr(0x74)+chr(0x73))} hostinfo={bool(n.get(chr(0x68)+chr(0x6f)+chr(0x73)+chr(0x74)+chr(0x69)+chr(0x6e)+chr(0x66)+chr(0x6f))}\")
'" 2>&1
