#!/usr/bin/env bash
# Retry with -o StrictHostKeyChecking=accept-new for 13.20
set +e
echo "=== SSH to 13.20 with auto-accept ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@192.168.13.20 'echo OK; whoami; uname -a' 2>&1 | head -8
echo ""
echo "=== From 13.20, ping svyatoslava public IP ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@192.168.13.20 'ping -c 3 -W 4 <polygon-vm-public-ip> 2>&1 | tail -6' 2>&1
echo ""
echo "=== From 13.20, ping svyatoslava Tailscale IP (100.64.0.24) ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@192.168.13.20 'ping -c 3 -W 4 100.64.0.24 2>&1 | tail -6' 2>&1
echo ""
echo "=== From 13.20, ssh to svyatoslava (try different ports) ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@192.168.13.20 'nc -zv -w 3 <polygon-vm-public-ip> 22 2>&1; echo "---"; nc -zv -w 3 <polygon-vm-public-ip> 80 2>&1; echo "---"; nc -zv -w 3 <polygon-vm-public-ip> 443 2>&1; echo "---"; nc -zv -w 3 <polygon-vm-public-ip> 2379 2>&1' 2>&1
echo ""
echo "=== From 13.20, ssh to svyatoslava public IP ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@192.168.13.20 'ssh -o ConnectTimeout=3 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@<polygon-vm-public-ip> "echo SSH_OK" 2>&1' 2>&1 | head -3
echo ""
echo "=== check what services are on 95.165.170.190 (NPM) ==="
nc -zv -w 3 95.165.170.190 22 2>&1
nc -zv -w 3 95.165.170.190 80 2>&1
nc -zv -w 3 95.165.170.190 81 2>&1
nc -zv -w 3 95.165.170.190 443 2>&1
