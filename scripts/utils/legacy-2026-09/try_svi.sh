#!/usr/bin/env bash
set +e
echo "=== check skygate's known_hosts for svi ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "cat /home/skyadmin/.ssh/known_hosts 2>&1" 2>&1 | grep -iE "svi|svyat|45\.152" | head -5
echo ""
echo "=== try 'root@<polygon-vm-hostname>' from this Windows host ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes root@<polygon-vm-hostname> "echo SSH_OK; whoami; hostname" 2>&1 | head -5
echo ""
echo "=== try 'root@<polygon-vm-public-ip>' from this Windows host ==="
ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o BatchMode=yes root@<polygon-vm-public-ip> "echo SSH_OK; whoami; hostname" 2>&1 | head -5
echo ""
echo "=== from skygate: try 'root@svi' / 'root@<polygon-vm-hostname>' ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "timeout 8 ssh -o ConnectTimeout=3 -o StrictHostKeyChecking=accept-new root@svi 'echo SSH_OK; whoami; hostname' 2>&1" 2>&1 | head -5
echo ""
echo "=== from skygate: try 'root@100.64.0.24' ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "timeout 8 ssh -o ConnectTimeout=3 -o StrictHostKeyChecking=accept-new root@100.64.0.24 'echo SSH_OK; whoami; hostname' 2>&1" 2>&1 | head -5
echo ""
echo "=== check MagicDNS for 'svi' on headscale ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "docker exec headscale headscale nodes list 2>&1 | grep -i svi" 2>&1 | head -3
