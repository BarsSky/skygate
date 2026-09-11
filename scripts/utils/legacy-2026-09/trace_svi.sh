#!/usr/bin/env bash
set +e
echo "=== from skygate: traceroute to <polygon-vm-public-ip> ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "which traceroute; which tracepath; which mtr; echo '---'; (traceroute -n -w 2 -m 10 <polygon-vm-public-ip> 2>&1 || tracepath -n -m 10 <polygon-vm-public-ip> 2>&1) | head -15" 2>&1
echo ""
echo "=== from skygate: same traceroute to 95.165.170.190 (control) ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "traceroute -n -w 2 -m 10 95.165.170.190 2>&1 | head -15" 2>&1
echo ""
echo "=== from skygate: same traceroute to 8.8.8.8 (control) ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "traceroute -n -w 2 -m 10 8.8.8.8 2>&1 | head -15" 2>&1
echo ""
echo "=== arp scan on skygate's local subnet ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "arp -an 2>&1; echo '---'; for i in 1 2 6 9 13 20 29 34 67; do timeout 2 ping -c 1 -W 1 192.168.13.\$i 2>&1 | grep -E 'from|unreachable' | head -1; done" 2>&1
echo ""
echo "=== skygate DNS resolver ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "cat /etc/resolv.conf 2>&1" 2>&1
echo ""
echo "=== from this Windows: nslookup skynas.ru 192.168.13.1 (gateway DNS) ==="
nslookup skynas.ru 192.168.13.1 2>&1 | head -10
echo ""
echo "=== check HTTP/HTTPS response on <polygon-vm-public-ip> (does it 302 redirect?) ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "curl -v --max-time 5 http://<polygon-vm-public-ip>/ 2>&1 | head -20" 2>&1
echo ""
echo "=== from Windows: traceroute to <polygon-vm-public-ip> ==="
tracert -d -h 10 -w 3 <polygon-vm-public-ip> 2>&1 | Select-Object -First 15
