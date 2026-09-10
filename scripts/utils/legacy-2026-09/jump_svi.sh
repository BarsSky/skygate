#!/usr/bin/env bash
set +e
# SSH jump from skygate -> karolina -> 45.152.198.217
# Use heredoc to avoid quoting issues
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new root@karolina 'cat > /tmp/jump.sh' << 'JUMP_EOF'
#!/bin/bash
exec ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new root@45.152.198.217 "\$@"
JUMP_EOF
chmod +x /tmp/jump.sh
echo '== test from karolina via jump =='
/tmp/jump.sh 'echo SSH_OK_VIA_KAROLINA; whoami; hostname; cat /etc/os-release | head -3; ip -br addr 2>&1; ss -tlnp 2>&1 | grep -E \":22\b\"'"
