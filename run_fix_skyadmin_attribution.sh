#!/bin/bash
# Wrapper to run fix_admin_attribution.sh with .env loaded
cd /home/admin/skygate
set -a
source .env
set +a
export SKYGATE_ADMIN_USER
export SKYGATE_ADMIN_PASS
exec bash /tmp/fix_admin_attribution.sh
