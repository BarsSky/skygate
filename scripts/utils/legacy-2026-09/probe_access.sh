#!/usr/bin/env bash
# Search EVERYTHING for svyatoslava access path
set +e
echo "=== known_hosts entries for svyatoslava ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "grep -i svyat /home/skyadmin/.ssh/known_hosts 2>&1; echo '---'; grep -i svyat /home/skyadmin/.ssh/known_hosts.old 2>&1" 2>&1
echo ""
echo "=== check deploy/ dir for svyatoslava bootstrap config ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ls /home/skyadmin/skygate/deploy 2>&1; echo '==='; find /home/skyadmin/skygate -name '*svyat*' 2>/dev/null" 2>&1
echo ""
echo "=== check repo for STANDBY_HOST / svyatoslava in .env / deploy scripts ==="
cd C:/Projects/skygate
git grep -l "svyatoslava\|STANDBY_HOST" -- 'deploy' 'scripts' 2>&1 | head -10
echo ""
echo "=== look in current process env + any Tailscale node state file ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "
    cat /var/lib/skygate/ha-state/state.json 2>&1 | head -20
    echo '==='
    cat /home/skyadmin/.ssh/known_hosts | head -5
" 2>&1 | head -20
echo ""
echo "=== one more shot: 100.64.0.24 via karolina/emilia as jump host? ==="
echo "(not gonna actually try, but check if jumphost config exists)"
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "grep -i 'jump\|proxy' /home/skyadmin/.ssh/config 2>&1" 2>&1
