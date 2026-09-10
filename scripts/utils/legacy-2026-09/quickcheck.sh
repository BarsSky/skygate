#!/usr/bin/env bash
set +e
echo "=== ping from this Windows host ==="
ping -n 3 -w 4 45.152.198.217 2>&1 | tail -4
echo ""
echo "=== ssh 45.152.198.217 ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes skyadmin@45.152.198.217 "echo SSH_OK; whoami; hostname; uname -a; ip -br addr" 2>&1
echo ""
echo "=== port scan from skygate ==="
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 'for p in 22 53 80 443 41641; do out=$(timeout 1 nc -zv 45.152.198.217 $p 2>&1); echo "$out" | grep -q "succeeded" && echo "  $p: OPEN" || echo "  $p: closed/filtered"; done' 2>&1
