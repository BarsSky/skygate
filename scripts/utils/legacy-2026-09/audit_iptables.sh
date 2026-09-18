#!/usr/bin/env bash
# Full iptables + routes audit on skygate VM
set +e
echo "=== CURRENT IPTABLES STATE (root needed) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo iptables -t filter -S 2>&1 | head -50" 2>&1
echo ""
echo "=== DOCKER-USER chain (the B179 trap) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo iptables -L DOCKER-USER -n -v 2>&1; echo '==='; sudo iptables -t filter -L DOCKER-USER 2>&1" 2>&1
echo ""
echo "=== INPUT chain (B179 trap was here too) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo iptables -L INPUT -n -v 2>&1 | head -30" 2>&1
echo ""
echo "=== any block for <polygon-vm-public-ip> (svyatoslava) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "sudo iptables-save 2>&1 | grep -i '45\.152\|svyat' | head -20" 2>&1
echo ""
echo "=== persistent iptables rules (saved to /etc/iptables) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ls /etc/iptables/ 2>&1; echo '==='; cat /etc/iptables/rules.v4 2>&1 | head -30" 2>&1
echo ""
echo "=== ip route full ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ip route show table all 2>&1 | head -30" 2>&1
echo ""
echo "=== what is listening + recent established ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ss -tunap 2>&1 | head -20" 2>&1
