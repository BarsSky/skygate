#!/bin/bash
# Wrapper to run check_v0.23.0.sh with .env loaded
cd /home/skyadmin/skygate
set -a
source .env
set +a
export SKYGATE_ADMIN_USER
export SKYGATE_ADMIN_PASS
exec bash /tmp/check_v0.23.0.sh
