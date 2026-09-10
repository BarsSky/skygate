#!/usr/bin/env bash
set +e
# Setup /tmp/svi-jump wrapper on skygate for easy access
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "cat > /tmp/svi-jump.sh << 'JUMP_EOF'
#!/bin/bash
# Jump to svi (svyatoslava-1) via karolina
exec ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -o ProxyCommand='ssh -W %h:%p -o StrictHostKeyChecking=accept-new root@karolina' root@45.152.198.217 \"\$@\"
JUMP_EOF
chmod +x /tmp/svi-jump.sh
cat /tmp/svi-jump.sh
echo '--- test ---'
/tmp/svi-jump.sh 'echo WORKING; whoami; hostname; uname -r'
" 2>&1 | head -25
