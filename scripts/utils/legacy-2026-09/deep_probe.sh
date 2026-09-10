#!/usr/bin/env bash
# Deep probe of 45.152.198.217 — all common ports + traceroute
set +e
echo "=== port scan of 45.152.198.217 (top 20 ports) ==="
for p in 22 80 443 2222 2379 50443 41641 8080 50444 50445 19999 53 110 143 25 465 587 993 995 3389 5900 5432 3306 6379 27017 8888 8000 8883; do
    out=$(timeout 3 nc -zv -w 2 45.152.198.217 $p 2>&1)
    if echo "$out" | grep -q "succeeded\|open"; then
        echo "  $p: OPEN"
    fi
done
echo ""
echo "=== traceroute to 45.152.198.217 (Windows tracert) ==="
tracert -d -h 10 -w 2 45.152.198.217 2>&1 | head -20
echo ""
echo "=== whois on 45.152.198.217 (if available) ==="
nslookup 45.152.198.217 2>&1 | head -10
echo ""
echo "=== check if 45.152.198.217 is on the same /24 as 95.165.170.190 ==="
echo "95.165.170.190: routes through (operator says svyatoslava pings this)"
nslookup 95.165.170.190 2>&1 | head -5
echo ""
echo "=== Try via Tailscale exit node: route through emilia/karolina/sharlotta to reach svyat ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale status --json 2>&1 | python3 -c '
import json,sys
d=json.load(sys.stdin)
peers=d.get(\"Peer\",{})
for k,p in peers.items():
    if p.get(\"Online\") and \"exit\" in str(p.get(\"Capabilities\",[])).lower():
        print(\"  exit node:\", p.get(\"HostName\"), p.get(\"TailscaleIPs\"))
'" 2>&1
echo ""
echo "=== can we SSH to svyatoslava via skygate tailscale (jumphost)? ==="
ssh -o ConnectTimeout=8 -o BatchMode=yes skyadmin@192.168.13.69 'nc -zv -w 3 45.152.198.217 22 2>&1' 2>&1
