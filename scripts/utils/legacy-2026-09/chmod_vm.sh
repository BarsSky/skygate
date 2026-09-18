#!/usr/bin/env bash
# Make the new scripts executable on the VM
set -e
ssh -o ConnectTimeout=10 -o BatchMode=yes skyadmin@192.168.13.69 "
    cd /home/skyadmin/skygate
    chmod +x scripts/ha-state/state.sh scripts/ha-phase0.sh scripts/ha-phase7.sh scripts/ha-phase9.sh scripts/ha-status.sh scripts/check_ha_state.sh
    chmod +x .githooks/pre-commit .githooks/pre-push
    echo '== verify executable =='
    ls -la scripts/ha-state/state.sh scripts/ha-phase0.sh scripts/ha-phase7.sh scripts/ha-phase9.sh scripts/ha-status.sh scripts/check_ha_state.sh .githooks/pre-commit
    echo '== run B-check =='
    bash scripts/check_ha_state.sh 2>&1 | tail -8
"
