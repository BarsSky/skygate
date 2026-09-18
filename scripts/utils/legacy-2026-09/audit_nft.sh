#!/usr/bin/env bash
# Check iptables-legacy + nftables + full routes
set +e
echo "=== iptables-legacy tables (the warning hinted at these) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo iptables-legacy -t filter -L -n -v 2>&1" 2>&1 | head -40
echo ""
echo "=== nftables ruleset (if any) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo nft list ruleset 2>&1" 2>&1 | head -60
echo ""
echo "=== full ip route show ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ip route show 2>&1; echo '==='; ip route show table all 2>&1 | head -50" 2>&1
echo ""
echo "=== /etc/iptables/rules.v4 (persistent) full ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "cat /etc/iptables/rules.v4 2>&1 | tail -40" 2>&1
