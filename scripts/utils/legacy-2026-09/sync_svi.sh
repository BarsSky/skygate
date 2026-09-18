#!/usr/bin/env bash
set +e
# Sync skygate repo from skygate to svi
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 '
echo "== source: skygate repo =="
cd /home/skyadmin/skygate
git log --oneline -2
echo "== sync to svi =="
rsync -az --delete \
  --exclude=.git \
  --exclude=bin \
  --exclude=tmp/agent_* \
  /home/skyadmin/skygate/ svi:/home/skyadmin/skygate/
echo "== verify svi has the new files =="
ssh svi "cd /home/skyadmin/skygate && git log --oneline -2; ls scripts/ha-state/state.sh scripts/ha-phase0.sh scripts/ha-phase7.sh scripts/ha-phase9.sh scripts/ha-status.sh scripts/check_ha_state.sh 2>&1"
' 2>&1 | head -25
