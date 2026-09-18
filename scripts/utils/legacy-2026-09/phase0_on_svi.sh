#!/usr/bin/env bash
set +e
# Pull latest skygate repo + run ha-phase0.sh on svi
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 'echo "== ssh svi: pull latest skygate =="
ssh -o ConnectTimeout=10 svi "cd ~/skygate 2>/dev/null && pwd || (mkdir -p ~/skygate && cd ~/skygate && git init -q && git remote add origin https://github.com/BarsSky/skygate.git); cd ~/skygate; git fetch origin main --depth=1 2>&1 | tail -3; git reset --hard origin/main 2>&1 | tail -5; git log --oneline -3" 2>&1' 2>&1 | head -20
echo ""
echo "== install scripts =="
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 'ssh svi "cd ~/skygate; chmod +x scripts/ha-*.sh scripts/ha-state/state.sh scripts/check_ha_state.sh 2>&1; ls -la scripts/ha-state/state.sh scripts/ha-phase0.sh scripts/check_ha_state.sh" 2>&1' 2>&1 | head -10
echo ""
echo "== run B-check on svi =="
ssh -o ConnectTimeout=15 -o BatchMode=yes skyadmin@192.168.13.69 'ssh svi "cd ~/skygate; bash scripts/check_ha_state.sh 2>&1 | tail -10" 2>&1' 2>&1 | head -15
echo ""
echo "== run ha-phase0.sh on svi =="
ssh -o ConnectTimeout=30 -o BatchMode=yes skyadmin@192.168.13.69 'ssh svi "cd ~/skygate; SKYGATE_STANDBY_HOST=svi SKYGATE_PRIMARY_HOST=skygate-host-1 bash scripts/ha-phase0.sh 2>&1" 2>&1' 2>&1 | head -25
