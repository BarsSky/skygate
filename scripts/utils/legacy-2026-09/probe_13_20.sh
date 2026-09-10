#!/usr/bin/env bash
# Test from 13.20 (LAN host) and 95.165.170.190 (NPM)
set +e
echo "=== SSH to 13.20 (operator's other LAN host) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.20 'echo OK; whoami; uname -a; ip addr show 2>&1 | head -20' 2>&1 | head -15
echo ""
echo "=== From 13.20, ping svyatoslava public IP ==="
ssh -o ConnectTimeout=8 -o BatchMode=yes skyadmin@192.168.13.20 'ping -c 3 -W 3 45.152.198.217 2>&1 | tail -6' 2>&1
echo ""
echo "=== From 13.20, ping svyatoslava Tailscale IP (100.64.0.24) ==="
ssh -o ConnectTimeout=8 -o BatchMode=yes skyadmin@192.168.13.20 'ping -c 3 -W 3 100.64.0.24 2>&1 | tail -6' 2>&1
echo ""
echo "=== SSH to NPM fronting (95.165.170.190) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@95.165.170.190 'echo OK; whoami; uname -a' 2>&1 | head -5
echo ""
echo "=== From NPM, ping svyatoslava public IP ==="
ssh -o ConnectTimeout=8 -o BatchMode=yes skyadmin@95.165.170.190 'ping -c 3 -W 3 45.152.198.217 2>&1 | tail -6' 2>&1
echo ""
echo "=== From NPM, ping skygate LAN IP ==="
ssh -o ConnectTimeout=8 -o BatchMode=yes skyadmin@95.165.170.190 'ping -c 3 -W 3 192.168.13.69 2>&1 | tail -6' 2>&1
echo ""
echo "=== From NPM, ssh to svyatoslava public IP ==="
ssh -o ConnectTimeout=8 -o BatchMode=yes skyadmin@95.165.170.190 'ssh -o ConnectTimeout=3 -o BatchMode=yes skyadmin@45.152.198.217 "echo SSH_OK" 2>&1' 2>&1 | head -5
