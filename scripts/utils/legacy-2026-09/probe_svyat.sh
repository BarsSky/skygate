#!/usr/bin/env bash
# Deep probe: Tailscale state on skygate + possible alternative IPs
set +e
echo "=== Tailscale status on skygate VM ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale status 2>&1 | head -30" 2>&1
echo ""
echo "=== Tailscale ping from skygate to svyatoslava ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale ping --c 2 <polygon-vm-hostname> 2>&1 | head -5" 2>&1
echo ""
echo "=== Local tailscale state ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "tailscale netcheck 2>&1 | head -10" 2>&1
echo ""
echo "=== skygate HA env (any svyatoslava hints?) ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "grep -i svyat /home/skyadmin/skygate/.env /home/skyadmin/skygate/.env.example 2>&1 | head -10" 2>&1
echo ""
echo "=== HA state on skygate VM ==="
ssh -o ConnectTimeout=5 -o BatchMode=yes skyadmin@192.168.13.69 "ls /var/lib/skygate/ha-state/ 2>&1; cat /var/lib/skygate/ha-state/state.json 2>&1 | head -30" 2>&1
