#!/usr/bin/env bash
set +e
echo "=== ping from this Windows host ==="
ping -n 3 -w 4 <polygon-vm-public-ip> 2>&1 | tail -4
echo ""
echo "=== ssh <polygon-vm-public-ip> ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@<polygon-vm-public-ip> "echo SSH_OK; whoami; hostname; uname -a; ip -br addr" 2>&1
echo ""
echo "=== port scan from skygate ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 'for p in 22 53 80 443 41641; do out=$(timeout 1 nc -zv <polygon-vm-public-ip> $p 2>&1); echo "$out" | grep -q "succeeded" && echo "  $p: OPEN" || echo "  $p: closed/filtered"; done' 2>&1
