#!/usr/bin/env bash
set +e
# Add 'svi' SSH config entry to skygate's .ssh/config
# This enables `ssh svi` directly (via karolina jump)
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 '
cat >> /home/skyadmin/.ssh/config << "EOF"

Host svi
    HostName <polygon-vm-public-ip>
    User root
    ProxyCommand ssh -W %h:%p -o StrictHostKeyChecking=accept-new root@karolina
    StrictHostKeyChecking accept-new
EOF
chmod 600 /home/skyadmin/.ssh/config
echo "== ssh config tail =="
tail -10 /home/skyadmin/.ssh/config
echo ""
echo "== test: ssh svi =="
ssh -o ConnectTimeout=10 svi "echo SSH_OK_DIRECT; whoami; hostname; ip -br addr" 2>&1
' 2>&1 | head -25
